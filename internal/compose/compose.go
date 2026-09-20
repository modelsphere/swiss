// Package compose merges the four layers into one values document.
//
// The layers have disjoint key sets, enforced by values.Owner. That is the whole
// design: if a key could be set by two layers, every later question about this
// system becomes a question about precedence, and there is no good answer to any
// of them. Merge order still matters for the derived layer, which yields to
// anything a human actually asked for.
package compose

import (
	"fmt"

	"github.com/aceforeverd/swiss/internal/catalog"
	"github.com/aceforeverd/swiss/internal/plan"
	"github.com/aceforeverd/swiss/internal/site"
	"github.com/aceforeverd/swiss/internal/values"
)

type Input struct {
	Catalog   string // catalog repo url or path, recorded in the plan
	Ref       string // catalog commit sha
	Entry     catalog.Entry
	Variant   catalog.Variant
	Profile   site.Profile
	Release   string
	Namespace string
	Overrides values.Tree
}

// Compose resolves an entry, a variant, a profile and a set of overrides into a
// plan. It does not touch a cluster and does not render a chart; preflight and
// render are separate steps on purpose, so this one is pure and testable.
func Compose(in Input) (*plan.Plan, error) {
	if in.Release == "" {
		return nil, fmt.Errorf("release name is required")
	}
	ns := in.Namespace
	if ns == "" {
		ns = in.Profile.Namespace
	}
	if ns == "" {
		return nil, fmt.Errorf("no namespace: pass one, or set namespace in the site profile")
	}

	catalogVals, err := catalogLayer(in.Entry, in.Variant)
	if err != nil {
		return nil, fmt.Errorf("catalog layer: %w", err)
	}
	siteVals, err := siteLayer(in.Profile, in.Entry, in.Variant)
	if err != nil {
		return nil, fmt.Errorf("site layer: %w", err)
	}
	// The form is checked before anything is merged, so a rejected override
	// never half-applies.
	if err := values.CheckOwnership(in.Overrides, values.LayerForm); err != nil {
		return nil, fmt.Errorf("overrides: %w", err)
	}

	out := values.Tree{}
	prov := values.Provenance{}
	values.Merge(out, catalogVals, values.LayerCatalog, prov)
	values.Merge(out, siteVals, values.LayerSite, prov)
	applyDerived(out, prov, in)
	values.Merge(out, in.Overrides, values.LayerForm, prov)

	p := &plan.Plan{
		APIVersion: plan.APIVersion,
		Release:    plan.Release{Name: in.Release, Namespace: ns},
		Source: plan.SourceRef{
			Catalog: in.Catalog,
			Ref:     in.Ref,
			Model:   in.Entry.Name,
			Variant: in.Variant.ID,
		},
		Chart: plan.ChartRef{
			Name:    in.Variant.Chart.Name,
			Version: in.Variant.Chart.Version,
			Repo:    in.Profile.ChartRepo,
			Path:    in.Profile.ChartPath,
		},
		Engine:     in.Variant.Engine,
		Profile:    in.Profile.Name,
		Overrides:  in.Overrides,
		Values:     out,
		Provenance: prov,
	}
	if err := p.ComputeHash(); err != nil {
		return nil, err
	}
	return p, nil
}

// catalogLayer renders the entry and variant into chart values.
//
// Three fields are projected rather than copied, because the catalog schema
// refuses a second spelling of each: model.name comes from servedName,
// model.gpus from requires.gpus, and image.tag from the variant's image. Two
// spellings of one fact drift, which is the same line the charts take with
// nvidia.com/gpu.
func catalogLayer(e catalog.Entry, v catalog.Variant) (values.Tree, error) {
	out := values.Tree{}
	values.Merge(out, v.Values, values.LayerCatalog, nil)

	if err := values.Set(out, "model.name", e.ServedModelName()); err != nil {
		return nil, err
	}
	if err := values.Set(out, "model.gpus", fmt.Sprintf("%d", v.Requires.GPUs)); err != nil {
		return nil, err
	}
	if _, ok := values.Get(out, "modelCheck.requiredGlobs"); !ok {
		if err := values.Set(out, "modelCheck.requiredGlobs", toAnySlice(e.Source.RequiredGlobsOrDefault())); err != nil {
			return nil, err
		}
	}
	if v.Image != nil {
		if err := values.Set(out, "image.tag", v.Image.Tag); err != nil {
			return nil, err
		}
		if v.Image.Digest != "" {
			if err := values.Set(out, "image.digest", v.Image.Digest); err != nil {
				return nil, err
			}
		}
	}
	return out, values.CheckOwnership(out, values.LayerCatalog)
}

