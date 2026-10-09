package llmsvc

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/values"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ErrChartPath and ErrChartRepo are the plans llmsvc refuses before a write.
// Apply maps both to 400; anything else FromPlan returns is a server error.
var (
	ErrChartPath = errors.New("a chartPath plan cannot use llmsvc: the operator has no path")
	ErrChartRepo = errors.New("llmsvc requires a chart repo")
)

// FromPlan maps a plan onto an LLMService. Empty layers are omitted.
// createNamespace and the helmfile are not carried: swiss creates the namespace,
// and the operator does not run helmfile.
func FromPlan(p *plan.Plan, action, note string) (*unstructured.Unstructured, error) {
	if p == nil {
		return nil, fmt.Errorf("nil plan")
	}
	if p.Chart.Repo == "" {
		if p.Chart.Path != "" {
			return nil, ErrChartPath
		}
		return nil, ErrChartRepo
	}

	var layers []Layer
	for _, name := range plan.Layers {
		tree := p.Layers[name]
		if len(tree) == 0 {
			continue
		}
		layers = append(layers, Layer{Name: name, Values: tree})
	}

	obj := &LLMService{
		Spec: Spec{
			Chart: ChartSpec{
				Name:    p.Chart.Name,
				Repo:    p.Chart.Repo,
				Version: p.Chart.Version,
			},
			Layers: layers,
			Model:  modelFromPlan(p),
		},
	}
	obj.APIVersion = APIVersion
	obj.Kind = Kind
	obj.Name = p.Release.Name
	obj.Namespace = p.Release.Namespace
	obj.Annotations = annotations(p, action, note)
	return ToUnstructured(obj)
}

func modelFromPlan(p *plan.Plan) *ModelSpec {
	m := ModelSpec{
		Name:    p.Source.Model,
		Version: p.Source.Version,
		Variant: p.Source.Variant,
		HF:      p.Source.HF,
		Engine:  p.Engine,
	}
	if m == (ModelSpec{}) {
		return nil
	}
	return &m
}

func annotations(p *plan.Plan, action, note string) map[string]string {
	ann := map[string]string{}
	put := func(k, v string) {
		if v != "" {
			ann[k] = v
		}
	}
	put(AnnPlanHash, p.Hash)
	put(AnnCatalog, p.Source.Catalog)
	put(AnnCatalogName, p.Source.CatalogName)
	put(AnnCatalogRef, p.Source.Ref)
	put(AnnEntryDigest, p.Source.Digest)
	put(AnnProfile, p.Profile)
	put(AnnAction, action)
	put(AnnNote, note)
	if len(ann) == 0 {
		return nil
	}
	return ann
}

// IsExternal reports an LLMService swiss did not write: none of its annotations
// are under swiss.modelsphere.dev/. Operator annotations do not count.
func IsExternal(u *unstructured.Unstructured) bool {
	if u == nil {
		return true
	}
	for k := range u.GetAnnotations() {
		if strings.HasPrefix(k, SwissAnnotationPrefix) {
			return false
		}
	}
	return true
}

// ToPlan is the inverse of FromPlan. A plan-hash annotation is checked against
// the spec; an object without one, including an external LLMService, gets a
// computed hash and no check. The helmfile is regenerated the way compose does.
func ToPlan(u *unstructured.Unstructured) (*plan.Plan, error) {
	if u == nil {
		return nil, fmt.Errorf("nil object")
	}
	obj, err := FromUnstructured(u)
	if err != nil {
		return nil, err
	}

	layers := map[string]values.Tree{}
	for _, layer := range obj.Spec.Layers {
		if layer.Name == "" || len(layer.Values) == 0 {
			continue
		}
		tree, err := copyValues(layer.Values)
		if err != nil {
			return nil, fmt.Errorf("layer %s: %w", layer.Name, err)
		}
		layers[layer.Name] = tree
	}
	if len(layers) == 0 {
		layers = nil
	}

	ann := u.GetAnnotations()
	p := &plan.Plan{
		APIVersion: plan.APIVersion,
		Release:    plan.Release{Name: u.GetName(), Namespace: u.GetNamespace()},
		Source: plan.SourceRef{
			Catalog:     ann[AnnCatalog],
			CatalogName: ann[AnnCatalogName],
			Ref:         ann[AnnCatalogRef],
			Digest:      ann[AnnEntryDigest],
		},
		Chart: plan.ChartRef{
			Name:    obj.Spec.Chart.Name,
			Version: obj.Spec.Chart.Version,
			Repo:    obj.Spec.Chart.Repo,
		},
		Profile: ann[AnnProfile],
		Layers:  layers,
	}
	if m := obj.Spec.Model; m != nil {
		p.Source.Model = m.Name
		p.Source.Version = m.Version
		p.Source.Variant = m.Variant
		p.Source.HF = m.HF
		p.Engine = m.Engine
	}

	if h := ann[AnnPlanHash]; h != "" {
		p.Hash = h
		if err := p.VerifyHash(); err != nil {
			return nil, err
		}
	} else if err := p.ComputeHash(); err != nil {
		return nil, err
	}
	if doc, err := p.RenderHelmfile(""); err == nil {
		p.Helmfile = doc
	}
	return p, nil
}

// copyValues detaches a layer from the unstructured object. JSON is the form
// the plan hash is computed over, so the copy stays hash-stable.
func copyValues(t values.Tree) (values.Tree, error) {
	if len(t) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	var out values.Tree
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}
