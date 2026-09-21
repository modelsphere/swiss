package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/aceforeverd/swiss/internal/cluster"
	"github.com/aceforeverd/swiss/internal/plan"
	"github.com/aceforeverd/swiss/internal/site"
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
	// Plan is the status key written beside the release on every apply. It is
	// the only thing that can say an apply was started and never finished --
	// helm's own status describes the last apply that returned.
	Plan *planStatus `json:"planStatus,omitempty"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()

	ns, release := r.PathValue("namespace"), r.PathValue("release")
	rel, err := s.releaseRecord(ctx, ns, release)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	out := releaseStatus{Release: release, Namespace: ns}
	if rel != nil {
		out.Exists, out.Revision, out.HelmStatus = true, rel.Revision, rel.Status
		if st := parseStatus(rel.SwissStatus); st.Phase != "" {
			out.Plan = &st
		}
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

// entrypointAuth is the per-request half of the entrypoint credentials: an
// operator testing a key that is not the one in the site profile, or a header
// the profile does not carry. Never echoed back in a result.
type entrypointAuth struct {
	APIKey  string            `json:"apiKey,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type probeResult struct {
	URL       string   `json:"url"`
	OK        bool     `json:"ok"`
	Status    int      `json:"status,omitempty"`
	LatencyMS int64    `json:"latencyMs"`
	Models    []string `json:"models,omitempty"`
	Error     string   `json:"error,omitempty"`
	Body      string   `json:"body,omitempty"`
	// SentHeaders names the headers the call carried, values omitted. Enough to
	// tell "the key was not sent" from "the key was wrong"; never the key.
	SentHeaders []string `json:"sentHeaders,omitempty"`
}

// handleProbe asks the entrypoint whether the model is actually servable.
//
// Through openresty rather than the pod: a ready pod behind a route that was
// never published serves nobody, and that gap is exactly what the ModelRoute
// plumbing exists to close. Nothing else in swiss checks it end to end.
func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()

	var auth entrypointAuth
	// An empty body is a valid request: it means "use whatever the profile says".
	_ = json.NewDecoder(r.Body).Decode(&auth)

	ns, release := r.PathValue("namespace"), r.PathValue("release")
	p, err := s.currentPlan(ctx, ns, release)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	base, hdr, err := s.entrypoint(ctx, p, auth)
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	url := base + "/v1/models"

	res := probeResult{URL: url, SentHeaders: sentHeaderNames(hdr)}
	started := time.Now()
	body, code, err := httpGet(ctx, url, hdr)
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

