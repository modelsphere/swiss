package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/modelsphere/swiss/internal/exec"
	"github.com/modelsphere/swiss/internal/llmsvc"
	"github.com/modelsphere/swiss/internal/plan"
)

// llmBackend stores the release as an LLMService. The operator runs helm.
type llmBackend struct{ s *Server }

// errNoSuchRelease is what Current returns for a missing LLMService.
// Uninstall matches it with errors.Is; the text stays "no such release".
var errNoSuchRelease = errors.New("no such release")

func (b llmBackend) Name() string { return backendLLMSVC }

func (b llmBackend) Current(ctx context.Context, ns, release string) (*plan.Plan, error) {
	ns, err := b.s.resolveNamespace(ctx, ns, release)
	if err != nil {
		return nil, err
	}
	u, err := b.s.llms.Get(ctx, ns, release)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, errNoSuchRelease
		}
		return nil, err
	}
	return llmsvc.ToPlan(u)
}

func (b llmBackend) Revisions(ctx context.Context, ns, release string) ([]revision, error) {
	ns, err := b.s.resolveNamespace(ctx, ns, release)
	if err != nil {
		return nil, err
	}
	u, err := b.s.llms.Get(ctx, ns, release)
	if err != nil {
		return nil, err
	}
	obj, err := llmsvc.FromUnstructured(u)
	if err != nil {
		return nil, err
	}
	var history []llmsvc.HistoryEntry
	var helmRev int64
	if obj.Status != nil {
		history = obj.Status.History
		if obj.Status.Helm != nil {
			helmRev = obj.Status.Helm.Revision
		}
	}

	out := make([]revision, 0, len(history))
	seen := map[int]bool{}
	for _, h := range history {
		row := revision{
			Revision: int(h.Revision),
			PlanHash: h.Hash,
			Current:  h.Revision == helmRev,
		}
		if h.ControllerRevision != "" {
			crs, err := b.s.llms.ControllerRevisions(ctx, ns, []string{h.ControllerRevision})
			if err != nil && !apierrors.IsNotFound(err) {
				return nil, err
			}
			if err == nil && len(crs) == 1 {
				if snap, err := decodeRevision(crs[0]); err == nil {
					fillRevision(&row, snap)
				}
			}
		}
		seen[row.Revision] = true
		out = append(out, row)
	}

	// Legacy archives stay readable. They are listed after the operator's
	// history and are not a migration.
	archived, err := b.s.archivedRevisions(ctx, ns, release)
	if err != nil {
		return nil, err
	}
	for _, n := range archived {
		if seen[n] {
			continue
		}
		row := revision{Revision: n, Current: int64(n) == helmRev}
		if data, err := b.s.probe.Secret(ctx, archiveRef(ns, release, n)); err == nil {
			if sum, err := parsePlanSummary([]byte(data[plan.MetaFile])); err == nil {
				row.Model, row.Version, row.Variant = sum.Model, sum.Version, sum.Variant
				row.Chart = sum.Chart
				row.PlanHash = sum.Hash
			}
		}
		out = append(out, row)
	}
	return out, nil
}

func (b llmBackend) Archived(ctx context.Context, ns, release string, rev int) (*plan.Plan, error) {
	ns, err := b.s.resolveNamespace(ctx, ns, release)
	if err != nil {
		return nil, err
	}
	u, err := b.s.llms.Get(ctx, ns, release)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return b.s.planSecret(ctx, ns, release, rev)
		}
		return nil, err
	}
	obj, err := llmsvc.FromUnstructured(u)
	if err != nil {
		return nil, err
	}
	if obj.Status != nil {
		for _, h := range obj.Status.History {
			if int(h.Revision) != rev {
				continue
			}
			if h.ControllerRevision == "" {
				break
			}
			crs, err := b.s.llms.ControllerRevisions(ctx, ns, []string{h.ControllerRevision})
			if err != nil {
				return nil, err
			}
			if len(crs) == 0 {
				break
			}
			return planFromRevision(ns, release, crs[0])
		}
	}
	return b.s.planSecret(ctx, ns, release, rev)
}

