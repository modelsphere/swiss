package server

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/exec"
	"github.com/modelsphere/swiss/internal/llmsvc"
	"github.com/modelsphere/swiss/internal/plan"
)

// uninstallInProgress is the DELETE body when the release is already
// uninstalling and this call does not ask again.
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
	// llm means the operator owns teardown. Reap waits for the LLMService to
	// disappear and does not scan pods or helm.
	llm bool
	// started is the request time, or the deletionTimestamp after a restart.
	// The final audit row uses it, so a repeat DELETE must not overwrite it.
	started  time.Time
	planHash string
	// finalAudit is set once cleanup has succeeded, so a delete error retries
	// the row and a second reap does not write it again.
	finalAudit bool
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

// trackLLMUninstall marks a release whose LLMService the operator is deleting.
// planHash and started stick to whatever was recorded first: a repeat DELETE
// and a restart must not move the final audit's clock.
func (s *Server) trackLLMUninstall(ns, release, planHash string, started time.Time) {
	s.uninstMu.Lock()
	defer s.uninstMu.Unlock()
	key := ns + "/" + release
	s.trackLocked(key)
	st := s.uninst[key]
	st.llm = true
	if st.planHash == "" {
		st.planHash = planHash
	}
	if st.started.IsZero() && !started.IsZero() {
		st.started = started
	}
}

func (s *Server) llmUninstalling(ns, release string) bool {
	s.uninstMu.Lock()
	defer s.uninstMu.Unlock()
	st := s.uninst[ns+"/"+release]
	return st != nil && st.llm
}

