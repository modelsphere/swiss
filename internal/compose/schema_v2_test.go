package compose

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/modelsphere/swiss/internal/site"
)

var testProfile = site.Profile{
	Name: "test", Namespace: "models",
	Model:     site.ModelPaths{PathTemplate: "/models/{{name}}"},
	ChartPath: "/charts",
}

// A v2 plan pins the variant file it was composed from, and says it is v2.
func TestComposeV2PlanUsesVariantPin(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "catalog-v2")
	c, err := catalog.OpenWithSchema(context.Background(), root, catalog.SchemaV2)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Resolve(context.Background(), "qwen3-8b", "vllm-tp2-910b", "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Compose(Input{
		Catalog: c.Source(), Ref: c.Ref(), Schema: c.Schema(), Resolved: r,
		Profile: testProfile, Release: "qwen", ChartVersion: "0.7.6",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Source.Digest != r.Digest || p.Source.Version != r.Version || p.Source.Variant != "vllm-tp2-910b" {
		t.Fatalf("plan pin = %+v, want %s@%s %s", p.Source, r.Variant.ID, r.Version, r.Digest)
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
}

// A v1 plan pins the entry, and records no schema, as before v2.
func TestComposeV1PlanRecordsNoSchema(t *testing.T) {
	r := catalog.Resolved{
		Model: "m", Source: catalog.Source{HF: "org/m"}, ServedName: "m",
		Version: "1.0.0", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Variant: catalog.Variant{
			ID: "v", Engine: "sglang",
			Chart:    catalog.Chart{Name: "sglang", Version: "0.8.0"},
			Requires: catalog.Requires{GPUs: 1},
		},
	}
	p, err := Compose(Input{
		Catalog: "https://example.com/index.json", Ref: "sha256:ref", Schema: catalog.SchemaV1, Resolved: r,
		Profile: testProfile, Release: "m", ChartVersion: "0.8.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Source.Version != r.Version || p.Source.Digest != r.Digest || p.Source.Schema != "" {
		t.Fatalf("v1 plan source = %+v", p.Source)
	}
}
