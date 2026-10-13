package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// memFetcher is a Fetcher none of this package's own, with schemaVersion
// unset: the schema has to come from the index it serves.
type memFetcher map[string][]byte

func (m memFetcher) Index(context.Context) ([]byte, error) { return m["index-v2.json"], nil }
func (m memFetcher) Schema() string                        { return "" }
func (memFetcher) String() string                          { return "mem" }

func (m memFetcher) Fetch(_ context.Context, rel string) ([]byte, error) {
	b, ok := m[rel]
	if !ok {
		return nil, fmt.Errorf("%s: not found", rel)
	}
	return b, nil
}

// v2Variant is one variant file of model "m".
type v2Variant struct {
	id, version, served string
	extra               string         // appended to the file
	indexVersion        string         // what the index claims, when it disagrees with the file
	index               map[string]any // extra keys on its index entry
}

func variantYAML(v v2Variant) []byte {
	served := ""
	if v.served != "" {
		served = "servedName: " + v.served + "\n"
	}
	return []byte(fmt.Sprintf(`apiVersion: catalog.swiss/v2
kind: Variant
model: m
id: %s
version: %s
%sengine: sglang
chart: {name: sglang, version: "0.8.0"}
requires: {gpus: 1}
%s`, v.id, v.version, served, v.extra))
}

