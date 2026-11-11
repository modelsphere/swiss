package plan

import "github.com/modelsphere/swiss/internal/values"

// RouteOf is the path a release with these values publishes on the site's
// entrypoint, or "" when it publishes none: modelRoute.nginx.route, else the
// serviceId, else the release name. The status page and compose both read it
// here, so the URL a deploy writes into a chart is the one the page shows.
func RouteOf(vals values.Tree, release string) string {
	if v, ok := values.Get(vals, "modelRoute.enabled"); ok {
		if on, _ := v.(bool); !on {
			return ""
		}
	}
	for _, path := range []string{"modelRoute.nginx.route", "serviceId"} {
		if v, ok := values.Get(vals, path); ok {
			if s, _ := v.(string); s != "" {
				return s
			}
		}
	}
	return release
}
