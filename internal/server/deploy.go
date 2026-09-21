package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	ServiceID string      `json:"serviceId,omitempty"`
	LocalPath string      `json:"localPath,omitempty"`
	Overrides values.Tree `json:"overrides,omitempty"`
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
	release := req.Release
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
	out := planRequest{
		Model:     prev.Source.Model,
		Version:   req.Version,
		Variant:   prev.Source.Variant,
		Release:   prev.Release.Name,
		Namespace: prev.Release.Namespace,
		Overrides: prev.Overrides,
	}
	if len(prev.Edits) > 0 {
		out.EditsYAML = mustYAML(prev.Edits)
	}
	if req.Variant != "" {
		out.Variant = req.Variant
	}
	return out, nil
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
	if rel.SwissPlan == nil {
		return nil, fmt.Errorf("release %q has no plan beside it -- swiss did not deploy it", release)
	}
	return plan.ParseYAML(rel.SwissPlan)
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

	started := time.Now()
	res, err := s.runner().Diff(ctx, p)
	s.record(ctx, "diff", p, res, err, started)
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

		st, err := exec.Lookup(ctx, s.probe, p.Release.Namespace, p.Release.Name)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		if err := exec.Check(st, mode, p); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if err := exec.CheckRevision(st, req.ExpectRevision); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}

		// Write-ahead: the plan is recorded before the cluster changes, so a
		// permissions or quota failure costs nothing. A live release with no
		// plan beside it reads as hand-installed, which is the one thing the
		// reconciliation view must never say about swissd's own work.
		started := time.Now()
		if err := s.writePlan(ctx, p, planStatus{
			Phase: phaseApplying, Action: actionName(mode), StartedAt: stamp(started),
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "plan not recorded, nothing applied: "+err.Error())
			return
		}

		// Everything past the write-ahead outlives the request.
		ctx, cancel = detach(ctx)
		defer cancel()

		res, applyErr := s.runner().Apply(ctx, p)
		s.record(ctx, actionName(mode), p, res, applyErr, started)

		after, _ := exec.Lookup(ctx, s.probe, p.Release.Namespace, p.Release.Name)

		status := planStatus{
			Phase: phaseApplied, Action: actionName(mode),
			StartedAt: stamp(started), UpdatedAt: stamp(time.Now()),
			Revision: after.Revision,
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
	s.recordRelease(ctx, "uninstall", ns, release, planHash, res, uninstallErr, started)
	if uninstallErr != nil {
		writeError(w, http.StatusInternalServerError, uninstallErr.Error())
		return
	}

	// Best effort, and reported rather than swallowed: the release is gone
	// either way, and a stray plan ConfigMap shows up in the deployments view
	// as a plan with no release rather than as anything dangerous.
	var planErr string
	if s.writer != nil {
		ref := ns + "/" + cluster.PlanConfigMapPrefix + release
		if err := s.writer.DeleteConfigMap(ctx, ref); err != nil {
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
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func (s *Server) writePlan(ctx context.Context, p *plan.Plan, st planStatus) error {
	if s.writer == nil {
		return fmt.Errorf("no cluster writer")
	}
	doc, err := p.YAML()
	if err != nil {
		return err
	}
	status, err := yaml.Marshal(st)
	if err != nil {
		return err
	}
	ref := p.Release.Namespace + "/" + cluster.PlanConfigMapPrefix + p.Release.Name
	return s.writer.PutConfigMap(ctx, ref, map[string]string{
		"plan.yaml":   string(doc),
		"status.yaml": string(status),
	})
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

func (s *Server) record(ctx context.Context, action string, p *plan.Plan, res exec.Result, err error, started time.Time) {
	s.recordRelease(ctx, action, p.Release.Namespace, p.Release.Name, p.Hash, res, err, started)
}

// recordRelease is the audit write for operations that name a release rather
// than a plan. Uninstall is the only one: it needs no plan to run, so it cannot
// always supply a hash, and an empty one is honest rather than missing.
func (s *Server) recordRelease(ctx context.Context, action, namespace, release, planHash string, res exec.Result, err error, started time.Time) {
	if s.store == nil {
		return
	}
	run := store.Run{
		Namespace: namespace, Release: release,
		Action: action, PlanHash: planHash, Changed: res.Changed, Output: res.Output,
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

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSON(w, http.StatusOK, map[string]any{"runs": []any{}})
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	runs, err := s.store.Runs(ctx, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func mustYAML(t values.Tree) string {
	b, err := yaml.Marshal(t)
	if err != nil {
		return ""
	}
	return string(b)
}
