package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aceforeverd/swiss/internal/cluster"
	"github.com/aceforeverd/swiss/internal/exec"
	"github.com/aceforeverd/swiss/internal/plan"
	"github.com/aceforeverd/swiss/internal/values"
)

type releaseStatus struct {
	Release    string        `json:"release"`
	Namespace  string        `json:"namespace"`
	Exists     bool          `json:"exists"`
	Revision   int           `json:"revision"`
	HelmStatus string        `json:"helmStatus,omitempty"`
	Pods       []cluster.Pod `json:"pods"`
	Ready      int           `json:"ready"`
	Total      int           `json:"total"`
	Route      string        `json:"route,omitempty"`
	Warning    string        `json:"warning,omitempty"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()

	ns, release := r.PathValue("namespace"), r.PathValue("release")
	st, err := exec.Lookup(ctx, s.probe, ns, release)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	out := releaseStatus{
		Release: release, Namespace: ns,
		Exists: st.Exists, Revision: st.Revision, HelmStatus: st.Status,
	}

	// The chart labels engine pods app=<release>-<engine>; without a stored plan
	// the engine is unknown, so fall back to the helm instance label.
	selector := "app.kubernetes.io/instance=" + release
	if p, err := s.currentPlan(ctx, ns, release); err == nil {
		selector = fmt.Sprintf("app=%s-%s", release, p.Engine)
		out.Route = routeOf(p)
	}
	pods, err := s.probe.Pods(ctx, ns, selector)
	if err != nil {
		out.Warning = err.Error()
	}
	out.Pods = pods
	out.Total = len(pods)
	for _, p := range pods {
		if p.Ready {
			out.Ready++
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type probeResult struct {
	URL       string   `json:"url"`
	OK        bool     `json:"ok"`
	Status    int      `json:"status,omitempty"`
	LatencyMS int64    `json:"latencyMs"`
	Models    []string `json:"models,omitempty"`
	Error     string   `json:"error,omitempty"`
	Body      string   `json:"body,omitempty"`
}

// handleProbe asks the entrypoint whether the model is actually servable.
//
// Through openresty rather than the pod: a ready pod behind a route that was
// never published serves nobody, and that gap is exactly what the ModelRoute
// plumbing exists to close. Nothing else in swiss checks it end to end.
func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()

	ns, release := r.PathValue("namespace"), r.PathValue("release")
	p, err := s.currentPlan(ctx, ns, release)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	prof, err := s.Profile(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if prof.Route.NginxService == "" {
		writeError(w, http.StatusPreconditionFailed,
			"site profile names no route.nginxService, so there is no entrypoint to ask")
		return
	}
	svcNS, svcName, err := cluster.SplitRef(prof.Route.NginxService)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	route := routeOf(p)
	if route == "" {
		writeError(w, http.StatusPreconditionFailed, "this release publishes no route")
		return
	}

	port := prof.Route.NginxPort
	if port == 0 {
		port = 8080
	}
	url := fmt.Sprintf("http://%s.%s.svc:%d/%s/v1/models", svcName, svcNS, port, route)

	res := probeResult{URL: url}
	started := time.Now()
	body, code, err := httpGet(ctx, url)
	res.LatencyMS = time.Since(started).Milliseconds()
	res.Status = code
	if err != nil {
		res.Error = err.Error()
		writeJSON(w, http.StatusOK, res)
		return
	}
	res.OK = code == http.StatusOK
	res.Models = modelIDs(body)
	if !res.OK || len(res.Models) == 0 {
		res.Body = snippet(body)
	}
	writeJSON(w, http.StatusOK, res)
}

func routeOf(p *plan.Plan) string {
	if v, ok := values.Get(p.Values, "modelRoute.nginx.route"); ok {
		if s, _ := v.(string); s != "" {
			return s
		}
	}
	if v, ok := values.Get(p.Values, "serviceId"); ok {
		if s, _ := v.(string); s != "" {
			return s
		}
	}
	return p.Release.Name
}

func httpGet(ctx context.Context, url string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return b, resp.StatusCode, err
}

func modelIDs(body []byte) []string {
	var doc struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil
	}
	out := make([]string, 0, len(doc.Data))
	for _, d := range doc.Data {
		if d.ID != "" {
			out = append(out, d.ID)
		}
	}
	return out
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}
