package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/modelsphere/swiss/internal/autoscale"
	"github.com/modelsphere/swiss/internal/exec"
	"github.com/modelsphere/swiss/internal/llmsvc"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/values"
)

const defaultAutoscalePeriod = 60 * time.Second

// runAutoscaler polls decision-gen until ctx is cancelled. The first tick
// waits one period. A tick never delays shutdown: helm apply detaches, and
// this goroutine returns as soon as ctx is done.
func (s *Server) runAutoscaler(ctx context.Context) {
	timer := time.NewTimer(s.autoscalePeriod(ctx))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.autoscaleTick(ctx)
			timer.Reset(s.autoscalePeriod(ctx))
		}
	}
}

func (s *Server) autoscalePeriod(ctx context.Context) time.Duration {
	p, err := s.Profile(ctx)
	if err != nil || p == nil {
		return defaultAutoscalePeriod
	}
	d, err := time.ParseDuration(strings.TrimSpace(p.Scaler.AutoscalePeriod))
	if err != nil || d <= 0 {
		return defaultAutoscalePeriod
	}
	return d
}

func (s *Server) autoscaleTick(ctx context.Context) {
	if ctx.Err() != nil || s.probe == nil {
		return
	}
	refs, err := s.deploymentRefs(ctx)
	if err != nil {
		s.log.WarnContext(ctx, "autoscale list", "err", err)
		return
	}
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if s.autoscaleOne(ctx, ref.Namespace, ref.Name) {
			seen[autoscaleKey(ref.Namespace, ref.Name)] = true
		}
	}
	// Releases this pass did not mark are no longer autoscaling.
	s.pruneAutoscale(seen)
}

// autoscaleOne reports whether this release stays in the pass. False drops it.
// A read that fails before the plan is known keeps the previous entry.
func (s *Server) autoscaleOne(ctx context.Context, ns, release string) bool {
	b, _, err := s.backendFor(ctx, ns, release)
	if err != nil {
		s.log.WarnContext(ctx, "autoscale", "release", release, "err", err)
		s.noteAutoscaleError(ns, release, err)
		return true
	}
	if b == nil {
		return false
	}
	p, err := b.Current(ctx, ns, release)
	if err != nil {
		s.log.WarnContext(ctx, "autoscale", "release", release, "err", err)
		s.noteAutoscaleError(ns, release, err)
		return true
	}
	if !autoscaleOn(p) {
		return false
	}
	st := autoscaleState{Namespace: ns, Release: release, Enabled: true, Current: planReplicaCount(p)}
	live, rev, err := s.autoscaleLive(ctx, ns, release, b.Name(), p)
	if err != nil {
		s.log.WarnContext(ctx, "autoscale", "release", release, "err", err)
		st.Outcome = outcomeError
		st.Error = err.Error()
		s.recordAutoscale(st)
		return true
	}
	st.Current = liveReplicaCount(live)
	d, err := s.queryDecision(ctx, ns, autoscaleServiceID(p))
	if err != nil {
		if errors.Is(err, autoscale.ErrNoDecision) {
			s.log.DebugContext(ctx, "autoscale", "release", release, "err", err)
			st.Outcome = outcomeNoDecision
			st.NoDecision = true
			s.recordAutoscale(st)
			return true
		}
		s.log.WarnContext(ctx, "autoscale", "release", release, "err", err)
		st.Outcome = outcomeError
		st.Error = err.Error()
		s.recordAutoscale(st)
		return true
	}
	res := autoscale.Decide(live, d)
	st.Current = res.Current
	st.Decision = res.Decision
	if !res.Scale {
		if res.HoldReason != "" {
			s.log.DebugContext(ctx, "autoscale hold", "release", release, "hold", res.HoldReason)
			st.Outcome = outcomeHeld
			st.Hold = res.HoldReason
			s.recordAutoscale(st)
			return true
		}
		want := res.Want
		st.Want = &want
		st.Outcome = outcomeUnchanged
		s.recordAutoscale(st)
		return true
	}
	want := res.Want
	st.Want = &want
	if _, scaleErr := s.scaleTo(ctx, ns, release, res.Current, res.Want, rev); scaleErr != nil {
		if errors.Is(scaleErr, errAutoscaleInactive) {
			s.dropAutoscale(autoscaleKey(ns, release))
			return false
		}
		s.log.WarnContext(ctx, "autoscale", "release", release, "err", scaleErr)
		st.Outcome = outcomeError
		st.Error = scaleErr.Error()
		s.recordAutoscale(st)
		return true
	}
	st.Outcome = outcomeScaledDown
	if res.Want > res.Current {
		st.Outcome = outcomeScaledUp
	}
	now := s.clock()
	st.LastScaled = &now
	s.recordAutoscale(st)
	return true
}

