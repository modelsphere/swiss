package server

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aceforeverd/swiss/internal/cluster"
	"github.com/aceforeverd/swiss/internal/config"
	"github.com/aceforeverd/swiss/internal/plan"
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
	first := models[0].(map[string]any)
	if first["latest"] == "" || len(first["versions"].([]any)) == 0 {
		t.Fatalf("a model must publish at least one version: %v", first)
	}
	// A listing must not carry variant values -- that is what keeps it one
	// request rather than one per model.
	for _, v := range first["versions"].([]any) {
		for _, va := range v.(map[string]any)["variants"].([]any) {
			if _, ok := va.(map[string]any)["values"]; ok {
				t.Error("index leaked entry values into the listing")
			}
		}
	}
}

func TestCatalogModelEndpointFetchesTheEntry(t *testing.T) {
	srv := testServer(t, fakeProbe())
	code, body := get(t, srv, "/api/catalog/modelforge")
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
			SwissFiles: map[string]string{"plan.yaml": "source:\n  model: modelforge\n  variant: sglang-tp2\n  ref: sha256:stale\nprofile: prod\n"}},
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
			if !row["managed"].(bool) || row["model"] != "modelforge" {
				t.Errorf("plan not read off the release: %v", row)
			}
		}
	}
}

// Readiness is about the cluster only. A catalog or profile that has not been
// fetched, or cannot be, must not take swissd out of the Service.
func TestReadyzGatesOnTheClusterOnly(t *testing.T) {
	probe := fakeProbe()
	delete(probe.Maps, "swiss/site-profile")
	srv := testServer(t, probe)

	code, body := get(t, srv, "/readyz")
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d: %v", code, body)
	}
	checks := body["checks"].(map[string]any)
	if checks["cluster"] != "ok" {
		t.Errorf("cluster should be ok: %v", checks)
	}
	if checks["catalog"] != "not fetched" || checks["profile"] != "not fetched" {
		t.Errorf("readiness must not fetch anything: %v", checks)
	}

	probe.PingErr = errors.New("connection refused")
	srv2 := testServer(t, probe)
	if code, body := get(t, srv2, "/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("an unreachable cluster must be not-ready: %d %v", code, body)
	}
}

// A readiness probe runs every ten seconds; it must not drive a catalog fetch
// on an idle server, forever.
func TestReadyzDoesNotFetchTheCatalog(t *testing.T) {
	srv := testServer(t, fakeProbe())
	for range 3 {
		get(t, srv, "/readyz")
	}
	_, body := get(t, srv, "/readyz")
	if body["checks"].(map[string]any)["catalog"] != "not fetched" {
		t.Fatal("readyz fetched the catalog")
	}

	// Once something actually uses it, readyz reports the cached ref.
	get(t, srv, "/api/catalog")
	_, body = get(t, srv, "/readyz")
	if body["checks"].(map[string]any)["catalog"] == "not fetched" {
		t.Fatal("readyz should report an already-fetched catalog")
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
		// No login: these tests are about what the endpoints answer, not about
		// who is allowed to ask. The login itself is tested in auth_test.go,
		// which turns it back on.
		Server: config.Server{
			Addr: ":0", CacheTTL: 60 * time.Second,
			Auth: config.Auth{Disabled: true, TokenTTL: 24 * time.Hour, CookieSecure: "auto"},
		},
	}
	c.Origin = "test"
	return c
}

// planValues is what helm receives: the union of a plan's layer documents. The
// plan stores the layers and nothing else, so a test asking "what got composed"
// unions them the same way every other reader does.
func planValues(body map[string]any) map[string]any {
	layers, _ := body["layers"].(map[string]any)
	out := map[string]any{}
	for _, layer := range plan.Layers {
		tree, ok := layers[layer].(map[string]any)
		if !ok {
			continue
		}
		mergeInto(out, tree)
	}
	return out
}

func mergeInto(dst, src map[string]any) {
	for k, v := range src {
		if sub, ok := v.(map[string]any); ok {
			cur, _ := dst[k].(map[string]any)
			if cur == nil {
				cur = map[string]any{}
			}
			mergeInto(cur, sub)
			dst[k] = cur
			continue
		}
		dst[k] = v
	}
}

// layerOf is which layer's value for a path survived. Read back to front:
// several documents may set one path, and the last writer wins.
func layerOf(body map[string]any, path string) string {
	layers, _ := body["layers"].(map[string]any)
	for i := len(plan.Layers) - 1; i >= 0; i-- {
		layer := plan.Layers[i]
		cur, ok := layers[layer].(map[string]any)
		if !ok {
			continue
		}
		var node any = cur
		found := true
		for _, seg := range strings.Split(path, ".") {
			m, isMap := node.(map[string]any)
			if !isMap {
				found = false
				break
			}
			node, found = m[seg]
			if !found {
				break
			}
		}
		if found {
			return layer
		}
	}
	return ""
}

// layerDoc is one of a plan's override documents, as the API returns them.
func layerDoc(body map[string]any, layer string) map[string]any {
	layers, _ := body["layers"].(map[string]any)
	doc, _ := layers[layer].(map[string]any)
	return doc
}
