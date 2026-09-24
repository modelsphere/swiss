package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/values"
)

// Each API is a different request shape and a different place to find the
// answer. Getting either wrong reports a healthy model as broken.
func TestChatBodyPerAPI(t *testing.T) {
	for _, tc := range []struct {
		api     string
		path    string
		wantKey string
	}{
		{"chat", "/v1/chat/completions", "messages"},
		{"completions", "/v1/completions", "prompt"},
		{"messages", "/v1/messages", "messages"},
	} {
		path, body, err := chatBody(tc.api, "kimi", "ping", 8)
		if err != nil {
			t.Fatalf("%s: %v", tc.api, err)
		}
		if path != tc.path {
			t.Errorf("%s: path %q, want %q", tc.api, path, tc.path)
		}
		m := body.(map[string]any)
		if _, ok := m[tc.wantKey]; !ok {
			t.Errorf("%s: body has no %q: %v", tc.api, tc.wantKey, m)
		}
		if m["model"] != "kimi" || m["max_tokens"] != 8 {
			t.Errorf("%s: model and budget must be sent: %v", tc.api, m)
		}
	}

	if _, _, err := chatBody("grpc", "kimi", "ping", 8); err == nil {
		t.Error("an unknown api should be refused rather than guessed at")
	}
}

func TestParseReplyPerAPI(t *testing.T) {
	for _, tc := range []struct{ api, raw, want string }{
		{"chat", `{"choices":[{"message":{"content":" ok "}}]}`, "ok"},
		{"completions", `{"choices":[{"text":"ok"}]}`, "ok"},
		{"messages", `{"content":[{"type":"text","text":"ok"}]}`, "ok"},
		// Both chat APIs may send the text as blocks rather than a string.
		{"chat", `{"choices":[{"message":{"content":[{"type":"text","text":"ok"}]}}]}`, "ok"},
		{"chat", `{"choices":[]}`, ""},
		{"chat", `not json`, ""},
	} {
		if got := parseReply(tc.api, []byte(tc.raw)).text; got != tc.want {
			t.Errorf("%s %s: text %q, want %q", tc.api, tc.raw, got, tc.want)
		}
	}
}

// A reasoning model answers in a second channel, and with a small token budget
// that channel is all there is. Reading only the answer field reports a model
// that generated hundreds of tokens as one that generated none.
func TestParseReplyKeepsTheThinkingChannelSeparate(t *testing.T) {
	for _, tc := range []struct{ name, api, raw, text, reasoning string }{
		{
			name: "sglang reasoning_content",
			api:  "chat",
			raw:  `{"choices":[{"message":{"content":"","reasoning_content":"the user wants"},"finish_reason":"length"}]}`,
			text: "", reasoning: "the user wants",
		},
		{
			name: "vllm reasoning",
			api:  "chat",
			raw:  `{"choices":[{"message":{"content":null,"reasoning":"hmm"}}]}`,
			text: "", reasoning: "hmm",
		},
		{
			name: "thinking blocks precede the answer",
			api:  "messages",
			raw:  `{"content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`,
			text: "ok", reasoning: "hmm",
		},
		{
			name: "budget spent thinking",
			api:  "messages",
			raw:  `{"content":[{"type":"thinking","thinking":"hmm"}],"stop_reason":"max_tokens"}`,
			text: "", reasoning: "hmm",
		},
	} {
		got := parseReply(tc.api, []byte(tc.raw))
		if got.text != tc.text || got.reasoning != tc.reasoning {
			t.Errorf("%s: got text %q reasoning %q, want %q / %q", tc.name, got.text, got.reasoning, tc.text, tc.reasoning)
		}
	}
}

