package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/exec"
	"github.com/modelsphere/swiss/internal/plan"
)

const (
	backendHelm   = "helm"
	backendLLMSVC = "llmsvc"

	// crdServedTTL is how long a discovery answer is reused. A swissd whose
	// cluster has no LLMService CRD must not call that API on every page.
	crdServedTTL = 30 * time.Second
)

// backend is one way of storing a release. Handlers pick one and stop there.
type backend interface {
	Name() string // "helm" | "llmsvc"
	Current(ctx context.Context, ns, release string) (*plan.Plan, error)
	Apply(ctx context.Context, p *plan.Plan, mode exec.Mode, o applyOpts) (applyResult, error)
	Uninstall(ctx context.Context, ns, release string) (uninstallResult, error)
	Revisions(ctx context.Context, ns, release string) ([]revision, error)
	Archived(ctx context.Context, ns, release string, rev int) (*plan.Plan, error)
}

// applyOpts is what an apply, install, or rollback asks of either backend.
type applyOpts struct {
	ExpectRevision int
	Action         string
	Note           string
	ForceConflicts bool
}

// applyResult is the success body both backends produce. Failures are errors.
type applyResult struct {
	Revision    int
	Output      string
	Status      string
	StatusError string
}

// uninstallResult is the success body. PlanError is the helm cleanup failure,
// reported because the release is already gone.
type uninstallResult struct {
	Output    string
	PlanError string
}

// httpFailure is an error whose text and status are the HTTP response.
type httpFailure struct {
	code int
	msg  string
}

func (e *httpFailure) Error() string { return e.msg }

func fail(code int, format string, args ...any) error {
	return &httpFailure{code: code, msg: fmt.Sprintf(format, args...)}
}

func failErr(code int, err error) error {
	if err == nil {
		return nil
	}
	return &httpFailure{code: code, msg: err.Error()}
}

func writeStatus(w http.ResponseWriter, err error, fallback int) {
	code := fallback
	var hf *httpFailure
	if errors.As(err, &hf) {
		code = hf.code
	}
	writeError(w, code, err.Error())
}

// chooseBackend reads ownership off the cluster. served is false when the CRD
// is absent, and then an LLMService is invisible.
func chooseBackend(served, hasService, hasConfigMap bool) (name string, cleanupPending bool) {
	if served && hasService {
		return backendLLMSVC, hasConfigMap
	}
	if hasConfigMap {
		return backendHelm, false
	}
	return "", false
}

func (s *Server) applyWith() string {
	if s.cfg.Server.ApplyWith == backendLLMSVC {
		return backendLLMSVC
	}
	return backendHelm
}

func (s *Server) helmBackend() backend { return helmBackend{s} }
func (s *Server) llmBackend() backend  { return llmBackend{s} }

func sortRevisions(out []revision) {
	sort.Slice(out, func(i, j int) bool { return out[i].Revision > out[j].Revision })
}

// llmsvcAccess is what this process can do with LLMServices right now.
// A discovery error is cached as not served. Forbidden is a separate cache:
// the CRD is served, and get/list is skipped until the TTL passes.
type llmsvcAccess struct {
	served    bool
	forbidden bool
	discErr   error
}

// visible is true when get/list may run. A discovery error and a cached
// Forbidden both hide the API, which is how a helm install keeps working.
func (a llmsvcAccess) visible() bool {
	return a.served && a.discErr == nil && !a.forbidden
}

// llmsvcAccess reports discovery, cached for crdServedTTL. A nil client is an
// unserved CRD and makes no discovery call. A discovery error is cached like
// a normal answer and logged once per TTL.
func (s *Server) llmsvcAccess(ctx context.Context) llmsvcAccess {
	if s.llms == nil {
		return llmsvcAccess{}
	}
	s.servedMu.Lock()
	defer s.servedMu.Unlock()
	if s.servedAt.IsZero() || time.Since(s.servedAt) >= crdServedTTL {
		ok, err := s.llms.Served(ctx)
		s.servedAt = time.Now()
		if err != nil {
			s.servedOK = false
			s.servedErr = err
			s.forbidden = false
			s.log.WarnContext(ctx, "LLMService discovery failed", "err", err)
		} else {
			s.servedOK = ok
			s.servedErr = nil
			if !ok {
				s.forbidden = false
			}
		}
	}
	forbidden := s.forbidden && !s.forbidAt.IsZero() && time.Since(s.forbidAt) < crdServedTTL
	return llmsvcAccess{served: s.servedOK, forbidden: forbidden, discErr: s.servedErr}
}

