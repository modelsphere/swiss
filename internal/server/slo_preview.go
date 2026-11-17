package server

import (
	"context"
	"maps"

	"github.com/modelsphere/swiss/internal/llmsvc"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/values"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

func (s *Server) sloPreviewPlan(ctx context.Context, p *plan.Plan) (*plan.Plan, error) {
	if !s.llmsvcAccess(ctx).visible() {
		return p, nil
	}
	cur, err := s.llms.Get(ctx, p.Release.Namespace, p.Release.Name)
	if apierrors.IsNotFound(err) {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	tree, err := llmsvc.SLOBindingValues(cur)
	if err != nil {
		return nil, err
	}
	if tree == nil {
		return p, nil
	}
	preview := *p
	preview.Layers = maps.Clone(p.Layers)
	if preview.Layers == nil {
		preview.Layers = map[string]values.Tree{}
	}
	preview.Layers[values.LayerCanarySLO] = tree
	if err := preview.ComputeHash(); err != nil {
		return nil, err
	}
	return &preview, nil
}
