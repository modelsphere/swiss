package server

import (
	"context"
	"fmt"
	"net/http"

	"github.com/modelsphere/swiss/internal/llmsvc"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type canaryManagedError struct{ Owner *llmsvc.CanaryOwner }

func (e *canaryManagedError) Error() string {
	return fmt.Sprintf("service is managed by LLMCanary %s/%s (UID %s); manage configuration and cleanup from the experiment", e.Owner.Namespace, e.Owner.Name, e.Owner.UID)
}

func refuseCanaryService(u *unstructured.Unstructured) error {
	if owner := llmsvc.CanaryOwnership(u); owner != nil {
		return &canaryManagedError{Owner: owner}
	}
	return nil
}

func (s *Server) refuseCanaryWrite(ctx context.Context, ns, release string) error {
	access := s.llmsvcAccess(ctx)
	if access.discErr != nil || access.forbidden {
		return fail(http.StatusBadGateway, "cannot verify service ownership: %s", access.why())
	}
	if !access.served || s.llms == nil {
		return nil
	}
	ns, err := s.resolveNamespace(ctx, ns, release)
	if err != nil {
		return failErr(http.StatusBadGateway, err)
	}
	u, err := s.llms.Get(ctx, ns, release)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		if apierrors.IsForbidden(err) {
			s.noteLLMSVCForbidden()
		}
		return fail(http.StatusBadGateway, "cannot verify service ownership: %s", err)
	}
	return refuseCanaryService(u)
}
