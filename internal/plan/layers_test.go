package plan

import (
	"encoding/json"
	"testing"

	"github.com/aceforeverd/swiss/internal/values"
)

func layered() *Plan {
	return &Plan{
		APIVersion: APIVersion,
		Release:    Release{Name: "r", Namespace: "ns"},
		Chart:      ChartRef{Name: "sglang", Version: "0.8.0"},
		Layers: map[string]values.Tree{
			"catalog": {"extraArgs": []any{"--tp-size=8"}, "model": values.Tree{"mountPath": "/model"}},
			"site":    {"image": values.Tree{"repository": "harbor/x"}},
			"form":    {"replicaCount": 2},
		},
	}
}

// The layers are disjoint, so their union is what helm receives -- and it does
// not depend on merge order, which is what makes storing the composed document
// alongside them unnecessary rather than merely redundant.
func TestValuesIsTheUnionOfTheLayers(t *testing.T) {
	v := layered().Values()
	if got, _ := values.Get(v, "replicaCount"); got != 2 {
		t.Errorf("form layer missing: %v", v)
	}
	if got, _ := values.Get(v, "model.mountPath"); got != "/model" {
		t.Errorf("catalog layer missing: %v", v)
	}
	if got, _ := values.Get(v, "image.repository"); got != "harbor/x" {
		t.Errorf("site layer missing: %v", v)
	}
}

// Which layer set a path is the document it is in, which is exactly what a
// stored provenance map said.
func TestLayerOfReplacesProvenance(t *testing.T) {
	p := layered()
	for path, want := range map[string]string{
		"replicaCount":     "form",
		"model.mountPath":  "catalog",
		"image.repository": "site",
		"nothing.here":     "",
	} {
		if got := p.LayerOf(path); got != want {
			t.Errorf("LayerOf(%q) = %q, want %q", path, got, want)
		}
	}
}

// The stored document carries the layers and nothing derived from them: not the
// composed values, not a provenance map, and not a second copy of the form's or
// the editor's input -- those are two of the layers.
func TestStoredPlanCarriesNeitherValuesNorProvenance(t *testing.T) {
	doc, err := layered().YAML()
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(mustJSON(t, doc), &out); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"values", "provenance", "overrides", "edits"} {
		if _, present := out[gone]; present {
			t.Errorf("%q is derived and must not be stored", gone)
		}
	}
	if _, present := out["layers"]; !present {
		t.Error("layers must be stored")
	}
}

func mustJSON(t *testing.T, yamlDoc []byte) []byte {
	t.Helper()
	p, err := ParseYAML(yamlDoc)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The point of storing the real documents: a path several layers set keeps
// every value, and the order says which one applies. The old shape stored the
// merge result split by winner, so a form value an edit overrode was simply
// gone.
func TestOverriddenValuesSurviveInTheirOwnLayer(t *testing.T) {
	p := &Plan{
		APIVersion: APIVersion,
		Release:    Release{Name: "r", Namespace: "ns"},
		Chart:      ChartRef{Name: "sglang", Version: "0.8.0"},
		Layers: map[string]values.Tree{
			"catalog": {"replicaCount": 1},
			"form":    {"replicaCount": 2},
			"edit":    {"replicaCount": 5},
		},
	}
	if got, _ := values.Get(p.Values(), "replicaCount"); got != 5 {
		t.Fatalf("last writer must win: %v", got)
	}
	if got := p.LayerOf("replicaCount"); got != "edit" {
		t.Errorf("LayerOf must name the winner, got %q", got)
	}
	// And what the losers asked for is still on record.
	for layer, want := range map[string]any{"catalog": 1, "form": 2} {
		if got, _ := values.Get(p.Layers[layer], "replicaCount"); got != want {
			t.Errorf("%s layer lost its value: %v", layer, got)
		}
	}
}
