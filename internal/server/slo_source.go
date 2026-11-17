package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/llmsvc"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

type sloSource struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
	ServiceID string `json:"serviceId,omitempty"`
	Inherited bool   `json:"inherited"`
	CanEdit   bool   `json:"canEdit"`
}

func (s *Server) resolveSLOSource(ctx context.Context, ns, release string) (sloSource, error) {
	out := sloSource{ServiceID: release, CanEdit: true}
	if s.llms != nil {
		u, err := s.llms.Get(ctx, ns, release)
		if err != nil && !apierrors.IsNotFound(err) {
			return out, err
		}
		if err == nil {
			a := llmsvc.CanaryAssigned(u)
			if a != nil {
				allowed, policyErr := s.llms.LiveSLO(ctx, a)
				if policyErr != nil {
					return out, policyErr
				}
				out.CanEdit = allowed
			}
			if u.GetAnnotations()["canary.modelsphere.dev/slo-binding"] != "" {
				out.Inherited = true
				out.CanEdit = false
			}
			if u.GetDeletionTimestamp() != nil {
				out.CanEdit = false
			}
		}
	}
	rel, err := s.releaseRecord(ctx, ns, release)
	if err != nil {
		return out, err
	}
	if rel == nil {
		return out, nil
	}
	count := 0
	for _, ref := range rel.Objects {
		if ref.Kind != "ModelRoute" {
			continue
		}
		count++
		if count > 1 {
			return out, fmt.Errorf("multiple ModelRoutes: cannot select SLO requirement")
		}
		route, err := s.probe.Object(ctx, ref)
		if err != nil {
			return out, err
		}
		if route == nil {
			return out, fmt.Errorf("ModelRoute is missing")
		}
		slo, _ := route.Spec["slo"].(map[string]any)
		name, _ := slo["name"].(string)
		if name == "" {
			out.ServiceID = ""
			return out, nil
		}
		sloNS, sloName, qualified := strings.Cut(name, "/")
		if !qualified {
			sloNS = ref.Namespace
			sloName = name
		}
		out.Namespace, out.Name = sloNS, sloName
		requirement, err := s.sloRequirement(ctx, rel.Objects, sloNS, sloName)
		if err != nil {
			return out, err
		}
		if requirement == nil {
			out.ServiceID = ""
			return out, nil
		}
		out.ServiceID, _ = requirement.Spec["serviceId"].(string)
		if out.ServiceID == "" {
			return out, fmt.Errorf("SLO requirement has no serviceId")
		}
	}
	return out, nil
}

func (s *Server) sloRequirement(ctx context.Context, objects []cluster.ObjectRef, ns, name string) (*cluster.Object, error) {
	for _, ref := range objects {
		if ref.Kind == "LLMSLORequirement" && ref.Namespace == ns && ref.Name == name {
			return s.probe.Object(ctx, ref)
		}
	}
	// An inherited or shared requirement is not part of this release's manifest.
	var found *cluster.Object
	for _, version := range []string{"inference.modelsphere.dev/v1alpha1", "inference.x-k8s.io/v1alpha1"} {
		obj, err := s.probe.Object(ctx, cluster.ObjectRef{APIVersion: version, Kind: "LLMSLORequirement", Namespace: ns, Name: name})
		if err != nil {
			return nil, err
		}
		if obj != nil {
			if found != nil {
				return nil, fmt.Errorf("multiple SLO requirements for %s/%s across API groups", ns, name)
			}
			found = obj
		}
	}
	return found, nil
}