// v2Catalog publishes model "m" with vars; def names the default, "" for none.
func v2Catalog(t *testing.T, def string, tuning []map[string]any, vars ...v2Variant) (Catalog, memFetcher) {
	t.Helper()
	f := memFetcher{}
	var listed []map[string]any
	for _, v := range vars {
		raw := variantYAML(v)
		path := "models/m/" + v.id + ".yaml"
		f[path] = raw
		ver := v.version
		if v.indexVersion != "" {
			ver = v.indexVersion
		}
		pin := map[string]any{
			"id": v.id, "version": ver, "path": path, "digest": contentRef(raw),
			"engine": "sglang", "chart": map[string]any{"name": "sglang", "version": "0.8.0"},
			"requires": map[string]any{"gpus": 1}, "history": "models/m/" + v.id + ".history.json",
		}
		for k, x := range v.index {
			pin[k] = x
		}
		listed = append(listed, pin)
		hist, err := json.Marshal(map[string]any{
			"model": "m", "id": v.id, "path": path,
			"revisions": []map[string]any{{"version": ver, "digest": contentRef(raw), "commit": strings.Repeat("c", 40)}},
		})
		if err != nil {
			t.Fatal(err)
		}
		f["models/m/"+v.id+".history.json"] = hist
	}
	model := map[string]any{
		"name": "m", "source": map[string]any{"hf": "org/m"},
		"reports": []string{"m-report.html"}, "tuning": tuning, "variants": listed,
	}
	if def != "" {
		model["defaultVariant"] = def
	}
	idx, err := json.Marshal(map[string]any{"apiVersion": APIVersionV2, "count": 1, "models": []any{model}})
	if err != nil {
		t.Fatal(err)
	}
	f["index-v2.json"] = idx
	c, err := OpenFetcher(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.(*V2); !ok {
		t.Fatalf("got %T: a fetcher of no known type must still be read as v2", c)
	}
	return c, f
}

// The schema advises deprecating a variant file before deleting it; doing so
// must not stop it resolving, and the listing says so.
func TestV2DeprecatedVariantResolves(t *testing.T) {
	c, _ := v2Catalog(t, "", nil,
		v2Variant{id: "a", version: "1.0.0"},
		v2Variant{id: "b", version: "1.0.0", extra: "deprecated: superseded by a\nreplaces: old-b\n",
			index: map[string]any{"deprecated": "superseded by a", "replaces": "old-b"}},
	)
	if _, err := c.Resolve(context.Background(), "m", "b", ""); err != nil {
		t.Fatal(err)
	}
	m, _ := c.Model("m")
	if b := m.Variants[1]; b.Deprecated != "superseded by a" || b.Replaces != "old-b" {
		t.Errorf("listing = %+v", b)
	}
}

// A version is the variant's own. The default variant's version is not a name
// for every variant: b at 1.1.0 is refused when b's history has no 1.1.0.
func TestV2VersionIsTheVariantsOwn(t *testing.T) {
	c, _ := v2Catalog(t, "a", nil,
		v2Variant{id: "a", version: "1.1.0"},
		v2Variant{id: "b", version: "1.2.0"},
	)
	ctx := context.Background()
	for _, version := range []string{"", "1.2.0"} {
		r, err := c.Resolve(ctx, "m", "b", version)
		if err != nil || r.Version != "1.2.0" {
			t.Errorf("b at %q = %+v, %v", version, r, err)
		}
	}
	if _, err := c.Resolve(ctx, "m", "b", "1.1.0"); err == nil || !strings.Contains(err.Error(), "b has no version 1.1.0 (have: 1.2.0)") {
		t.Errorf("b at 1.1.0 must be refused: %v", err)
	}
	if r, err := c.Resolve(ctx, "m", "", "1.1.0"); err != nil || r.Variant.ID != "a" {
		t.Errorf("the default at its own version = %+v, %v", r, err)
	}
	if got, err := c.History(ctx, "m", "b"); err != nil || len(got) != 1 || got[0].Version != "1.2.0" || !got[0].Current {
		t.Errorf("history = %+v, %v", got, err)
	}
}

// servedName is a variant file's own: one without any is the model's name, not
// a sibling's.
func TestV2ServedNameIsPerVariant(t *testing.T) {
	c, _ := v2Catalog(t, "a", nil,
		v2Variant{id: "a", version: "1.0.0"},
		v2Variant{id: "b", version: "1.0.0", served: "m-b"},
	)
	for id, want := range map[string]string{"a": "m", "b": "m-b"} {
		r, err := c.Resolve(context.Background(), "m", id, "")
		if err != nil || r.ServedName != want {
			t.Errorf("%s served as %q (%v), want %s", id, r.ServedName, err, want)
		}
	}
}

// The file is the version the index says it is.
func TestV2StaleVariantVersionIsRefused(t *testing.T) {
	c, _ := v2Catalog(t, "a", nil,
		v2Variant{id: "a", version: "1.0.0"},
		v2Variant{id: "b", version: "1.0.0", indexVersion: "1.0.1"},
	)
	if _, err := c.Resolve(context.Background(), "m", "b", ""); err == nil || !strings.Contains(err.Error(), "index is stale") {
		t.Errorf("b must be refused as stale, got %v", err)
	}
}

// One bad variant file blocks resolving only itself, and is retried rather
// than remembered.
func TestV2BadFileBlocksOnlyItself(t *testing.T) {
	c, f := v2Catalog(t, "a", nil,
		v2Variant{id: "a", version: "1.0.0"},
		v2Variant{id: "b", version: "1.0.0"},
	)
	ctx := context.Background()
	good := f["models/m/b.yaml"]
	f["models/m/b.yaml"] = append([]byte("# rewritten\n"), good...)
	if _, err := c.Resolve(ctx, "m", "", ""); err != nil {
		t.Fatalf("a bad sibling must not block the default: %v", err)
	}
	if _, err := c.Resolve(ctx, "m", "b", ""); err == nil || !strings.Contains(err.Error(), "rewritten") {
		t.Errorf("b must be refused with why: %v", err)
	}
	f["models/m/b.yaml"] = good
	if _, err := c.Resolve(ctx, "m", "b", ""); err != nil {
		t.Errorf("a failure must not be remembered: %v", err)
	}
}

// A variant file is judged by the same per-variant rules as a v1 entry's.
func TestV2VariantIsValidated(t *testing.T) {
	unknown := memFetcher{}
	raw := []byte("apiVersion: catalog.swiss/v2\nkind: Variant\nmodel: m\nid: a\nversion: 1.0.0\nengine: trtllm\nchart: {name: trtllm, version: \"1.0.0\"}\nrequires: {gpus: 1}\n")
	unknown["models/m/a.yaml"] = raw
	unknown["index-v2.json"] = []byte(fmt.Sprintf(`{"apiVersion":"catalog.swiss/v2","count":1,"models":[{"name":"m","source":{"hf":"org/m"},"variants":[{"id":"a","version":"1.0.0","path":"models/m/a.yaml","digest":%q,"engine":"trtllm","chart":{"name":"trtllm","version":"1.0.0"},"requires":{"gpus":1}}]}]}`, contentRef(raw)))
	uc, err := OpenFetcher(context.Background(), unknown)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uc.Resolve(context.Background(), "m", "", ""); err == nil || !strings.Contains(err.Error(), "is not sglang or vllm") {
		t.Errorf("an unknown engine must be refused by v1's rule: %v", err)
	}
}

// With no defaultVariant the first variant is the default, as the schema says.
func TestV2FirstVariantIsTheDefault(t *testing.T) {
	c, _ := v2Catalog(t, "", nil,
		v2Variant{id: "a", version: "1.0.0"},
		v2Variant{id: "b", version: "1.2.0"},
	)
	m, _ := c.Model("m")
	if m.DefaultVariant != "a" || !m.Variants[0].Default || m.Variants[1].Default {
		t.Errorf("model = %+v", m)
	}
	if r, err := c.Resolve(context.Background(), "m", "", ""); err != nil || r.Variant.ID != "a" || !r.Variant.Default {
		t.Errorf("default = %+v, %v", r.Variant, err)
	}
}

// Tuning names each side as id@version, which is what the shared shape keeps.
func TestV2Tuning(t *testing.T) {
	c, _ := v2Catalog(t, "opt", []map[string]any{{
		"baseline": "base@1.0.2", "optimized": "opt@1.0.1", "uplift": 50.7,
		"workloads": []map[string]any{{"name": "8k + 1k", "uplift": 30.4}}, "report": "m-report.html",
	}, {
		"baseline": "base", "optimized": "opt",
	}},
		v2Variant{id: "base", version: "1.0.2"},
		v2Variant{id: "opt", version: "1.0.2"},
	)
	m, _ := c.Model("m")
	if len(m.Tuning) != 2 {
		t.Fatalf("tuning = %+v", m.Tuning)
	}
	got := m.Tuning[0]
	if got.Baseline != (VariantRef{"base", "1.0.2"}) || got.Optimized != (VariantRef{"opt", "1.0.1"}) ||
		got.Uplift == nil || *got.Uplift != 50.7 || len(got.Workloads) != 1 || got.Report != "m-report.html" {
		t.Errorf("tuning = %+v", got)
	}
	if m.Tuning[1].Optimized != (VariantRef{ID: "opt"}) {
		t.Errorf("a pair without a version keeps none: %+v", m.Tuning[1])
	}
	idx := c.(*V2).Index
	if mm, _ := idx.Model("m"); len(mm.Reports) != 1 || mm.Reports[0] != "m-report.html" {
		t.Errorf("reports = %v", mm.Reports)
	}
}

// Drift reads each variant's own pin: a variant that moved on is a new
// version, and a rewrite at the version a release is on is still caught.
func TestV2PublishedIsPerVariant(t *testing.T) {
	c, f := v2Catalog(t, "a", nil,
		v2Variant{id: "a", version: "1.1.0"},
		v2Variant{id: "b", version: "1.0.1"},
	)
	b := contentRef(f["models/m/b.yaml"])
	if got, ok := c.Published("m", "b", "1.0.1"); !ok || got != b {
		t.Errorf("b@1.0.1 = %q, %v; want its own digest", got, ok)
	}
	if _, ok := c.Published("m", "b", "1.0.0"); ok {
		t.Error("b is no longer published at 1.0.0")
	}
	if _, ok := c.Published("m", "b", "1.1.0"); ok {
		t.Error("the default's version is not b's")
	}
}

// The v1 types carry nothing of v2: its keys are unknown keys to a v1 index.
func TestV1IndexStaysStrict(t *testing.T) {
	good := index(map[string]string{"1.0.0": testEntry, "1.1.0": testEntryV2})
	if _, err := parseIndex([]byte(good)); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]string{
		"variant digest": strings.Replace(good, `"variants":[{"id":"v"`, `"variants":[{"id":"v","digest":"sha256:x"`, 1),
		"variant path":   strings.Replace(good, `"variants":[{"id":"v"`, `"variants":[{"id":"v","path":"models/m/v.yaml"`, 1),
		"reports":        strings.Replace(good, `{"name":"m",`, `{"name":"m","reports":["r.html"],`, 1),
	} {
		if bad == good {
			t.Fatalf("%s: fixture did not change", name)
		}
		if _, err := parseIndex([]byte(bad)); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Errorf("%s: v1 must refuse it, got %v", name, err)
		}
	}
}