func (b llmBackend) Apply(ctx context.Context, p *plan.Plan, mode exec.Mode, o applyOpts) (applyResult, error) {
	if err := b.requirePlanSchemaV2(p); err != nil {
		return applyResult{}, err
	}
	if p.Chart.Path != "" {
		return applyResult{}, failErr(http.StatusBadRequest, llmsvc.ErrChartPath)
	}
	s := b.s
	ref := p.Release.Namespace + "/" + p.Release.Name

	if mode == exec.Install {
		st, err := exec.Lookup(ctx, s.probe, p.Release.Namespace, p.Release.Name)
		if err != nil {
			return applyResult{}, failErr(http.StatusBadGateway, err)
		}
		if err := exec.Check(st, mode, p); err != nil {
			return applyResult{}, failErr(http.StatusConflict, err)
		}
		if err := exec.CheckRevision(st, o.ExpectRevision); err != nil {
			return applyResult{}, failErr(http.StatusConflict, err)
		}
	}

	var cur *unstructured.Unstructured
	failed := false
	if mode != exec.Install {
		var err error
		cur, err = s.llms.Get(ctx, p.Release.Namespace, p.Release.Name)
		if apierrors.IsNotFound(err) {
			return applyResult{}, fail(http.StatusConflict, "no release %s: use `swiss install` to create it", ref)
		}
		if err != nil {
			return applyResult{}, failErr(http.StatusBadGateway, err)
		}
		if llmsvc.IsExternal(cur) {
			return applyResult{}, fail(http.StatusConflict, "release %s is an external LLMService: swiss did not deploy it, so it cannot be upgraded", ref)
		}
		obj, err := llmsvc.FromUnstructured(cur)
		if err != nil {
			return applyResult{}, failErr(http.StatusBadGateway, err)
		}
		phase, helmRev := "", 0
		if obj.Status != nil {
			phase = obj.Status.Phase
			if obj.Status.Helm != nil {
				helmRev = int(obj.Status.Helm.Revision)
			}
		}
		if phase == llmsvc.PhaseApplying {
			return applyResult{}, fail(http.StatusConflict, "release %s is %s: wait for it, or `helm rollback` first", ref, phase)
		}
		failed = phase == llmsvc.PhaseFailed
		if err := exec.CheckRevision(exec.State{Exists: true, Revision: helmRev}, o.ExpectRevision); err != nil {
			return applyResult{}, failErr(http.StatusConflict, err)
		}
	}

	if mode == exec.Install && p.CreateNamespace && s.writer != nil {
		if err := s.writer.EnsureNamespace(ctx, p.Release.Namespace); err != nil {
			return applyResult{}, fail(http.StatusInternalServerError, "namespace not created, nothing applied: %s", err.Error())
		}
	}

	fresh, err := llmsvc.FromPlan(p, o.Action, o.Note)
	if err != nil {
		return applyResult{}, failErr(fromPlanStatus(err), err)
	}
	// Spec decides the generation. The force-conflicts annotation is named
	// after that generation, so it is applied after the prediction.
	pred := predictedGeneration(cur, fresh)
	force := ""
	if o.ForceConflicts {
		force = strconv.FormatInt(pred, 10)
	}

	started := time.Now()
	ctx, cancel := detach(ctx)
	defer cancel()
	if o.ForceConflicts {
		s.log.WarnContext(ctx, "applying with --force-conflicts: fields owned by another manager will be overwritten",
			"release", p.Release.Name, "namespace", p.Release.Namespace, "action", o.Action)
	}

	var wrote *unstructured.Unstructured
	if cur == nil {
		if force != "" {
			anns := fresh.GetAnnotations()
			if anns == nil {
				anns = map[string]string{}
			}
			anns[llmsvc.AnnForceConflicts] = force
			fresh.SetAnnotations(anns)
		}
		wrote, err = s.llms.Create(ctx, fresh)
		if apierrors.IsAlreadyExists(err) {
			return applyResult{}, fail(http.StatusConflict, "release %s already exists at revision %d: use `swiss apply` to upgrade it", ref, b.helmRevision(ctx, p.Release.Namespace, p.Release.Name))
		}
	} else {
		cur.Object["spec"] = fresh.Object["spec"]
		cur.SetAnnotations(mergeReleaseAnnotations(cur.GetAnnotations(), fresh.GetAnnotations(), force))
		wrote, err = s.llms.Update(ctx, cur)
		if apierrors.IsConflict(err) {
			return applyResult{}, failErr(http.StatusConflict, &exec.RevisionConflictError{Msg: "the release moved under you; diff again"})
		}
	}
	if err != nil {
		return applyResult{}, failErr(http.StatusBadGateway, err)
	}

	if o.ForceConflicts && strconv.FormatInt(wrote.GetGeneration(), 10) != wrote.GetAnnotations()[llmsvc.AnnForceConflicts] {
		patched, perr := b.patchForce(ctx, wrote.GetNamespace(), wrote.GetName(), wrote.GetGeneration())
		if perr != nil {
			s.log.ErrorContext(ctx, "force-conflicts annotation not patched", "release", p.Release.Name, "err", perr)
		} else {
			wrote = patched
		}
	}

	// A write that changes only annotations leaves the generation alone, so a
	// failed release's status is already "observed" -- the last attempt's.
	// Turning skip-schema-validation on for a release its schema refused is
	// exactly that write. The operator marks it Applying before it tries
	// again, so wait for a status written after ours.
	after := ""
	if failed && wrote.GetGeneration() == cur.GetGeneration() && wrote.GetResourceVersion() != cur.GetResourceVersion() {
		after = wrote.GetResourceVersion()
	}
	got, waitErr := s.llms.WaitApplied(ctx, p.Release.Namespace, p.Release.Name, wrote.GetGeneration(), after)
	summary := ""
	var obj *llmsvc.LLMService
	if got != nil {
		obj, _ = llmsvc.FromUnstructured(got)
		summary = llmSummary(obj)
	}
	var applyErr error
	produced := 0
	switch {
	case waitErr != nil:
		applyErr = waitErr
		if summary == "" {
			summary = waitErr.Error()
		}
	case obj != nil && obj.Status != nil && obj.Status.Phase == llmsvc.PhaseFailed:
		msg := obj.Status.Message
		if msg == "" {
			msg = "llmservice " + ref + " failed"
		}
		applyErr = fmt.Errorf("%s", msg)
	case obj != nil && obj.Status != nil && obj.Status.Helm != nil:
		produced = int(obj.Status.Helm.Revision)
	}
	s.record(ctx, o.Action, p, exec.Result{Output: summary}, applyErr, started, o.Note, func() int {
		if applyErr != nil {
			return 0
		}
		return produced
	}())
	if applyErr != nil {
		return applyResult{}, failErr(http.StatusInternalServerError, applyErr)
	}
	return applyResult{Revision: produced, Output: summary, Status: phaseApplied}, nil
}

