package server

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/exec"
	"github.com/modelsphere/swiss/internal/plan"
)

// uninstallInProgress is the DELETE body when helm has already been asked to
// remove the release and this call does not ask again.
const uninstallInProgress = "uninstall already in progress"

// uninstallPoll is how often a running swissd looks for pods that have
// finished terminating. The deployments list and a release's status do the
// same check when they are read, so a poll is not the only way a row drops.
const uninstallPoll = 5 * time.Second

// stuckTerminatingExtra is how long past a pod's grace period a deleting pod
// still counts as remaining. Past that it is treated as gone.
const stuckTerminatingExtra = 5 * time.Minute

// defaultTerminationGrace is what Kubernetes uses when a pod sets neither
// deletionGracePeriodSeconds nor terminationGracePeriodSeconds.
const defaultTerminationGrace = 30 * time.Second

// statusFile is the key writePlan stores beside the plan files.
const statusFile = "status.yaml"

type uninstState struct {
	inflight bool
}

func (s *Server) uninstallLoop(ctx context.Context) {
	s.reapUninstalling(ctx)
	ticker := time.NewTicker(uninstallPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reapUninstalling(ctx)
		}
	}
}

func (s *Server) trackUninstalling(ns, release string) {
	s.uninstMu.Lock()
	defer s.uninstMu.Unlock()
	s.trackLocked(ns + "/" + release)
}

func (s *Server) trackLocked(key string) {
	if s.uninst == nil {
		s.uninst = map[string]*uninstState{}
	}
	if s.uninst[key] == nil {
		s.uninst[key] = &uninstState{}
	}
}

func (s *Server) beginUninstall(ns, release string) bool {
	s.uninstMu.Lock()
	defer s.uninstMu.Unlock()
	key := ns + "/" + release
	if st := s.uninst[key]; st != nil && st.inflight {
		return false
	}
	s.trackLocked(key)
	s.uninst[key].inflight = true
	return true
}

func (s *Server) endUninstall(ns, release string) {
	s.uninstMu.Lock()
	defer s.uninstMu.Unlock()
	if st := s.uninst[ns+"/"+release]; st != nil {
		st.inflight = false
	}
}

func (s *Server) forgetUninstalling(ns, release string) {
	s.uninstMu.Lock()
	defer s.uninstMu.Unlock()
	delete(s.uninst, ns+"/"+release)
}

func (s *Server) uninstallInflight(ns, release string) bool {
	s.uninstMu.Lock()
	defer s.uninstMu.Unlock()
	st := s.uninst[ns+"/"+release]
	return st != nil && st.inflight
}