// Why generation stopped is the difference between a broken model and one that
// ran out of budget, so it has to reach the page.
func TestParseReplyCarriesTheStopReason(t *testing.T) {
	if got := parseReply("chat", []byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"length"}]}`)).finish; got != "length" {
		t.Errorf("chat finish = %q", got)
	}
	if got := parseReply("messages", []byte(`{"content":[{"text":"ok"}],"stop_reason":"max_tokens"}`)).finish; got != "max_tokens" {
		t.Errorf("messages finish = %q", got)
	}
}

// The check goes through the entrypoint, like the /v1/models probe: a ready pod
// behind a route that was never published serves nobody.
func TestChatNeedsAnEntrypoint(t *testing.T) {
	_, doc := livePlan(t, planRequest{Model: "glm5.1", Release: "r", ServiceID: "r"})
	probe := liveProbe()
	probe.Rel = append(probe.Rel, cluster.Release{
		Name: "r", Namespace: "modelforge", Status: "deployed", Revision: 1, SwissFiles: doc,
	})
	srv, _ := deployServerWith(t, probe, true)

	// profileYAML names no route.nginxService.
	code, out := post(t, srv, "/api/releases/modelforge/r/chat", map[string]any{})
	if code != http.StatusPreconditionFailed {
		t.Fatalf("want 412, got %d %v", code, out)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "nginxService") {
		t.Errorf("the refusal should name what is missing: %q", msg)
	}
}

// A check reports the request it made, so "swissd cannot reach it" can be told
// from "the request was wrong" by running the same line from a debug pod. The
// key is a shell variable in it: a result never carries the value of a header.
func TestCheckReportsTheCurlItSent(t *testing.T) {
	p, _ := livePlan(t, planRequest{
		Model: "modelforge", Release: "r", ServiceID: "r",
		Overrides: values.Tree{
			"modelRoute": values.Tree{"enabled": true, "nginx": values.Tree{"route": "glm-53"}},
		},
	})
	probe := liveProbe()
	probe.Secrets = map[string]map[string]string{"swiss/entrypoint": {"apiKey": "sk-must-not-appear"}}
	probe.Maps["swiss/site-profile"] = map[string]string{
		"profile.yaml": profileYAML +
			"route:\n  nginxService: infra/openresty\n  auth:\n    secretRef: swiss/entrypoint\n",
	}
	s := New(testConfig("prod-b300"), probe, discardLogger(), "test")

	ep, err := s.entrypoint(context.Background(), p, entrypointAuth{})
	if err != nil {
		t.Fatal(err)
	}
	if got := ep.url("/v1/models"); got != "http://openresty.infra.svc:8080/glm-53/v1/models" {
		t.Fatalf("url = %q", got)
	}

	get := ep.curl(http.MethodGet, ep.url("/v1/models"), nil)
	for _, want := range []string{
		"curl -sS",
		// Double quotes: single ones would send the variable name itself.
		`-H "Authorization: Bearer $SWISS_API_KEY"`,
		"'http://openresty.infra.svc:8080/glm-53/v1/models'",
	} {
		if !strings.Contains(get, want) {
			t.Errorf("curl %q is missing %q", get, want)
		}
	}
	if strings.Contains(get, "-X") {
		t.Errorf("a GET needs no method: %q", get)
	}

	post := ep.curl(http.MethodPost, ep.url("/v1/chat/completions"), map[string]any{"model": "kimi"})
	for _, want := range []string{
		"-X POST",
		"-H 'Content-Type: application/json'",
		`-d '{"model":"kimi"}'`,
	} {
		if !strings.Contains(post, want) {
			t.Errorf("curl %q is missing %q", post, want)
		}
	}
	for _, line := range []string{get, post} {
		if strings.Contains(line, "sk-must-not-appear") {
			t.Fatalf("the key reached the rendered request: %s", line)
		}
	}
}

func TestChatNeedsAPlanBesideTheRelease(t *testing.T) {
	srv, _ := deployServerWith(t, liveProbe(), true)
	code, out := post(t, srv, "/api/releases/modelforge/by-hand/chat", map[string]any{})
	if code != http.StatusNotFound {
		t.Fatalf("want 404 for a release swiss did not deploy, got %d %v", code, out)
	}
}

// The health check reads the cluster and spends a few tokens; it changes
// nothing, so a read-only swissd must still serve it.
func TestChatIsNotGatedByAllowDeploy(t *testing.T) {
	srv, _ := deployServer(t, false)
	code, _ := post(t, srv, "/api/releases/modelforge/glm-53/chat", map[string]any{})
	if code == http.StatusForbidden {
		t.Error("the health check changes nothing and must not need allowDeploy")
	}
}

// The status endpoint is where the detail page reads the install phase from.
// helm's own status describes the last apply that returned; this is the only
// thing that can report one that never did.
func TestStatusCarriesThePlanPhase(t *testing.T) {
	probe := liveProbe()
	probe.Rel = append(probe.Rel, cluster.Release{
		Name: "stuck", Namespace: "modelforge", Status: "pending-upgrade", Revision: 2,
		SwissFiles:  map[string]string{"plan.yaml": "source:\n  model: modelforge\n"},
		SwissStatus: []byte("phase: applying\naction: apply\nstartedAt: 2026-09-21T10:00:00Z\n"),
	})
	srv := testServer(t, probe)

	code, body := get(t, srv, "/api/releases/modelforge/stuck/status")
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	st, ok := body["planStatus"].(map[string]any)
	if !ok {
		t.Fatalf("the recorded phase must reach the detail page: %v", body)
	}
	if st["phase"] != "applying" || st["action"] != "apply" {
		t.Errorf("phase not carried through: %v", st)
	}
	if st["startedAt"] != "2026-09-21T10:00:00Z" {
		t.Errorf("startedAt not carried through: %v", st)
	}
}

// A release with nothing recorded beside it must not grow an invented phase.
func TestStatusOmitsThePhaseWhenNoneWasRecorded(t *testing.T) {
	srv := testServer(t, liveProbe())
	code, body := get(t, srv, "/api/releases/modelforge/by-hand/status")
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	if _, present := body["planStatus"]; present {
		t.Errorf("an untracked release has no phase: %v", body)
	}
}

// The status page shows where a model is reached from outside, which is the
// site's gateway and the route it publishes -- a fact no check needs and an
// operator always does.
func TestStatusCarriesTheGatewayURL(t *testing.T) {
	probe := liveProbe()
	probe.Maps["swiss/site-profile"] = map[string]string{"profile.yaml": profileYAML + "route:\n  gateway: https://llm.example.com\n"}
	// Routing on: a release that publishes no route has no outside URL, which
	// TestNoURLWhenRoutingIsOff covers.
	_, doc := livePlan(t, planRequest{
		Model: "glm5.1", Release: "r", ServiceID: "r",
		Overrides: values.Tree{"modelRoute": values.Tree{"enabled": true}},
	})
	probe.Rel = append(probe.Rel, cluster.Release{
		Name: "r", Namespace: "modelforge", Status: "deployed", Revision: 1, SwissFiles: doc,
	})
	srv := testServer(t, probe)

	code, body := get(t, srv, "/api/releases/modelforge/r/status")
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	if body["url"] != "https://llm.example.com/r" {
		t.Errorf("url = %v, want the gateway and the route", body["url"])
	}
}
