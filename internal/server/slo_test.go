package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/cluster"
)

// sloStub stands in for slo-api as it actually behaves: /readyz is public,
// /config/{route} needs the token, spec.priority is carried as a boolean, and
// any other top-level field is refused. A stub that echoed back whatever it
// was sent would hide the very mismatch this proxy exists to absorb.
func sloStub(t *testing.T, ready, registered bool, sent *map[string]any) *httptest.Server {
	t.Helper()
	allowed := map[string]bool{
		"route": true, "highPriority": true, "minimumDeployment": true,
		"maximumDeployment": true, "ttft": true, "otps": true,
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/readyz" {
			if ready {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
				return
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "not_ready", "reason": "cr watch not synced"})
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer slo-secret" {
			t.Errorf("auth: %q", got)
		}
		if r.URL.Path != "/config/glm" {
			t.Errorf("path %s", r.URL.Path)
		}
		if !registered {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
				"code": "not_found", "message": "route 'glm' not registered"}})
			return
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"route":             "glm",
				"highPriority":      false,
				"minimumDeployment": map[string]any{"type": "replica", "value": 1},
			})
		case http.MethodPut:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if sent != nil {
				*sent = body
			}
			for k := range body {
				if !allowed[k] {
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
						"code": "invalid_request", "message": "unknown field(s): " + k}})
					return
				}
			}
			_ = json.NewEncoder(w).Encode(body)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(up.Close)
	return up
}

func sloSwissd(t *testing.T, up *httptest.Server) *httptest.Server {
	t.Helper()
	cfg := testConfig("prod")
	cfg.Server.AllowDeploy = true
	s := New(cfg, cluster.Fake{
		Maps: map[string]map[string]string{
			"swiss/site-profile": {"profile.yaml": profileYAML + "\nscaler:\n  sloAddress: " + up.URL + "\n  sloTokenSecret: llm-scaler/slo-api\n"},
		},
		Secrets: map[string]map[string]string{"llm-scaler/slo-api": {"token": "slo-secret"}},
	}, discardLogger(), "test")
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func TestSLOProxiesTheDefineAndTheDecision(t *testing.T) {
	var sent map[string]any
	srv := sloSwissd(t, sloStub(t, true, true, &sent))

	code, body := get(t, srv, "/api/releases/models/glm/slo")
	if code != 200 || body["found"] != true {
		t.Fatalf("get %d %v", code, body)
	}
	// The server says highPriority; the client is handed the CRD's own integer
	// alongside it, so both vocabularies read the same.
	if body["route"] != "glm" || body["priority"] != float64(0) || body["highPriority"] != false {
		t.Fatalf("config: %v", body)
	}
	min, _ := body["minimumDeployment"].(map[string]any)
	if min["value"] != float64(1) {
		t.Fatalf("minimumDeployment: %v", body["minimumDeployment"])
	}

	code, body = put(t, srv, "/api/releases/models/glm/slo", map[string]any{
		"priority":          10,
		"minimumDeployment": map[string]any{"type": "replica", "value": 1},
	})
	if code != 200 {
		t.Fatalf("put %d %v", code, body)
	}
	if sent["route"] != "glm" || sent["highPriority"] != true {
		t.Errorf("upstream body: %v", sent)
	}
	if _, ok := sent["priority"]; ok {
		t.Errorf("priority is not a field the slo server knows: %v", sent)
	}
	if body["priority"] != float64(10) || body["highPriority"] != true {
		t.Errorf("reply: %v", body)
	}
}

// 1..9 are real tiers in the CRD — decision-gen schedules and preempts by them
// — but the server flattens anything below 10 to 0. Refuse here rather than
// report success for a number nobody wrote.
func TestSLORefusesAPriorityTheServerCannotStore(t *testing.T) {
	var sent map[string]any
	srv := sloSwissd(t, sloStub(t, true, true, &sent))

	code, body := put(t, srv, "/api/releases/models/glm/slo", map[string]any{"priority": 4})
	if code != http.StatusBadRequest {
		t.Fatalf("status %d: %v", code, body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "high (10) or normal (0)") {
		t.Errorf("the message has to say what can be stored, got %q", msg)
	}
	if sent != nil {
		t.Errorf("nothing should have reached the slo server: %v", sent)
	}
}

// swissd adds `found` on a read. A client that sends back what it read must
// not be handed an error about a field swissd itself invented.
func TestSLOStripsTheMarkerItAddedOnRead(t *testing.T) {
	var sent map[string]any
	srv := sloSwissd(t, sloStub(t, true, true, &sent))

	code, body := get(t, srv, "/api/releases/models/glm/slo")
	if code != 200 {
		t.Fatalf("get %d %v", code, body)
	}
	code, body = put(t, srv, "/api/releases/models/glm/slo", body)
	if code != 200 {
		t.Fatalf("round trip %d: %v", code, body)
	}
	if _, ok := sent["found"]; ok {
		t.Errorf("found reached the slo server: %v", sent)
	}
}

// A 404 from a server whose CR watch never synced means "I know nothing about
// any route", not "this release has no requirement" — most often because it
// watches a different LLMSLORequirement API group than the installed CRD.
func TestSLOReportsAnUnsyncedServerRatherThanNoRequirement(t *testing.T) {
	srv := sloSwissd(t, sloStub(t, false, false, nil))

	code, body := get(t, srv, "/api/releases/models/glm/slo")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("get %d: %v", code, body)
	}
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "not ready") || !strings.Contains(msg, "API group") {
		t.Errorf("the message has to name the cause, got %q", msg)
	}
	if body["found"] == true {
		t.Errorf("an unsynced server cannot report found: %v", body)
	}

	code, body = put(t, srv, "/api/releases/models/glm/slo", map[string]any{"priority": 0})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("put %d: %v", code, body)
	}
}

// From a ready server the same 404 is the honest answer: no requirement yet.
func TestSLOFoundIsFalseWhenAReadyServerHasNoRequirement(t *testing.T) {
	srv := sloSwissd(t, sloStub(t, true, false, nil))

	code, body := get(t, srv, "/api/releases/models/glm/slo")
	if code != 200 || body["found"] != false {
		t.Fatalf("get %d: %v", code, body)
	}
}

func TestSLOPutIsRefusedWhenReadOnly(t *testing.T) {
	cfg := testConfig("prod")
	s := New(cfg, fakeProbe(), discardLogger(), "test")
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/releases/models/glm/slo", strings.NewReader(`{"ttft":{}}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestSLORequiresAToken(t *testing.T) {
	srv := testServer(t, fakeProbe())
	code, body := get(t, srv, "/api/releases/models/glm/slo")
	if code != http.StatusConflict {
		t.Fatalf("status %d: %v", code, body)
	}
}