func (s *Server) uninstallingKeys() []string {
	s.uninstMu.Lock()
	defer s.uninstMu.Unlock()
	out := make([]string, 0, len(s.uninst))
	for key, st := range s.uninst {
		if st != nil && st.inflight {
			continue
		}
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// planPhase is the phase recorded in the plan ConfigMap, or "" when there is
// no plan. A read error is "" as well: the caller that cares about the
// difference is the one writing the phase.
func (s *Server) planPhase(ctx context.Context, ns, release string) string {
	data, err := s.probe.ConfigMap(ctx, planRef(ns, release))
	if err != nil || len(data) == 0 {
		return ""
	}
	return parseStatus([]byte(data[statusFile])).Phase
}

// markUninstalling writes the phase and the metadata label before helm runs.
// hadPlan is false when there is nothing beside the release to keep listed.
func (s *Server) markUninstalling(ctx context.Context, ns, release string) (hadPlan bool, err error) {
	ref := planRef(ns, release)
	data, err := s.probe.ConfigMap(ctx, ref)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if len(data) == 0 {
		return false, nil
	}
	if s.writer == nil {
		return false, fmt.Errorf("no cluster writer")
	}
	prev := parseStatus([]byte(data[statusFile]))
	next := planStatus{
		Phase:     phaseUninstalling,
		Action:    "uninstall",
		Revision:  prev.Revision,
		StartedAt: stamp(time.Now()),
		Note:      prev.Note,
	}
	files := maps.Clone(data)
	raw, err := yaml.Marshal(next)
	if err != nil {
		return false, err
	}
	files[statusFile] = string(raw)
	if err := s.writer.PutConfigMap(ctx, ref, files, phaseLabel(next.Phase)); err != nil {
		return false, err
	}
	return true, nil
}

// finishUninstallFailed puts the plan back to failed so a destroy error is
// not stuck on uninstalling, and drops the metadata label with it.
func (s *Server) finishUninstallFailed(ctx context.Context, ns, release string, cause error) error {
	if s.writer == nil {
		return fmt.Errorf("no cluster writer")
	}
	ref := planRef(ns, release)
	data, err := s.probe.ConfigMap(ctx, ref)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return fmt.Errorf("plan configmap %s is empty", ref)
	}
	prev := parseStatus([]byte(data[statusFile]))
	next := planStatus{
		Phase:     phaseFailed,
		Action:    "uninstall",
		Revision:  prev.Revision,
		StartedAt: prev.StartedAt,
		UpdatedAt: stamp(time.Now()),
		Error:     cause.Error(),
		Note:      prev.Note,
	}
	files := maps.Clone(data)
	raw, err := yaml.Marshal(next)
	if err != nil {
		return err
	}
	files[statusFile] = string(raw)
	return s.writer.PutConfigMap(ctx, ref, files, nil)
}

// refuseUninstalling is the 409 for install, upgrade and rollback.
func (s *Server) refuseUninstalling(ctx context.Context, ns, release string) error {
	if s.planPhase(ctx, ns, release) == phaseUninstalling {
		return fail(409, "release %s/%s is uninstalling", ns, release)
	}
	// A cached Forbidden hides the CRD the same way an unserved one does. Calling
	// through it would turn every helm apply into another refused get.
	if s.llms == nil || !s.llmsvcAccess(ctx).visible() {
		return nil
	}
	u, err := s.llms.Get(ctx, ns, release)
	if apierrors.IsForbidden(err) {
		s.noteLLMSVCForbidden()
		return nil
	}
	if err != nil || u.GetDeletionTimestamp().IsZero() {
		return nil
	}
	return fail(409, "release %s/%s is uninstalling", ns, release)
}

func (s *Server) reapUninstalling(ctx context.Context) {
	s.ensureUninstalling(ctx)
	for _, key := range s.uninstallingKeys() {
		ns, name, ok := strings.Cut(key, "/")
		if !ok {
			continue
		}
		s.reapRelease(ctx, ns, name)
	}
}

// ensureUninstalling fills the in-memory set from plan ConfigMaps once.
// Later marks are tracked as they are written; a restarted process has an
// empty set and learns it here, from the label.
func (s *Server) ensureUninstalling(ctx context.Context) {
	s.uninstMu.Lock()
	ready := s.uninstReady
	s.uninstMu.Unlock()
	if ready {
		return
	}
	if err := s.rebuildUninstalling(ctx); err != nil {
		return
	}
	s.uninstMu.Lock()
	s.uninstReady = true
	s.uninstMu.Unlock()
}

func (s *Server) rebuildUninstalling(ctx context.Context) error {
	refs, err := s.probe.ManagedRefs(ctx)
	if err != nil {
		s.log.WarnContext(ctx, "uninstalling set not rebuilt", "err", err)
		return err
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		if ref.Labels[cluster.LabelPhase] != phaseUninstalling {
			continue
		}
		key := ref.Namespace + "/" + ref.Name
		seen[key] = true
		s.trackUninstalling(ref.Namespace, ref.Name)
	}
	s.uninstMu.Lock()
	defer s.uninstMu.Unlock()
	for key, st := range s.uninst {
		if (st != nil && st.inflight) || seen[key] {
			continue
		}
		delete(s.uninst, key)
	}
	return nil
}

// reapRelease drops the plan once helm no longer has the release and no pod
// matches either selector. Once helm has dropped the release, archive Secrets
// are removed even while pods remain, so a restart between the helm uninstall
// and that delete does not leave them behind. A lookup or list error leaves
// the release listed; a delete error is retried on the next check. The
// returned string is the delete error, for the uninstall response.
func (s *Server) reapRelease(ctx context.Context, ns, release string) string {
	if s.uninstallInflight(ns, release) {
		return ""
	}
	ref := planRef(ns, release)
	data, err := s.probe.ConfigMap(ctx, ref)
	if apierrors.IsNotFound(err) || err == nil && len(data) == 0 {
		s.forgetUninstalling(ns, release)
		return ""
	}
	if err != nil {
		s.log.WarnContext(ctx, "plan not read, release stays listed", "namespace", ns, "release", release, "err", err)
		s.trackUninstalling(ns, release)
		return ""
	}
	if parseStatus([]byte(data[statusFile])).Phase != phaseUninstalling {
		s.forgetUninstalling(ns, release)
		return ""
	}
	remain, err := s.podsRemain(ctx, ns, release, data)
	if err != nil {
		s.log.WarnContext(ctx, "pods not listed, release stays listed", "namespace", ns, "release", release, "err", err)
		s.trackUninstalling(ns, release)
		return ""
	}
	// Helm still recording the release means uninstall did not finish. Dropping
	// the plan here would make a swiss release look hand-installed. A lookup
	// that fails is the same as a pod list that fails: keep the row. The lookup
	// happens even when pods remain, because the archive Secrets go as soon as
	// helm has dropped the release and do not wait for those pods.
	st, err := exec.Lookup(ctx, s.probe, ns, release)
	if err != nil {
		s.log.WarnContext(ctx, "helm release not read, release stays listed", "namespace", ns, "release", release, "err", err)
		s.trackUninstalling(ns, release)
		return ""
	}
	if !st.Exists {
		s.deleteArchives(ctx, ns, release)
	}
	if remain || st.Exists {
		s.trackUninstalling(ns, release)
		return ""
	}
	if s.writer == nil {
		s.trackUninstalling(ns, release)
		return ""
	}
	if err := s.writer.DeleteConfigMap(ctx, ref); err != nil {
		s.log.ErrorContext(ctx, "plan configmap not removed", "release", release, "err", err)
		s.trackUninstalling(ns, release)
		return err.Error()
	}
	s.forgetUninstalling(ns, release)
	return ""
}

// podsRemain is true when any pod matches the chart label or the helm instance
// label. Finished pods (Succeeded or Failed) are ignored: helm test and hook
// Jobs leave them behind after the workload is gone. A pod that is being
// deleted still counts during its grace period. It is ignored only once it is
// stuck: now is past deletionTimestamp plus the grace period plus five
// minutes, or its node is NotReady or not in the node list. Grace is
// metadata.deletionGracePeriodSeconds, else spec.terminationGracePeriodSeconds,
// else 30s. An unscheduled pod has no node, so only the time rule applies.
// Node readiness is listed only when a matched pod is terminating; if that
// list fails, a terminating pod stays counted unless the time rule already
// says it is stuck. Engine pods carry app=<release>-<chart> and need not carry
// the instance label, so either selector still having a pod keeps the release.
func (s *Server) podsRemain(ctx context.Context, ns, release string, files map[string]string) (bool, error) {
	sels, err := podSelectors(release, files)
	if err != nil {
		return true, err
	}
	var matched []cluster.Pod
	terminating := false
	for _, sel := range sels {
		pods, err := s.probe.Pods(ctx, ns, sel)
		if err != nil {
			return true, err
		}
		for _, pod := range pods {
			if pod.Phase == string(corev1.PodSucceeded) || pod.Phase == string(corev1.PodFailed) {
				continue
			}
			matched = append(matched, pod)
			if podTerminating(pod) {
				terminating = true
			}
		}
	}
	if len(matched) == 0 {
		return false, nil
	}
	// A node list is cluster-wide. Skip it unless a terminating pod needs it.
	var nodeReady func(string) (ready, found bool)
	if terminating {
		nodeReady = s.nodeReady(ctx, ns, release)
	}
	now := s.clock()
	for _, pod := range matched {
		if podStuckTerminating(pod, now, nodeReady) {
			continue
		}
		return true, nil
	}
	return false, nil
}

func (s *Server) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// nodeReady is nil when the node list cannot be read. Callers then apply only
// the time rule, which keeps a terminating pod that is still inside its window.
func (s *Server) nodeReady(ctx context.Context, ns, release string) func(string) (ready, found bool) {
	nodes, err := s.probe.Nodes(ctx)
	if err != nil {
		s.log.WarnContext(ctx, "nodes not listed, only the grace rule can ignore a terminating pod",
			"namespace", ns, "release", release, "err", err)
		return nil
	}
	ready := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		ready[n.Name] = n.Ready
	}
	return func(name string) (bool, bool) {
		r, ok := ready[name]
		return r, ok
	}
}

