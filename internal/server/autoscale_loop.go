package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/modelsphere/swiss/internal/autoscale"
	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/exec"
	"github.com/modelsphere/swiss/internal/llmsvc"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/values"
)

const defaultAutoscalePeriod = 30 * time.Second

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
	nss := s.releaseNamespaces()
	if len(nss) == 0 {
		nss = []string{""}
	}
	var cms []cluster.ConfigMapObj
	for _, ns := range nss {
		listed, err := s.probe.ConfigMapList(ctx, ns, "owner=swiss")
		if err != nil {
			s.log.WarnContext(ctx, "autoscale list", "namespace", ns, "err", err)
			continue
		}
		cms = append(cms, listed...)
	}
	sort.Slice(cms, func(i, j int) bool {
		if cms[i].Namespace != cms[j].Namespace {
			return cms[i].Namespace < cms[j].Namespace
		}
		return cms[i].Name < cms[j].Name
	})
	for _, cm := range cms {
		if !strings.HasPrefix(cm.Name, autoscalePrefix) {
			continue
		}
		release := strings.TrimPrefix(cm.Name, autoscalePrefix)
		if cm.Labels["name"] != "" && cm.Labels["name"] != release {
			continue
		}
		if release == "" {
			continue
		}
		s.autoscaleOne(ctx, cm.Namespace, release)
	}
}

func (s *Server) autoscaleOne(ctx context.Context, ns, release string) {
	view, err := s.readAutoscale(ctx, ns, release)
	if err != nil {
		s.log.WarnContext(ctx, "autoscale config", "release", release, "err", err)
		return
	}
	if !view.Found || view.Config == nil || !view.Config.Enabled {
		return
	}
	cfg := *view.Config

	b, _, err := s.backendFor(ctx, ns, release)
	if err != nil {
		s.noteAutoscaleError(ctx, ns, release, err)
		return
	}
	if b == nil {
		s.noteAutoscaleError(ctx, ns, release, fmt.Errorf("release %s/%s is not managed by swiss", ns, release))
		return
	}
	p, err := b.Current(ctx, ns, release)
	if err != nil {
		s.noteAutoscaleError(ctx, ns, release, err)
		return
	}
	live, rev, err := s.autoscaleLive(ctx, ns, release, b.Name(), p)
	if err != nil {
		s.noteAutoscaleError(ctx, ns, release, err)
		return
	}

	d := s.queryDecision(ctx, ns, serviceIDOf(p))
	var prev autoscale.Status
	if view.Status != nil {
		prev = *view.Status
	}
	now := time.Now()
	res := autoscale.Decide(now, cfg, prev, live, d)
	if !res.Scale {
		s.recordAutoscale(ctx, ns, release, res, nil, 0, 0, false)
		return
	}
	produced, scaleErr := s.scaleTo(ctx, ns, release, res.Current, res.Want, rev)
	if errors.Is(scaleErr, errAutoscaleInactive) {
		return
	}
	res = autoscale.NoteApply(now, res, applyKind(scaleErr))
	s.recordAutoscale(ctx, ns, release, res, scaleErr, res.Current, produced, scaleErr == nil)
}

func applyKind(err error) autoscale.ApplyKind {
	switch {
	case err == nil:
		return autoscale.ApplyOK
	case errors.Is(err, errApplyBusy):
		return autoscale.ApplyBusy
	case revisionConflict(err):
		return autoscale.ApplyConflict
	default:
		return autoscale.ApplyFailed
	}
}

func revisionConflict(err error) bool {
	var rc *exec.RevisionConflictError
	return errors.As(err, &rc)
}

func (s *Server) noteAutoscaleError(ctx context.Context, ns, release string, err error) {
	if err == nil {
		return
	}
	s.log.WarnContext(ctx, "autoscale", "release", release, "err", err)
	_ = s.writeAutoscaleStatus(ctx, ns, release, func(st *autoscale.Status) {
		st.LastTickAt = time.Now().UTC().Format(time.RFC3339)
		st.LastError = err.Error()
		st.HoldReason = err.Error()
	})
}

