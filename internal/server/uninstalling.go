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
// matches either selector. A lookup or list error leaves the release listed;
// a delete error is retried on the next check. The returned string is the
// delete error, for the uninstall response.
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
	if remain {
		s.trackUninstalling(ns, release)
		return ""
	}
	// Helm still recording the release means uninstall did not finish. Dropping
	// the plan here would make a swiss release look hand-installed. A lookup
	// that fails is the same as a pod list that fails: keep the row.
	st, err := exec.Lookup(ctx, s.probe, ns, release)
	if err != nil {
		s.log.WarnContext(ctx, "helm release not read, release stays listed", "namespace", ns, "release", release, "err", err)
		s.trackUninstalling(ns, release)
		return ""
	}
	if st.Exists {
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
// Jobs leave them behind after the workload is gone. Engine pods carry
// app=<release>-<chart> and need not carry the instance label, so either
// selector still having a pod keeps the release.
func (s *Server) podsRemain(ctx context.Context, ns, release string, files map[string]string) (bool, error) {
	sels, err := podSelectors(release, files)
	if err != nil {
		return true, err
	}
	var remain bool
	for _, sel := range sels {
		pods, err := s.probe.Pods(ctx, ns, sel)
		if err != nil {
			return true, err
		}
		for _, pod := range pods {
			if pod.Phase == string(corev1.PodSucceeded) || pod.Phase == string(corev1.PodFailed) {
				continue
			}
			remain = true
			break
		}
	}
	return remain, nil
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
