package catalog

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetcherAppendsIndexBySchema(t *testing.T) {
	f1, err := NewFetcherWithSchema("https://models.example.com/catalog/", SchemaV1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(f1.String(), "/index.json") {
		t.Fatalf("v1: got %s", f1)
	}
	f2, err := NewFetcherWithSchema("https://models.example.com/catalog/", SchemaV2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(f2.String(), "/index-v2.json") {
		t.Fatalf("v2: got %s", f2)
	}
	// Explicit .json wins.
	fx, err := NewFetcherWithSchema("https://models.example.com/catalog/index-v2.json", SchemaV1)
	if err != nil {
		t.Fatal(err)
	}
	if fx.String() != "https://models.example.com/catalog/index-v2.json" {
		t.Fatalf("explicit: got %s", fx)
	}
}

// v1 through the interface: each variant is at its entry's version, pinned by
// the entry file.
func TestOpenV1ThroughTheInterface(t *testing.T) {
	srv := catalogServer(t)
	c, err := Open(context.Background(), srv.URL+"/catalog")
	if err != nil {
		t.Fatal(err)
	}
	if c.Schema() != SchemaV1 {
		t.Fatalf("schema = %q", c.Schema())
	}
	e, err := c.(*V1).Entry(context.Background(), "m", "")
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Resolve(context.Background(), "m", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "1.1.0" || r.Digest != e.Digest || r.Variant.ID != "v" || r.ServedName != "m" {
		t.Fatalf("resolved = %+v", r)
	}
	revs, err := c.History(context.Background(), "m", "v")
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 2 || revs[0].Version != "1.1.0" || !revs[0].Current || revs[0].Digest != e.Digest ||
		revs[1].Version != "1.0.0" || revs[1].Current {
		t.Errorf("history = %+v, want both published, newest first", revs)
	}
	var nf *NotFoundError
	if _, err := c.History(context.Background(), "m", "nope"); !errors.As(err, &nf) {
		t.Errorf("an unknown variant is not found: %v", err)
	}
	m, _ := c.Model("m")
	if len(m.Variants) != 1 || m.Variants[0].Version != "1.1.0" || m.Variants[0].Digest != e.Digest || m.DefaultVariant != "v" {
		t.Errorf("model = %+v", m)
	}
	old, err := c.Resolve(context.Background(), "m", "v", "1.0.0")
	if err != nil || old.Version != "1.0.0" {
		t.Fatalf("v at 1.0.0 = %+v, %v", old, err)
	}
	if d, ok := c.Published("m", "v", "1.0.0"); !ok || d != old.Digest {
		t.Errorf("published 1.0.0 = %q, %v", d, ok)
	}
}

func v2FixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	root := filepath.Join("..", "..", "testdata", "catalog-v2")
	if _, err := os.Stat(filepath.Join(root, "index-v2.json")); err != nil {
		t.Fatalf("v2 fixtures: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/catalog/", func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, "/catalog/")
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		_, _ = w.Write(b)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func TestOpenV2ResolvesVariants(t *testing.T) {
	srv := v2FixtureServer(t)
	c, err := OpenWithSchema(context.Background(), srv.URL+"/catalog/", SchemaV2)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.(*V2); !ok || c.Schema() != SchemaV2 {
		t.Fatalf("got %T, schema %q", c, c.Schema())
	}
	m, ok := c.Model("qwen3-8b")
	if !ok || len(m.Variants) < 2 || m.DefaultVariant != "vllm-tp1-910b" {
		t.Fatalf("model = %+v", m)
	}
	r, err := c.Resolve(context.Background(), "qwen3-8b", "vllm-tp1-910b", "")
	if err != nil {
		t.Fatal(err)
	}
	pin := m.Variants[0]
	if r.Version != pin.Version || r.Digest != pin.Digest || r.ServedName != "Qwen3-8B" || !r.Variant.Default {
		t.Fatalf("resolved = %+v, pin %+v", r, pin)
	}
	if len(r.Variant.Requires.GPUProduct) == 0 || r.Variant.Requires.GPUProduct[0] != "Ascend-910B3" ||
		r.Variant.Requires.VendorOrDefault() != "ascend" || r.Variant.Values == nil {
		t.Fatalf("variant = %+v", r.Variant)
	}
}

func TestOpenV2LocalDir(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "catalog-v2")
	c, err := OpenWithSchema(context.Background(), root, SchemaV2)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Resolve(context.Background(), "kimi-k2.5", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Variant.ID != "sglang-pp2-lws" || r.Variant.Requires.TopologyOrDefault() != TopologyLWS {
		t.Fatalf("default variant = %+v", r.Variant)
	}
}

// An explicit index-v2.json loads as v2 without a schemaVersion -- the CLI has
// no other way to say it -- and a contradicting one says what to change.
func TestExplicitV2IndexNeedsNoSchemaVersion(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "catalog-v2")
	srv := v2FixtureServer(t)
	for _, loc := range []string{filepath.Join(root, "index-v2.json"), srv.URL + "/catalog/index-v2.json"} {
		c, err := Open(context.Background(), loc)
		if err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		if c.Schema() != SchemaV2 {
			t.Fatalf("%s: schema = %q", loc, c.Schema())
		}
		if _, err := c.Resolve(context.Background(), "qwen3-8b", "", ""); err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
	}
	if _, err := OpenWithSchema(context.Background(), filepath.Join(root, "index-v2.json"), SchemaV1); err == nil ||
		!strings.Contains(err.Error(), "set schemaVersion: v2") {
		t.Errorf("explicit v1 on a v2 index: %v", err)
	}
}