// An index cached by a swissd from before v2 is found by this one: the v1 key
// is the location alone, as it was. v2 has a slot of its own.
func TestCacheKeysBySchema(t *testing.T) {
	const loc = "https://models.example.com/catalog/"
	for _, schema := range []string{"", SchemaV1, " v1 "} {
		if got := CacheKey(loc, schema); got != loc {
			t.Errorf("CacheKey(%q) = %q, want the location alone", schema, got)
		}
	}
	if CacheKey(loc, SchemaV2) == loc {
		t.Error("v2 must not share v1's slot")
	}

	dir := writeCatalog(t, indexJSON)
	cache := NewCache(t.TempDir())
	// Where the swissd before v2 wrote it: sha256(loc).
	if err := os.WriteFile(cache.file(dir), []byte(indexJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cache.Open(mustFetcher(t, dir), dir, ""); err != nil {
		t.Fatalf("the pre-v2 cache file must be found: %v", err)
	}

	v2, f := v2Catalog(t, "", nil, v2Variant{id: "a", version: "1.0.0"})
	if err := cache.Save("mem", SchemaV2, v2); err != nil {
		t.Fatal(err)
	}
	got, _, err := cache.Open(f, "mem", SchemaV2)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.(*V2); !ok || got.Ref() != v2.Ref() {
		t.Errorf("cached v2 = %T %s, want %s", got, got.Ref(), v2.Ref())
	}
}