// noteLLMSVCForbidden caches a get/list Forbidden for one TTL so the next
// request does not call the API again.
func (s *Server) noteLLMSVCForbidden() {
	s.servedMu.Lock()
	defer s.servedMu.Unlock()
	s.forbidden = true
	s.forbidAt = time.Now()
}

// releaseNamespaces is the server's list scope. Nil means cluster-wide.
func (s *Server) releaseNamespaces() []string {
	k, ok := s.probe.(*cluster.Kube)
	if !ok {
		return nil
	}
	scopes := k.Scopes()
	if len(scopes) == 0 || (len(scopes) == 1 && scopes[0] == metav1.NamespaceAll) {
		return nil
	}
	return scopes
}

func (s *Server) resolveNamespace(ctx context.Context, namespace, release string) (string, error) {
	if namespace != "" {
		return namespace, nil
	}
	prof, err := s.Profile(ctx)
	if err != nil {
		return "", fmt.Errorf("no namespace for release %q, and the site profile is unreadable: %w", release, err)
	}
	if prof.Namespace == "" {
		return "", fmt.Errorf("no namespace for release %q: pass one, or set namespace in the site profile", release)
	}
	return prof.Namespace, nil
}

func (s *Server) hasPlanConfigMap(ctx context.Context, namespace, release string) (bool, error) {
	data, err := s.probe.ConfigMap(ctx, planRef(namespace, release))
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return len(data) > 0, nil
}

// backendFor returns the backend that owns this release, or nil when swiss
// does not manage it. cleanupPending is set when an LLMService and a helm
// plan ConfigMap both still exist.
func (s *Server) backendFor(ctx context.Context, namespace, release string) (backend, bool, error) {
	ns, err := s.resolveNamespace(ctx, namespace, release)
	if err != nil {
		return nil, false, err
	}
	access := s.llmsvcAccess(ctx)
	hasService := false
	if access.visible() && s.llms != nil {
		_, err := s.llms.Get(ctx, ns, release)
		switch {
		case err == nil:
			hasService = true
		case apierrors.IsNotFound(err):
		case apierrors.IsForbidden(err):
			s.noteLLMSVCForbidden()
		default:
			return nil, false, err
		}
	}
	hasCM, err := s.hasPlanConfigMap(ctx, ns, release)
	if err != nil {
		return nil, false, err
	}
	name, cleanup := chooseBackend(access.visible(), hasService, hasCM)
	switch name {
	case backendLLMSVC:
		return s.llmBackend(), cleanup, nil
	case backendHelm:
		return s.helmBackend(), false, nil
	default:
		return nil, false, nil
	}
}

// readBackend is backendFor for callers that still have to answer when the
// release is not managed: that answer is the helm one, which is how "no such
// release" and "no plan beside it" are phrased today.
func (s *Server) readBackend(ctx context.Context, namespace, release string) (backend, error) {
	b, _, err := s.backendFor(ctx, namespace, release)
	if err != nil || b != nil {
		return b, err
	}
	return s.helmBackend(), nil
}

// backendForApply picks the owner of an existing release, and the configured
// backend for a new install. An upgrade of something swiss does not manage
// stays on helm, which refuses it the way it does today.
func (s *Server) backendForApply(ctx context.Context, p *plan.Plan, mode exec.Mode) (backend, error) {
	b, _, err := s.backendFor(ctx, p.Release.Namespace, p.Release.Name)
	if err != nil {
		return nil, failErr(http.StatusBadGateway, err)
	}
	if b != nil {
		return b, nil
	}
	if mode == exec.Install {
		if s.applyWith() == backendLLMSVC && s.llmsvcAccess(ctx).visible() {
			return s.llmBackend(), nil
		}
		return s.helmBackend(), nil
	}
	return s.helmBackend(), nil
}
