package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/cluster"
)

func TestSLOProxiesTheDefineAndTheDecision(t *testing.T) {
	var gotPut bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer slo-secret" {
			t.Errorf("auth: %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/config/glm" {
			t.Errorf("path %s", r.URL.Path)
		}
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(map[string]any{
				"route":             "glm",
				"priority":          0,
				"highPriority":      false,
				"minimumDeployment": map[string]any{"type": "replica", "value": 1},
			})
		default:
			gotPut = true
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["route"] != "glm" {
				t.Errorf("route: %v", body["route"])
			}
			json.NewEncoder(w).Encode(body)
		}
	}))
	defer up.Close()

	cfg := testConfig("prod")
	cfg.Server.AllowDeploy = true
	s := New(cfg, cluster.Fake{
		Maps: map[string]map[string]string{
			"swiss/site-profile": {"profile.yaml": profileYAML + "\nscaler:\n  sloAddress: " + up.URL + "\n  sloTokenSecret: llm-scaler/slo-api\n"},
		},
		Secrets: map[string]map[string]string{"llm-scaler/slo-api": {"token": "slo-secret"}},
	}, discardLogger(), "test")
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	code, body := get(t, srv, "/api/releases/modelforge/glm/slo")
	if code != 200 || body["found"] != true {
		t.Fatalf("get %d %v", code, body)
	}
	if body["route"] != "glm" || body["priority"] != float64(0) {
		t.Fatalf("config: %v", body)
	}
	min, _ := body["minimumDeployment"].(map[string]any)
	if min["value"] != float64(1) {
		t.Fatalf("minimumDeployment: %v", body["minimumDeployment"])
	}

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/releases/modelforge/glm/slo", strings.NewReader(
		`{"priority":4,"minimumDeployment":{"type":"replica","value":1}}`,
	))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !gotPut {
		t.Fatalf("put status %d forwarded %v", resp.StatusCode, gotPut)
	}
}

func TestSLOPutIsRefusedWhenReadOnly(t *testing.T) {
	cfg := testConfig("prod")
	s := New(cfg, fakeProbe(), discardLogger(), "test")
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/releases/modelforge/glm/slo", strings.NewReader(`{"ttft":{}}`))
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
	code, body := get(t, srv, "/api/releases/modelforge/glm/slo")
	if code != http.StatusConflict {
		t.Fatalf("status %d: %v", code, body)
	}
}