type chatRequest struct {
	// API selects the shape of the request: "chat" (/v1/chat/completions),
	// "completions" (/v1/completions) or "messages" (/v1/messages).
	API    string `json:"api,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	// Model overrides the served name taken from the plan, for the case where
	// the engine is serving under a name the plan does not predict.
	Model     string `json:"model,omitempty"`
	MaxTokens int    `json:"maxTokens,omitempty"`
	entrypointAuth
}

type chatResult struct {
	URL         string   `json:"url"`
	API         string   `json:"api"`
	Model       string   `json:"model,omitempty"`
	OK          bool     `json:"ok"`
	Status      int      `json:"status,omitempty"`
	LatencyMS   int64    `json:"latencyMs"`
	Reply       string   `json:"reply,omitempty"`
	Error       string   `json:"error,omitempty"`
	Body        string   `json:"body,omitempty"`
	SentHeaders []string `json:"sentHeaders,omitempty"`
}

// handleChat sends a real inference request through the entrypoint.
//
// /v1/models, which handleProbe calls, proves the route resolves and the engine
// answers. It does not prove the model can generate a token: weights can be
// half-loaded, a tensor-parallel peer can be missing, and the model list still
// comes back. This is the check that costs a few tokens and actually answers
// the question, so it is manual rather than polled.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 2*time.Minute)
	defer cancel()

	var req chatRequest
	// An empty body is a valid request: it means "just check it answers".
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Prompt == "" {
		req.Prompt = "Reply with the single word: ok"
	}
	if req.MaxTokens <= 0 {
		req.MaxTokens = 32
	}
	if req.API == "" {
		req.API = "chat"
	}

	ns, release := r.PathValue("namespace"), r.PathValue("release")
	p, err := s.currentPlan(ctx, ns, release)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	base, hdr, err := s.entrypoint(ctx, p, req.entrypointAuth)
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, err.Error())
		return
	}

	model := req.Model
	if model == "" {
		model = servedName(p)
	}

	path, body, err := chatBody(req.API, model, req.Prompt, req.MaxTokens)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	res := chatResult{URL: base + path, API: req.API, Model: model, SentHeaders: sentHeaderNames(hdr)}
	started := time.Now()
	raw, code, err := httpPostJSON(ctx, res.URL, body, hdr)
	res.LatencyMS = time.Since(started).Milliseconds()
	res.Status = code
	if err != nil {
		res.Error = err.Error()
		writeJSON(w, http.StatusOK, res)
		return
	}
	res.Reply = replyText(req.API, raw)
	// A 200 carrying no text is a failure worth reporting as one: the engine
	// answered and generated nothing.
	res.OK = code == http.StatusOK && res.Reply != ""
	if !res.OK {
		res.Body = snippet(raw)
	}
	writeJSON(w, http.StatusOK, res)
}

// entrypoint is the openresty base URL for a release's route and the headers to
// call it with. Both checks go through the entrypoint rather than the pod: a
// ready pod behind an unpublished route serves nobody.
func (s *Server) entrypoint(ctx context.Context, p *plan.Plan, auth entrypointAuth) (string, http.Header, error) {
	prof, err := s.Profile(ctx)
	if err != nil {
		return "", nil, err
	}
	if prof.Route.NginxService == "" {
		return "", nil, fmt.Errorf("site profile names no route.nginxService, so there is no entrypoint to ask")
	}
	svcNS, svcName, err := cluster.SplitRef(prof.Route.NginxService)
	if err != nil {
		return "", nil, err
	}
	route := routeOf(p)
	if route == "" {
		return "", nil, fmt.Errorf("this release publishes no route")
	}
	port := prof.Route.NginxPort
	if port == 0 {
		port = 8080
	}
	hdr, err := s.entrypointHeaders(ctx, prof.Route.Auth, auth)
	if err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("http://%s.%s.svc:%d/%s", svcName, svcNS, port, route), hdr, nil
}

// entrypointHeaders resolves what the checks send: the site profile's static
// headers and its API key, with the request's own overriding both.
//
// The profile is a ConfigMap, so it names a Secret rather than holding the key.
// A request-supplied key wins outright -- that is what makes it possible to test
// a credential before writing it into the cluster.
func (s *Server) entrypointHeaders(ctx context.Context, cfg site.RouteAuth, req entrypointAuth) (http.Header, error) {
	hdr := http.Header{}
	for k, v := range cfg.Headers {
		hdr.Set(k, v)
	}
	for k, v := range req.Headers {
		hdr.Set(k, v)
	}

	key := req.APIKey
	if key == "" && cfg.SecretRef != "" {
		data, err := s.probe.Secret(ctx, cfg.SecretRef)
		if err != nil {
			return nil, fmt.Errorf("route.auth.secretRef %s: %w", cfg.SecretRef, err)
		}
		name := cfg.SecretKey
		if name == "" {
			name = "apiKey"
		}
		if key = data[name]; key == "" {
			return nil, fmt.Errorf("secret %s has no key %q", cfg.SecretRef, name)
		}
	}
	if key != "" {
		hdr.Set(cfg.HeaderName(), cfg.KeyPrefix()+key)
	}
	return hdr, nil
}

// sentHeaderNames is what a result may report: names, never values.
func sentHeaderNames(h http.Header) []string {
	out := make([]string, 0, len(h))
	for k := range h {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func chatBody(api, model, prompt string, maxTokens int) (string, any, error) {
	messages := []map[string]string{{"role": "user", "content": prompt}}
	switch api {
	case "chat":
		return "/v1/chat/completions", map[string]any{
			"model": model, "messages": messages, "max_tokens": maxTokens, "stream": false,
		}, nil
	case "completions":
		return "/v1/completions", map[string]any{
			"model": model, "prompt": prompt, "max_tokens": maxTokens, "stream": false,
		}, nil
	case "messages":
		return "/v1/messages", map[string]any{
			"model": model, "messages": messages, "max_tokens": maxTokens,
		}, nil
	}
	return "", nil, fmt.Errorf("unknown api %q: want chat, completions or messages", api)
}

// replyText pulls the generated text out of whichever response shape came back.
// Decoded leniently: the point is to show the operator what the model said, and
// a field an engine spells differently should not read as a failed check.
func replyText(api string, raw []byte) string {
	var doc struct {
		Choices []struct {
			Text    string `json:"text"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	if api == "messages" {
		for _, c := range doc.Content {
			if c.Text != "" {
				return strings.TrimSpace(c.Text)
			}
		}
		return ""
	}
	for _, c := range doc.Choices {
		if c.Message.Content != "" {
			return strings.TrimSpace(c.Message.Content)
		}
		if c.Text != "" {
			return strings.TrimSpace(c.Text)
		}
	}
	return ""
}

// servedName is the name the engine answers to, which is model.name after the
// catalog's servedName projection -- not the catalog entry's own name.
func servedName(p *plan.Plan) string {
	if v, ok := values.Get(p.Values, "model.name"); ok {
		if s, _ := v.(string); s != "" {
			return s
		}
	}
	return p.Source.Model
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

func httpGet(ctx context.Context, url string, hdr http.Header) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	copyHeader(req, hdr)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return b, resp.StatusCode, err
}

// httpPostJSON is the chat check's transport. The timeout is generous because
// the first request against a freshly loaded model pays for a cold cache, and a
// check that times out at five seconds would report a healthy model as broken.
func httpPostJSON(ctx context.Context, url string, body any, hdr http.Header) ([]byte, int, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, 0, err
	}
	copyHeader(req, hdr)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 110 * time.Second}).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return b, resp.StatusCode, err
}

func copyHeader(req *http.Request, hdr http.Header) {
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
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
