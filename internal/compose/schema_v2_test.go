package compose

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/modelsphere/swiss/internal/site"
)

func TestComposeV2PlanUsesVariantPin(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "catalog-v2")
	c, err := catalog.OpenWithSchema(context.Background(), root, catalog.SchemaV2)
	if err != nil {
		t.Fatal(err)
	}
	e, err := c.Entry(context.Background(), "qwen3-8b", "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := e.DefaultVariant()
	if err != nil {
		t.Fatal(err)
	}
	p, err := Compose(Input{
		Catalog: c.Fetcher.String(), Ref: c.Ref, Entry: e, Variant: v,
		Profile: site.Profile{
			Name: "test", Namespace: "models",
			Model:     site.ModelPaths{PathTemplate: "/models/{{name}}"},
			ChartPath: "/charts",
		},
		Release: "qwen", ChartVersion: "0.7.6",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Source.Digest != v.Digest || p.Source.Version != v.Version {
		t.Fatalf("plan pin = %+v, want variant %+v", p.Source, v)
	}
	if len(v.Requires.GPUProduct) == 0 {
		t.Fatal("gpuProduct empty")
	}
	if p.Hash == "" {
		t.Fatal("empty hash")
	}
}

func TestComposeV1PlanHashUsesEntryPin(t *testing.T) {
	// Minimal in-memory entry/variant (no network): v1 pins live on the entry.
	e := catalog.Entry{
		APIVersion: catalog.APIVersion,
		Name:       "m",
		Version:    "1.0.0",
		Digest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Source:     catalog.Source{HF: "org/m"},
		Variants: []catalog.Variant{{
			ID: "v", Engine: "sglang",
			Chart:    catalog.Chart{Name: "sglang", Version: "0.8.0"},
			Requires: catalog.Requires{GPUs: 1},
		}},
	}
	v := e.Variants[0]
	p, err := Compose(Input{
		Catalog: "https://example.com/index.json", Ref: "sha256:ref",
		Entry: e, Variant: v,
		Profile: site.Profile{
			Name: "test", Namespace: "models",
			Model:     site.ModelPaths{PathTemplate: "/models/{{name}}"},
			ChartPath: "/charts",
		},
		Release: "m", ChartVersion: "0.8.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Source.Version != e.Version || p.Source.Digest != e.Digest {
		t.Fatalf("v1 plan must use entry pin: %+v", p.Source)
	}
}
