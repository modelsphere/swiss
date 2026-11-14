package server

import (
	"context"
	"fmt"

	"github.com/modelsphere/swiss/internal/llmsvc"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/values"
	"gopkg.in/yaml.v3"
)

func (s *Server) composeLive(ctx context.Context, req planRequest) (*plan.Plan, error) {
	if req.FromRelease == "" || req.Namespace == "" || s.llms == nil {
		return nil, fmt.Errorf("reuseSource requires an existing LLMService and its namespace")
	}
	u, err := s.llms.Get(ctx, req.Namespace, req.FromRelease)
	if err != nil {
		return nil, err
	}
	p, err := llmsvc.ToPlan(u)
	if err != nil {
		return nil, err
	}
	p = clonePlan(p)
	form := p.Layers[values.LayerForm]
	if form == nil {
		form = values.Tree{}
	}
	if req.OverridesYAML != "" {
		var extra values.Tree
		if err = yaml.Unmarshal([]byte(req.OverridesYAML), &extra); err != nil {
			return nil, fmt.Errorf("advanced overrides: %w", err)
		}
		values.Merge(form, extra, values.LayerForm, nil)
	}
	values.Merge(form, req.Overrides, values.LayerForm, nil)
	p.Layers[values.LayerForm] = form
	if req.EditsYAML != "" {
		var edits values.Tree
		if err = yaml.Unmarshal([]byte(req.EditsYAML), &edits); err != nil {
			return nil, fmt.Errorf("edits: %w", err)
		}
		p.Layers[values.LayerEdit] = edits
	}
	// Recorded keys stay; the value is whatever the new layers hold.
	p.Inputs = plan.Answered(p.Inputs, p.Values())
	if err = p.ComputeHash(); err != nil {
		return nil, err
	}
	p.Helmfile, err = p.RenderHelmfile("")
	return p, err
}
