package compose

import (
	"testing"

	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/modelsphere/swiss/internal/values"
)

const masterKey = "storageKVOffload.mooncake.master"

func inputsInput() Input {
	in := testInput()
	in.Resolved.Variant.Inputs = []catalog.Input{{
		Key:     masterKey,
		Title:   "Mooncake master",
		Pattern: `[^\s:/]+:[0-9]+`,
		Example: "mooncake-master.example:50051",
	}}
	in.Resolved.Variant.Values["storageKVOffload"] = map[string]any{"enabled": true, "mooncake": map[string]any{"protocol": "rdma"}}
	return in
}

// Compose does not answer inputs and does not refuse one nobody answered.
func TestComposeSucceedsWhenAnInputIsUnanswered(t *testing.T) {
	p, err := Compose(inputsInput())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := values.Get(p.Values(), masterKey); ok {
		t.Error("an unanswered input was written into the composed values")
	}
}

// Site profile inputs and an input default are the web's to apply.
func TestComposeDoesNotApplySiteInputsOrTheDefault(t *testing.T) {
	in := inputsInput()
	in.Resolved.Variant.Inputs[0].Default = "tuned.svc:50051"
	in.Profile.Inputs = map[string]any{masterKey: "mc-{{model}}.svc:50051"}
	p, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := values.Get(p.Values(), masterKey); ok {
		t.Errorf("%s = %v, written from the site profile or the default", masterKey, got)
	}
	if got, _ := values.Get(p.Values(), "storageKVOffload.mooncake.protocol"); got != "rdma" {
		t.Errorf("the variant's own mooncake values were lost: protocol = %v", got)
	}
}

// A form override of a dotted input key still lands in the form layer.
func TestAFormOverrideOfAnInputKeyLandsInTheFormLayer(t *testing.T) {
	in := inputsInput()
	in.Overrides = values.Tree{"storageKVOffload": map[string]any{"mooncake": map[string]any{"master": "mine.svc:50051"}}}
	p, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := values.Get(p.Values(), masterKey); got != "mine.svc:50051" || p.LayerOf(masterKey) != values.LayerForm {
		t.Errorf("%s = %v from %s, want the form's", masterKey, got, p.LayerOf(masterKey))
	}
}

// A value the variant already gives the key stands in the catalog layer.
func TestAVariantValueForAnInputKeyStands(t *testing.T) {
	in := inputsInput()
	in.Resolved.Variant.Values["storageKVOffload"].(map[string]any)["mooncake"].(map[string]any)["master"] = "mooncake-master.example:50051"
	in.Resolved.Variant.Inputs[0].Default = "tuned.svc:50051"
	in.Profile.Inputs = map[string]any{masterKey: "site.svc:50051"}
	p, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := values.Get(p.Values(), masterKey); got != "mooncake-master.example:50051" || p.LayerOf(masterKey) != values.LayerCatalog {
		t.Errorf("%s = %v from %s, want the catalog's", masterKey, got, p.LayerOf(masterKey))
	}
}

// Compose does not record annotations. The client supplies those, including
// when the variant declares inputs.
func TestComposeLeavesAdditionalAnnotationsEmpty(t *testing.T) {
	plain, err := Compose(testInput())
	if err != nil {
		t.Fatal(err)
	}
	if plain.AdditionalAnnotations != nil {
		t.Fatalf("compose recorded annotations: %v", plain.AdditionalAnnotations)
	}

	p, err := Compose(inputsInput())
	if err != nil {
		t.Fatal(err)
	}
	if p.AdditionalAnnotations != nil {
		t.Fatalf("compose recorded annotations for a variant with inputs: %v", p.AdditionalAnnotations)
	}
}

// Profile inputs never land in composed values, including when the variant
// does not declare the key.
func TestSiteInputsDoNothingWhenTheVariantDoesNotDeclareTheKey(t *testing.T) {
	in := testInput()
	in.Profile.Inputs = map[string]any{masterKey: "site.svc:50051"}
	p, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := values.Get(p.Values(), masterKey); ok {
		t.Error("a site input landed in a variant that does not declare it")
	}
}