func (s *Server) recordAutoscale(ctx context.Context, ns, release string, res autoscale.Result, scaleErr error, from, rev int, scaled bool) {
	now := time.Now().UTC().Format(time.RFC3339)
	err := s.writeAutoscaleStatus(ctx, ns, release, func(st *autoscale.Status) {
		st.LastTickAt = now
		st.LastDecision = res.Decision
		st.Current = intPtr(res.Current)
		st.HoldReason = ""
		st.LastError = ""
		st.LastFailedWant = res.LastFailedWant
		st.LastFailedAt = res.LastFailedAt
		st.Failures = res.Failures
		st.RetryAfter = res.RetryAfter
		if scaleErr != nil {
			st.Desired = intPtr(res.Want)
			if errors.Is(scaleErr, errApplyBusy) {
				st.HoldReason = autoscale.HoldApplyInProgress
				return
			}
			st.LastError = scaleErr.Error()
			return
		}
		if res.HoldReason != "" {
			st.HoldReason = res.HoldReason
			if res.Failed {
				st.LastError = res.HoldReason
			}
			return
		}
		st.Desired = intPtr(res.Want)
		if scaled {
			st.LastScaleAt = now
			st.LastScaleFrom = intPtr(from)
			st.LastScaleTo = intPtr(res.Want)
			st.LastRevision = intPtr(rev)
		}
	})
	if err != nil {
		s.log.WarnContext(ctx, "autoscale status", "release", release, "err", err)
	}
}

func (s *Server) queryDecision(ctx context.Context, ns, serviceID string) autoscale.Decision {
	prof, err := s.Profile(ctx)
	if err != nil {
		return autoscale.Decision{Error: err.Error()}
	}
	base := prof.Scaler.DecisionServer()
	n, err := autoscale.Query(ctx, s.decisionHTTP, base, prof.Scaler.ServerHeaders, ns, serviceID)
	if err != nil {
		return autoscale.Decision{Error: err.Error()}
	}
	return autoscale.Decision{OK: true, Replicas: n}
}

func (s *Server) autoscaleLive(ctx context.Context, ns, release, backend string, p *plan.Plan) (autoscale.Live, int, error) {
	live := autoscale.Live{Backend: backend, PD: disaggregated(p), PlanReplicaCount: planReplicaCount(p)}
	if !s.tryApplyLock(ns, release) {
		live.LockBusy = true
	} else {
		s.unlockApply(ns, release)
	}

	if data, err := s.probe.ConfigMap(ctx, planRef(ns, release)); err != nil && !apierrors.IsNotFound(err) {
		return live, 0, err
	} else if err == nil {
		live.PlanPhase = parseStatus([]byte(data["status.yaml"])).Phase
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

// errAutoscaleInactive is a config that was disabled or deleted after Decide.
// The tick stops. It is not a failure and it does not write status.
var errAutoscaleInactive = errors.New("autoscale inactive")

func (s *Server) scaleTo(ctx context.Context, ns, release string, from, want, expectRev int) (int, error) {
	if s.store == nil {
		return 0, fmt.Errorf("no database: autoscale needs a stored plan")
	}
	if !s.tryApplyLock(ns, release) {
		return 0, &applyBusyError{ns: ns, release: release}
	}
	defer s.unlockApply(ns, release)
	// Re-read under the lock. A disable or delete after Decide must not apply.
	on, err := s.autoscalingEnabled(ctx, ns, release)
	if err != nil {
		return 0, err
	}
	if !on {
		return 0, errAutoscaleInactive
	}
	b, _, err := s.backendFor(ctx, ns, release)
	if err != nil {
		return 0, err
	}
	if b == nil {
		return 0, fmt.Errorf("release %s/%s is not managed by swiss", ns, release)
	}
	cur, err := b.Current(ctx, ns, release)
	if err != nil {
		return 0, err
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
	if err != nil {
		return 0, err
	}
	return res.Revision, nil
}
