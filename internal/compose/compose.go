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
	"strings"

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
	Edits     values.Tree
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
	// base is only what the defaults need to see: whether the site already set
	// a value one of them would otherwise compute.
	base := values.Tree{}
	values.Merge(base, catalogVals, values.LayerCatalog, nil)
	values.Merge(base, siteVals, values.LayerSite, nil)

	derivedVals := values.Tree{}
	if err := applyDefaults(siteVals, derivedVals, base, in); err != nil {
		return nil, err
	}
	// The layers, each the document it actually is. Merging them in order is
	// what produces the values, so the outcome is determined by the order and
	// nothing needs a separate record of which one won.
	layers := map[string]values.Tree{}
	for name, tree := range map[string]values.Tree{
		values.LayerCatalog: catalogVals,
		values.LayerSite:    siteVals,
		values.LayerDerived: derivedVals,
		values.LayerForm:    in.Overrides,
		values.LayerEdit:    in.Edits,
	} {
		if len(tree) > 0 {
			layers[name] = tree
		}
	}

	p := &plan.Plan{
		APIVersion: plan.APIVersion,
		Release:    plan.Release{Name: in.Release, Namespace: ns},
		Source: plan.SourceRef{
			Catalog: in.Catalog,
			Ref:     in.Ref,
			Model:   in.Entry.Name,
			Version: in.Entry.Version,
			Digest:  in.Entry.Digest,
			Variant: in.Variant.ID,
		},
		Chart: plan.ChartRef{
			Name:    in.Variant.Chart.Name,
			Version: in.Variant.Chart.Version,
			Repo:    in.Profile.ChartRepo,
			Path:    in.Profile.ChartPath,
		},
		Engine:          in.Variant.Engine,
		Profile:         in.Profile.Name,
		CreateNamespace: in.Profile.CreateNamespace,
		Layers:          layers,
	}
	if err := p.ComputeHash(); err != nil {
		return nil, err
	}
	// Best effort: a profile that names neither a chart repo nor a path leaves
	// this empty, and the runner resolves it against --chart-root instead.
	if doc, err := p.RenderHelmfile(""); err == nil {
		p.Helmfile = doc
	}
	return p, nil
}

// catalogLayer renders the entry and variant into chart values.
//
// No layer is restricted to a set of keys: variants[].values is the chart
// schema, not ours, and the merge order already decides the outcome. The catalog
// goes first, so everything after it can override what it set -- and what a
// reader needs to see is which values that happened to, which is plan.Shadowed.
//
// Three fields are projected on top rather than copied, because each has a
// first-class spelling on the variant: model.name comes from servedName,
// model.gpus from requires.gpus, and image.tag from the variant image. The
// projection is applied after the merge and so wins over the same key in
// values, which is what stops two spellings of one fact from drifting.
func catalogLayer(e catalog.Entry, v catalog.Variant) (values.Tree, error) {
	out := values.Tree{}
	values.Merge(out, v.Values, values.LayerCatalog, nil)

	if err := values.Set(out, "model.name", e.ServedModelName()); err != nil {
		return nil, err
	}
	if err := values.Set(out, "model.gpus", fmt.Sprintf("%d", v.Requires.GPUs)); err != nil {
		return nil, err
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
	return out, nil
}

// siteLayer renders the profile into chart values for this model.
func siteLayer(p site.Profile, e catalog.Entry, v catalog.Variant) (values.Tree, error) {
	out := values.Tree{}

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
	return out, nil
}

// applyDefaults fills values the site can work out but the deploy may override.
// It runs before the form is merged, so an explicit override simply wins and is
// attributed to the form.
//
// Every rule is listed here by name -- that is what keeps it from being magic --
// and each one lands in provenance, so it shows up in a plan rather than
// appearing in a cluster from nowhere.
func applyDefaults(site, derived, base values.Tree, in Input) error {
	// model.localPath from the site's path template. Owned by the form because
	// weights move and a deploy has to be able to say so.
	localPath, err := in.Profile.LocalPath(in.Entry.Source.HF, in.Entry.Name)
	if err != nil {
		return err
	}
	if err := values.Set(site, "model.localPath", localPath); err != nil {
		return err
	}

	// The scheduler and priority class a GPU workload lands on are a property of
	// the cluster, not of the model. Set only when the profile names one, so an
	// unset profile still leaves the chart's default alone.
	for path, v := range map[string]string{
		"priorityClassName": in.Profile.Schedule.PriorityClassName,
		"schedulerName":     in.Profile.Schedule.SchedulerName,
	} {
		if v == "" {
			continue
		}
		if err := values.Set(site, path, v); err != nil {
			return err
		}
	}

	// Every feature's enabled flag is written down, never left to the chart:
	// scaler, sloRequirement, cart and serviceMonitor all default to ENABLED in
	// charts/sglang, so a deploy that mentions none of them silently gets all
	// four -- and that set is a chart default, free to change under a release
	// that never asked for any of it.
	//
	// A section the form touched is one the deploy wants: it is switched on
	// explicitly unless the form said otherwise, which is how filling in scaling
	// numbers turns the scaler on and naming a route turns routing on.
	for feature, onByDefault := range map[string]bool{
		"cart":           true,
		"modelRoute":     false,
		"sloRequirement": false,
		"scaler":         false,
		"serviceMonitor": false,
	} {
		if _, set := values.Get(in.Overrides, feature+".enabled"); set {
			continue // the form is explicit; its merge lands after this
		}
		want := onByDefault
		if touched(in.Overrides, feature) {
			want = true
		}
		if err := values.Set(derived, feature+".enabled", want); err != nil {
			return err
		}
	}

	// cache.maxSlotsPerNode: how many pods of this model can share a node's
	// cache directory. The chart's own comment gives the rule -- 1 for an 8-GPU
	// model, 4 for a 2-GPU one -- which is just floor(node GPUs / model GPUs).
	//
	// Only when the site turned the cache on. A disabled cache must leave NOTHING
	// under cache: -- the key does not exist in every chart version, and a values
	// file carrying a section the chart has never heard of is rejected by its
	// schema rather than ignored.
	if in.Profile.Cache.Enabled && in.Profile.Nodes.GPUsPerNode > 0 && in.Variant.Requires.GPUs > 0 {
		if _, set := values.Get(base, "cache.maxSlotsPerNode"); !set {
			slots := max(in.Profile.Nodes.GPUsPerNode/in.Variant.Requires.GPUs, 1)
			if err := values.Set(derived, "cache.maxSlotsPerNode", slots); err != nil {
				return err
			}
		}
	}
	return nil
}

// touched reports whether the form said anything under a feature, in which case
// its enabled flag is the form's to set rather than something to default off.
func touched(overrides values.Tree, feature string) bool {
	for _, path := range values.LeafPaths(overrides) {
		if path == feature || strings.HasPrefix(path, feature+".") {
			return true
		}
	}
	return false
}

func toAnySlice(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}
