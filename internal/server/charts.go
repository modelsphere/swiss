package server

import (
	"context"
	"net/http"
	"time"

	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/modelsphere/swiss/internal/chart"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/site"
)

type chartListing struct {
	versions []string
	at       time.Time
}

func (s *Server) resolveChart(ctx context.Context, prof *site.Profile, c catalog.Chart, want string, running plan.ChartRef) (string, error) {
	spec, err := chart.ParseSpec(c.Version)
	if err != nil {
		return "", err
	}
	var keep string
	if running.Name == c.Name {
		keep = running.Version
	}
	return chart.Resolve(ctx, spec, want, keep, func(ctx context.Context) ([]string, error) {
		return s.chartVersions(ctx, prof, c.Name)
	})
}

// chartVersions is cached for the catalog TTL: a form lists them, and the plan
// right after should not read the registry again.
func (s *Server) chartVersions(ctx context.Context, prof *site.Profile, name string) ([]string, error) {
	key := prof.ChartRepo + "\x00" + prof.ChartPath + "\x00" + name
	s.mu.Lock()
	cached, ok := s.charts[key]
	s.mu.Unlock()
	if ok && time.Since(cached.at) < s.cfg.Server.CacheTTL {
		return cached.versions, nil
	}
	vs, err := chart.Versions(ctx, prof.ChartRepo, prof.ChartPath, name)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.charts[key] = chartListing{versions: vs, at: time.Now()}
	s.mu.Unlock()
	return vs, nil
}

// variantFor is the variant a /api/catalog/{model}/... request names with its
// catalog, version and variant query parameters, defaulting as compose does.
func (s *Server) variantFor(ctx context.Context, w http.ResponseWriter, r *http.Request) (catalog.Variant, bool) {
	c, _, ok := s.catalogFor(ctx, w, r)
	if !ok {
		return catalog.Variant{}, false
	}
	q := r.URL.Query()
	e, err := c.Entry(ctx, r.PathValue("model"), q.Get("version"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return catalog.Variant{}, false
	}
	var v catalog.Variant
	if id := q.Get("variant"); id != "" {
		v, err = e.Variant(id)
	} else {
		v, err = e.DefaultVariant()
	}
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return catalog.Variant{}, false
	}
	return v, true
}

// handleChartVersions lists the versions a variant's chart.version allows,
// newest first. A pinned variant has its one, with no registry read.
func (s *Server) handleChartVersions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	v, ok := s.variantFor(ctx, w, r)
	if !ok {
		return
	}
	spec, err := chart.ParseSpec(v.Chart.Version)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	versions := []string{spec.Exact()}
	if versions[0] == "" {
		prof, err := s.Profile(ctx)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		have, err := s.chartVersions(ctx, prof, v.Chart.Name)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		versions = spec.Match(have)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"chart":    v.Chart.Name,
		"range":    spec.String(),
		"variant":  v.ID,
		"versions": versions,
	})
}
