package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/modelsphere/swiss/internal/catalog"
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
