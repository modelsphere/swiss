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

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/site"
	"github.com/modelsphere/swiss/internal/values"
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
	// URL is that route on the site's gateway, when the profile names one.
	URL string `json:"url,omitempty"`
	// Model is what the engine advertises, so a caller can name it in a request.
	Model string `json:"model,omitempty"`
	// AuthHeader and AuthPrefix are how the entrypoint expects a key, for the
	// benefit of a worked example. Names only -- the key itself stays in its
	// Secret. AuthPrefix is not omitted when empty: a custom header usually
	// carries the key with no scheme, and that is a real answer.
	AuthHeader string `json:"authHeader,omitempty"`
	AuthPrefix string `json:"authPrefix"`
	Warning    string `json:"warning,omitempty"`
	// Drift is set when the catalog republished the version and variant this
	// release runs, under a different entry digest. A warning: upgrade is
	// still accepted. Computed here, for one release, and not on the list.
	Drift string `json:"drift,omitempty"`
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
	//
	// Decoded from the record already in hand rather than through currentPlan,
	// which would look the same release up a second time. This endpoint is
	// polled every fifteen seconds by two pages.
	selector := "app.kubernetes.io/instance=" + release
	if p, err := planOf(rel); err == nil {
		selector = fmt.Sprintf("app=%s-%s", release, p.Engine)
		out.Route = routeOf(p)
		out.Model = servedName(p)
		out.Drift = s.releaseDigestDrift(ctx, p)
		if prof, err := s.Profile(ctx); err == nil {
			out.URL = prof.Route.ModelURL(out.Route)
			out.AuthHeader = prof.Route.Auth.HeaderName()
			out.AuthPrefix = prof.Route.Auth.KeyPrefix()
		}
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

// releaseDigestDrift compares the digest this release pinned with the digest
// its catalog now publishes for that same version and variant. A catalog that
// cannot be read leaves the warning empty: status is polled, and a dead
// catalog must not fail the page.
func (s *Server) releaseDigestDrift(ctx context.Context, p *plan.Plan) string {
	repo, err := s.ReleaseCatalog(ctx, p.Source.CatalogName, p.Source.Catalog)
	if err != nil {
		return ""
	}
	cat, err := s.openCatalog(ctx, repo.URL)
	if err != nil {
		return ""
	}
	return digestDrift(cat.Index, p.Source.Model, p.Source.Version, p.Source.Variant, p.Source.Digest)
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
	// Curl is the request swissd sent, as a runnable line. The credential is a
	// shell variable in it, never the key.
	Curl string `json:"curl,omitempty"`
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
	ep, err := s.entrypoint(ctx, p, auth)
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	url := ep.url("/v1/models")

	res := probeResult{
		URL:         url,
		SentHeaders: sentHeaderNames(ep.header),
		Curl:        ep.curl(http.MethodGet, url, nil),
	}
	started := time.Now()
	body, code, err := httpGet(ctx, url, ep.header)
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
	URL       string `json:"url"`
	API       string `json:"api"`
	Model     string `json:"model,omitempty"`
	OK        bool   `json:"ok"`
	Status    int    `json:"status,omitempty"`
	LatencyMS int64  `json:"latencyMs"`
	Reply     string `json:"reply,omitempty"`
	// Reasoning is the thinking channel, which is where a reasoning model puts
	// everything it produced when the token budget ran out before the answer.
	Reasoning    string   `json:"reasoning,omitempty"`
	FinishReason string   `json:"finishReason,omitempty"`
	Error        string   `json:"error,omitempty"`
	Body         string   `json:"body,omitempty"`
	SentHeaders  []string `json:"sentHeaders,omitempty"`
	Curl         string   `json:"curl,omitempty"`
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
	ep, err := s.entrypoint(ctx, p, req.entrypointAuth)
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

	res := chatResult{
		URL: ep.url(path), API: req.API, Model: model,
		SentHeaders: sentHeaderNames(ep.header),
	}
	res.Curl = ep.curl(http.MethodPost, res.URL, body)
	started := time.Now()
	raw, code, err := httpPostJSON(ctx, res.URL, body, ep.header)
	res.LatencyMS = time.Since(started).Milliseconds()
	res.Status = code
	if err != nil {
		res.Error = err.Error()
		writeJSON(w, http.StatusOK, res)
		return
	}
	out := parseReply(req.API, raw)
	res.Reply, res.Reasoning, res.FinishReason = out.text, out.reasoning, out.finish
	// A 200 carrying nothing generated is a failure worth reporting as one. Text
	// that never left the thinking channel still came from a model that ran.
	res.OK = code == http.StatusOK && (res.Reply != "" || res.Reasoning != "")
	if !res.OK {
		res.Body = snippet(raw)
	}
	writeJSON(w, http.StatusOK, res)
}

// call is the request a check makes: where it goes, what it carries, and which
// header holds the credential -- so the request can be shown without it.
type call struct {
	base   string
	header http.Header
	secret string // header name carrying the key, "" when none is sent
	shown  string // what that header prints as instead of the key
}

func (c call) url(path string) string { return c.base + path }

// curl is the request the server sent, as a line that can be run from a debug
// pod. The key is a shell variable rather than the value: a result names the
// headers it sent and never their values, and this is a result.
func (c call) curl(method, url string, body any) string {
	b := &strings.Builder{}
	b.WriteString("curl -sS")
	if method != http.MethodGet {
		fmt.Fprintf(b, " -X %s", method)
	}
	hdr := c.header.Clone()
	if hdr == nil {
		hdr = http.Header{}
	}
	if body != nil {
		hdr.Set("Content-Type", "application/json")
	}
	for _, name := range sentHeaderNames(hdr) {
		if c.secret != "" && name == http.CanonicalHeaderKey(c.secret) {
			// Double-quoted so the shell expands the variable. Inside single
			// quotes it would send the variable name itself, which looks like
			// it works and does not.
			fmt.Fprintf(b, " -H %s", shellExpand(name+": "+c.shown))
			continue
		}
		fmt.Fprintf(b, " -H %s", shellQuote(name+": "+hdr.Get(name)))
	}
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return ""
		}
		fmt.Fprintf(b, " -d %s", shellQuote(string(raw)))
	}
	fmt.Fprintf(b, " %s", shellQuote(url))
	return b.String()
}

