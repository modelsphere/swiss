package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aceforeverd/swiss/internal/catalog"
	"github.com/aceforeverd/swiss/internal/cluster"
	"github.com/aceforeverd/swiss/internal/compose"
	"github.com/aceforeverd/swiss/internal/exec"
	"github.com/aceforeverd/swiss/internal/plan"
	"github.com/aceforeverd/swiss/internal/store"
	"github.com/aceforeverd/swiss/internal/values"
	"gopkg.in/yaml.v3"
)

type planRequest struct {
	// FromRelease recomposes a deployed release: its stored plan supplies the
	// form layer, the model and the variant, and only the catalog layer moves.
	FromRelease string `json:"fromRelease,omitempty"`
	Model       string `json:"model"`
	Version     string `json:"version,omitempty"`
	Variant     string `json:"variant,omitempty"`
	Release     string `json:"release,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	// ServiceID is the identity modelRoute, sloRequirement and the scaler all
	// key off, and LocalPath overrides the site's path template. Both are form
	// values; they are named here rather than left to Overrides because a
	// deploy form should not have to know the key path.
	ServiceID string `json:"serviceId,omitempty"`
	LocalPath string `json:"localPath,omitempty"`
	// GPUProducts narrows the deploy to these accelerator products, any one of
	// which will do. Named here rather than left to Overrides because the node
	// label they match on depends on the variant's vendor, which is the
	// catalog's business and not the form's.
	GPUProducts []string    `json:"gpuProducts,omitempty"`
	Overrides   values.Tree `json:"overrides,omitempty"`
	// OverridesYAML is the advanced section: a values fragment typed by hand.
	// Parsed here rather than in the browser so there is one parser, and the
	// ownership check still decides what it may contain.
	OverridesYAML string `json:"overridesYAML,omitempty"`
	// EditsYAML is the plan editor: applied after every layer, exempt from
	// ownership, and labelled "edit" wherever the plan is shown.
	EditsYAML string `json:"editsYAML,omitempty"`
}

type applyRequest struct {
	PlanHash string `json:"planHash"`
	// ExpectRevision is the live helm revision the diff was computed against.
	// It is the whole of the optimistic lock: the revision is helm's, held in
	// the cluster, so two operators who diffed the same release cannot both
	// apply. A version column in swissd's own database would have been a lock
	// on a copy rather than on the thing being changed.
	//
	// Zero -- absent -- asserts nothing, which is what an apply that skipped the
	// diff sends. The diff is optional, so the lock it carries is optional with
	// it; the caller is giving up the concurrency check, not evading one.
	ExpectRevision int `json:"expectRevision,omitempty"`
	// Note is why this is being applied, in the operator's own words. Optional,
	// recorded in the audit log and beside the release, and read by nothing --
	// a diff says what changed, and only a person can say why.
	Note string `json:"note,omitempty"`
}

func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()

	var req planRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := s.compose(ctx, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.store != nil {
		if err := s.store.PutPlan(ctx, p); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) compose(ctx context.Context, req planRequest) (*plan.Plan, error) {
	if req.FromRelease != "" {
		var err error
		if req, err = s.carryForward(ctx, req); err != nil {
			return nil, err
		}
	}
	if req.Model == "" {
		return nil, fmt.Errorf("model is required")
	}
	cat, err := s.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	entry, err := cat.Entry(ctx, req.Model, req.Version)
	if err != nil {
		return nil, err
	}
	var v catalog.Variant
	if req.Variant == "" {
		v, err = entry.DefaultVariant()
	} else {
		v, err = entry.Variant(req.Variant)
	}
	if err != nil {
		return nil, err
	}
	prof, err := s.Profile(ctx)
	if err != nil {
		return nil, err
	}
	// The release is named after the service by default: serviceId is the
	// identity the route, the scaler and the SLO all carry, and a release under
	// a different name is one more name for the same thing.
	release := req.Release
	if release == "" {
		release = req.ServiceID
	}
	if release == "" {
		release = entry.Name
	}

	overrides := values.Tree{}
	if req.OverridesYAML != "" {
		var extra values.Tree
		if err := yaml.Unmarshal([]byte(req.OverridesYAML), &extra); err != nil {
			return nil, fmt.Errorf("advanced overrides: %w", err)
		}
		values.Merge(overrides, extra, values.LayerForm, nil)
	}
	values.Merge(overrides, req.Overrides, values.LayerForm, nil)
	if req.ServiceID != "" {
		if err := values.Set(overrides, "serviceId", req.ServiceID); err != nil {
			return nil, err
		}
	}
	if req.LocalPath != "" {
		if err := values.Set(overrides, "model.localPath", req.LocalPath); err != nil {
			return nil, err
		}
	}
	// Affinity rather than nodeSelector: nodeSelector ANDs its labels, so it
	// cannot say "any of these products". A single term with one In expression
	// is the OR. Set at the nodeSelectorTerms path so a preferred rule sitting
	// beside it survives.
	if len(req.GPUProducts) > 0 {
		vals := make([]any, 0, len(req.GPUProducts))
		for _, p := range req.GPUProducts {
			vals = append(vals, p)
		}
		terms := []any{values.Tree{"matchExpressions": []any{values.Tree{
			"key":      v.Requires.ProductLabel(),
			"operator": "In",
			"values":   vals,
		}}}}
		const path = "affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms"
		if err := values.Set(overrides, path, terms); err != nil {
			return nil, err
		}
	}
	// The scaler owns the replica count, so a fixed count cannot ride along
	// beside it: helm renders both and the scaler wins at a time nobody chose.
	// Enforced here rather than in the form, so an upgrade carrying an older
	// plan forward cannot resurrect one either.
	if v, ok := values.Get(overrides, "scaler.enabled"); ok {
		if on, _ := v.(bool); on {
			delete(overrides, "replicaCount")
		}
	}

	var edits values.Tree
	if req.EditsYAML != "" {
		if err := yaml.Unmarshal([]byte(req.EditsYAML), &edits); err != nil {
			return nil, fmt.Errorf("plan edits: %w", err)
		}
	}

	return compose.Compose(compose.Input{
		Catalog:   cat.Fetcher.String(),
		Ref:       cat.Ref,
		Entry:     entry,
		Variant:   v,
		Profile:   *prof,
		Release:   release,
		Namespace: req.Namespace,
		Overrides: overrides,
		Edits:     edits,
	})
}

// carryForward fills a request from a release's last plan, so an upgrade keeps
// the deploy inputs and moves only the model version. It needs no database: the
// whole plan sits beside the release.
func (s *Server) carryForward(ctx context.Context, req planRequest) (planRequest, error) {
	prev, err := s.currentPlan(ctx, req.Namespace, req.FromRelease)
	if err != nil {
		return req, err
	}
	// Release and namespace come from the previous plan and are not overridable.
	// They identify the release being upgraded; changing them does not rename
	// anything, it installs a second release beside the first.
	out := planRequest{
		Model:     prev.Source.Model,
		Version:   req.Version,
		Variant:   prev.Source.Variant,
		Release:   prev.Release.Name,
		Namespace: prev.Release.Namespace,
		Overrides: prev.Layers[values.LayerForm],
	}
	if req.Variant != "" {
		out.Variant = req.Variant
	}

	// A request carrying the form carries all of it. `swiss upgrade` and the
	// version picker send no overrides and mean "move the catalog, keep every
	// setting"; the upgrade form sends the whole form and is authoritative.
	//
	// The overrides merge rather than replace, so a value no form field covers
	// -- set once from a flag or the advanced box -- survives an upgrade instead
	// of being dropped by a form that never knew about it. Edits do replace:
	// the form shows them, so an empty box means the operator emptied it.
	if len(req.Overrides) == 0 {
		if edits := prev.Layers[values.LayerEdit]; len(edits) > 0 {
			out.EditsYAML = mustYAML(edits)
		}
		return out, nil
	}
	values.Merge(out.Overrides, req.Overrides, values.LayerForm, nil)
	out.EditsYAML = req.EditsYAML
	// serviceId is identity, not a setting: the route, the scaler and the SLO
	// are all named after it. Changing it on an upgrade renames none of them --
	// helm renders a new set under the new name and orphans the old ones, on a
	// release that keeps its name either way. Refused rather than ignored, so a
	// caller that meant it learns that a new service is a new deploy.
	if req.ServiceID != "" && req.ServiceID != serviceIDOf(prev) {
		return req, fmt.Errorf(
			"serviceId cannot change on an upgrade: %s is deployed as %q, not %q -- deploy a new release to run a second service",
			prev.Release.Name, serviceIDOf(prev), req.ServiceID)
	}
	if req.LocalPath != "" {
		out.LocalPath = req.LocalPath
	}
	if len(req.GPUProducts) > 0 {
		out.GPUProducts = req.GPUProducts
	}
	return out, nil
}

// serviceIDOf is the identity a release is deployed under. Read from the
// composed values rather than the form layer alone: a plan whose form never set
// one still runs under whatever the catalog or the chart named it, and that is
// the name an upgrade has to keep. Falls back to the release name, which is
// what a chart with no serviceId of its own uses.
func serviceIDOf(p *plan.Plan) string {
	if v, ok := values.Get(p.Values(), "serviceId"); ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return p.Release.Name
}

// currentPlan is the plan a release was last deployed from, read from the
// cluster and nowhere else.
//
// The plan ConfigMap is the source of truth. etcd is replicated and backed up;
// a sqlite file on one ReadWriteOnce volume is neither, so a database row
// claiming to know what is running is a second answer that can disagree with
// the cluster -- and it would disagree exactly when an apply fails between the
// two writes. The database is the audit log, and losing it costs the log.
func (s *Server) currentPlan(ctx context.Context, namespace, release string) (*plan.Plan, error) {
	rel, err := s.releaseRecord(ctx, namespace, release)
	if err != nil {
		return nil, err
	}
	if rel == nil {
		return nil, fmt.Errorf("no release %q", release)
	}
	if len(rel.SwissFiles) == 0 {
		return nil, fmt.Errorf("release %q has no plan beside it -- swiss did not deploy it", release)
	}
	return plan.FromFiles(rel.SwissFiles)
}

// releaseRecord is the live release and whatever swiss recorded beside it, or
// nil when there is no such release. Absent is not an error: callers ask about
// releases that legitimately do not exist yet.
func (s *Server) releaseRecord(ctx context.Context, namespace, release string) (*cluster.Release, error) {
	releases, err := s.probe.Releases(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range releases {
		if r.Name == release && (namespace == "" || r.Namespace == namespace) {
			found := r
			return &found, nil
		}
	}
	return nil, nil
}

func (s *Server) handleReleasePlan(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	p, err := s.currentPlan(ctx, r.PathValue("namespace"), r.PathValue("release"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 5*time.Minute)
	defer cancel()

	p, err := s.planFromRequest(ctx, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	st, err := exec.Lookup(ctx, s.probe, p.Release.Namespace, p.Release.Name)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	// Not audited: a diff changes nothing, and one row per preview buried the
	// applies. What it would have recorded is in the apply's own output, which
	// is helmfile's diff.
	res, err := s.runner().Diff(ctx, p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"planHash": p.Hash,
		"changed":  res.Changed,
		"output":   res.Output,
		// Carry these back so apply can assert nothing moved in between.
		"revision": st.Revision,
		"exists":   st.Exists,
	})
}

func (s *Server) handleApply(mode exec.Mode) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := contextWithTimeout(r, 15*time.Minute)
		defer cancel()

		var req applyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if s.store == nil {
			writeError(w, http.StatusServiceUnavailable, "no database: apply needs a stored plan")
			return
		}
		p, err := s.store.Plan(ctx, req.PlanHash)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}

		s.applyPlan(ctx, w, p, mode, req.ExpectRevision, actionName(mode), req.Note)
	}
}

// applyPlan is the cluster-changing half, shared by apply, install and
// rollback. They differ in where the plan came from and in nothing after that.
func (s *Server) applyPlan(ctx context.Context, w http.ResponseWriter, p *plan.Plan, mode exec.Mode, expectRevision int, action, note string) {
	st, err := exec.Lookup(ctx, s.probe, p.Release.Namespace, p.Release.Name)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := exec.Check(st, mode, p); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err := exec.CheckRevision(st, expectRevision); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	// The plan that produced the live revision is archived before it is
	// overwritten, so every applied revision stays reproducible from the
	// cluster -- which is what rollback reads, and what a lost database must
	// not cost.
	if st.Exists {
		if err := s.archivePlan(ctx, p.Release.Namespace, p.Release.Name, st.Revision); err != nil {
			s.log.WarnContext(ctx, "previous plan not archived", "release", p.Release.Name, "err", err)
		}
	}

	// Write-ahead: the plan is recorded before the cluster changes, so a
	// permissions or quota failure costs nothing. A live release with no
	// plan beside it reads as hand-installed, which is the one thing the
	// reconciliation view must never say about swissd's own work.
	started := time.Now()
	if err := s.writePlan(ctx, p, planStatus{
		Phase: phaseApplying, Action: action, StartedAt: stamp(started), Note: note,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "plan not recorded, nothing applied: "+err.Error())
		return
	}

	// Everything past the write-ahead outlives the request.
	ctx, cancel := detach(ctx)
	defer cancel()

	res, applyErr := s.runner().Apply(ctx, p)

	// Looked up before the audit write, so the row can name the revision this
	// produced -- which is what makes the log a list of rollback targets rather
	// than a list of timestamps. A failed apply names none: helm may have left
	// a revision behind, but it is not one to go back to.
	after, _ := exec.Lookup(ctx, s.probe, p.Release.Namespace, p.Release.Name)
	produced := after.Revision
	if applyErr != nil {
		produced = 0
	}
	s.record(ctx, action, p, res, applyErr, started, note, produced)

	status := planStatus{
		Phase: phaseApplied, Action: action,
		StartedAt: stamp(started), UpdatedAt: stamp(time.Now()),
		Revision: after.Revision, Note: note,
	}
	if applyErr != nil {
		status.Phase, status.Error = phaseFailed, applyErr.Error()
	}
	// Best effort: the plan is already recorded, only the phase goes stale.
	var statusErr string
	if err := s.writePlan(ctx, p, status); err != nil {
		statusErr = err.Error()
		s.log.ErrorContext(ctx, "plan status not updated", "release", p.Release.Name, "err", err)
	}
	if applyErr != nil {
		writeError(w, http.StatusInternalServerError, applyErr.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"planHash":    p.Hash,
		"release":     p.Release.Name,
		"revision":    after.Revision,
		"output":      res.Output,
		"status":      phaseApplied,
		"statusError": statusErr,
	})
}

// handleUninstall removes a release and the plan recorded beside it.
//
// The order is deliberate and is the write-ahead in reverse. An apply records
// the plan first so a failure cannot leave a live release with nothing beside
// it; an uninstall removes the release first for the same reason. Dropping the
// ConfigMap up front and then failing to uninstall would turn a release swiss
// deployed into an `untracked` row -- the one thing that is supposed to mean
// somebody installed by hand.
func (s *Server) handleUninstall(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Minute)
	defer cancel()

	ns, release := r.PathValue("namespace"), r.PathValue("release")
	st, err := exec.Lookup(ctx, s.probe, ns, release)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := exec.CheckUninstall(st, ns, release); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	// Read the plan before the release goes, only to name it in the audit row.
	// A release with no readable plan is still removable -- see Runner.Uninstall.
	planHash := ""
	if p, err := s.currentPlan(ctx, ns, release); err == nil {
		planHash = p.Hash
	}

	// The uninstall outlives the request, like an apply: a browser navigating
	// away must not SIGKILL helm halfway through deleting a release.
	started := time.Now()
	ctx, cancel = detach(ctx)
	defer cancel()

	res, uninstallErr := s.runner().Uninstall(ctx, ns, release)
	s.recordRelease(ctx, "uninstall", ns, release, planHash, res, uninstallErr, started, "", 0)
	if uninstallErr != nil {
		writeError(w, http.StatusInternalServerError, uninstallErr.Error())
		return
	}

	// Best effort, and reported rather than swallowed: the release is gone
	// either way, and a stray plan ConfigMap shows up in the deployments view
	// as a plan with no release rather than as anything dangerous.
	var planErr string
	if s.writer != nil {
		// The history goes with it: one Secret per revision would otherwise be
		// left behind for a release that no longer exists.
		if revs, err := s.archivedRevisions(ctx, ns, release); err == nil {
			for _, r := range revs {
				if err := s.writer.DeleteSecret(ctx, archiveRef(ns, release, r)); err != nil {
					s.log.WarnContext(ctx, "archived plan not removed", "release", release, "revision", r, "err", err)
				}
			}
		}
		if err := s.writer.DeleteConfigMap(ctx, planRef(ns, release)); err != nil {
			planErr = err.Error()
			s.log.ErrorContext(ctx, "plan configmap not removed", "release", release, "err", err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"release":   release,
		"namespace": ns,
		"output":    res.Output,
		"planError": planErr,
	})
}

const (
	phaseApplying = "applying"
	phaseApplied  = "applied"
	phaseFailed   = "failed"
)

// applyBudget bounds the cluster-changing half of an apply, which runs detached
// from the request. It is a deadlock guard, not a rollout timeout: helmDefaults
// set wait: false, so helmfile returns once the upgrade is accepted rather than
// once 40 minutes of weights have loaded.
const applyBudget = 15 * time.Minute

// detach is the context the cluster-changing half of an apply runs under: the
// caller's values, none of the caller's cancellation, and a deadline of its own.
//
// helmfile runs under exec.CommandContext, so while this was the request's
// context a browser navigating away SIGKILLed helm mid-upgrade -- leaving the
// release in pending-upgrade, which Check then refuses until someone runs
// `helm rollback` by hand. A client disconnect must not be able to wedge a
// release. The bookkeeping after the apply is detached for the same reason: a
// cancelled context there drops the audit row and strands status.yaml on
// "applying", describing an apply that in fact finished.
func detach(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), applyBudget)
}

type planStatus struct {
	Phase     string `yaml:"phase" json:"phase"`
	Action    string `yaml:"action,omitempty" json:"action,omitempty"`
	Revision  int    `yaml:"revision,omitempty" json:"revision,omitempty"`
	StartedAt string `yaml:"startedAt,omitempty" json:"startedAt,omitempty"`
	UpdatedAt string `yaml:"updatedAt,omitempty" json:"updatedAt,omitempty"`
	Error     string `yaml:"error,omitempty" json:"error,omitempty"`
	// Note is why this was done. Written here as well as to the audit log
	// because etcd is the half that is backed up: the reason a release was
	// rolled back outlives the sqlite file it was also recorded in.
	Note string `yaml:"note,omitempty" json:"note,omitempty"`
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// planRef is the live workspace beside a release -- a ConfigMap, because that
// one is mounted. History is a Secret per revision, named and labelled the way
// helm names and labels its own: an archive is read, never mounted.
func planRef(namespace, release string) string {
	return namespace + "/" + cluster.PlanConfigMapPrefix + release
}

func archiveRef(namespace, release string, revision int) string {
	return fmt.Sprintf("%s/%s%s.v%d", namespace, cluster.PlanSecretPrefix, release, revision)
}

func archiveLabels(release string, revision int) map[string]string {
	return map[string]string{
		"owner":                        "swiss",
		"name":                         release,
		"version":                      strconv.Itoa(revision),
		"app.kubernetes.io/managed-by": "swiss",
	}
}

func archiveSelector(release string) string { return "owner=swiss,name=" + release }

// archivePlan copies the live workspace to the revision it produced, then drops
// whatever has fallen out of retention.
func (s *Server) archivePlan(ctx context.Context, namespace, release string, revision int) error {
	if s.writer == nil || revision <= 0 {
		return nil
	}
	data, err := s.probe.ConfigMap(ctx, planRef(namespace, release))
	if err != nil {
		return err
	}
	if len(data) == 0 {
		// Nothing beside the release: it was not deployed by swiss, so there is
		// no workspace to keep.
		return nil
	}
	if err := s.writer.PutSecret(ctx, archiveRef(namespace, release, revision), data, archiveLabels(release, revision)); err != nil {
		return err
	}
	return s.pruneArchives(ctx, namespace, release, revision)
}

// pruneArchives keeps the newest planHistory revisions. It lists rather than
// deleting revision-N, because revisions are not always contiguous: a failed
// apply still advances helm's counter.
func (s *Server) pruneArchives(ctx context.Context, namespace, release string, newest int) error {
	keep := s.cfg.Server.PlanHistory
	revs, err := s.archivedRevisions(ctx, namespace, release)
	if err != nil || len(revs) <= keep {
		return err
	}
	for _, r := range revs[keep:] {
		if err := s.writer.DeleteSecret(ctx, archiveRef(namespace, release, r)); err != nil {
			return err
		}
	}
	return nil
}

// archivedRevisions is every revision with a plan beside it, newest first.
func (s *Server) archivedRevisions(ctx context.Context, namespace, release string) ([]int, error) {
	names, err := s.probe.SecretNames(ctx, namespace, archiveSelector(release))
	if err != nil {
		return nil, err
	}
	prefix := cluster.PlanSecretPrefix + release + ".v"
	var out []int
	for _, name := range names {
		n, err := strconv.Atoi(strings.TrimPrefix(name, prefix))
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(out)))
	return out, nil
}

func (s *Server) writePlan(ctx context.Context, p *plan.Plan, st planStatus) error {
	if s.writer == nil {
		return fmt.Errorf("no cluster writer")
	}
	// The ConfigMap is the workspace: mounted, its keys are exactly the files
	// helmfile is run against. status.yaml is the one addition -- how the last
	// apply ended, which no file helmfile reads would carry.
	files, err := p.Files("")
	if err != nil {
		return err
	}
	status, err := yaml.Marshal(st)
	if err != nil {
		return err
	}
	files["status.yaml"] = string(status)
	return s.writer.PutConfigMap(ctx, planRef(p.Release.Namespace, p.Release.Name), files)
}

func (s *Server) planFromRequest(ctx context.Context, r *http.Request) (*plan.Plan, error) {
	var body struct {
		planRequest
		PlanHash string `json:"planHash,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.PlanHash != "" {
		if s.store == nil {
			return nil, fmt.Errorf("no database: pass a model instead of a planHash")
		}
		return s.store.Plan(ctx, body.PlanHash)
	}
	return s.compose(ctx, body.planRequest)
}

