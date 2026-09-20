package values

import (
	"fmt"
	"sort"
	"strings"
)

// Layer names. Order here is merge order, lowest precedence first.
const (
	LayerChart   = "chart"   // the chart's own values.yaml; never written by us
	LayerCatalog = "catalog" // what makes this model run
	LayerSite    = "site"    // what makes it work here
	LayerDerived = "derived" // computed from catalog x site; a provenance label only
	LayerForm    = "form"    // how much, where, how routed
)

// LayerDerived never appears here, and that is the point: ownership says who MAY
// write a path, provenance says who DID. A derived value fills a path its real
// owner left empty and is labelled "derived" in the plan, but the owner can
// always set it outright -- which is what "a derived value is overridable" has
// to mean. Giving derived its own ownership would make the computed value
// unsettable by the only layer that knows better.
//
// Owner assigns each values path to exactly one layer. A layer may write a path
// only if it owns it, which is the rule that keeps "who set this" answerable and
// stops the design from needing a precedence UI.
//
// Longest matching prefix wins, and anything unmatched belongs to the form. That
// default matters: when the charts grow a key, it becomes settable at deploy
// time without a change here, and no rendered value silently goes missing. The
// alternative -- default to rejecting -- fails closed in the wrong direction,
// blocking deploys for a key nobody has classified yet.
var owners = []struct {
	prefix string
	layer  string
}{
	// Catalog: model identity, and how it parallelizes.
	{"model.name", LayerCatalog}, // from servedName
	{"model.gpus", LayerCatalog}, // from requires.gpus
	{"model.mountPath", LayerCatalog},
	{"model.hostPathType", LayerCatalog},
	{"model.contextLength", LayerCatalog},
	{"modelCheck", LayerCatalog},
	{"extraArgs", LayerCatalog},
	{"commandOverride", LayerCatalog},
	{"env", LayerCatalog},
	{"volumes", LayerCatalog},
	{"volumeMounts", LayerCatalog},
	{"resources", LayerCatalog},
	{"lws.enabled", LayerCatalog},
	{"lws.size", LayerCatalog},
	{"lws.distPort", LayerCatalog},
	{"startupProbe", LayerCatalog},
	{"readinessProbe", LayerCatalog},
	{"livenessProbe", LayerCatalog},
	{"terminationGracePeriodSeconds", LayerCatalog},
	{"lifecycle", LayerCatalog},
	{"hangWatcher", LayerCatalog},
	{"healthEndpointGeneration", LayerCatalog},
	{"image.tag", LayerCatalog},
	{"image.digest", LayerCatalog},

	// Form, stated rather than left to the default below because both are
	// central to a deploy. serviceId is the identity modelRoute, sloRequirement
	// and the scaler all key off. model.localPath has a site-wide default built
	// from the path template, but weights move and a deploy has to be able to
	// say where they are.
	{"serviceId", LayerForm},
	{"model.localPath", LayerForm},
	// Whether to scrape this release is a deploy decision; which Prometheus
	// picks it up is not. serviceMonitor.labels below stays with the site.
	{"serviceMonitor.enabled", LayerForm},

	// Site: what makes it work in this cluster. image.repository is split from
	// image.tag deliberately -- the catalog pins which build, the site says
	// which mirror it is pulled from, and neither can answer the other.
	{"cache", LayerSite},
	{"image.repository", LayerSite},
	{"scaler.serverAddress", LayerSite},
	{"scaler.serverHeaders", LayerSite},
	{"modelRoute.nginx.outputConfigMap", LayerSite},
	{"modelRoute.nginx.service", LayerSite},
	{"modelRoute.nginx.selector", LayerSite},
	{"modelRoute.monitor.outputConfigMap", LayerSite},
	{"serviceMonitor", LayerSite},
	{"securityContext", LayerSite},
}

// Owner returns the layer that owns path.
func Owner(path string) string {
	best, bestLen := LayerForm, -1
	for _, r := range owners {
		if (path == r.prefix || strings.HasPrefix(path, r.prefix+".")) && len(r.prefix) > bestLen {
			best, bestLen = r.layer, len(r.prefix)
		}
	}
	return best
}

// CheckOwnership reports every leaf in t that layer is not allowed to write.
// Callers get all of them at once: fixing a catalog entry one rejected key per
// round trip is how a validation error becomes a reason not to use the tool.
func CheckOwnership(t Tree, layer string) error {
	var bad []string
	for _, p := range LeafPaths(t) {
		if o := Owner(p); o != layer {
			bad = append(bad, fmt.Sprintf("%s (owned by %s)", p, o))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return fmt.Errorf("%s layer may not set:\n  %s", layer, strings.Join(bad, "\n  "))
}
