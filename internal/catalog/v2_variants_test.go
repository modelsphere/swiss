package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// memFetcher is a Fetcher none of this package's own: the schema has to come
// from the index it serves, not from knowing the concrete type.
type memFetcher map[string][]byte

func (m memFetcher) Index(context.Context) (*Index, []byte, error) {
	b := m["index-v2.json"]
	idx, err := parseIndexForSchema(b, "")
	return idx, b, err
}

func (m memFetcher) Fetch(_ context.Context, rel string) ([]byte, error) {
	b, ok := m[rel]
	if !ok {
		return nil, fmt.Errorf("%s: not found", rel)
	}
	return b, nil
}

func (memFetcher) String() string { return "mem" }

// v2Variant is one variant file of model "m"; extra is appended to it.
type v2Variant struct {
	id, version, served, extra string
	indexVersion               string // what the index claims, when it disagrees with the file
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

// v2Catalog publishes model "m" with vars, the first one default unless
// defaultVariant names another, and tuning as given.
func v2Catalog(t *testing.T, def string, tuning []map[string]any, vars ...v2Variant) (*Catalog, memFetcher) {
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
		listed = append(listed, map[string]any{
			"id": v.id, "version": ver, "path": path, "digest": contentRef(raw),
			"engine": "sglang", "chart": map[string]any{"name": "sglang", "version": "0.8.0"},
			"requires": map[string]any{"gpus": 1}, "history": "models/m/" + v.id + ".history.json",
		})
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
	if c.SchemaVersion != SchemaV2 {
		t.Fatalf("schema = %q: a fetcher of no known type must still be read as v2", c.SchemaVersion)
	}
	return c, f
}

func variantIDs(e Entry) []string {
	var ids []string
	for _, v := range e.Variants {
		ids = append(ids, v.ID)
	}
	return ids
}

// The schema advises deprecating a variant file before deleting it; doing so
// must not stop the model loading.
func TestV2DeprecatedVariantLoads(t *testing.T) {
	c, _ := v2Catalog(t, "", nil,
		v2Variant{id: "a", version: "1.0.0"},
		v2Variant{id: "b", version: "1.0.0", extra: "deprecated: superseded by a\nreplaces: old-b\n"},
	)
	e, err := c.Entry(context.Background(), "m", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.Variant("b")
	if err != nil {
		t.Fatal(err)
	}
	if b.Deprecated != "superseded by a" {
		t.Errorf("deprecated = %v", b.Deprecated)
	}
}

// Each variant is at its own version. The label the index lists, and no
// version at all, are every variant -- whichever is asked first -- and another
// version keeps the variants at it.
func TestV2VersionsArePerVariant(t *testing.T) {
	c, _ := v2Catalog(t, "a", nil,
		v2Variant{id: "a", version: "1.1.0"},
		v2Variant{id: "b", version: "1.0.0"},
	)
	m, _ := c.Index.Model("m")
	if m.Latest != "1.1.0" {
		t.Fatalf("label = %q, want the default variant's version", m.Latest)
	}
	ctx := context.Background()
	for _, want := range []string{"1.1.0", "", "1.1.0"} {
		e, err := c.Entry(ctx, "m", want)
		if err != nil {
			t.Fatal(err)
		}
		if got := variantIDs(e); len(got) != 2 {
			t.Fatalf("Entry(%q) = %v, want both variants", want, got)
		}
		if b, _ := e.Variant("b"); b.Version != "1.0.0" {
			t.Errorf("b pins %q, want its own 1.0.0", b.Version)
		}
	}
	old, err := c.Entry(ctx, "m", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if got := variantIDs(old); len(got) != 1 || got[0] != "b" {
		t.Errorf("Entry(1.0.0) = %v, want b alone", got)
	}
	if _, err := c.Entry(ctx, "m", "0.9.0"); err == nil || !strings.Contains(err.Error(), "a@1.1.0, b@1.0.0") {
		t.Errorf("a version no variant is at should say what is: %v", err)
	}
}

// servedName is a variant file's own: a default without one is the model's
// name, not a sibling's.
func TestV2ServedNameIsPerVariant(t *testing.T) {
	c, _ := v2Catalog(t, "a", nil,
		v2Variant{id: "a", version: "1.0.0"},
		v2Variant{id: "b", version: "1.0.0", served: "m-b"},
	)
	e, err := c.Entry(context.Background(), "m", "")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := e.Variant("a")
	b, _ := e.Variant("b")
	if got := e.ServedNameOf(a); got != "m" {
		t.Errorf("a served as %q, want m", got)
	}
	if got := e.ServedNameOf(b); got != "m-b" {
		t.Errorf("b served as %q, want m-b", got)
	}
	if e.ServedName != "" {
		t.Errorf("entry servedName = %q; variants disagree, so it names none", e.ServedName)
	}
}

// The file is the version it says it is, as in v1.
func TestV2StaleVariantVersionIsRefused(t *testing.T) {
	c, _ := v2Catalog(t, "a", nil,
		v2Variant{id: "a", version: "1.0.0"},
		v2Variant{id: "b", version: "1.0.0", indexVersion: "1.0.1"},
	)
	e, err := c.Entry(context.Background(), "m", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Variant("b"); err == nil || !strings.Contains(err.Error(), "index is stale") {
		t.Errorf("b must be refused as stale, got %v", err)
	}
}

// One bad variant file blocks deploying only itself, and is retried rather
// than remembered.
func TestV2BadSiblingDoesNotBlockTheRest(t *testing.T) {
	c, f := v2Catalog(t, "a", nil,
		v2Variant{id: "a", version: "1.0.0"},
		v2Variant{id: "b", version: "1.0.0"},
	)
	good := f["models/m/b.yaml"]
	f["models/m/b.yaml"] = append([]byte("# rewritten\n"), good...)

	ctx := context.Background()
	e, err := c.Entry(ctx, "m", "")
	if err != nil {
		t.Fatalf("a bad sibling must not fail the model: %v", err)
	}
	if v, err := e.DefaultVariant(); err != nil || v.ID != "a" {
		t.Errorf("default = %v, %v", v.ID, err)
	}
	if _, err := e.Variant("b"); err == nil || !strings.Contains(err.Error(), "rewritten") {
		t.Errorf("b must be refused with why: %v", err)
	}
	if !strings.Contains(e.Unavailable["b"], "does not match the index") {
		t.Errorf("unavailable = %v", e.Unavailable)
	}

	f["models/m/b.yaml"] = good
	again, err := c.Entry(ctx, "m", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Variants) != 2 || len(again.Unavailable) != 0 {
		t.Errorf("a failure must not be memoised: %v, unavailable %v", variantIDs(again), again.Unavailable)
	}
}

// An unavailable default is an error, not a quiet fall back to a sibling.
func TestV2UnavailableDefaultIsAnError(t *testing.T) {
	c, f := v2Catalog(t, "a", nil,
		v2Variant{id: "a", version: "1.0.0"},
		v2Variant{id: "b", version: "1.0.0"},
	)
	delete(f, "models/m/a.yaml")
	e, err := c.Entry(context.Background(), "m", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.DefaultVariant(); err == nil || !strings.Contains(err.Error(), `default variant "a" is unavailable`) {
		t.Errorf("got %v", err)
	}
}

// Every variant failing is the model failing.
func TestV2NoLoadableVariantIsAnError(t *testing.T) {
	c, f := v2Catalog(t, "", nil, v2Variant{id: "a", version: "1.0.0"})
	delete(f, "models/m/a.yaml")
	if _, err := c.Entry(context.Background(), "m", ""); err == nil {
		t.Fatal("want an error")
	}
}

// With no defaultVariant the first variant is the default, as the schema says,
// and the index marks it so the pages agree with the deploy.
func TestV2FirstVariantIsTheDefault(t *testing.T) {
	c, _ := v2Catalog(t, "", nil,
		v2Variant{id: "a", version: "1.0.0"},
		v2Variant{id: "b", version: "1.2.0"},
	)
	m, _ := c.Index.Model("m")
	if !m.Versions[0].Variants[0].Default || m.Latest != "1.0.0" {
		t.Errorf("first variant not the default: %+v", m.Versions[0])
	}
}

// Tuning names its pair as id@version; the pages read v1's shape.
func TestV2TuningIsProjected(t *testing.T) {
	c, _ := v2Catalog(t, "opt", []map[string]any{{
		"baseline": "base@1.0.2", "optimized": "opt@1.0.1", "uplift": 50.7,
		"workloads": []map[string]any{{"name": "8k + 1k", "uplift": 30.4}}, "report": "m-report.html",
	}, {
		"baseline": "base", "optimized": "opt",
	}},
		v2Variant{id: "base", version: "1.0.2"},
		v2Variant{id: "opt", version: "1.0.2"},
	)
	m, _ := c.Index.Model("m")
	if len(m.Tuning) != 2 {
		t.Fatalf("tuning = %+v", m.Tuning)
	}
	got := m.Tuning[0]
	if got.Baseline != "base" || got.Optimized != "opt" || got.Version != "1.0.1" || got.Uplift == nil || *got.Uplift != 50.7 ||
		len(got.Workloads) != 1 || got.Report != "m-report.html" {
		t.Errorf("tuning = %+v", got)
	}
	if m.Tuning[1].Version != "1.0.2" {
		t.Errorf("a pair without a version is the optimized variant's current one: %+v", m.Tuning[1])
	}
	if len(m.Reports) != 1 || m.Reports[0] != "m-report.html" {
		t.Errorf("reports = %v", m.Reports)
	}
}

// Drift reads each variant's own pin, so a variant that moved on is a new
// version and a rewrite at the version a release is on is still caught.
func TestPublishedDigestIsPerVariantOnV2(t *testing.T) {
	c, f := v2Catalog(t, "a", nil,
		v2Variant{id: "a", version: "1.1.0"},
		v2Variant{id: "b", version: "1.0.1"},
	)
	b := contentRef(f["models/m/b.yaml"])
	if got, ok := c.Index.PublishedDigest("m", "1.0.1", "b"); !ok || got != b {
		t.Errorf("b@1.0.1 = %q, %v; want its own digest", got, ok)
	}
	if _, ok := c.Index.PublishedDigest("m", "1.0.0", "b"); ok {
		t.Error("b is no longer published at 1.0.0")
	}
	if _, ok := c.Index.PublishedDigest("m", "1.1.0", "b"); ok {
		t.Error("the label is not b's version")
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
		if c.SchemaVersion != SchemaV2 {
			t.Fatalf("%s: schema = %q", loc, c.SchemaVersion)
		}
		if _, err := c.Entry(context.Background(), "qwen3-8b", ""); err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
	}
	if _, err := OpenWithSchema(context.Background(), filepath.Join(root, "index-v2.json"), SchemaV1); err == nil ||
		!strings.Contains(err.Error(), "set schemaVersion: v2") {
		t.Errorf("explicit v1 on a v2 index: %v", err)
	}
}

// v1 is unchanged: a base location reads index.json, strictly, with no
// schemaVersion, and the v2-only keys IndexModel and IndexVariant now have are
// still unknown keys to it.
func TestV1IndexStaysStrict(t *testing.T) {
	c, err := Open(context.Background(), catalogServer(t).URL+"/catalog/index.json")
	if err != nil {
		t.Fatal(err)
	}
	if c.SchemaVersion != SchemaV1 {
		t.Fatalf("schema = %q", c.SchemaVersion)
	}
	good := index(map[string]string{"1.0.0": testEntry, "1.1.0": testEntryV2})
	if _, err := parseIndexForSchema([]byte(good), ""); err != nil {
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
		if _, err := parseIndexForSchema([]byte(bad), ""); err == nil || !strings.Contains(err.Error(), "schema v2 only") {
			t.Errorf("%s: v1 must refuse it, got %v", name, err)
		}
	}
}

// An index cached by a swissd from before v2 is found by this one: the v1 key
// is the location alone, as it was.
func TestV1CacheFileNameIsUnchanged(t *testing.T) {
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
}
