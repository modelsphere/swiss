package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/modelsphere/swiss/internal/exec"
	"github.com/modelsphere/swiss/internal/llmsvc"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/values"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"net/http"
	"reflect"
)

func (s *Server) composeAssignedAdaptive(ctx context.Context, req planRequest) (*plan.Plan, error) {
	if req.FromRelease == "" || req.Namespace == "" || s.llms == nil {
		return nil, fmt.Errorf("adaptive upgrade requires an existing LLMService")
	}
	u, err := s.llms.Get(ctx, req.Namespace, req.FromRelease)
	if err != nil {
		return nil, err
	}
	a := llmsvc.CanaryAssigned(u)
	if a == nil || !a.Verified || a.Role != "arm" || llmsvc.CanaryOwnership(u) != nil {
		return nil, fmt.Errorf("adaptive upgrade requires a verified assigned arm")
	}
	if req.OverridesYAML != "" || req.EditsYAML != "" || req.Model != "" || req.Version != "" || req.Variant != "" || req.Catalog != "" || req.ChartVersion != "" || req.Release != "" || req.ServiceID != "" || req.LocalPath != "" || len(req.GPUProducts) != 0 || req.ForceGPUProducts || req.CreateNamespace {
		return nil, fmt.Errorf("assigned arms permit only adaptive concurrency changes")
	}
	if len(llmsvc.WithoutAdaptiveValues(map[string]any(req.Overrides))) != 0 {
		return nil, fmt.Errorf("assigned arms permit only adaptive concurrency changes")
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

func refuseCanaryPlan(u *unstructured.Unstructured, p *plan.Plan, mode exec.Mode, action string) error {
	err := refuseCanaryService(u)
	if err == nil {
		return nil
	}
	a := llmsvc.CanaryAssigned(u)
	if llmsvc.CanaryOwnership(u) != nil || a == nil || !a.Verified || a.Role != "arm" || mode != exec.Upgrade || action != "apply" {
		return err
	}
	next, mapErr := llmsvc.FromPlan(p, action, "")
	if mapErr != nil {
		return mapErr
	}
	before, _, _ := unstructured.NestedMap(u.Object, "spec")
	after, _, _ := unstructured.NestedMap(next.Object, "spec")
	if !sameCanarySpec(llmsvc.WithoutAdaptiveSpec(before), llmsvc.WithoutAdaptiveSpec(after)) {
		return err
	}
	current, mapErr := llmsvc.ToPlan(u)
	if mapErr != nil {
		return mapErr
	}
	if !reflect.DeepEqual(current.Source, p.Source) || current.Profile != p.Profile {
		return err
	}
	if u.GetAnnotations()[llmsvc.AdaptiveBaselineKey] == "" {
		return fail(http.StatusConflict, "experiment adaptive baseline is not ready; retry after the Canary controller reconciles")
	}
	return nil
}

func (s *Server) refuseCanaryPlanWrite(ctx context.Context, p *plan.Plan, mode exec.Mode, action string) error {
	err := s.refuseCanaryWrite(ctx, p.Release.Namespace, p.Release.Name)
	if err == nil {
		return nil
	}
	var assigned *canaryAssignedError
	if !errors.As(err, &assigned) || s.llms == nil {
		return err
	}
	u, getErr := s.llms.Get(ctx, p.Release.Namespace, p.Release.Name)
	if getErr != nil {
		return err
	}
	return refuseCanaryPlan(u, p, mode, action)
}

func sameCanarySpec(before, after map[string]any) bool {
	a, err := json.Marshal(before)
	if err != nil {
		return false
	}
	b, err := json.Marshal(after)
	return err == nil && bytes.Equal(a, b)
}