func (b llmBackend) Uninstall(ctx context.Context, ns, release string) (uninstallResult, error) {
	s := b.s
	ns, err := s.resolveNamespace(ctx, ns, release)
	if err != nil {
		return uninstallResult{}, failErr(http.StatusBadGateway, err)
	}
	if s.llms == nil {
		return uninstallResult{}, fail(http.StatusBadGateway, "no LLMService client")
	}
	u, err := s.llms.Get(ctx, ns, release)
	if apierrors.IsNotFound(err) {
		return uninstallResult{}, fail(http.StatusNotFound, "no release %s/%s", ns, release)
	}
	if err != nil {
		return uninstallResult{}, failErr(http.StatusBadGateway, err)
	}
	// The operator's finalizer holds the object through route removal and pod
	// termination. This call returns once that delete is accepted; the reap
	// drops swiss's records when the object is finally gone.
	if !u.GetDeletionTimestamp().IsZero() {
		s.trackLLMUninstall(ns, release, u.GetAnnotations()[llmsvc.AnnPlanHash], u.GetDeletionTimestamp().Time)
		return uninstallResult{Output: uninstallInProgress}, nil
	}

	planHash := ""
	if p, err := b.Current(ctx, ns, release); err == nil {
		planHash = p.Hash
	} else if errors.Is(err, errNoSuchRelease) {
		return uninstallResult{}, fail(http.StatusNotFound, "no release %s/%s", ns, release)
	}

	prev, err := s.snapshotPlan(ctx, ns, release)
	if err != nil {
		return uninstallResult{}, fail(http.StatusInternalServerError, "plan not marked, nothing uninstalled: %s", err.Error())
	}
	hadPlan, err := s.markUninstalling(ctx, ns, release)
	if err != nil {
		return uninstallResult{}, fail(http.StatusInternalServerError, "plan not marked, nothing uninstalled: %s", err.Error())
	}

	started := time.Now()
	delErr := s.llms.Delete(ctx, ns, release)
	if apierrors.IsNotFound(delErr) {
		if hadPlan {
			if rerr := s.restorePlan(ctx, ns, release, prev); rerr != nil {
				s.log.ErrorContext(ctx, "plan not restored after delete NotFound", "namespace", ns, "release", release, "err", rerr)
			}
		}
		s.forgetUninstalling(ns, release)
		return uninstallResult{}, fail(http.StatusNotFound, "no release %s/%s", ns, release)
	}
	out := llmDeleteRequested(ns, release)
	if delErr != nil {
		s.recordRelease(ctx, "uninstall", ns, release, planHash, exec.Result{Output: out}, delErr, started, "", 0)
		if hadPlan {
			if ferr := s.finishUninstallFailed(ctx, ns, release, delErr); ferr != nil {
				s.log.ErrorContext(ctx, "plan not marked failed", "namespace", ns, "release", release, "err", ferr)
			}
		}
		return uninstallResult{}, failErr(http.StatusInternalServerError, delErr)
	}
	// Record before tracking. The object may already be gone, and a reap that
	// sees the tracking first would write the final row ahead of this one.
	s.recordRelease(ctx, "uninstall", ns, release, planHash, exec.Result{Output: out}, nil, started, "", 0)
	s.trackLLMUninstall(ns, release, planHash, started)
	return uninstallResult{Output: uninstallInProgress}, nil
}

