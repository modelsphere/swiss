package server

import (
	"context"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/modelsphere/swiss/internal/exec"
	"github.com/modelsphere/swiss/internal/plan"
)

// helmBackend is the helmfile path: write-ahead ConfigMap, archive Secrets,
// helmfile apply, status.yaml. Moved here so a handler does not know it is helm.
type helmBackend struct{ s *Server }

func (b helmBackend) Name() string { return backendHelm }

func (b helmBackend) Current(ctx context.Context, namespace, release string) (*plan.Plan, error) {
	rel, err := b.s.releaseRecord(ctx, namespace, release)
	if err != nil {
		return nil, err
	}
	return planOf(rel)
}

func (b helmBackend) Revisions(ctx context.Context, ns, release string) ([]revision, error) {
	archived, err := b.s.archivedRevisions(ctx, ns, release)
	if err != nil {
		return nil, err
	}

	out := make([]revision, 0, len(archived)+1)
	for _, n := range archived {
		rev := revision{Revision: n}
		if data, err := b.s.probe.Secret(ctx, archiveRef(ns, release, n)); err == nil {
			if sum, err := parsePlanSummary([]byte(data[plan.MetaFile])); err == nil {
				rev.Model, rev.Version, rev.Variant = sum.Model, sum.Version, sum.Variant
				rev.Chart = sum.Chart
			}
		}
		out = append(out, rev)
	}

	// The live workspace is the current revision, and is not an archive.
	if st, err := exec.Lookup(ctx, b.s.probe, ns, release); err == nil && st.Exists {
		cur := revision{Revision: st.Revision, Current: true}
		if p, err := b.Current(ctx, ns, release); err == nil {
			cur.Model, cur.Version, cur.Variant = p.Source.Model, p.Source.Version, p.Source.Variant
			cur.Chart, cur.PlanHash = chartRef(p.Chart.Name, p.Chart.Version), p.Hash
		}
		out = append(out, cur)
	}

	sortRevisions(out)
	return out, nil
}

func (b helmBackend) Archived(ctx context.Context, ns, release string, rev int) (*plan.Plan, error) {
	return b.s.planSecret(ctx, ns, release, rev)
}

func (b helmBackend) Apply(ctx context.Context, p *plan.Plan, mode exec.Mode, o applyOpts) (applyResult, error) {
	s := b.s
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

	// The plan that produced the live revision is archived before it is
	// overwritten, so every applied revision stays reproducible from the
	// cluster -- which is what rollback reads, and what a lost database must
	// not cost.
	if st.Exists {
		if err := s.archivePlan(ctx, p.Release.Namespace, p.Release.Name, st.Revision); err != nil {
			s.log.WarnContext(ctx, "previous plan not archived", "release", p.Release.Name, "err", err)
		}
	}

	// The plan is recorded in the release's own namespace, so with
	// createNamespace that namespace has to exist first. helm creates it during
	// the apply, which is too late: the write-ahead below would fail into a
	// namespace that is not there yet and nothing would ever be applied.
	//
	// Creating it here rather than dropping the write-ahead: an empty namespace
	// is the one cluster change that costs nothing if the apply then fails, and
	// helm still applies its own Namespace object afterwards.
	if p.CreateNamespace && s.writer != nil {
		if err := s.writer.EnsureNamespace(ctx, p.Release.Namespace); err != nil {
			return applyResult{}, fail(http.StatusInternalServerError, "namespace not created, nothing applied: %s", err.Error())
		}
	}

	// Write-ahead: the plan is recorded before the cluster changes, so a
	// permissions or quota failure costs nothing. A live release with no
	// plan beside it reads as hand-installed, which is the one thing the
	// reconciliation view must never say about swissd's own work.
	started := time.Now()
	if err := s.writePlan(ctx, p, planStatus{
		Phase: phaseApplying, Action: o.Action, StartedAt: stamp(started), Note: o.Note,
		Forced: o.ForceConflicts,
	}); err != nil {
		msg := "plan not recorded, nothing applied: " + err.Error()
		if !p.CreateNamespace && apierrors.IsNotFound(err) {
			msg += "\n\nThe namespace does not exist. Recompose with createNamespace to have it created, or ask an admin for it -- createNamespace has to be in the plan, so setting it on the apply does nothing."
		}
		return applyResult{}, fail(http.StatusInternalServerError, "%s", msg)
	}

	// Everything past the write-ahead outlives the request.
	ctx, cancel := detach(ctx)
	defer cancel()

	// Logged as well as recorded: taking fields from another manager is the one
	// apply that silently undoes somebody else's hand edit, and the log is
	// where that is noticed by anyone not watching this release.
	if o.ForceConflicts {
		s.log.WarnContext(ctx, "applying with --force-conflicts: fields owned by another manager will be overwritten",
			"release", p.Release.Name, "namespace", p.Release.Namespace, "action", o.Action)
	}
	res, applyErr := s.runner().Apply(ctx, p, exec.ApplyOptions{ForceConflicts: o.ForceConflicts})

	// Looked up before the audit write, so the row can name the revision this
	// produced -- which is what makes the log a list of rollback targets rather
	// than a list of timestamps. A failed apply names none: helm may have left
	// a revision behind, but it is not one to go back to.
	after, _ := exec.Lookup(ctx, s.probe, p.Release.Namespace, p.Release.Name)
	produced := after.Revision
	if applyErr != nil {
		produced = 0
	}
	s.record(ctx, o.Action, p, res, applyErr, started, o.Note, produced)

	status := planStatus{
		Phase: phaseApplied, Action: o.Action,
		StartedAt: stamp(started), UpdatedAt: stamp(time.Now()),
		Revision: after.Revision, Note: o.Note, Forced: o.ForceConflicts,
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
		return applyResult{}, failErr(http.StatusInternalServerError, applyErr)
	}
	return applyResult{
		Revision:    after.Revision,
		Output:      res.Output,
		Status:      phaseApplied,
		StatusError: statusErr,
	}, nil
}

func (b helmBackend) Uninstall(ctx context.Context, ns, release string) (uninstallResult, error) {
	s := b.s
	st, err := exec.Lookup(ctx, s.probe, ns, release)
	if err != nil {
		return uninstallResult{}, failErr(http.StatusBadGateway, err)
	}
	if err := exec.CheckUninstall(st, ns, release); err != nil {
		return uninstallResult{}, failErr(http.StatusNotFound, err)
	}

	// Read the plan before the release goes, only to name it in the audit row.
	// A release with no readable plan is still removable -- see Runner.Uninstall.
	planHash := ""
	if p, err := b.Current(ctx, ns, release); err == nil {
		planHash = p.Hash
	}

	// The uninstall outlives the request, like an apply: a browser navigating
	// away must not SIGKILL helm halfway through deleting a release.
	started := time.Now()
	ctx, cancel := detach(ctx)
	defer cancel()

	res, uninstallErr := s.runner().Uninstall(ctx, ns, release)
	s.recordRelease(ctx, "uninstall", ns, release, planHash, res, uninstallErr, started, "", 0)
	if uninstallErr != nil {
		return uninstallResult{}, failErr(http.StatusInternalServerError, uninstallErr)
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
	return uninstallResult{Output: res.Output, PlanError: planErr}, nil
}