func podTerminating(p cluster.Pod) bool {
	return p.DeletionTimestamp != nil && !p.DeletionTimestamp.IsZero()
}

// podStuckTerminating reports whether a deleting pod should be treated as gone.
// nodeReady is nil when the node list could not be read: only the time rule
// applies, so a pod still inside its window counts as remaining. An empty node
// name is unscheduled and is not treated as a missing node.
func podStuckTerminating(p cluster.Pod, now time.Time, nodeReady func(string) (ready, found bool)) bool {
	if !podTerminating(p) {
		return false
	}
	if now.After(p.DeletionTimestamp.Add(podGrace(p) + stuckTerminatingExtra)) {
		return true
	}
	if p.Node == "" || nodeReady == nil {
		return false
	}
	ready, found := nodeReady(p.Node)
	return !found || !ready
}

func podGrace(p cluster.Pod) time.Duration {
	if p.DeletionGracePeriodSeconds != nil {
		return time.Duration(*p.DeletionGracePeriodSeconds) * time.Second
	}
	if p.TerminationGracePeriodSeconds != nil {
		return time.Duration(*p.TerminationGracePeriodSeconds) * time.Second
	}
	return defaultTerminationGrace
}

func podSelectors(release string, files map[string]string) ([]string, error) {
	instance := "app.kubernetes.io/instance=" + release
	if len(files) == 0 {
		return []string{instance}, nil
	}
	p, err := plan.FromFiles(files)
	if err != nil {
		return nil, err
	}
	if p.Chart.Name == "" {
		return []string{instance}, nil
	}
	appSel := fmt.Sprintf("app=%s-%s", release, p.Chart.Name)
	if appSel == instance {
		return []string{instance}, nil
	}
	return []string{instance, appSel}, nil
}
