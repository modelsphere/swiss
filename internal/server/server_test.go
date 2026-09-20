package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aceforeverd/swiss/internal/cluster"
	"github.com/aceforeverd/swiss/internal/config"
)

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

const profileYAML = `
name: prod
namespace: modelforge
model:
  pathTemplate: /mnt/disk0/models/{{name}}
`

func testServer(t *testing.T, probe cluster.Probe) *httptest.Server {
	t.Helper()
	cfg := testConfig("prod-b300")
	cfg.Server.Peers = []config.Peer{{Name: "dev", URL: "https://swiss.dev.internal"}}
	if err := cfg.ValidateServer(); err != nil {
		t.Fatal(err)
	}
	s := New(cfg, probe, discardLogger(), "test")
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func fakeProbe() cluster.Fake {
	return cluster.Fake{
		Maps: map[string]map[string]string{
			"swiss/site-profile": {"profile.yaml": profileYAML},
		},
	}
}

func get(t *testing.T, srv *httptest.Server, path string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%s: not JSON: %s", path, b)
	}
	return resp.StatusCode, out
}

func TestClusterEndpointCarriesProfileAndPeers(t *testing.T) {
	srv := testServer(t, fakeProbe())
	code, body := get(t, srv, "/api/cluster")
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	if body["name"] != "prod-b300" || body["namespace"] != "modelforge" {
		t.Errorf("unexpected cluster info: %v", body)
	}
	if body["catalogRef"] == "" || body["version"] != "test" {
		t.Errorf("missing catalog ref or version: %v", body)
	}
	if len(body["peers"].([]any)) != 1 {
		t.Errorf("peers not served: %v", body["peers"])
	}
}

func TestCatalogEndpointServesIndexOnly(t *testing.T) {
	srv := testServer(t, fakeProbe())
	code, body := get(t, srv, "/api/catalog")
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	idx := body["index"].(map[string]any)
	models := idx["models"].([]any)
	if len(models) == 0 {
		t.Fatal("no models")
	}
	// A listing must not carry variant values -- that is what keeps it one
	// request rather than one per model.
	first := models[0].(map[string]any)
	for _, v := range first["variants"].([]any) {
		if _, ok := v.(map[string]any)["values"]; ok {
			t.Error("index leaked entry values into the listing")
		}
	}
}

func TestCatalogModelEndpointFetchesTheEntry(t *testing.T) {
	srv := testServer(t, fakeProbe())
	code, body := get(t, srv, "/api/catalog/qwen3.6-35b-a3b")
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	entry := body["entry"].(map[string]any)
	variants := entry["variants"].([]any)
	if _, ok := variants[0].(map[string]any)["values"]; !ok {
		t.Error("an entry fetch must include the variant values")
	}

	if code, _ := get(t, srv, "/api/catalog/nope"); code != 404 {
		t.Errorf("unknown model should 404, got %d", code)
	}
}

// The reconciliation view exists for this row: a release nobody's inventory
// knows about is how two engines end up on one set of GPUs.
func TestDeploymentsFlagsUntrackedReleases(t *testing.T) {
	probe := fakeProbe()
	probe.Rel = []cluster.Release{
		{Name: "by-hand", Namespace: "modelforge", Chart: "sglang-0.8.0", Status: "deployed", Revision: 1},
		{Name: "glm-53", Namespace: "modelforge", Chart: "sglang-0.8.0", Status: "deployed", Revision: 4,
			SwissPlan: []byte("source:\n  model: qwen3.6-35b-a3b\n  variant: sglang-tp2\n  ref: sha256:stale\nprofile: prod\n")},
	}
	srv := testServer(t, probe)
	code, body := get(t, srv, "/api/deployments")
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	summary := body["summary"].(map[string]any)
	if summary["untracked"].(float64) != 1 {
		t.Errorf("want 1 untracked release, got %v", summary)
	}
	// The managed one was deployed from a catalog ref that no longer matches.
	if summary["catalogBehind"].(float64) != 1 {
		t.Errorf("want 1 release behind the catalog, got %v", summary)
	}

	for _, d := range body["deployments"].([]any) {
		row := d.(map[string]any)
		switch row["release"] {
		case "by-hand":
			if row["managed"].(bool) || row["drift"] == "" {
				t.Errorf("hand-installed release must be reported untracked: %v", row)
			}
		case "glm-53":
			if !row["managed"].(bool) || row["model"] != "qwen3.6-35b-a3b" {
				t.Errorf("plan not read off the release: %v", row)
			}
		}
	}
}

func TestReadyzNamesTheFailingDependency(t *testing.T) {
	probe := fakeProbe()
	delete(probe.Maps, "swiss/site-profile") // profile ConfigMap missing
	srv := testServer(t, probe)
	code, body := get(t, srv, "/readyz")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", code)
	}
	checks := body["checks"].(map[string]any)
	if checks["profile"] == "ok" {
		t.Error("profile check should have failed")
	}
	if checks["cluster"] != "ok" {
		t.Errorf("cluster should still be ok: %v", checks)
	}
}

// A published catalog going briefly unreachable should not empty the
// marketplace, so a failed refresh keeps serving what was last fetched.
func TestCatalogRefreshFailureServesPrevious(t *testing.T) {
	cfg := testConfig("c")
	cfg.Server.CacheTTL = time.Nanosecond
	s := New(cfg, fakeProbe(), discardLogger(), "test")

	first, err := s.Catalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.Catalog = "/nonexistent/catalog"
	again, err := s.Catalog(t.Context())
	if err != nil {
		t.Fatalf("a failed refresh must fall back, not fail: %v", err)
	}
	if again.Ref != first.Ref {
		t.Error("fallback did not serve the previously fetched catalog")
	}
}

// The shipped example must stay loadable. It is the thing people copy, and the
// chart's ConfigMap is written to the same shape -- so a field renamed in
// Config without updating the example breaks both, silently, until a pod
// crashloops.
func TestExampleConfigLoads(t *testing.T) {
	cfg, err := config.Load("../../examples/swiss.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateServer(); err != nil {
		t.Fatalf("the shipped example must satisfy swissd: %v", err)
	}
	// A duration that silently parsed as zero would be defaulted and never
	// noticed, so assert the example's own value survived.
	if cfg.Server.CacheTTL != 60*time.Second {
		t.Errorf("cacheTTL = %s, want 60s -- duration parsing may be silently defaulting", cfg.Server.CacheTTL)
	}
	if len(cfg.Server.Peers) == 0 {
		t.Error("the example should demonstrate peers; the switcher is not obvious otherwise")
	}
}

func testConfig(cluster string) *config.Config {
	c := &config.Config{
		Catalog: "../../../swiss-catalog",
		Cluster: config.Cluster{
			Name:    cluster,
			Profile: config.Profile{ConfigMap: "swiss/site-profile", Key: "profile.yaml"},
		},
		Server: config.Server{Addr: ":0", CacheTTL: 60 * time.Second},
	}
	c.Origin = "test"
	return c
}