// siteLayer renders the profile into chart values for this model.
func siteLayer(p site.Profile, e catalog.Entry, v catalog.Variant) (values.Tree, error) {
	out := values.Tree{}

	localPath, err := p.LocalPath(e.Source.HF, e.Name)
	if err != nil {
		return nil, err
	}
	if err := values.Set(out, "model.localPath", localPath); err != nil {
		return nil, err
	}
	if v.Image != nil {
		if err := values.Set(out, "image.repository", p.MirrorImage(v.Image.Repository)); err != nil {
			return nil, err
		}
	}
	if p.Cache.Enabled {
		if err := values.Set(out, "cache.enabled", true); err != nil {
			return nil, err
		}
		if p.Cache.HostPath != "" {
			if err := values.Set(out, "cache.hostPath", p.Cache.HostPath); err != nil {
				return nil, err
			}
		}
	}
	if p.Scaler.ServerAddress != "" {
		if err := values.Set(out, "scaler.serverAddress", p.Scaler.ServerAddress); err != nil {
			return nil, err
		}
	}
	if len(p.Scaler.ServerHeaders) > 0 {
		h := map[string]any{}
		for k, val := range p.Scaler.ServerHeaders {
			h[k] = val
		}
		if err := values.Set(out, "scaler.serverHeaders", h); err != nil {
			return nil, err
		}
	}
	for path, val := range map[string]string{
		"modelRoute.nginx.outputConfigMap":   p.Route.NginxConfigMap,
		"modelRoute.nginx.service":           p.Route.NginxService,
		"modelRoute.nginx.selector":          p.Route.NginxSelector,
		"modelRoute.monitor.outputConfigMap": p.Route.MonitorConfigMap,
	} {
		if val == "" {
			continue
		}
		if err := values.Set(out, path, val); err != nil {
			return nil, err
		}
	}
	values.Merge(out, p.Extra, values.LayerSite, nil)
	return out, values.CheckOwnership(out, values.LayerSite)
}

// applyDerived computes the short, enumerated set of values that follow from
// catalog x site and that nobody should be asked to work out by hand.
//
// Two rules keep this from becoming magic, and both are load-bearing: every rule
// is listed here by name, and a derived value is skipped entirely when a human
// already set it. It is also recorded in provenance, so it shows up in a plan as
// "derived" rather than appearing in a cluster from nowhere.
func applyDerived(out values.Tree, prov values.Provenance, in Input) {
	// cache.maxSlotsPerNode: how many pods of this model can share a node's
	// cache directory. The chart's own comment gives the rule -- 1 for an 8-GPU
	// model, 4 for a 2-GPU one -- which is just floor(node GPUs / model GPUs).
	if in.Profile.Nodes.GPUsPerNode > 0 && in.Variant.Requires.GPUs > 0 {
		if _, set := values.Get(out, "cache.maxSlotsPerNode"); !set {
			if _, overridden := values.Get(in.Overrides, "cache.maxSlotsPerNode"); !overridden {
				slots := in.Profile.Nodes.GPUsPerNode / in.Variant.Requires.GPUs
				if slots < 1 {
					slots = 1
				}
				if err := values.Set(out, "cache.maxSlotsPerNode", slots); err == nil {
					prov["cache.maxSlotsPerNode"] = values.LayerDerived
				}
			}
		}
	}
}

func toAnySlice(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}
