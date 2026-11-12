package plan

import "github.com/modelsphere/swiss/internal/values"

// RouteOf is the path a release with these values publishes on the site's
// entrypoint, or "" when it publishes none: modelRoute.nginx.route, else the
// serviceId, else the release name. The status page and compose both read it
// here, so the URL a deploy writes into a chart is the one the page shows.
func RouteOf(vals values.Tree, release string) string {
	route, _ := NamedRouteOf(vals, release)
	return route
}

// NamedRouteOf is RouteOf, and whether the values name the route themselves
// (a non-empty string modelRoute.nginx.route or serviceId) rather than it
// falling back to the release name. A chart may fall back to a default of its
// own, so only a named route is one both sides agree on.
func NamedRouteOf(vals values.Tree, release string) (route string, named bool) {
	if v, ok := values.Get(vals, "modelRoute.enabled"); ok {
		if on, _ := v.(bool); !on {
			return "", false
		}
	}
	for _, path := range []string{"modelRoute.nginx.route", "serviceId"} {
		if v, ok := values.Get(vals, path); ok {
			if s, _ := v.(string); s != "" {
				return s, true
			}
		}
	}
	return release, false
}
