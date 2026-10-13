package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// v2Server is a site whose one catalog, "next", is the checked-in v2 fixture.
func v2Server(t *testing.T) *httptest.Server {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "testdata", "catalog-v2"))
	if err != nil {
		t.Fatal(err)
	}
	probe := liveProbe()
	probe.Maps["swiss/site-profile"] = map[string]string{
		"profile.yaml": profileYAML + "catalogs:\n  - name: next\n    url: " + root + "\n    schemaVersion: v2\n",
	}
	srv, _ := deployServerWith(t, probe, true)
	return srv
}

// A plan from a v2 catalog pins the variant file, at the variant's own version.
func TestPlanFromAV2Catalog(t *testing.T) {
	srv := v2Server(t)
	code, p := post(t, srv, "/api/plans", map[string]any{
		"model": "qwen3-8b", "variant": "vllm-tp2-910b", "serviceId": "q", "chartVersion": "0.7.6",
	})
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, p)
	}
	src := p["source"].(map[string]any)
	if src["schema"] != "v2" || src["version"] != "1.0.0" || src["variant"] != "vllm-tp2-910b" || src["catalogName"] != "next" ||
		!strings.HasPrefix(src["digest"].(string), "sha256:ed023547") {
		t.Errorf("source = %v", src)
	}
}

// version is the variant's own: one its history does not list is refused, not
// swapped for the one it is at.
func TestPlanFromAV2CatalogRefusesAnotherVersion(t *testing.T) {
	srv := v2Server(t)
	code, out := post(t, srv, "/api/plans", map[string]any{
		"model": "qwen3-8b", "variant": "vllm-tp2-910b", "version": "0.9.0", "serviceId": "q", "chartVersion": "0.7.6",
	})
	if code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "has no version 0.9.0 (have: 1.0.0)") {
		t.Errorf("status %d: %v", code, out)
	}
}

// A variant's history, newest first, for either schema; the default variant's
// when none is named.
func TestCatalogHistory(t *testing.T) {
	srv := v2Server(t)
	code, body := get(t, srv, "/api/catalog/qwen3-8b/history?catalog=next")
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	revs := body["revisions"].([]any)
	top := revs[0].(map[string]any)
	if body["schema"] != "v2" || body["variant"] != "vllm-tp1-910b" || len(revs) != 1 ||
		top["version"] != "1.0.0" || top["current"] != true || top["commit"] != "2c4b33de827e2ee18f1581a692c390645dbfc1ef" {
		t.Errorf("v2 history = %v", body)
	}
	for _, path := range []string{"/api/catalog/qwen3-8b/history?catalog=next&variant=nope", "/api/catalog/nope/history?catalog=next"} {
		if code, body := get(t, srv, path); code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404: %v", path, code, body)
		}
	}

	v1 := testServer(t, liveProbe())
	code, body = get(t, v1, "/api/catalog/qwen3.6-35b-a3b/history")
	if code != http.StatusOK {
		t.Fatalf("v1: status %d: %v", code, body)
	}
	if body["schema"] != "v1" || len(body["revisions"].([]any)) == 0 || body["revisions"].([]any)[0].(map[string]any)["current"] != true {
		t.Errorf("v1 history = %v", body)
	}
}

// The listing and model pages serve v1's documents, which is what the web
// reads; a v2 catalog says so rather than impersonating one.
func TestV2CatalogIsNotListedForTheWebYet(t *testing.T) {
	srv := v2Server(t)
	for _, path := range []string{"/api/catalog?catalog=next", "/api/catalog/qwen3-8b?catalog=next"} {
		code, out := get(t, srv, path)
		if code != http.StatusNotImplemented || !strings.Contains(out["error"].(string), "schema v2") {
			t.Errorf("%s: status %d: %v", path, code, out)
		}
	}
}
