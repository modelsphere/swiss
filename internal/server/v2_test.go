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

// The listing serves models, each variant at its own pin, for either schema;
// only v1 also serves its index.
func TestCatalogListingServesModelsForEitherSchema(t *testing.T) {
	code, body := get(t, v2Server(t), "/api/catalog?catalog=next")
	if code != http.StatusOK {
		t.Fatalf("v2: status %d: %v", code, body)
	}
	if body["schema"] != "v2" || body["site"] != "https://modelsphere.github.io/model-catalog/" || body["index"] != nil {
		t.Errorf("v2 listing = schema %v site %v index %v", body["schema"], body["site"], body["index"])
	}
	qwen := modelNamed(t, body, "qwen3-8b")
	if qwen["defaultVariant"] != "vllm-tp1-910b" {
		t.Errorf("defaultVariant = %v", qwen["defaultVariant"])
	}
	for _, v := range qwen["variants"].([]any) {
		v := v.(map[string]any)
		if v["version"] != "1.0.0" || !strings.HasPrefix(v["digest"].(string), "sha256:") || v["values"] != nil {
			t.Errorf("v2 variant = %v", v)
		}
	}

	code, body = get(t, testServer(t, fakeProbe()), "/api/catalog")
	if code != http.StatusOK {
		t.Fatalf("v1: status %d: %v", code, body)
	}
	if body["schema"] != "v1" || body["index"] == nil {
		t.Errorf("v1 listing = schema %v, index %v", body["schema"], body["index"] != nil)
	}
	m := modelNamed(t, body, "qwen3.6-35b-a3b")
	for _, v := range m["variants"].([]any) {
		if v.(map[string]any)["version"] == "" {
			t.Errorf("v1 variant has no version: %v", v)
		}
	}
}

// The cluster says which schema each catalog is, so a page can mark v1 legacy.
func TestClusterNamesEachCatalogsSchema(t *testing.T) {
	for want, srv := range map[string]*httptest.Server{"v2": v2Server(t), "v1": testServer(t, fakeProbe())} {
		_, body := get(t, srv, "/api/cluster")
		cats := body["catalogs"].([]any)
		if got := cats[0].(map[string]any)["schema"]; got != want {
			t.Errorf("schema = %v, want %s", got, want)
		}
	}
}

// The model endpoint resolves one variant, at its own version on v2; v1 also
// serves the entry.
func TestCatalogModelResolvesAVariantForEitherSchema(t *testing.T) {
	code, body := get(t, v2Server(t), "/api/catalog/qwen3-8b?catalog=next&variant=vllm-tp2-910b")
	if code != http.StatusOK {
		t.Fatalf("v2: status %d: %v", code, body)
	}
	v := body["variant"].(map[string]any)
	if body["schema"] != "v2" || body["entry"] != nil || v["id"] != "vllm-tp2-910b" || v["version"] != "1.0.0" ||
		v["servedName"] != "Qwen3-8B" || !strings.HasPrefix(v["digest"].(string), "sha256:ed023547") {
		t.Errorf("v2 model = schema %v entry %v variant %v", body["schema"], body["entry"], v)
	}
	if v["values"] == nil || v["image"].(map[string]any)["repository"] != "quay.io/ascend/vllm-ascend" {
		t.Errorf("a resolved variant carries its values and image: %v", v)
	}
	if body["model"].(map[string]any)["name"] != "qwen3-8b" || body["localPath"] == nil {
		t.Errorf("model %v, localPath %v", body["model"], body["localPath"])
	}
	for _, path := range []string{"/api/catalog/nope?catalog=next", "/api/catalog/qwen3-8b?catalog=next&variant=nope",
		"/api/catalog/qwen3-8b?catalog=next&variant=vllm-tp2-910b&version=0.9.0"} {
		if code, body := get(t, v2Server(t), path); code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404: %v", path, code, body)
		}
	}

	code, body = get(t, testServer(t, fakeProbe()), "/api/catalog/qwen3.6-35b-a3b")
	if code != http.StatusOK {
		t.Fatalf("v1: status %d: %v", code, body)
	}
	v = body["variant"].(map[string]any)
	if body["schema"] != "v1" || body["entry"] == nil || v["default"] != true || v["version"] == "" {
		t.Errorf("v1 model = schema %v entry %v variant %v", body["schema"], body["entry"] != nil, v)
	}
}

func modelNamed(t *testing.T, listing map[string]any, name string) map[string]any {
	t.Helper()
	for _, m := range listing["models"].([]any) {
		if m := m.(map[string]any); m["name"] == name {
			return m
		}
	}
	t.Fatalf("no model %s in %v", name, listing["models"])
	return nil
}
