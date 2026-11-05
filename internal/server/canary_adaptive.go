package server

import (
	"context"
	"fmt"
	"github.com/modelsphere/swiss/internal/llmsvc"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/values"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func (s *Server) composeAdaptive(ctx context.Context, req planRequest) (*plan.Plan, error) {
	if req.FromRelease == "" || req.Namespace == "" || s.llms == nil {
		return nil, fmt.Errorf("adaptive upgrade requires an existing LLMService")
	}
	u, err := s.llms.Get(ctx, req.Namespace, req.FromRelease)
	if err != nil {
		return nil, err
	}
	if req.OverridesYAML != "" || req.EditsYAML != "" || req.Model != "" || req.Version != "" || req.Variant != "" || req.Catalog != "" || req.ChartVersion != "" || req.Release != "" || req.ServiceID != "" || req.LocalPath != "" || len(req.GPUProducts) != 0 || req.ForceGPUProducts || req.CreateNamespace {
		return nil, fmt.Errorf("adaptiveOnly permits only adaptive concurrency changes")
	}
	if len(llmsvc.WithoutAdaptiveValues(map[string]any(req.Overrides))) != 0 {
		return nil, fmt.Errorf("adaptiveOnly permits only adaptive concurrency changes")
	}
	requested, _, err := unstructured.NestedMap(map[string]any(req.Overrides), "modelRoute", "nginx", "values")
	if err != nil || len(requested) == 0 {
		return nil, fmt.Errorf("adaptive concurrency values are required")
	}
	p, err := llmsvc.ToPlan(u)
	if err != nil {
		return nil, err
	}
	p = clonePlan(p)
	form := llmsvc.WithoutAdaptiveValues(map[string]any(p.Layers[values.LayerForm]))
	if err = unstructured.SetNestedMap(form, requested, "modelRoute", "nginx", "values"); err != nil {
		return nil, err
	}
	// Restore every non-adaptive route value alongside the replacement.
	original, _, _ := unstructured.NestedMap(map[string]any(p.Layers[values.LayerForm]), "modelRoute", "nginx", "values")
	for key, value := range original {
		if !llmsvc.AdaptiveKey(key) {
			requested[key] = value
		}
	}
	if err = unstructured.SetNestedMap(form, requested, "modelRoute", "nginx", "values"); err != nil {
		return nil, err
	}
	p.Layers[values.LayerForm] = values.Tree(form)
	if err = p.ComputeHash(); err != nil {
		return nil, err
	}
	p.Helmfile, err = p.RenderHelmfile("")
	return p, err
}
