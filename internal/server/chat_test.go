package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aceforeverd/swiss/internal/cluster"
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

func TestReplyTextPerAPI(t *testing.T) {
	for _, tc := range []struct{ api, raw, want string }{
		{"chat", `{"choices":[{"message":{"content":" ok "}}]}`, "ok"},
		{"completions", `{"choices":[{"text":"ok"}]}`, "ok"},
		{"messages", `{"content":[{"text":"ok"}]}`, "ok"},
		{"chat", `{"choices":[]}`, ""},
		{"chat", `not json`, ""},
	} {
		if got := replyText(tc.api, []byte(tc.raw)); got != tc.want {
			t.Errorf("%s %s: got %q, want %q", tc.api, tc.raw, got, tc.want)
		}
	}
}

// The check goes through the entrypoint, like the /v1/models probe: a ready pod
// behind a route that was never published serves nobody.
func TestChatNeedsAnEntrypoint(t *testing.T) {
	_, doc := livePlan(t, planRequest{Model: "glm5.1", Release: "r", ServiceID: "r"})
	probe := liveProbe()
	probe.Rel = append(probe.Rel, cluster.Release{
		Name: "r", Namespace: "modelforge", Status: "deployed", Revision: 1, SwissPlan: doc,
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
		SwissPlan:   []byte("source:\n  model: modelforge\n"),
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