// noteAutoscaleError marks a failed read on the entry this process already
// has. A release with no entry yet is left absent; the caller still counts
// the key as seen so a transient error does not prune it once it exists.
func (s *Server) noteAutoscaleError(ns, release string, err error) {
	st, ok := s.autoscaleStateOf(ns, release)
	if !ok {
		return
	}
	st.Outcome = outcomeError
	st.Error = err.Error()
	s.recordAutoscale(st)
}

// liveReplicaCount is the count the loop saw: DisaggregatedSet slices when
// set, otherwise the plan's replicaCount.
func liveReplicaCount(live autoscale.Live) int {
	if live.DSSlices != nil && *live.DSSlices >= 0 {
		return *live.DSSlices
	}
	if live.PlanReplicaCount < 0 {
		return 0
	}
	return live.PlanReplicaCount
}

func (s *Server) queryDecision(ctx context.Context, ns, serviceID string) (autoscale.Decision, error) {
	prof, err := s.Profile(ctx)
	if err != nil {
		return autoscale.Decision{}, err
	}
	base := prof.Scaler.DecisionServer()
	n, err := autoscale.Query(ctx, s.decisionHTTP, base, prof.Scaler.ServerHeaders, ns, serviceID)
	if err != nil {
		return autoscale.Decision{}, err
	}
	return autoscale.Decision{OK: true, Replicas: n}, nil
}