func (s *Server) runner() exec.Runner {
	return exec.Runner{HelmBin: s.cfg.Server.HelmBin, HelmfileBin: s.cfg.Server.HelmfileBin}
}

func (s *Server) record(ctx context.Context, action string, p *plan.Plan, res exec.Result, err error, started time.Time, note string, revision int) {
	s.recordRelease(ctx, action, p.Release.Namespace, p.Release.Name, p.Hash, res, err, started, note, revision)
}

// recordRelease is the audit write for operations that name a release rather
// than a plan. Uninstall is the only one: it needs no plan to run, so it cannot
// always supply a hash, and an empty one is honest rather than missing.
func (s *Server) recordRelease(ctx context.Context, action, namespace, release, planHash string, res exec.Result, err error, started time.Time, note string, revision int) {
	if s.store == nil {
		return
	}
	run := store.Run{
		Namespace: namespace, Release: release,
		Action: action, PlanHash: planHash, Changed: res.Changed, Output: res.Output,
		Note: note, Revision: revision,
		StartedAt: started.UTC().Format(time.RFC3339), EndedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err != nil {
		run.Error = err.Error()
	}
	if _, e := s.store.RecordRun(ctx, run); e != nil {
		s.log.ErrorContext(ctx, "run not recorded", "err", e)
	}
}

func actionName(m exec.Mode) string {
	if m == exec.Install {
		return "install"
	}
	return "apply"
}

// handleRuns serves the operation log: what was attempted, and how it ended.
//
// A swissd with no database serves an empty log rather than an error. Losing
// the volume costs the history and nothing else -- the deployments view still
// answers what is running, because that is read from the cluster.
func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSON(w, http.StatusOK, map[string]any{"runs": []any{}, "hasStore": false})
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	runs, err := s.store.RunsFiltered(ctx, store.RunFilter{
		Namespace: q.Get("namespace"),
		Release:   q.Get("release"),
		Action:    q.Get("action"),
		Limit:     limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if runs == nil {
		runs = []store.Run{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs, "hasStore": true})
}

// handleRun is one row with its output -- the helmfile stderr behind a failed
// apply, which lives nowhere else once the process is gone.
func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "no database: this swissd keeps no operation log")
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "run id must be a number")
		return
	}
	run, err := s.store.Run(ctx, id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func mustYAML(t values.Tree) string {
	b, err := yaml.Marshal(t)
	if err != nil {
		return ""
	}
	return string(b)
}
