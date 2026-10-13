package compose

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/modelsphere/swiss/internal/site"
	"github.com/modelsphere/swiss/internal/values"
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
		Catalog: c.Fetcher.String(), Ref: c.Ref, Schema: c.SchemaVersion, Entry: e, Variant: v,
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
	if p.Source.Schema != catalog.SchemaV2 {
		t.Fatalf("a v2 plan records its schema: %+v", p.Source)
	}
	// Which kind of file Digest is of renders nothing, so it is not hashed:
	// an older swissd dropping the field must not see an edited plan.
	hash := p.Hash
	p.Source.Schema = ""
	if err := p.ComputeHash(); err != nil || p.Hash != hash {
		t.Fatalf("schema must not be hashed: %v", err)
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
	if p.Source.Schema != "" {
		t.Fatalf("a v1 plan records no schema, as before v2: %+v", p.Source)
	}
}

// A v2 variant advertises its own servedName; one without any is the model's.
func TestComposeServesTheVariantsOwnName(t *testing.T) {
	e := catalog.Entry{
		APIVersion: catalog.APIVersion, Name: "m", Version: "1.0.0",
		Source: catalog.Source{HF: "org/m"},
		Variants: []catalog.Variant{
			{ID: "a", Engine: "sglang", Chart: catalog.Chart{Name: "sglang", Version: "0.8.0"}, Requires: catalog.Requires{GPUs: 1}},
			{ID: "b", Engine: "sglang", Chart: catalog.Chart{Name: "sglang", Version: "0.8.0"}, Requires: catalog.Requires{GPUs: 1}, ServedName: "m-b"},
		},
	}
	for _, tc := range []struct{ variant, want string }{{"a", "m"}, {"b", "m-b"}} {
		v, _ := e.Variant(tc.variant)
		p, err := Compose(Input{
			Catalog: "mem", Ref: "sha256:ref", Schema: catalog.SchemaV2, Entry: e, Variant: v,
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
		if got, _ := values.Get(p.Values(), "model.name"); got != tc.want {
			t.Errorf("%s: model.name = %v, want %s", tc.variant, got, tc.want)
		}
	}
}