func (b llmBackend) helmRevision(ctx context.Context, ns, name string) int {
	u, err := b.s.llms.Get(ctx, ns, name)
	if err != nil {
		return 0
	}
	obj, err := llmsvc.FromUnstructured(u)
	if err != nil || obj.Status == nil || obj.Status.Helm == nil {
		return 0
	}
	return int(obj.Status.Helm.Revision)
}

func (b llmBackend) patchForce(ctx context.Context, ns, name string, generation int64) (*unstructured.Unstructured, error) {
	path := "/metadata/annotations/" + strings.ReplaceAll(llmsvc.AnnForceConflicts, "/", "~1")
	payload, err := json.Marshal([]map[string]string{{
		"op":    "add",
		"path":  path,
		"value": strconv.FormatInt(generation, 10),
	}})
	if err != nil {
		return nil, err
	}
	return b.s.llms.Patch(ctx, ns, name, types.JSONPatchType, payload)
}

// requirePlanSchemaV2 refuses the llmsvc backend unless the plan being written
// records schema v2. Empty means v1: a plan composed from a v1 repo, and every
// plan stored before v2. Rollback writes an already-composed plan, so the gate
// is that plan's source.schema rather than whatever the site profile's catalog
// says now -- the catalog may have been renamed, or its name pointed at a
// different repo, since the plan was stored. Helm is not checked. A refusal
// returns before any namespace or object write.
func (b llmBackend) requirePlanSchemaV2(p *plan.Plan) error {
	if p == nil {
		return fail(http.StatusBadRequest, "nil plan")
	}
	schema := strings.TrimSpace(p.Source.Schema)
	if schema == "" {
		schema = catalog.SchemaV1
	}
	if schema != catalog.SchemaV2 {
		return fail(http.StatusBadRequest, "LLMService backend requires catalog schema v2; this plan records schema %s; use the helm backend or migrate the catalog to v2", schema)
	}
	return nil
}

