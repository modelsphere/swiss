package server

import (
	"context"
	"fmt"

	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/values"
	"gopkg.in/yaml.v3"
)

func (s *Server) carryForward(ctx context.Context, req planRequest) (planRequest, error) {
	prev, err := s.currentPlan(ctx, req.Namespace, req.FromRelease)
	if err != nil {
		return req, err
	}
	catalogName, movedFrom, err := s.upgradeCatalog(ctx, prev, req.Catalog)
	if err != nil {
		return req, err
	}
	out := planRequest{
		Catalog: catalogName, Model: prev.Source.Model, Version: req.Version, Variant: prev.Source.Variant,
		Release: prev.Release.Name, Namespace: prev.Release.Namespace, Overrides: prev.Layers[values.LayerForm],
		ChartVersion: req.ChartVersion, runningChart: prev.Chart, movedFrom: movedFrom, GPUResource: req.GPUResource,
		canarySystem: cloneTree(prev.Layers[values.LayerCanarySystem]),
	}
	if req.Variant != "" {
		out.Variant = req.Variant
	}
	out, bare := carryUserConfiguration(prev, req, out)
	if bare {
		return s.withAutoscale(prev, out), nil
	}
	if req.ServiceID != "" && req.ServiceID != serviceIDOf(prev) {
		return req, fmt.Errorf("serviceId cannot change on an upgrade: %s is deployed as %q, not %q -- deploy a new release to run a second service", prev.Release.Name, serviceIDOf(prev), req.ServiceID)
	}
	if req.LocalPath != "" {
		out.LocalPath = req.LocalPath
	}
	if len(req.GPUProducts) > 0 {
		out.GPUProducts, out.ForceGPUProducts = req.GPUProducts, req.ForceGPUProducts
	}
	return s.withAutoscale(prev, out), nil
}

func (s *Server) upgradeCatalog(ctx context.Context, previous *plan.Plan, requested string) (string, *plan.Plan, error) {
	own, err := s.ReleaseCatalog(ctx, previous.Source.CatalogName, previous.Source.Catalog)
	if requested == "" {
		if err != nil {
			return "", nil, fmt.Errorf("cannot upgrade %s: %w", previous.Release.Name, err)
		}
		return own.Name, nil, nil
	}
	if err != nil || own.Name != requested {
		return requested, previous, nil
	}
	return requested, nil, nil
}

func carryUserConfiguration(previous *plan.Plan, req, out planRequest) (planRequest, bool) {
	switch {
	case req.UserValues != nil:
		out.Overrides, out.EditsYAML, out.UserValues = values.Tree{}, req.EditsYAML, req.UserValues
		out.ServiceID = serviceIDOf(previous)
	case len(req.Overrides) == 0:
		if edits := previous.Layers[values.LayerEdit]; len(edits) > 0 {
			out.EditsYAML = mustYAML(edits)
		}
	default:
		if out.Overrides == nil {
			out.Overrides = values.Tree{}
		}
		values.Merge(out.Overrides, req.Overrides, values.LayerForm, nil)
		out.EditsYAML = req.EditsYAML
	}
	bare := req.UserValues == nil && len(req.Overrides) == 0
	if bare && out.GPUResource == "" {
		resource, _ := values.Get(previous.Layers[values.LayerDerived], "model.gpuResource")
		out.GPUResource, _ = resource.(string)
	}
	return out, bare
}

// withAutoscale copies the live autoscale layer onto an upgrade while the
// previous plan is autoscaling. Compose drops it again when the plan it builds
// is not autoscaling, so replicaCount falls back to form and edit.
func (s *Server) withAutoscale(prev *plan.Plan, out planRequest) planRequest {
	if prev == nil || !autoscaleOn(prev) {
		return out
	}
	layer := prev.Layers[values.LayerAutoscale]
	if len(layer) == 0 {
		return out
	}
	out.autoscale = cloneTree(layer)
	return out
}

// checkMove refuses a cross-catalog upgrade that would put another model, or the
// same one on another engine, behind the release's existing serviceId and route.
// Model ids are only unique within a catalog; HF is the identity across them.
func (s *Server) checkMove(ctx context.Context, prev *plan.Plan, to string, r catalog.Resolved) error {
	refuse := func(format string, args ...any) error {
		return fmt.Errorf("cannot move %s to catalog %q: %s -- deploy a new release instead", prev.Release.Name, to, fmt.Sprintf(format, args...))
	}
	hf := prev.Source.HF
	if hf == "" {
		var err error
		if hf, err = s.recordedHF(ctx, prev); err != nil {
			return refuse("its plan predates recording the model's HF repo, and its own catalog could not supply it (%v)", err)
		}
	}
	if r.Source.HF != hf {
		return refuse("%s there is %s, not %s, so it is a different model", r.Model, r.Source.HF, hf)
	}
	if r.Variant.Engine != prev.Engine {
		return refuse("variant %s runs on %s there, not %s", r.Variant.ID, r.Variant.Engine, prev.Engine)
	}
	return nil
}

func mustYAML(t values.Tree) string {
	b, err := yaml.Marshal(t)
	if err != nil {
		return ""
	}
	return string(b)
}
