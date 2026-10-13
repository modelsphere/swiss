package server

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/plan"
)

// indexOnly serves an index and no documents: drift reads the index alone.
type indexOnly struct{ raw string }

func (f indexOnly) Index(context.Context) ([]byte, error) { return []byte(f.raw), nil }
func (indexOnly) Schema() string                          { return "" }
func (indexOnly) String() string                          { return "mem" }
func (indexOnly) Fetch(_ context.Context, rel string) ([]byte, error) {
	return nil, fmt.Errorf("%s: not served", rel)
}

func openIndex(t *testing.T, raw string) catalog.Catalog {
	t.Helper()
	c, err := catalog.OpenFetcher(context.Background(), indexOnly{raw})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

const (
	defDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	altDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	stale     = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

func changed(version, variant string) string {
	return "version " + version + " variant " + variant + ": upstream digest changed since this was deployed"
}

// v1 pins the version's entry file, which every variant of it shares.
func TestDigestDriftV1UsesEntryDigest(t *testing.T) {
	c := openIndex(t, `{"apiVersion":"catalog.swiss/v1","count":1,"models":[{"name":"m","source":{"hf":"org/m"},"latest":"1.0.0",
		"versions":[{"version":"1.0.0","path":"models/m/1.0.0.yaml","digest":"`+defDigest+`",
		"variants":[{"id":"v","engine":"sglang","chart":{"name":"sglang","version":"0.8.0"},"requires":{"gpus":1}}]}]}]}`)
	if got := digestDrift(c, "m", "1.0.0", "v", defDigest); got != "" {
		t.Fatalf("unchanged v1: got %q, want none", got)
	}
	if got := digestDrift(c, "m", "1.0.0", "v", stale); got != changed("1.0.0", "v") {
		t.Fatalf("rewritten v1: got %q", got)
	}
}

// v2 pins each variant file on its own version and lists only the current
// one. A variant that moved on is a new version, not a rewrite; a rewrite at
// the version a release is on is caught whatever the default variant is at.
func TestDigestDriftV2PerVariant(t *testing.T) {
	c := openIndex(t, `{"apiVersion":"catalog.swiss/v2","count":1,"models":[{"name":"m","source":{"hf":"org/m"},"defaultVariant":"default",
		"variants":[
		 {"id":"default","version":"1.1.0","path":"models/m/default.yaml","digest":"`+defDigest+`","engine":"sglang","chart":{"name":"sglang","version":"0.8.0"},"requires":{"gpus":1}},
		 {"id":"alt","version":"1.0.1","path":"models/m/alt.yaml","digest":"`+altDigest+`","engine":"sglang","chart":{"name":"sglang","version":"0.8.0"},"requires":{"gpus":1}}]}]}`)
	for _, tc := range []struct {
		name, version, variant, pinned, want string
	}{
		{"default unchanged", "1.1.0", "default", defDigest, ""},
		{"alt unchanged", "1.0.1", "alt", altDigest, ""},
		{"alt moved on since", "1.0.0", "alt", stale, ""},
		{"alt rewritten in place", "1.0.1", "alt", stale, changed("1.0.1", "alt")},
		{"default rewritten in place", "1.1.0", "default", stale, changed("1.1.0", "default")},
	} {
		if got := digestDrift(c, "m", tc.version, tc.variant, tc.pinned); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The checked-in v2 fixture, through a real open: a non-default variant's own
// pin is not drift.
func TestDigestDriftV2FixtureNonDefaultPin(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "catalog-v2")
	c, err := catalog.OpenWithSchema(context.Background(), root, catalog.SchemaV2)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Resolve(context.Background(), "qwen3-8b", "vllm-tp2-910b", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := digestDrift(c, "qwen3-8b", r.Version, r.Variant.ID, r.Digest); got != "" {
		t.Fatalf("unchanged non-default pin: got %q", got)
	}
	if got := digestDrift(c, "qwen3-8b", r.Version, r.Variant.ID, stale); got != changed(r.Version, r.Variant.ID) {
		t.Fatalf("rewritten non-default: got %q", got)
	}
}

func TestDriftIgnoresAPlanFromTheOtherSchema(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "testdata", "catalog-v2"))
	if err != nil {
		t.Fatal(err)
	}
	probe := cluster.Fake{Maps: map[string]map[string]string{
		"swiss/site-profile": {"profile.yaml": profileYAML + "catalogs:\n  - name: public\n    url: " + root + "\n    schemaVersion: v2\n"},
	}}
	s := New(testConfig("prod"), probe, discardLogger(), "test")
	pinned := func(schema, digest string) *plan.Plan {
		return &plan.Plan{Source: plan.SourceRef{
			CatalogName: "public", Model: "qwen3-8b", Version: "1.0.0", Variant: "vllm-tp2-910b",
			Digest: digest, Schema: schema,
		}}
	}
	const v1Entry = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	if got := s.releaseDigestDrift(t.Context(), pinned("", v1Entry)); got != "" {
		t.Errorf("a v1 release against the v2 catalog: got %q, want none", got)
	}
	want := "version 1.0.0 variant vllm-tp2-910b: upstream digest changed since this was deployed"
	if got := s.releaseDigestDrift(t.Context(), pinned(catalog.SchemaV2, v1Entry)); got != want {
		t.Errorf("a v2 release whose file was rewritten: got %q, want %q", got, want)
	}
}