func (s *Server) autoscaleLive(ctx context.Context, ns, release, backend string, p *plan.Plan) (autoscale.Live, int, error) {
	live := autoscale.Live{Backend: backend, PD: disaggregated(p), PlanReplicaCount: planReplicaCount(p)}
	if data, err := s.probe.ConfigMap(ctx, planRef(ns, release)); err != nil && !apierrors.IsNotFound(err) {
		return live, 0, err
	} else if err == nil {
		live.PlanPhase = parseStatus([]byte(data["status.yaml"])).Phase
	}
	// An uninstalling release holds in Decide. Its lock may be the
	// uninstall's, and a probe would only contend with a repeat DELETE.
	if live.PlanPhase == phaseUninstalling {
		return live, 0, nil
	}
	if !s.tryApplyLock(ns, release) {
		live.LockBusy = true
	} else {
		s.unlockApply(ns, release)
	}

	rev := 0
	switch backend {
	case backendHelm:
		st, err := exec.Lookup(ctx, s.probe, ns, release)
		if err != nil {
			return live, 0, err
		}
		live.HelmStatus = st.Status
		rev = st.Revision
	case backendLLMSVC:
		if s.llms == nil {
			return live, 0, fmt.Errorf("no LLMService client")
		}
		u, err := s.llms.Get(ctx, ns, release)
		if err != nil {
			return live, 0, err
		}
		obj, err := llmsvc.FromUnstructured(u)
		if err != nil {
			return live, 0, err
		}
		live.LLMGeneration = u.GetGeneration()
		if obj.Status != nil {
			live.LLMPhase = obj.Status.Phase
			live.LLMObservedGeneration = obj.Status.ObservedGeneration
			if obj.Status.Helm != nil {
				live.HelmStatus = obj.Status.Helm.Status
				rev = int(obj.Status.Helm.Revision)
			}
		}
		// A deletionTimestamp or status.phase Deleting is the operator tearing
		// the release down. Probing workloads would only race that.
		if !u.GetDeletionTimestamp().IsZero() || live.LLMPhase == llmsvc.PhaseDeleting {
			live.LLMDeleting = true
			return live, 0, nil
		}
	}

	name := fullnameOf(p, release)
	ds, err := s.probe.DisaggregatedSet(ctx, ns+"/"+name)
	if err != nil {
		return live, 0, err
	}
	if ds == nil {
		live.DSMissing = true
	} else {
		live.DSGeneration = ds.Generation
		live.DSObservedGeneration = ds.ObservedGeneration
		if ds.Slices != nil && *ds.Slices >= 0 {
			n := *ds.Slices
			live.DSSlices = &n
		}
		for _, role := range ds.Roles {
			live.Roles = append(live.Roles, autoscale.Role{
				Replicas: role.Replicas, ReadyReplicas: role.ReadyReplicas, UpdatedReplicas: role.UpdatedReplicas,
			})
		}
	}

	n := live.PlanReplicaCount
	if live.DSSlices != nil {
		n = *live.DSSlices
	}
	if ds != nil && n > 0 {
		live.Routers = make([]autoscale.Router, n)
		for i := 0; i < n; i++ {
			dep, err := s.probe.Deployment(ctx, fmt.Sprintf("%s/%s-router-s%d", ns, name, i))
			if err != nil {
				return live, 0, err
			}
			if dep == nil {
				live.Routers[i].Missing = true
				continue
			}
			live.Routers[i] = autoscale.Router{
				Generation: dep.Generation, ObservedGeneration: dep.ObservedGeneration,
				SpecReplicas: dep.SpecReplicas, ReadyReplicas: dep.ReadyReplicas,
			}
		}
	}
	return live, rev, nil
}

// errAutoscaleInactive is a release that stopped autoscaling after Decide.
// The tick stops. It is not a failure and it writes nothing.
var errAutoscaleInactive = errors.New("autoscale inactive")

func (s *Server) scaleTo(ctx context.Context, ns, release string, from, want, expectRev int) (int, error) {
	if s.store == nil {
		return 0, fmt.Errorf("no database: autoscale needs a stored plan")
	}
	if !s.tryApplyLock(ns, release) {
		return 0, &applyBusyError{ns: ns, release: release}
	}
	defer s.unlockApply(ns, release)
	// Re-read under the lock. A plan that is no longer autoscaling must not apply.
	b, _, err := s.backendFor(ctx, ns, release)
	if err != nil {
		return 0, err
	}
	if b == nil {
		return 0, errAutoscaleInactive
	}
	cur, err := b.Current(ctx, ns, release)
	if err != nil {
		return 0, err
	}
	if !autoscaleOn(cur) {
		return 0, errAutoscaleInactive
	}
	p := clonePlan(cur)
	if p.Layers == nil {
		p.Layers = map[string]values.Tree{}
	}
	p.Layers[values.LayerAutoscale] = values.Tree{"replicaCount": want}
	if err := p.ComputeHash(); err != nil {
		return 0, err
	}
	if err := s.store.PutPlan(ctx, p); err != nil {
		return 0, err
	}
	res, err := s.applyLocked(ctx, p, exec.Upgrade, "", applyOpts{
		ExpectRevision: expectRev,
		Action:         "autoscale",
		Note:           fmt.Sprintf("autoscale %d -> %d (decision-gen)", from, want),
	})
	// An uninstall that started after Decide stops the tick the same way:
	// applyLocked refused it under the lock, before anything was applied.
	if errors.Is(err, errUninstalling) {
		return 0, errAutoscaleInactive
	}
	if err != nil {
		return 0, err
	}
	return res.Revision, nil
}