// fromPlanStatus is 400 for a plan llmsvc cannot represent and 500 otherwise.
// Classification is errors.Is on the sentinels, not the error text.
func fromPlanStatus(err error) int {
	if errors.Is(err, llmsvc.ErrChartPath) || errors.Is(err, llmsvc.ErrChartRepo) {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

// mergeReleaseAnnotations keeps every annotation swiss does not own, replaces
// the swiss.modelsphere.dev/ set and skip-schema-validation with fresh (so a
// stale note, or a flag turned off, is dropped), and removes force-conflicts
// unless this apply sets it.
func mergeReleaseAnnotations(cur, fresh map[string]string, setForce string) map[string]string {
	owned := func(k string) bool {
		return strings.HasPrefix(k, llmsvc.SwissAnnotationPrefix) || k == llmsvc.AnnSkipSchemaValidation
	}
	out := make(map[string]string, len(cur)+len(fresh)+1)
	for k, v := range cur {
		if owned(k) || k == llmsvc.AnnForceConflicts {
			continue
		}
		out[k] = v
	}
	for k, v := range fresh {
		if owned(k) {
			out[k] = v
		}
	}
	if setForce != "" {
		out[llmsvc.AnnForceConflicts] = setForce
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func predictedGeneration(cur, next *unstructured.Unstructured) int64 {
	if cur == nil {
		return 1
	}
	if specJSON(cur) == specJSON(next) {
		if g := cur.GetGeneration(); g > 0 {
			return g
		}
		return 1
	}
	return cur.GetGeneration() + 1
}

func specJSON(u *unstructured.Unstructured) string {
	if u == nil {
		return ""
	}
	spec, ok := u.Object["spec"]
	if !ok {
		return ""
	}
	b, err := json.Marshal(spec)
	if err != nil {
		return ""
	}
	return string(b)
}

func llmSummary(obj *llmsvc.LLMService) string {
	phase, msg, chart := "", "", ""
	var rev int64
	if obj != nil && obj.Status != nil {
		phase, msg = obj.Status.Phase, obj.Status.Message
		if obj.Status.Chart != nil {
			chart = obj.Status.Chart.Version
		}
		if obj.Status.Helm != nil {
			rev = obj.Status.Helm.Revision
		}
	}
	return fmt.Sprintf("phase %s, chart %s, helm revision %d: %s", phase, chart, rev, msg)
}

type revisionSnap struct {
	Spec        llmsvc.Spec       `json:"spec"`
	Annotations map[string]string `json:"annotations"`
}

func decodeRevision(cr *appsv1.ControllerRevision) (revisionSnap, error) {
	var snap revisionSnap
	if cr == nil || len(cr.Data.Raw) == 0 {
		return snap, fmt.Errorf("controllerrevision has no data")
	}
	if err := json.Unmarshal(cr.Data.Raw, &snap); err != nil {
		return snap, err
	}
	return snap, nil
}

func fillRevision(row *revision, snap revisionSnap) {
	if snap.Spec.Model != nil {
		row.Model = snap.Spec.Model.Name
		row.Version = snap.Spec.Model.Version
		row.Variant = snap.Spec.Model.Variant
	}
	if snap.Spec.Chart.Name != "" {
		row.Chart = chartLabel(snap.Spec.Chart.Name, snap.Spec.Chart.Version)
	}
	if h := snap.Annotations[llmsvc.AnnPlanHash]; h != "" {
		row.PlanHash = h
	}
}

func planFromRevision(ns, release string, cr *appsv1.ControllerRevision) (*plan.Plan, error) {
	snap, err := decodeRevision(cr)
	if err != nil {
		return nil, err
	}
	obj := &llmsvc.LLMService{
		Spec: snap.Spec,
	}
	obj.Name = release
	obj.Namespace = ns
	obj.Annotations = snap.Annotations
	u, err := llmsvc.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	return llmsvc.ToPlan(u)
}

func chartLabel(name, version string) string {
	if name == "" {
		return ""
	}
	if version == "" {
		return name
	}
	return chartRef(name, version)
}
