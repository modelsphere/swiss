package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/modelsphere/swiss/internal/chart"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/compose"
	"github.com/modelsphere/swiss/internal/exec"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/store"
	"github.com/modelsphere/swiss/internal/values"
	"gopkg.in/yaml.v3"
)

type planRequest struct {
	// FromRelease recomposes a deployed release: its stored plan supplies the
	// form layer, the model and the variant, and only the catalog layer moves.
	FromRelease string `json:"fromRelease,omitempty"`
	// Catalog names the configured catalog to compose from. Required when the
	// site lists several. An upgrade naming none stays on the one its release
	// came from; naming another moves the release there, same model id.
	Catalog   string `json:"catalog,omitempty"`
	Model     string `json:"model"`
	Version   string `json:"version,omitempty"`
	Variant   string `json:"variant,omitempty"`
	Release   string `json:"release,omitempty"`
	Namespace string `json:"namespace,omitempty"`
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
	// CreateNamespace composes helmfile's createNamespace into the plan, on top
	// of the site profile's own setting. helm is what creates the namespace;
	// this is where the plan says so.
	CreateNamespace bool `json:"createNamespace,omitempty"`
	// ChartVersion must fall in the variant's chart.version. Empty resolves as
	// chart.Resolve does.
	ChartVersion string `json:"chartVersion,omitempty"`

	// Set by carryForward, never by the caller.
	runningChart plan.ChartRef
	movedFrom    *plan.Plan
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
	// ForceConflicts lets the upgrade take fields another manager owns, which
	// is what a hand `kubectl edit` leaves behind. Per apply, never stored in
	// the plan -- see exec.ApplyOptions.
	ForceConflicts bool `json:"forceConflicts,omitempty"`
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
		code := http.StatusBadRequest
		if errors.As(err, new(*chart.ListError)) {
			code = http.StatusBadGateway
		}
		writeError(w, code, err.Error())
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
	cat, repo, err := s.Catalog(ctx, req.Catalog)
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
	if req.movedFrom != nil {
		if err := s.checkMove(ctx, req.movedFrom, repo.Name, entry, v); err != nil {
			return nil, err
		}
	}
	prof, err := s.Profile(ctx)
	if err != nil {
		return nil, err
	}
	chartVersion, err := s.resolveChart(ctx, prof, v.Chart, req.ChartVersion, req.runningChart)
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
		Catalog:         cat.Fetcher.String(),
		CatalogName:     repo.Name,
		Ref:             cat.Ref,
		Entry:           entry,
		Variant:         v,
		ChartVersion:    chartVersion,
		Profile:         *prof,
		Release:         release,
		Namespace:       req.Namespace,
		Overrides:       overrides,
		Edits:           edits,
		CreateNamespace: req.CreateNamespace,
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
	// Naming no catalog stays on the release's own; naming another moves it
	// there, gated in compose by checkMove once the target entry is known.
	own, ownErr := s.ReleaseCatalog(ctx, prev.Source.CatalogName, prev.Source.Catalog)
	catalogName := req.Catalog
	var movedFrom *plan.Plan
	switch {
	case catalogName == "":
		if ownErr != nil {
			return req, fmt.Errorf("cannot upgrade %s: %w", prev.Release.Name, ownErr)
		}
		catalogName = own.Name
	case ownErr != nil || own.Name != catalogName:
		movedFrom = prev
	}
	out := planRequest{
		Catalog:      catalogName,
		Model:        prev.Source.Model,
		Version:      req.Version,
		Variant:      prev.Source.Variant,
		Release:      prev.Release.Name,
		Namespace:    prev.Release.Namespace,
		Overrides:    prev.Layers[values.LayerForm],
		ChartVersion: req.ChartVersion,
		runningChart: prev.Chart,
		movedFrom:    movedFrom,
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

// checkMove refuses a cross-catalog upgrade that would put another model, or the
// same one on another engine, behind the release's existing serviceId and route.
// Model ids are only unique within a catalog; HF is the identity across them.
func (s *Server) checkMove(ctx context.Context, prev *plan.Plan, to string, entry catalog.Entry, v catalog.Variant) error {
	refuse := func(format string, args ...any) error {
		return fmt.Errorf("cannot move %s to catalog %q: %s -- deploy a new release instead", prev.Release.Name, to, fmt.Sprintf(format, args...))
	}
	hf := prev.Source.HF
	if hf == "" {
		var err error
		if hf, err = s.recordedHF(ctx, prev); err != nil {
			return refuse("its plan predates recording the model's HF repo, and its own catalog could not supply it (%v)", err)
		}
	}
	if entry.Source.HF != hf {
		return refuse("%s there is %s, not %s, so it is a different model", entry.Name, entry.Source.HF, hf)
	}
	if v.Engine != prev.Engine {
		return refuse("variant %s runs on %s there, not %s", v.ID, v.Engine, prev.Engine)
	}
	return nil
}

// recordedHF reads the HF repo of a plan written before plans recorded it, from
// the catalog the plan names or sits at -- never the default, which may be
// another catalog with the same model name. Best effort, and bounded so a dead
// catalog costs a refusal rather than the request's whole timeout.
func (s *Server) recordedHF(ctx context.Context, prev *plan.Plan) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	name := prev.Source.CatalogName
	if name == "" {
		repo, ok := s.CatalogFrom(ctx, prev.Source.Catalog)
		if !ok {
			return "", fmt.Errorf("the site lists no catalog at %s", prev.Source.Catalog)
		}
		name = repo.Name
	}
	cat, _, err := s.Catalog(ctx, name)
	if err != nil {
		return "", err
	}
	m, ok := cat.Index.Model(prev.Source.Model)
	if !ok || m.Source.HF == "" {
		return "", fmt.Errorf("catalog %q has no %s", name, prev.Source.Model)
	}
	return m.Source.HF, nil
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
	b, err := s.readBackend(ctx, namespace, release)
	if err != nil {
		return nil, err
	}
	return b.Current(ctx, namespace, release)
}

// planOf decodes the plan beside a release record already in hand. Split out so
// a caller holding the record does not look the release up again to get it.
func planOf(rel *cluster.Release) (*plan.Plan, error) {
	if rel == nil {
		return nil, fmt.Errorf("no such release")
	}
	if len(rel.SwissFiles) == 0 {
		return nil, fmt.Errorf("release %q has no plan beside it -- swiss did not deploy it", rel.Name)
	}
	return plan.FromFiles(rel.SwissFiles)
}

// releaseRecord is the live release and whatever swiss recorded beside it, or
// nil when there is no such release. Absent is not an error: callers ask about
// releases that legitimately do not exist yet.
//
// An empty namespace means the site's, the same default compose applies. It
// used to mean "search every namespace in scope for this name", which made every
// status poll on the detail page a full cluster read -- twice over, once the
// plan was fetched too -- and would have answered with whichever namespace
// happened to sort first if two held the same release name.
func (s *Server) releaseRecord(ctx context.Context, namespace, release string) (*cluster.Release, error) {
	namespace, err := s.resolveNamespace(ctx, namespace, release)
	if err != nil {
		return nil, err
	}
	return s.probe.Release(ctx, namespace, release)
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
		s.applyPlan(ctx, w, p, mode, req.ExpectRevision, actionName(mode), req.Note, req.ForceConflicts)
	}
}

