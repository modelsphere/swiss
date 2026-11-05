package site

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"
	"time"
)

func profile() Profile {
	return Profile{
		Name: "prod",
		Model: ModelPaths{
			PathTemplate: "/mnt/disk0/models/{{org}}/{{name}}",
			Overrides:    map[string]string{"kimi-k2.5": "/mnt/disk0/models/Kimi-K2.5"},
		},
	}
}

func TestLocalPathUsesTheTemplate(t *testing.T) {
	got, err := profile().LocalPath("Qwen/Qwen3.6-35B-A3B", "qwen3.6-35b-a3b")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/mnt/disk0/models/Qwen/Qwen3.6-35B-A3B" {
		t.Fatalf("got %q", got)
	}
}

// Real layouts are not uniform: two of these live under an org directory and one
// does not, so a template with no escape hatch cannot express the cluster.
func TestOverrideWinsOverTheTemplate(t *testing.T) {
	got, err := profile().LocalPath("moonshotai/Kimi-K2.5", "kimi-k2.5")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/mnt/disk0/models/Kimi-K2.5" {
		t.Fatalf("got %q", got)
	}
}

func TestUnknownPlaceholderIsAnError(t *testing.T) {
	p := profile()
	p.Model.PathTemplate = "/models/{{nope}}"
	if _, err := p.LocalPath("a/b", "m"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestSecretReferenceQualifiesABareName(t *testing.T) {
	a := RouteAuth{SecretRef: "llm-openresty"}
	if got := a.SecretReference("swiss-system"); got != "swiss-system/llm-openresty" {
		t.Errorf("bare name: %q", got)
	}
	if got := (RouteAuth{SecretRef: "other/key"}).SecretReference("swiss-system"); got != "other/key" {
		t.Errorf("an explicit namespace must win: %q", got)
	}
	if got := (RouteAuth{}).SecretReference("swiss-system"); got != "" {
		t.Errorf("no ref stays no ref: %q", got)
	}
}

func TestModelURLJoinsTheGatewayAndRoute(t *testing.T) {
	r := Route{Gateway: "https://llm.example.com/"}
	if got := r.ModelURL("/kimi"); got != "https://llm.example.com/kimi" {
		t.Errorf("one slash between them: %q", got)
	}
	if got := (Route{}).ModelURL("kimi"); got != "" {
		t.Errorf("no gateway, no URL: %q", got)
	}
	if got := r.ModelURL(""); got != "" {
		t.Errorf("no route, no URL: %q", got)
	}
}

// The catalog set here wins over swissd's config file, so what it may say is
// checked when the profile is parsed rather than when a fetch fails.
func TestCatalogValidation(t *testing.T) {
	const head = "name: prod\nmodel:\n  pathTemplate: /models/{{name}}\nnodes:\n  gpus: []\n"

	for name, want := range map[string]string{
		"https://models.example.com/swiss-catalog/": "https://models.example.com/swiss-catalog/",
		"http://models.internal/catalog/":           "http://models.internal/catalog/",
		"/mnt/disk0/swiss-catalog":                  "/mnt/disk0/swiss-catalog",
		"  https://models.example.com/c/  ":         "https://models.example.com/c/",
		"":                                          "",
	} {
		p, err := Parse([]byte(head+"catalog: \""+name+"\"\n"), "test")
		if err != nil {
			t.Errorf("catalog %q must parse: %v", name, err)
			continue
		}
		if p.Catalog != want {
			t.Errorf("catalog %q parsed to %q, want %q", name, p.Catalog, want)
		}
	}

	// This document is read from a ConfigMap, so a relative path has nothing to
	// be relative to -- refused rather than resolved against whatever directory
	// swissd happens to be started in.
	for _, bad := range []string{"swiss-catalog", "./swiss-catalog", "../catalog", "oci://ghcr.io/modelsphere/catalog"} {
		if _, err := Parse([]byte(head+"catalog: \""+bad+"\"\n"), "test"); err == nil {
			t.Errorf("catalog %q must be refused", bad)
		}
	}
}

// A catalog list is what a page selects from, so each entry needs a usable,
// unique name and a location the single-catalog field would also accept.
func TestCatalogsValidation(t *testing.T) {
	const head = "name: prod\nmodel:\n  pathTemplate: /models/{{name}}\nnodes:\n  gpus: []\n"

	p, err := Parse([]byte(head+`catalogs:
  - name: " public "
    url: " https://models.example.com/swiss-catalog/ "
  - name: internal
    url: /mnt/disk0/internal-catalog
`), "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.CatalogRepos(); len(got) != 2 || got[0] != (CatalogRepo{Name: "public", URL: "https://models.example.com/swiss-catalog/"}) {
		t.Errorf("catalogs parsed to %+v, want both, trimmed", got)
	}

	for name, raw := range map[string]string{
		"both spellings": "catalog: /mnt/a\ncatalogs:\n  - name: b\n    url: /mnt/b\n",
		"no url":         "catalogs:\n  - name: a\n",
		"no name":        "catalogs:\n  - url: /mnt/a\n",
		"bad name":       "catalogs:\n  - name: Public Models\n    url: /mnt/a\n",
		"relative url":   "catalogs:\n  - name: a\n    url: ./catalog\n",
		"name twice":     "catalogs:\n  - name: a\n    url: /mnt/a\n  - name: a\n    url: /mnt/b\n",
		"url twice":      "catalogs:\n  - name: a\n    url: /mnt/a\n  - name: b\n    url: /mnt/a\n",
		"two defaults":   "catalogs:\n  - name: a\n    url: /mnt/a\n    default: true\n  - name: b\n    url: /mnt/b\n    default: true\n",
	} {
		if _, err := Parse([]byte(head+raw), "test"); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// Only a catalog catalog-server can serve may be marked writable, and the
// profile holds no token for it.
func TestWritableCatalog(t *testing.T) {
	const head = "name: prod\nmodel:\n  pathTemplate: /models/{{name}}\nnodes:\n  gpus: []\ncatalogs:\n  - name: internal\n"

	p, err := Parse([]byte(head+"    url: https://catalog.example.com/\n    schemaVersion: v2\n    writable: true\n"), "test")
	if err != nil {
		t.Fatal(err)
	}
	want := CatalogRepo{Name: "internal", URL: "https://catalog.example.com/", SchemaVersion: "v2", Writable: true}
	if got := p.CatalogRepos(); len(got) != 1 || got[0] != want {
		t.Errorf("got %+v, want %+v", got, want)
	}

	for name, raw := range map[string]string{
		"schema v1":      "    url: https://catalog.example.com/\n    schemaVersion: v1\n    writable: true\n",
		"no schema":      "    url: https://catalog.example.com/\n    writable: true\n",
		"a path":         "    url: /mnt/catalog\n    schemaVersion: v2\n    writable: true\n",
		"a token inline": "    url: https://catalog.example.com/\n    schemaVersion: v2\n    writable: true\n    token: hunter2\n",
	} {
		if _, err := Parse([]byte(head+raw), "test"); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// One catalog may be marked default; it is the one a page opens in.
func TestCatalogDefault(t *testing.T) {
	p, err := Parse([]byte("name: prod\nmodel:\n  pathTemplate: /models/{{name}}\nnodes:\n  gpus: []\n"+
		"catalogs:\n  - name: a\n    url: /mnt/a\n  - name: b\n    url: /mnt/b\n    default: true\n"), "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.CatalogRepos(); got[0].Default || !got[1].Default {
		t.Errorf("got %+v, want only b marked default", got)
	}
}

// A profile from before the list reads as one catalog named "default", so an
// existing site keeps working without editing its profile.
func TestLoneCatalogIsTheDefault(t *testing.T) {
	p, err := Parse([]byte("name: prod\nmodel:\n  pathTemplate: /models/{{name}}\nnodes:\n  gpus: []\ncatalog: /mnt/c\n"), "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.CatalogRepos(); len(got) != 1 || got[0] != (CatalogRepo{Name: DefaultCatalogName, URL: "/mnt/c"}) {
		t.Errorf("got %+v, want one catalog named default", got)
	}
	none, _ := Parse([]byte("name: prod\nmodel:\n  pathTemplate: /models/{{name}}\nnodes:\n  gpus: []\n"), "test")
	if got := none.CatalogRepos(); len(got) != 0 {
		t.Errorf("a profile naming no catalog lists none, got %+v", got)
	}
}

func TestSitesValidation(t *testing.T) {
	valid := `
name: prod
model:
  pathTemplate: /models/{{name}}
nodes:
  gpus: []
sites:
  - name: " dev "
    url: " https://swiss.dev.internal "
`
	p, err := Parse([]byte(valid), "test")
	if err != nil {
		t.Fatalf("valid sites must parse: %v", err)
	}
	if len(p.Sites) != 1 || p.Sites[0].Name != "dev" || p.Sites[0].URL != "https://swiss.dev.internal" {
		t.Fatalf("sites not parsed/trimmed: %+v", p.Sites)
	}

	for name, raw := range map[string]string{
		"missing name": `
name: prod
model:
  pathTemplate: /models/{{name}}
nodes:
  gpus: []
sites:
  - url: https://swiss.dev.internal
`,
		"missing url": `
name: prod
model:
  pathTemplate: /models/{{name}}
nodes:
  gpus: []
sites:
  - name: dev
`,
		"bad scheme": `
name: prod
model:
  pathTemplate: /models/{{name}}
nodes:
  gpus: []
sites:
  - name: dev
    url: swiss.dev.internal
`,
	} {
		if _, err := Parse([]byte(raw), name); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestGPUs(t *testing.T) {
	const head = "name: prod\nmodel:\n  pathTemplate: /models/{{name}}\n"
	p, err := Parse([]byte(head+`nodes:
  gpus:
    - vendor: " ascend "
      resource: volcano.sh/Ascend910B3
      labelKey: volcano.sh/npu-name
    - vendor: ascend
      resource: huawei.com/Ascend910
      labelKey: accelerator/huawei-ascend910
      products:
        - product: " Ascend-910B3 "
          values: [" module-910b-8 ", Ascend-910B3]
`), "test")
	if err != nil {
		t.Fatal(err)
	}
	gpus := p.Nodes.GPUTable()
	if len(gpus) != 2 || gpus.Default("ascend") != "volcano.sh/Ascend910B3" {
		t.Errorf("the table is the profile's, in its order: %+v", gpus)
	}
	if hw, _ := gpus.Lookup("huawei.com/Ascend910"); hw.Vendor != "ascend" || hw.Product("module-910b-8") != "Ascend-910B3" {
		t.Errorf("names and values must be trimmed: %+v", hw)
	}

	for name, gpus := range map[string]string{
		"no vendor":         "    - {resource: x.com/npu, labelKey: k}\n",
		"no resource":       "    - {vendor: ascend, labelKey: k}\n",
		"no labelKey":       "    - {vendor: nvidia, resource: nvidia.com/gpu}\n",
		"twice":             "    - {vendor: ascend, resource: x.com/npu, labelKey: k}\n    - {vendor: hygon, resource: x.com/npu, labelKey: j}\n",
		"empty product":     "    - {vendor: ascend, resource: r, labelKey: k, products: [{product: \" \"}]}\n",
		"product twice":     "    - {vendor: ascend, resource: r, labelKey: k, products: [{product: A}, {product: A}]}\n",
		"empty value":       "    - {vendor: ascend, resource: r, labelKey: k, products: [{product: A, values: [\"\"]}]}\n",
		"value of two":      "    - {vendor: ascend, resource: r, labelKey: k, products: [{product: A, values: [v]}, {product: B, values: [v]}]}\n",
		"another's product": "    - {vendor: ascend, resource: r, labelKey: k, products: [{product: A}, {product: B, values: [A]}]}\n",
	} {
		if _, err := Parse([]byte(head+"nodes:\n  gpus:\n"+gpus), "test"); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// The profile is the only GPU table, so it has to say: missing or null is
// refused (swissd migrates those once, at start), [] is a site without GPUs,
// and anything else is the operator's to fix.
func TestGPUsAreRequired(t *testing.T) {
	const head = "name: prod\nmodel:\n  pathTemplate: /models/{{name}}\n"
	for name, tc := range map[string]struct {
		nodes string
		ok    bool
	}{
		"no nodes":     {nodes: ""},
		"missing":      {nodes: "nodes:\n  gpusPerNode: 8\n"},
		"null":         {nodes: "nodes:\n  gpus:\n"},
		"empty map":    {nodes: "nodes:\n  gpus: {}\n"},
		"wrong type":   {nodes: "nodes:\n  gpus: nvidia\n"},
		"empty list":   {nodes: "nodes:\n  gpus: []\n", ok: true},
		"one resource": {nodes: "nodes:\n  gpus: [{vendor: nvidia, resource: nvidia.com/gpu, labelKey: nvidia.com/gpu.product}]\n", ok: true},
	} {
		_, err := Parse([]byte(head+tc.nodes), "test")
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok = %v", name, err, tc.ok)
		}
	}
	_, err := Parse([]byte(head), "test")
	if err == nil || !strings.Contains(err.Error(), "nodes.gpus is required") || !strings.Contains(err.Error(), "nvidia.com/gpu.product") {
		t.Errorf("the refusal must say what to add: %v", err)
	}
}

// nodes.gpuProductAliases is deprecated: ignored for placement, but read and
// served as written, so a site page from before nodes.gpus keeps working.
func TestOldGPUProductAliasesAreIgnored(t *testing.T) {
	p, err := Parse([]byte(`name: prod
model:
  pathTemplate: /models/{{name}}
nodes:
  gpus: []
  gpuProductAliases:
    Ascend-910B3:
      - accelerator=huawei-Ascend910
`), "test")
	if err != nil {
		t.Fatal(err)
	}
	if sels := p.Nodes.GPUProductAliases["Ascend-910B3"]; len(sels) != 1 || sels[0] != "accelerator=huawei-Ascend910" {
		t.Errorf("the old key must be read as written: %v", p.Nodes.GPUProductAliases)
	}
	if len(p.Nodes.GPUTable()) != 0 {
		t.Errorf("aliases must not reach the GPU table: %+v", p.Nodes.GPUTable())
	}
	if b, _ := json.Marshal(p.Nodes); !strings.Contains(string(b), `"gpuProductAliases":{"Ascend-910B3":["accelerator=huawei-Ascend910"]}`) {
		t.Errorf("aliases must still be served: %s", b)
	}
}

// applyWith is the backend a new install uses. Empty means helm. Anything else
// is a typo in the document an operator edits from the web, so it is refused
// when the profile is parsed.
func TestApplyWithValidation(t *testing.T) {
	const head = "name: prod\nmodel:\n  pathTemplate: /models/{{name}}\nnodes:\n  gpus: []\n"

	p, err := Parse([]byte(head), "test")
	if err != nil {
		t.Fatal(err)
	}
	if p.ApplyWith != "" {
		t.Fatalf("omitted applyWith = %q, want empty (helm)", p.ApplyWith)
	}

	for _, raw := range []string{"applyWith: helm\n", "applyWith: \" helm \"\n"} {
		p, err = Parse([]byte(head+raw), "test")
		if err != nil {
			t.Fatal(err)
		}
		if p.ApplyWith != "helm" {
			t.Fatalf("%q parsed to %q", raw, p.ApplyWith)
		}
	}
	p, err = Parse([]byte(head+"applyWith: \" llmsvc \"\n"), "test")
	if err != nil {
		t.Fatal(err)
	}
	if p.ApplyWith != "llmsvc" {
		t.Fatalf("applyWith = %q", p.ApplyWith)
	}

	for _, bad := range []string{"operator", "both", "LLM", "v2"} {
		if _, err := Parse([]byte(head+"applyWith: "+bad+"\n"), "test"); err == nil {
			t.Errorf("applyWith %q must be refused", bad)
		}
	}
}

func TestFeatureDefaultsUnsetIsOn(t *testing.T) {
	p, err := Parse([]byte(DefaultYAML("x")+"\ndefaults:\n  features:\n    scaler: false\n"), "test")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"cart": true, "serviceMonitor": true, "sloRequirement": true, "scaler": false}
	if got := p.Defaults.Features.Resolve(); !maps.Equal(got, want) {
		t.Errorf("Resolve() = %v, want %v", got, want)
	}
	if p.Route.Enabled {
		t.Error("the default profile routes nothing until a deploy asks")
	}
}

func TestPDScale(t *testing.T) {
	const head = "name: prod\nmodel:\n  pathTemplate: /models/{{name}}\nnodes:\n  gpus: []\n"

	p, err := Parse([]byte(head), "test")
	if err != nil {
		t.Fatal(err)
	}
	if p.PDScale.SyncPeriod() != 60*time.Second || p.PDScale.DeferTimeout() != 600*time.Second {
		t.Fatalf("unset: sync %s defer %s, want 60s 600s", p.PDScale.SyncPeriod(), p.PDScale.DeferTimeout())
	}

	p, err = Parse([]byte(head+"pdScale:\n  syncPeriodSeconds: 5\n  deferTimeoutSeconds: 0\n"), "test")
	if err != nil {
		t.Fatal(err)
	}
	if p.PDScale.SyncPeriod() != 5*time.Second || p.PDScale.DeferTimeout() != 0 {
		t.Fatalf("set: sync %s defer %s, want 5s 0s", p.PDScale.SyncPeriod(), p.PDScale.DeferTimeout())
	}

	for raw, want := range map[string]int{
		"scaler:\n  autoscalePeriod: 2s\n":                                   2,
		"scaler:\n  autoscalePeriod: 2s\npdScale:\n  syncPeriodSeconds: 7\n": 7,
		"scaler:\n  autoscalePeriod: soon\n":                                 0,
	} {
		p, err = Parse([]byte(head+raw), "test")
		if err != nil {
			t.Fatal(err)
		}
		if p.PDScale.SyncPeriodSeconds != want || p.Scaler.AutoscalePeriod != "" {
			t.Errorf("%q: syncPeriodSeconds %d autoscalePeriod %q, want %d and moved", raw, p.PDScale.SyncPeriodSeconds, p.Scaler.AutoscalePeriod, want)
		}
	}

	for _, bad := range []string{"pdScale:\n  syncPeriodSeconds: -1\n", "pdScale:\n  deferTimeoutSeconds: -1\n"} {
		if _, err := Parse([]byte(head+bad), "test"); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}
