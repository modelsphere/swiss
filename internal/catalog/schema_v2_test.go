package catalog

import (
	"context"
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

func TestOpenV1Unchanged(t *testing.T) {
	srv := catalogServer(t)
	c, err := Open(context.Background(), srv.URL+"/catalog")
	if err != nil {
		t.Fatal(err)
	}
	if c.SchemaVersion != SchemaV1 {
		t.Fatalf("schema = %q", c.SchemaVersion)
	}
	e, err := c.Entry(context.Background(), "m", "")
	if err != nil {
		t.Fatal(err)
	}
	if e.Version != "1.1.0" || len(e.Variants) != 1 {
		t.Fatalf("entry = %+v", e)
	}
}

func v2FixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	root := filepath.Join("..", "..", "testdata", "catalog-v2")
	if _, err := os.Stat(filepath.Join(root, "index-v2.json")); err != nil {
		t.Fatalf("v2 fixtures: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/catalog/index-v2.json", func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile(filepath.Join(root, "index-v2.json"))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_, _ = w.Write(b)
	})
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

func TestOpenV2LoadsVariants(t *testing.T) {
	srv := v2FixtureServer(t)
	c, err := OpenWithSchema(context.Background(), srv.URL+"/catalog/", SchemaV2)
	if err != nil {
		t.Fatal(err)
	}
	if c.SchemaVersion != SchemaV2 {
		t.Fatalf("schema = %q", c.SchemaVersion)
	}
	if _, ok := c.Index.Model("qwen3-8b"); !ok {
		t.Fatal("missing qwen3-8b")
	}
	e, err := c.Entry(context.Background(), "qwen3-8b", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Variants) < 2 {
		t.Fatalf("want multi-variant fixture, got %+v", e.Variants)
	}
	var v Variant
	for _, cand := range e.Variants {
		if cand.ID == "vllm-tp1-910b" {
			v = cand
			break
		}
	}
	if v.ID != "vllm-tp1-910b" {
		t.Fatalf("missing default variant in %+v", e.Variants)
	}
	if v.Digest == "" || v.Version == "" || v.Path == "" {
		t.Fatalf("variant pin missing: %+v", v)
	}
	if len(v.Requires.GPUProduct) == 0 || v.Requires.GPUProduct[0] != "Ascend-910B3" {
		t.Fatalf("gpuProduct = %v", v.Requires.GPUProduct)
	}
	if v.Requires.VendorOrDefault() != "ascend" {
		t.Fatalf("vendor = %q", v.Requires.Vendor)
	}
}

func TestOpenV2LocalDir(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "catalog-v2")
	if _, err := os.Stat(filepath.Join(root, "index-v2.json")); err != nil {
		t.Fatalf("v2 fixtures: %v", err)
	}
	c, err := OpenWithSchema(context.Background(), root, SchemaV2)
	if err != nil {
		t.Fatal(err)
	}
	e, err := c.Entry(context.Background(), "kimi-k2.5", "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := e.DefaultVariant()
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != "sglang-pp2-lws" || v.Requires.TopologyOrDefault() != TopologyLWS {
		t.Fatalf("default variant = %+v", v)
	}
}
