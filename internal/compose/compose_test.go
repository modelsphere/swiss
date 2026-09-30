package compose

import (
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/modelsphere/swiss/internal/site"
	"github.com/modelsphere/swiss/internal/values"
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
			Namespace: "models",
			Model:     site.ModelPaths{PathTemplate: "/mnt/disk0/models/{{name}}"},
			Registry:  site.Registry{Mirror: "ghcr.io/modelsphere"},
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
		"image.tag":        "v0.5.19",                    // catalog pins the build
		"image.repository": "ghcr.io/modelsphere/sglang", // site picks the mirror
	} {
		got, ok := values.Get(p.Values(), path)
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
		if got := p.LayerOf(path); got != want {
			t.Errorf("provenance[%s] = %q, want %q", path, got, want)
		}
	}
}

// A site that mirrors nothing writes no repository at all: the catalog's is
// already there, and a site layer restating it would make the plan say the site
// chose a registry it never named.
func TestNoMirrorLeavesTheCatalogImageAlone(t *testing.T) {
	in := testInput()
	in.Profile.Registry.Mirror = ""
	p, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := values.Get(p.Values(), "image.repository"); got != "lmsysorg/sglang" {
		t.Errorf("image.repository = %#v, want the catalog's", got)
	}
	if p.LayerOf("image.repository") != values.LayerCatalog {
		t.Errorf("provenance = %q, want the catalog", p.LayerOf("image.repository"))
	}
	if _, ok := values.Get(p.LayerValues()[values.LayerSite], "image.repository"); ok {
		t.Error("the site document must not restate the repository it did not rewrite")
	}
}

