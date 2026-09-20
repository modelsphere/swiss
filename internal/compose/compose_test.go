package compose

import (
	"strings"
	"testing"

	"github.com/aceforeverd/swiss/internal/catalog"
	"github.com/aceforeverd/swiss/internal/site"
	"github.com/aceforeverd/swiss/internal/values"
)

func testInput() Input {
	return Input{
		Catalog: "https://example.invalid/swiss-catalog",
		Ref:     "0000000000000000000000000000000000000000",
		Entry: catalog.Entry{
			APIVersion: catalog.APIVersion,
			Name:       "glm-5.3",
			Source:     catalog.Source{HF: "zai-org/GLM-5.3"},
		},
		Variant: catalog.Variant{
			ID:       "sglang-tp8-b300",
			Engine:   "sglang",
			Chart:    catalog.Chart{Name: "sglang", Version: "0.8.0"},
			Image:    &catalog.Image{Repository: "lmsysorg/sglang", Tag: "v0.5.19"},
			Requires: catalog.Requires{GPUs: 8},
			Values: values.Tree{
				"extraArgs": []any{"--tp-size=8"},
				"model":     map[string]any{"mountPath": "/model"},
			},
		},
		Profile: site.Profile{
			Name:      "prod",
			Namespace: "modelforge",
			Model:     site.ModelPaths{PathTemplate: "/mnt/disk0/models/{{name}}"},
			Registry:  site.Registry{Mirror: "harbor.4pd.io/hardcore-tech"},
			Cache:     site.Cache{Enabled: true, HostPath: "/mnt/disk0/sglang-cache"},
			Nodes:     site.Nodes{GPUsPerNode: 8},
		},
		Release: "glm-53",
	}
}

func TestComposeProjectsIdentityFieldsFromTheirSingleSpelling(t *testing.T) {
	p, err := Compose(testInput())
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]any{
		"model.name":       "glm-5.3", // from servedName/name
		"model.gpus":       "8",       // from requires.gpus, as a string
		"model.localPath":  "/mnt/disk0/models/GLM-5.3",
		"image.tag":        "v0.5.19",                            // catalog pins the build
		"image.repository": "harbor.4pd.io/hardcore-tech/sglang", // site picks the mirror
	} {
		got, ok := values.Get(p.Values, path)
		if !ok || got != want {
			t.Errorf("%s = %#v (present=%v), want %#v", path, got, ok, want)
		}
	}
}

func TestComposeRecordsProvenancePerLayer(t *testing.T) {
	p, err := Compose(testInput())
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"extraArgs":             values.LayerCatalog,
		"model.localPath":       values.LayerSite,
		"cache.maxSlotsPerNode": values.LayerDerived,
	} {
		if got := p.Provenance[path]; got != want {
			t.Errorf("provenance[%s] = %q, want %q", path, got, want)
		}
	}
}

// The chart's own comment: 1 slot for an 8-GPU model, 4 for a 2-GPU one.
func TestDerivedMaxSlotsFollowsTheChartsRule(t *testing.T) {
	for gpus, want := range map[int]int{8: 1, 2: 4, 1: 8} {
		in := testInput()
		in.Variant.Requires.GPUs = gpus
		p, err := Compose(in)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := values.Get(p.Values, "cache.maxSlotsPerNode")
		if got != want {
			t.Errorf("%d GPUs: maxSlotsPerNode = %v, want %d", gpus, got, want)
		}
	}
}

// cache.* is site-owned, so the site is the layer that can override the derived
// value -- it is the one that knows the node shapes the rule is guessing from.
func TestDerivedYieldsToAnExplicitOverride(t *testing.T) {
	in := testInput()
	in.Profile.Extra = values.Tree{"cache": map[string]any{"maxSlotsPerNode": 3}}
	p, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := values.Get(p.Values, "cache.maxSlotsPerNode")
	if got != 3 {
		t.Fatalf("maxSlotsPerNode = %v, want the override 3", got)
	}
	if p.Provenance["cache.maxSlotsPerNode"] != values.LayerSite {
		t.Fatalf("an explicit value must be attributed to the layer that set it, not to derived")
	}
}

func TestFormStillCannotSetASiteKey(t *testing.T) {
	in := testInput()
	in.Overrides = values.Tree{"cache": map[string]any{"hostPath": "/tmp/whatever"}}
	if _, err := Compose(in); err == nil {
		t.Fatal("a deploy form must not repoint a host cache path")
	}
}

func TestComposeRejectsAFormOverrideOfACatalogKey(t *testing.T) {
	in := testInput()
	in.Overrides = values.Tree{"extraArgs": []any{"--tp-size=2"}}
	_, err := Compose(in)
	if err == nil {
		t.Fatal("a deploy form must not be able to change the parallelism the variant was validated for")
	}
	if !strings.Contains(err.Error(), "extraArgs") {
		t.Errorf("error should name the offending key: %v", err)
	}
}

func TestComposeAllowsFormToSetScalingAndScheduling(t *testing.T) {
	in := testInput()
	in.Overrides = values.Tree{
		"replicaCount": 2,
		"scaler":       map[string]any{"maxReplicas": 8},
		"nodeSelector": map[string]any{"pool": "gpu"},
	}
	p, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := values.Get(p.Values, "scaler.maxReplicas"); got != 8 {
		t.Fatalf("scaler.maxReplicas = %v, want 8", got)
	}
}

func TestComposeNeedsANamespaceFromSomewhere(t *testing.T) {
	in := testInput()
	in.Profile.Namespace = ""
	if _, err := Compose(in); err == nil {
		t.Fatal("expected a refusal rather than a release in whatever namespace is current")
	}
}

func TestHashCoversValuesButNotProvenance(t *testing.T) {
	a, err := Compose(testInput())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Compose(testInput())
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash != b.Hash {
		t.Fatal("identical inputs must hash identically")
	}
	in := testInput()
	in.Overrides = values.Tree{"replicaCount": 4}
	c, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	if c.Hash == a.Hash {
		t.Fatal("a changed value must change the hash")
	}
}

func TestFormCanSetServiceIDAndOverrideLocalPath(t *testing.T) {
	in := testInput()
	in.Overrides = values.Tree{
		"serviceId": "modelforge-01-glm",
		"model":     map[string]any{"localPath": "/mnt/disk1/models/moved"},
	}
	p, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := values.Get(p.Values, "serviceId"); v != "modelforge-01-glm" {
		t.Errorf("serviceId = %v", v)
	}
	if v, _ := values.Get(p.Values, "model.localPath"); v != "/mnt/disk1/models/moved" {
		t.Errorf("localPath = %v, want the override", v)
	}
	if p.Provenance["model.localPath"] != values.LayerForm {
		t.Errorf("an overridden path must be attributed to the form, got %q", p.Provenance["model.localPath"])
	}
}

func TestLocalPathDefaultsFromTheSiteTemplate(t *testing.T) {
	p, err := Compose(testInput())
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := values.Get(p.Values, "model.localPath"); v != "/mnt/disk0/models/GLM-5.3" {
		t.Errorf("localPath = %v", v)
	}
	if p.Provenance["model.localPath"] != values.LayerSite {
		t.Errorf("the template default must be attributed to the site, got %q", p.Provenance["model.localPath"])
	}
}