// apiKeyVar is the shell variable the credential is left as.
const apiKeyVar = "$SWISS_API_KEY"

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// shellExpand quotes a value the shell must still expand a variable inside.
func shellExpand(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`").Replace(s) + `"`
}

// entrypoint is the openresty base URL for a release's route and the headers to
// call it with. Both checks go through the entrypoint rather than the pod: a
// ready pod behind an unpublished route serves nobody.
func (s *Server) entrypoint(ctx context.Context, p *plan.Plan, auth entrypointAuth) (call, error) {
	prof, err := s.Profile(ctx)
	if err != nil {
		return call{}, err
	}
	if prof.Route.NginxService == "" {
		return call{}, fmt.Errorf("site profile names no route.nginxService, so there is no entrypoint to ask")
	}
	svcNS, svcName, err := cluster.SplitRef(prof.Route.NginxService)
	if err != nil {
		return call{}, err
	}
	route := routeOf(p)
	if route == "" {
		return call{}, fmt.Errorf("this release publishes no route")
	}
	port := prof.Route.NginxPort
	if port == 0 {
		port = 8080
	}
	hdr, err := s.entrypointHeaders(ctx, prof.Route.Auth, auth)
	if err != nil {
		return call{}, err
	}
	c := call{base: fmt.Sprintf("http://%s.%s.svc:%d/%s", svcName, svcNS, port, route), header: hdr}
	// Whatever ended up under the auth header name is treated as the
	// credential, including one the profile set statically.
	if name := prof.Route.Auth.HeaderName(); hdr.Get(name) != "" {
		c.secret, c.shown = name, prof.Route.Auth.KeyPrefix()+apiKeyVar
	}
	return c, nil
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
		ref := cfg.SecretReference(s.namespace)
		data, err := s.probe.Secret(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("route.auth.secretRef %s: %w", ref, err)
		}
		name := cfg.SecretKey
		if name == "" {
			name = "apiKey"
		}
		if key = data[name]; key == "" {
			return nil, fmt.Errorf("secret %s has no key %q", ref, name)
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

// reply is what a check can report: the answer, the thinking channel it may
// have arrived in instead, and why generation stopped.
type reply struct {
	text      string
	reasoning string
	finish    string
}

// parseReply pulls the generated text out of whichever response shape came
// back. Both chat APIs put a reasoning model's output in a separate channel and
// leave the answer empty when the budget runs out mid-thought, so a check that
// reads only the answer reports a working model as broken.
//
// Decoded leniently: the point is to show the operator what the model said, and
// a field an engine spells differently should not read as a failed check.
func parseReply(api string, raw []byte) reply {
	var doc struct {
		Choices []struct {
			Text         string `json:"text"`
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content json.RawMessage `json:"content"`
				// sglang and vllm spell the thinking channel differently.
				ReasoningContent string `json:"reasoning_content"`
				Reasoning        string `json:"reasoning"`
			} `json:"message"`
		} `json:"choices"`
		Content    json.RawMessage `json:"content"`
		StopReason string          `json:"stop_reason"`
	}
	_ = json.Unmarshal(raw, &doc)

	if api == "messages" {
		text, thinking := contentText(doc.Content)
		return reply{text: text, reasoning: thinking, finish: doc.StopReason}
	}
	for _, c := range doc.Choices {
		text, thinking := contentText(c.Message.Content)
		if text == "" {
			text = strings.TrimSpace(c.Text)
		}
		if thinking == "" {
			thinking = strings.TrimSpace(c.Message.ReasoningContent)
		}
		if thinking == "" {
			thinking = strings.TrimSpace(c.Message.Reasoning)
		}
		r := reply{text: text, reasoning: thinking, finish: c.FinishReason}
		if r != (reply{}) {
			return r
		}
	}
	return reply{}
}

// contentText reads a content field that is a string in one engine and a list
// of typed blocks in the next, keeping thinking blocks out of the answer.
func contentText(raw json.RawMessage) (text, thinking string) {
	if len(raw) == 0 {
		return "", ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s), ""
	}
	var blocks []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return "", ""
	}
	var body, thought []string
	for _, b := range blocks {
		switch {
		case b.Thinking != "":
			thought = append(thought, b.Thinking)
		case b.Type == "thinking" || b.Type == "reasoning":
			thought = append(thought, b.Text)
		case b.Text != "":
			body = append(body, b.Text)
		}
	}
	return strings.TrimSpace(strings.Join(body, "")), strings.TrimSpace(strings.Join(thought, ""))
}

// servedName is the name the engine answers to, which is model.name after the
// catalog's servedName projection -- not the catalog entry's own name.
func servedName(p *plan.Plan) string {
	if v, ok := values.Get(p.Values(), "model.name"); ok {
		if s, _ := v.(string); s != "" {
			return s
		}
	}
	return p.Source.Model
}

// routeOf is the path this release publishes, or "" when it publishes none.
// Compose writes every feature flag into the plan, so modelRoute.enabled is
// always there to read: without this check a release with routing off still
// reported a route, and the status page advertised a URL nothing serves.
func routeOf(p *plan.Plan) string {
	if v, ok := values.Get(p.Values(), "modelRoute.enabled"); ok {
		if on, _ := v.(bool); !on {
			return ""
		}
	}
	if v, ok := values.Get(p.Values(), "modelRoute.nginx.route"); ok {
		if s, _ := v.(string); s != "" {
			return s
		}
	}
	if v, ok := values.Get(p.Values(), "serviceId"); ok {
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