// The mirror is an override like any other: visible in the plan as the site
// shadowing the catalog, rather than the catalog's value never being there.
func TestMirrorShadowsTheCatalogRepository(t *testing.T) {
	p, err := Compose(testInput())
	if err != nil {
		t.Fatal(err)
	}
	if p.LayerOf("image.repository") != values.LayerSite {
		t.Errorf("provenance = %q, want the site", p.LayerOf("image.repository"))
	}
	var found bool
	for _, s := range p.Shadowed() {
		if s.Path == "image.repository" {
			found = true
			if s.By != values.LayerSite || len(s.Under) != 1 || s.Under[0] != values.LayerCatalog {
				t.Errorf("unexpected shadow: %+v", s)
			}
		}
	}
	if !found {
		t.Errorf("the rewrite must show as a shadowed value: %+v", p.Shadowed())
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
		got, _ := values.Get(p.Values(), "cache.maxSlotsPerNode")
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
	got, _ := values.Get(p.Values(), "cache.maxSlotsPerNode")
	if got != 3 {
		t.Fatalf("maxSlotsPerNode = %v, want the override 3", got)
	}
	if p.LayerOf("cache.maxSlotsPerNode") != values.LayerSite {
		t.Fatalf("an explicit value must be attributed to the layer that set it, not to derived")
	}
}

// No layer is fenced off from a key any more. A form may repoint the host cache
// path the site set, and the merge order is what decides the outcome.
func TestFormMaySetASiteKey(t *testing.T) {
	in := testInput()
	in.Overrides = values.Tree{"cache": map[string]any{"hostPath": "/tmp/whatever"}}
	p, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := values.Get(p.Values(), "cache.hostPath"); v != "/tmp/whatever" {
		t.Errorf("the later layer wins: cache.hostPath = %v", v)
	}
	if p.LayerOf("cache.hostPath") != values.LayerForm {
		t.Errorf("attributed to %q, want form", p.LayerOf("cache.hostPath"))
	}
}

// Taking over extraArgs is the case worth reporting: helm replaces a list
// wholesale, so this drops every flag the variant was validated with. It is
// allowed, and it turns up in Shadowed rather than in an error.
func TestFormOverrideOfACatalogKeyIsReportedNotRefused(t *testing.T) {
	in := testInput()
	in.Overrides = values.Tree{"extraArgs": []any{"--tp-size=2"}}
	p, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := values.Get(p.Values(), "extraArgs"); len(v.([]any)) != 1 {
		t.Errorf("the later layer wins outright: %v", v)
	}
	var found bool
	for _, s := range p.Shadowed() {
		if s.Path != "extraArgs" {
			continue
		}
		found = true
		if s.By != values.LayerForm || len(s.Under) != 1 || s.Under[0] != values.LayerCatalog {
			t.Errorf("extraArgs: %s over %v, want form over [catalog]", s.By, s.Under)
		}
	}
	if !found {
		t.Errorf("extraArgs must be reported as shadowed, got %v", p.Shadowed())
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
	if got, _ := values.Get(p.Values(), "scaler.maxReplicas"); got != 8 {
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
		"serviceId": "qwen-01-glm",
		"model":     map[string]any{"localPath": "/mnt/disk1/models/moved"},
	}
	p, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := values.Get(p.Values(), "serviceId"); v != "qwen-01-glm" {
		t.Errorf("serviceId = %v", v)
	}
	if v, _ := values.Get(p.Values(), "model.localPath"); v != "/mnt/disk1/models/moved" {
		t.Errorf("localPath = %v, want the override", v)
	}
	if p.LayerOf("model.localPath") != values.LayerForm {
		t.Errorf("an overridden path must be attributed to the form, got %q", p.LayerOf("model.localPath"))
	}
}

func TestLocalPathDefaultsFromTheSiteTemplate(t *testing.T) {
	p, err := Compose(testInput())
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := values.Get(p.Values(), "model.localPath"); v != "/mnt/disk0/models/GLM-5.3" {
		t.Errorf("localPath = %v", v)
	}
	if p.LayerOf("model.localPath") != values.LayerSite {
		t.Errorf("the template default must be attributed to the site, got %q", p.LayerOf("model.localPath"))
	}
}

// The cache section does not exist in every chart version, so a site that turns
// it off must produce no cache key at all -- not even a derived one.
func TestDisabledCacheEmitsNothing(t *testing.T) {
	in := testInput()
	in.Profile.Cache = site.Cache{}
	p, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range values.LeafPaths(p.Values()) {
		if strings.HasPrefix(path, "cache") {
			t.Errorf("a disabled cache leaked %s", path)
		}
	}
	if _, ok := p.Values()["cache"]; ok {
		t.Error("the cache key itself must be absent")
	}
}

func TestEnabledCacheStillDerivesSlots(t *testing.T) {
	p, err := Compose(testInput())
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := values.Get(p.Values(), "cache.maxSlotsPerNode"); v != 1 {
		t.Fatalf("maxSlotsPerNode = %v, want 1", v)
	}
}

// Every feature's flag is written down. cart is the one that is on by default;
// the rest stay off until a deploy asks.
func TestFeatureDefaultsAreExplicit(t *testing.T) {
	p, err := Compose(testInput())
	if err != nil {
		t.Fatal(err)
	}
	for feature, want := range map[string]bool{
		"cart":           true,
		"modelRoute":     false,
		"sloRequirement": false,
		"scaler":         false,
		"serviceMonitor": false,
	} {
		v, ok := values.Get(p.Values(), feature+".enabled")
		if !ok {
			t.Errorf("%s.enabled is not written down", feature)
			continue
		}
		if v != want {
			t.Errorf("%s.enabled = %v, want %v", feature, v, want)
		}
	}
}

// A flag swiss cannot set is not swiss's to write down. metricsMock was pinned
// off in every plan, which put a key nothing here can change into every deploy
// and every diff.
func TestPlanWritesNoMetricsMock(t *testing.T) {
	p, err := Compose(testInput())
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := values.Get(p.Values(), "metricsMock.enabled"); ok {
		t.Errorf("metricsMock.enabled = %v; a plan must not mention it at all", v)
	}
}

// Filling in scaling numbers turns the scaler on; naming a route turns routing
// on. Neither leans on a chart default.
func TestTouchingASectionEnablesIt(t *testing.T) {
	in := testInput()
	in.Overrides = values.Tree{
		"scaler":     map[string]any{"maxReplicas": 8},
		"modelRoute": map[string]any{"nginx": map[string]any{"route": "glm-5"}},
	}
	p, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, feature := range []string{"scaler", "modelRoute"} {
		if v, _ := values.Get(p.Values(), feature+".enabled"); v != true {
			t.Errorf("%s.enabled = %v, want true", feature, v)
		}
	}
	if v, _ := values.Get(p.Values(), "sloRequirement.enabled"); v != false {
		t.Errorf("an untouched feature stays off: %v", v)
	}
}

// An explicit off wins over both the default and the touched rule.
func TestFormCanTurnAFeatureOff(t *testing.T) {
	in := testInput()
	in.Overrides = values.Tree{"cart": map[string]any{"enabled": false}}
	p, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := values.Get(p.Values(), "cart.enabled"); v != false {
		t.Fatalf("cart.enabled = %v, want false", v)
	}
	if p.LayerOf("cart.enabled") != values.LayerForm {
		t.Errorf("an explicit choice belongs to the form, got %q", p.LayerOf("cart.enabled"))
	}
}

func TestLayerValuesSplitByProvenance(t *testing.T) {
	p, err := Compose(testInput())
	if err != nil {
		t.Fatal(err)
	}
	layers := p.LayerValues()
	if _, ok := values.Get(layers[values.LayerCatalog], "extraArgs"); !ok {
		t.Error("extraArgs belongs to the catalog document")
	}
	// The catalog's repository is carried as the catalog wrote it, and the
	// site's mirror overrides it in merge order rather than replacing it out of
	// sight -- which is what makes the rewrite readable as a shadowed value.
	if got, _ := values.Get(layers[values.LayerCatalog], "image.repository"); got != "lmsysorg/sglang" {
		t.Errorf("the catalog document must carry its own repository, got %#v", got)
	}
	if got, _ := values.Get(layers[values.LayerSite], "image.repository"); got != "ghcr.io/modelsphere/sglang" {
		t.Errorf("the site document must carry the mirror, got %#v", got)
	}
	files := p.ValuesFiles()
	if len(files) < 2 || files[0] != "catalog.yaml" {
		t.Fatalf("values files must be in merge order: %v", files)
	}
}

// The edit layer is applied last and labelled, so it is visible wherever the
// plan is.
func TestEditLayerWinsLast(t *testing.T) {
	in := testInput()
	in.Overrides = values.Tree{"replicaCount": 2}
	in.Edits = values.Tree{
		"replicaCount": 9,
		"extraArgs":    []any{"--tp-size=4"}, // takes over the whole list from the catalog
		"somethingNew": "value",
	}
	p, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := values.Get(p.Values(), "replicaCount"); v != 9 {
		t.Errorf("edits apply last: replicaCount = %v", v)
	}
	if v, _ := values.Get(p.Values(), "extraArgs"); len(v.([]any)) != 1 {
		t.Errorf("edits may set a catalog key: %v", v)
	}
	for _, path := range []string{"replicaCount", "extraArgs", "somethingNew"} {
		if p.LayerOf(path) != values.LayerEdit {
			t.Errorf("%s should be attributed to edit, got %q", path, p.LayerOf(path))
		}
	}
}
