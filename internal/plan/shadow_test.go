package plan

import (
	"testing"

	"github.com/aceforeverd/swiss/internal/values"
)

func TestShadowedNamesTheLayerThatWonAndTheOnesItOvercame(t *testing.T) {
	p := &Plan{Layers: map[string]values.Tree{
		values.LayerCatalog: {"extraArgs": []any{"--tp-size=4"}, "env": []any{"A"}},
		values.LayerForm:    {"extraArgs": []any{"--tp-size=2"}, "replicaCount": 2},
		values.LayerEdit:    {"extraArgs": []any{"--tp-size=8"}},
	}}
	sh := p.Shadowed()
	if len(sh) != 1 {
		t.Fatalf("only extraArgs is set twice, got %v", sh)
	}
	if sh[0].Path != "extraArgs" || sh[0].By != values.LayerEdit {
		t.Errorf("got %+v, want extraArgs won by edit", sh[0])
	}
	// In merge order, so a reader can see the chain rather than just the loser.
	if len(sh[0].Under) != 2 || sh[0].Under[0] != values.LayerCatalog || sh[0].Under[1] != values.LayerForm {
		t.Errorf("under = %v, want [catalog form]", sh[0].Under)
	}
	// The winner is the one LayerOf reports, or the report contradicts the plan.
	if p.LayerOf("extraArgs") != sh[0].By {
		t.Errorf("Shadowed says %q, LayerOf says %q", sh[0].By, p.LayerOf("extraArgs"))
	}
}

// Sibling keys under one parent are not an override: the merge recurses into
// maps, so both survive. Reporting them would bury the real cases in noise.
func TestShadowedIgnoresSiblingKeysInTheSameMap(t *testing.T) {
	p := &Plan{Layers: map[string]values.Tree{
		values.LayerCatalog: {"model": map[string]any{"mountPath": "/model"}},
		values.LayerSite:    {"model": map[string]any{"localPath": "/mnt/disk0/x"}},
	}}
	if sh := p.Shadowed(); len(sh) != 0 {
		t.Errorf("sibling keys are not an override, got %v", sh)
	}
}

func TestShadowedIsEmptyWhenNothingOverlaps(t *testing.T) {
	p := &Plan{Layers: map[string]values.Tree{
		values.LayerCatalog: {"extraArgs": []any{"--tp-size=4"}},
		values.LayerForm:    {"replicaCount": 2},
	}}
	if sh := p.Shadowed(); len(sh) != 0 {
		t.Errorf("nothing overlaps, got %v", sh)
	}
}

// Derived fills a path nobody set; a form value on top of it is worth showing,
// because the number the reader replaced was computed rather than typed.
func TestShadowedReportsAFormOverrideOfADerivedDefault(t *testing.T) {
	p := &Plan{Layers: map[string]values.Tree{
		values.LayerDerived: {"model": map[string]any{"localPath": "/mnt/disk0/derived"}},
		values.LayerForm:    {"model": map[string]any{"localPath": "/mnt/disk9/weights"}},
	}}
	sh := p.Shadowed()
	if len(sh) != 1 || sh[0].Path != "model.localPath" || sh[0].By != values.LayerForm {
		t.Fatalf("got %v, want model.localPath won by form", sh)
	}
}