// applyPlan is the cluster-changing half, shared by apply, install and
// rollback. They differ in where the plan came from and in nothing after that.
func (s *Server) applyPlan(ctx context.Context, w http.ResponseWriter, p *plan.Plan, mode exec.Mode, expectRevision int, action, note string, forceConflicts bool) {
	b, err := s.backendForApply(ctx, p, mode)
	if err != nil {
		writeStatus(w, err, http.StatusBadGateway)
		return
	}
	res, err := b.Apply(ctx, p, mode, applyOpts{
		ExpectRevision: expectRevision,
		Action:         action,
		Note:           note,
		ForceConflicts: forceConflicts,
	})
	if err != nil {
		writeStatus(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"planHash":    p.Hash,
		"release":     p.Release.Name,
		"revision":    res.Revision,
		"output":      res.Output,
		"status":      res.Status,
		"statusError": res.StatusError,
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
	b, _, err := s.backendFor(ctx, ns, release)
	if err != nil {
		writeStatus(w, err, http.StatusBadGateway)
		return
	}
	if b == nil {
		b = s.helmBackend()
	}
	res, err := b.Uninstall(ctx, ns, release)
	if err != nil {
		writeStatus(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"release":   release,
		"namespace": ns,
		"output":    res.Output,
		"planError": res.PlanError,
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
	// Forced records that this apply took fields another manager owned. It
	// belongs beside the release rather than only in the plan, because the plan
	// deliberately does not carry it: this says the hand edits that were on
	// this release are gone, and which apply removed them.
	Forced bool `yaml:"forced,omitempty" json:"forced,omitempty"`
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