// claimLLMAudit returns the stored start and hash once, after cleanup has
// succeeded. A zero start means the process restarted without a timestamp.
func (s *Server) claimLLMAudit(ns, release string) (time.Time, string, bool) {
	s.uninstMu.Lock()
	defer s.uninstMu.Unlock()
	st := s.uninst[ns+"/"+release]
	if st == nil || st.finalAudit {
		return time.Time{}, "", false
	}
	st.finalAudit = true
	started := st.started
	if started.IsZero() {
		started = s.clock()
	}
	return started, st.planHash, true
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

// markUninstalling writes the phase and the metadata label before the release
// is removed. hadPlan is false when there is nothing beside the release to keep listed.
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

// snapshotPlan copies the plan ConfigMap so a delete that finds nothing can
// put it back. A missing map is an empty snapshot, not an error.
func (s *Server) snapshotPlan(ctx context.Context, ns, release string) (map[string]string, error) {
	data, err := s.probe.ConfigMap(ctx, planRef(ns, release))
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	return maps.Clone(data), nil
}

// restorePlan writes a snapshot back, including the phase label it had. An
// empty snapshot is a release that had no plan.
func (s *Server) restorePlan(ctx context.Context, ns, release string, data map[string]string) error {
	if len(data) == 0 {
		return nil
	}
	if s.writer == nil {
		return fmt.Errorf("no cluster writer")
	}
	phase := parseStatus([]byte(data[statusFile])).Phase
	return s.writer.PutConfigMap(ctx, planRef(ns, release), data, phaseLabel(phase))
}

// errUninstalling is what refuseUninstalling wraps, so the autoscaler can tell
// it from a failed apply.
var errUninstalling = errors.New("release is uninstalling")

func uninstallingFailure(ns, release string) error {
	return &httpFailure{code: http.StatusConflict, msg: fmt.Sprintf("release %s/%s is uninstalling", ns, release), err: errUninstalling}
}

// refuseUninstalling is the 409 for install, upgrade, rollback and the
// autoscaler. applyLocked calls it under the release's lock.
func (s *Server) refuseUninstalling(ctx context.Context, ns, release string) error {
	if s.planPhase(ctx, ns, release) == phaseUninstalling {
		return uninstallingFailure(ns, release)
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
	return uninstallingFailure(ns, release)
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
	if err := s.trackDeletingLLMServices(ctx, seen); err != nil {
		return err
	}
	s.uninstMu.Lock()
	defer s.uninstMu.Unlock()
	for key, st := range s.uninst {
		// An llm entry stays even when the object is already gone: the reap
		// still has to drop the plan, and a rebuild must not turn that into
		// a helm pod scan.
		if st != nil && (st.inflight || st.llm) || seen[key] {
			continue
		}
		delete(s.uninst, key)
	}
	return nil
}

// trackDeletingLLMServices records LLMServices the operator is still deleting.
// A labeled plan whose service still exists is the same kind of uninstall: the
// operator owns the teardown, so the helm pod scan must not run. A list error
// leaves the set unready so the next poll tries again. Forbidden is a grant
// problem, not a reason to treat every uninstalling plan as an LLMService.
func (s *Server) trackDeletingLLMServices(ctx context.Context, seen map[string]bool) error {
	if s.llms == nil || !s.llmsvcAccess(ctx).visible() {
		return nil
	}
	list, err := s.llms.List(ctx, s.releaseNamespaces())
	if apierrors.IsForbidden(err) {
		s.noteLLMSVCForbidden()
		return nil
	}
	if err != nil {
		s.log.WarnContext(ctx, "llmservices not listed, uninstalling set not rebuilt", "err", err)
		return err
	}
	found := map[string]struct {
		hash    string
		started time.Time
	}{}
	for _, u := range list {
		if u == nil {
			continue
		}
		key := u.GetNamespace() + "/" + u.GetName()
		info := struct {
			hash    string
			started time.Time
		}{hash: u.GetAnnotations()[llmsvc.AnnPlanHash]}
		if ts := u.GetDeletionTimestamp(); ts != nil && !ts.IsZero() {
			info.started = ts.Time
			seen[key] = true
			s.trackLLMUninstall(u.GetNamespace(), u.GetName(), info.hash, info.started)
		}
		found[key] = info
	}
	for key := range seen {
		info, ok := found[key]
		if !ok {
			continue
		}
		ns, name, ok := strings.Cut(key, "/")
		if !ok {
			continue
		}
		s.trackLLMUninstall(ns, name, info.hash, info.started)
	}
	return nil
}

// reapRelease drops the plan once helm no longer has the release and no pod
// matches either selector. The autoscale ConfigMap goes first, at the same
// point: the autoscaler holds while the release is uninstalling, and its
// config outlives the release only if that delete fails, which keeps the
// release listed until a later check removes it. Once helm has dropped the
// release, archive Secrets are removed even while pods remain, so a restart
// between the helm uninstall and that delete does not leave them behind. A
// lookup or list error leaves the release listed; a delete error is retried
// on the next check. The returned string is the delete error, for the
// uninstall response.
func (s *Server) reapRelease(ctx context.Context, ns, release string) string {
	if s.uninstallInflight(ns, release) {
		return ""
	}
	if s.llmUninstalling(ns, release) {
		return s.reapLLM(ctx, ns, release)
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
	if err := s.removeAutoscale(ctx, ns, release); err != nil {
		s.log.ErrorContext(ctx, "autoscale configmap not removed", "release", release, "err", err)
		s.trackUninstalling(ns, release)
		return err.Error()
	}
	if err := s.writer.DeleteConfigMap(ctx, ref); err != nil {
		s.log.ErrorContext(ctx, "plan configmap not removed", "release", release, "err", err)
		s.trackUninstalling(ns, release)
		return err.Error()
	}
	s.forgetUninstalling(ns, release)
	return ""
}

// reapLLM finishes an llmsvc uninstall once the operator has dropped the
// object. While the object exists, the operator is still working and a pod
// scan would race it. A recreated object (no deletionTimestamp) is someone
// else's release again, so tracking stops and nothing is deleted.
func (s *Server) reapLLM(ctx context.Context, ns, release string) string {
	if s.llms == nil || !s.llmsvcAccess(ctx).visible() {
		return ""
	}
	u, err := s.llms.Get(ctx, ns, release)
	if apierrors.IsNotFound(err) {
		return s.finishLLMGone(ctx, ns, release)
	}
	if apierrors.IsForbidden(err) {
		s.noteLLMSVCForbidden()
		return ""
	}
	if err != nil {
		s.log.WarnContext(ctx, "llmservice not read, uninstall stays tracked", "namespace", ns, "release", release, "err", err)
		return ""
	}
	if u.GetDeletionTimestamp().IsZero() {
		s.log.InfoContext(ctx, "llmservice recreated, uninstall tracking dropped", "namespace", ns, "release", release)
		s.forgetUninstalling(ns, release)
		return ""
	}
	s.trackLLMUninstall(ns, release, u.GetAnnotations()[llmsvc.AnnPlanHash], u.GetDeletionTimestamp().Time)
	return ""
}

// finishLLMGone drops what swiss wrote beside the release, then records one
// audit row. A cleanup error leaves the release tracked so the next check
// retries, including the audit.
func (s *Server) finishLLMGone(ctx context.Context, ns, release string) string {
	if s.writer == nil {
		return ""
	}
	s.deleteArchives(ctx, ns, release)
	if err := s.removeAutoscale(ctx, ns, release); err != nil {
		s.log.ErrorContext(ctx, "autoscale configmap not removed", "release", release, "err", err)
		return err.Error()
	}
	ref := planRef(ns, release)
	data, err := s.probe.ConfigMap(ctx, ref)
	if err != nil && !apierrors.IsNotFound(err) {
		s.log.WarnContext(ctx, "plan not read, release stays listed", "namespace", ns, "release", release, "err", err)
		return ""
	}
	if err == nil && len(data) > 0 && parseStatus([]byte(data[statusFile])).Phase == phaseUninstalling {
		if err := s.writer.DeleteConfigMap(ctx, ref); err != nil {
			s.log.ErrorContext(ctx, "plan configmap not removed", "release", release, "err", err)
			return err.Error()
		}
	}
	if started, hash, ok := s.claimLLMAudit(ns, release); ok {
		s.recordRelease(ctx, "uninstall", ns, release, hash, exec.Result{Output: llmGone(ns, release)}, nil, started, "", 0)
	}
	s.forgetUninstalling(ns, release)
	return ""
}

func llmDeleteRequested(ns, release string) string {
	return fmt.Sprintf("LLMService %s/%s deletion requested", ns, release)
}

func llmGone(ns, release string) string {
	return fmt.Sprintf("LLMService %s/%s is gone", ns, release)
}

// podsRemain is true when any pod matches the chart label or the helm instance
// label. Finished pods (Succeeded or Failed) are ignored: helm test and hook
// Jobs leave them behind after the workload is gone. A pod that is being
// deleted still counts during its grace period. It is ignored only once it is
// stuck: now is past deletionTimestamp plus the grace period plus five
// minutes, or the grace period is over and its node is NotReady or not in the
// node list. A node that is NotReady for a moment does not end the uninstall
// while its pods may still be shutting down. Grace is
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
// Inside its grace period it never is. After that, a NotReady or missing node
// is enough; otherwise it takes the five extra minutes. nodeReady is nil when
// the node list could not be read: only the time rule applies, so a pod still
// inside its window counts as remaining. An empty node name is unscheduled and
// is not treated as a missing node.
func podStuckTerminating(p cluster.Pod, now time.Time, nodeReady func(string) (ready, found bool)) bool {
	if !podTerminating(p) {
		return false
	}
	grace := podGrace(p)
	if now.After(p.DeletionTimestamp.Add(grace + stuckTerminatingExtra)) {
		return true
	}
	if !now.After(p.DeletionTimestamp.Add(grace)) {
		return false
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
