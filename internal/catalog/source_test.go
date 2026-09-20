package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testIndex = `{
  "apiVersion": "catalog.swiss/v1",
  "count": 1,
  "models": [
    {"name": "m", "source": {"hf": "org/m"},
     "variants": [{"id": "v", "engine": "sglang", "chart": {"name": "sglang", "version": "0.8.0"},
                   "requires": {"gpus": 2}}],
     "path": "models/m/entry.yaml"}
  ]
}`

const testEntry = `
apiVersion: catalog.swiss/v1
name: m
source: {hf: org/m}
variants:
  - id: v
    engine: sglang
    chart: {name: sglang, version: "0.8.0"}
    requires: {gpus: 2}
    values:
      extraArgs: [--tp-size=2]
`

func serve(t *testing.T, files map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for p, body := range files {
		mux.HandleFunc("/"+p, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		})
	}
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func TestOpenOverHTTPAndFetchEntry(t *testing.T) {
	srv := serve(t, map[string]string{
		"catalog/index.json":          testIndex,
		"catalog/models/m/entry.yaml": testEntry,
	})
	for _, loc := range []string{srv.URL + "/catalog", srv.URL + "/catalog/", srv.URL + "/catalog/index.json"} {
		c, err := Open(context.Background(), loc)
		if err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		if len(c.Index.Models) != 1 {
			t.Fatalf("%s: got %d models", loc, len(c.Index.Models))
		}
		e, err := c.Entry(context.Background(), "m")
		if err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		if e.Variants[0].Values == nil {
			t.Errorf("%s: entry fetched without its values", loc)
		}
	}
}

// The ref is a digest of the index bytes, so two consumers that read the same
// catalog agree on it without a git SHA or an ETag to coordinate through.
func TestRefIsContentAddressed(t *testing.T) {
	srv := serve(t, map[string]string{"catalog/index.json": testIndex})
	a, err := Open(context.Background(), srv.URL+"/catalog")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(context.Background(), srv.URL+"/catalog/index.json")
	if err != nil {
		t.Fatal(err)
	}
	if a.Ref != b.Ref || !strings.HasPrefix(a.Ref, "sha256:") {
		t.Fatalf("refs differ or malformed: %q vs %q", a.Ref, b.Ref)
	}
}

func TestIndexPathsCannotEscapeTheCatalog(t *testing.T) {
	bad := strings.Replace(testIndex, `"models/m/entry.yaml"`, `"../../../etc/passwd"`, 1)
	srv := serve(t, map[string]string{"catalog/index.json": bad})
	if _, err := Open(context.Background(), srv.URL+"/catalog"); err == nil {
		t.Fatal("an index is remote input; a path escaping the catalog must be refused")
	}
}

func TestIndexRejectsCountMismatch(t *testing.T) {
	bad := strings.Replace(testIndex, `"count": 1`, `"count": 7`, 1)
	if _, err := parseIndex([]byte(bad)); err == nil {
		t.Fatal("a half-written index must be refused, not guessed at")
	}
}

// The index summarises the entry. If they disagree, one is stale, and composing
// from the wrong one deploys a model nobody chose.
func TestStaleIndexIsDetected(t *testing.T) {
	srv := serve(t, map[string]string{
		"catalog/index.json":          testIndex,
		"catalog/models/m/entry.yaml": strings.Replace(testEntry, "name: m", "name: something-else", 1),
	})
	c, err := Open(context.Background(), srv.URL+"/catalog")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Entry(context.Background(), "m"); err == nil {
		t.Fatal("expected a stale-index error")
	}
}

func TestEntryValidationRunsOnFetch(t *testing.T) {
	// A site-owned key in a public catalog entry.
	bad := strings.Replace(testEntry, "extraArgs: [--tp-size=2]", "model: {localPath: /mnt/x}", 1)
	srv := serve(t, map[string]string{
		"catalog/index.json":          testIndex,
		"catalog/models/m/entry.yaml": bad,
	})
	c, err := Open(context.Background(), srv.URL+"/catalog")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Entry(context.Background(), "m")
	if err == nil || !strings.Contains(err.Error(), "localPath") {
		t.Fatalf("fetched entries must be validated, got %v", err)
	}
}

func TestLocalDirectoryAndFileBothWork(t *testing.T) {
	for _, loc := range []string{"../../../swiss-catalog", "../../../swiss-catalog/index.json"} {
		c, err := Open(context.Background(), loc)
		if err != nil {
			t.Skipf("example catalog not present: %v", err)
		}
		if _, err := c.Entry(context.Background(), "qwen3.6-35b-a3b"); err != nil {
			t.Errorf("%s: %v", loc, err)
		}
	}
}
