package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/plan"
)

// Multi-variant v2 projects every latest variant under one IndexVersion whose
// Digest is the default (or first) variant's. Compose pins the chosen
// variant's digest. digestDrift must compare against that variant pin, or a
// non-default deploy looks permanently drifted.
func TestDigestDriftV2MultiVariant(t *testing.T) {
	const (
		defDigest = "sha256:defaultaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		altDigest = "sha256:alternatebbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		stale     = "sha256:stalecccccccccccccccccccccccccccccccccccccccccccccccccccc"
	)
	idx := &catalog.Index{
		APIVersion: catalog.APIVersionV2,
		Models: []catalog.IndexModel{{
			Name:   "m",
			Latest: "1.0.0",
			Versions: []catalog.IndexVersion{{
				Version: "1.0.0",
				Path:    "models/m/default.yaml",
				Digest:  defDigest,
				Variants: []catalog.IndexVariant{
					{ID: "default", Default: true, Path: "models/m/default.yaml", Digest: defDigest, Version: "1.0.0"},
					{ID: "alt", Path: "models/m/alt.yaml", Digest: altDigest, Version: "1.0.0"},
				},
			}},
		}},
	}

	if got := digestDrift(idx, "m", "1.0.0", "alt", altDigest); got != "" {
		t.Fatalf("unchanged non-default variant: got %q, want none", got)
	}
	if got := digestDrift(idx, "m", "1.0.0", "default", defDigest); got != "" {
		t.Fatalf("unchanged default variant: got %q, want none", got)
	}

	want := "version 1.0.0 variant alt: upstream digest changed since this was deployed"
	if got := digestDrift(idx, "m", "1.0.0", "alt", stale); got != want {
		t.Fatalf("rewritten non-default: got %q, want %q", got, want)
	}
	wantDef := "version 1.0.0 variant default: upstream digest changed since this was deployed"
	if got := digestDrift(idx, "m", "1.0.0", "default", stale); got != wantDef {
		t.Fatalf("rewritten default: got %q, want %q", got, wantDef)
	}
}

// v1 variants have no IndexVariant.Digest; drift still uses IndexVersion.Digest.
func TestDigestDriftV1UsesEntryDigest(t *testing.T) {
	const (
		entry = "sha256:entrydddddddddddddddddddddddddddddddddddddddddddddddddddd"
		stale = "sha256:staleeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	)
	idx := &catalog.Index{
		APIVersion: catalog.APIVersion,
		Models: []catalog.IndexModel{{
			Name:   "m",
			Latest: "1.0.0",
			Versions: []catalog.IndexVersion{{
				Version: "1.0.0",
				Path:    "models/m/1.0.0.yaml",
				Digest:  entry,
				Variants: []catalog.IndexVariant{
					{ID: "v"},
				},
			}},
		}},
	}
	if got := digestDrift(idx, "m", "1.0.0", "v", entry); got != "" {
		t.Fatalf("unchanged v1: got %q, want none", got)
	}
	want := "version 1.0.0 variant v: upstream digest changed since this was deployed"
	if got := digestDrift(idx, "m", "1.0.0", "v", stale); got != want {
		t.Fatalf("rewritten v1: got %q, want %q", got, want)
	}
}

// Loads the multi-variant v2 golden and checks a non-default pin against the
// projected index the way status does after OpenWithSchema.
func TestDigestDriftV2FixtureNonDefaultPin(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "catalog-v2")
	c, err := catalog.OpenWithSchema(context.Background(), root, catalog.SchemaV2)
	if err != nil {
		t.Fatal(err)
	}
	e, err := c.Entry(context.Background(), "qwen3-8b", "")
	if err != nil {
		t.Fatal(err)
	}
	var alt catalog.Variant
	for _, v := range e.Variants {
		if v.ID == "vllm-tp2-910b" {
			alt = v
			break
		}
	}
	if alt.ID == "" || alt.Digest == "" {
		t.Fatalf("missing alt variant pin: %+v", e.Variants)
	}
	m, ok := c.Index.Model("qwen3-8b")
	if !ok {
		t.Fatal("missing model")
	}
	ver, err := m.Version(alt.Version)
	if err != nil {
		t.Fatal(err)
	}
	if ver.Digest == alt.Digest {
		t.Fatal("fixture setup: default version digest must differ from non-default variant digest")
	}
	if got := digestDrift(c.Index, "qwen3-8b", alt.Version, alt.ID, alt.Digest); got != "" {
		t.Fatalf("unchanged non-default fixture pin: got %q", got)
	}
	stale := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	want := "version " + alt.Version + " variant vllm-tp2-910b: upstream digest changed since this was deployed"
	if got := digestDrift(c.Index, "qwen3-8b", alt.Version, alt.ID, stale); got != want {
		t.Fatalf("rewritten non-default fixture: got %q, want %q", got, want)
	}
}

// Each v2 variant is versioned on its own and the index lists the current one
// only. A variant that moved on is a new version, not a rewrite; a rewrite at
// the version a release is on is caught whatever the default variant is at.
func TestDigestDriftV2PerVariantVersions(t *testing.T) {
	const (
		defDigest = "sha256:defaultaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		altDigest = "sha256:alternatebbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		stale     = "sha256:stalecccccccccccccccccccccccccccccccccccccccccccccccccccc"
	)
	idx := &catalog.Index{
		APIVersion: catalog.APIVersionV2,
		Models: []catalog.IndexModel{{
			Name:   "m",
			Latest: "1.1.0",
			Versions: []catalog.IndexVersion{{
				Version: "1.1.0", Path: "models/m/default.yaml", Digest: defDigest,
				Variants: []catalog.IndexVariant{
					{ID: "default", Default: true, Path: "models/m/default.yaml", Digest: defDigest, Version: "1.1.0"},
					{ID: "alt", Path: "models/m/alt.yaml", Digest: altDigest, Version: "1.0.1"},
				},
			}},
		}},
	}
	if got := digestDrift(idx, "m", "1.0.0", "alt", stale); got != "" {
		t.Errorf("alt deployed at 1.0.0 and since bumped: got %q, want none", got)
	}
	if got := digestDrift(idx, "m", "1.0.1", "alt", altDigest); got != "" {
		t.Errorf("alt unchanged at 1.0.1: got %q", got)
	}
	want := "version 1.0.1 variant alt: upstream digest changed since this was deployed"
	if got := digestDrift(idx, "m", "1.0.1", "alt", stale); got != want {
		t.Errorf("alt rewritten at 1.0.1: got %q, want %q", got, want)
	}
}

// A site switching a catalog to v2 compares no v1 release's pin against a v2
// variant file, however the versions line up: the fixture's variants are at
// 1.0.0, as a v1 release of the model might well be.
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
