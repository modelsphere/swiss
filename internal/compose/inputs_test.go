package compose

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/modelsphere/swiss/internal/plan"
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

// Level one: the site answers, in the derived layer, beside the variant's own
// values under the same section.
func TestTheSiteAnswersAnInputInTheDerivedLayer(t *testing.T) {
	in := inputsInput()
	in.Profile.Inputs = map[string]any{masterKey: "mc-{{model}}.svc:50051"}
	p, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := values.Get(p.Values(), masterKey); got != "mc-glm-5.3.svc:50051" {
		t.Errorf("%s = %v", masterKey, got)
	}
	if got := p.LayerOf(masterKey); got != values.LayerDerived {
		t.Errorf("answered by %s, want derived", got)
	}
	if got, _ := values.Get(p.Values(), "storageKVOffload.mooncake.protocol"); got != "rdma" {
		t.Errorf("the variant's own mooncake values were lost: protocol = %v", got)
	}
}

// Level two: the deployer overrides any answer, and needs no site answer to.
func TestTheDeployOverridesTheSitesAnswer(t *testing.T) {
	for name, site := range map[string]map[string]any{
		"over the site's answer": {masterKey: "site.svc:50051"},
		"with no site answer":    nil,
	} {
		in := inputsInput()
		in.Profile.Inputs = site
		in.Overrides = values.Tree{"storageKVOffload": map[string]any{"mooncake": map[string]any{"master": "mine.svc:50051"}}}
		p, err := Compose(in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got, _ := values.Get(p.Values(), masterKey); got != "mine.svc:50051" || p.LayerOf(masterKey) != values.LayerForm {
			t.Errorf("%s: %s = %v from %s, want the deploy's", name, masterKey, got, p.LayerOf(masterKey))
		}
	}
}

func TestAnUnansweredInputRefusesThePlan(t *testing.T) {
	in := inputsInput()
	_, err := Compose(in)
	if err == nil || !strings.Contains(err.Error(), "input storageKVOffload.mooncake.master (Mooncake master) is not set") {
		t.Fatalf("want a refusal naming the input, got %v", err)
	}
	// "" is checked like any answer, and fails the pattern.
	in.Overrides = values.Tree{"storageKVOffload": map[string]any{"mooncake": map[string]any{"master": ""}}}
	if _, err := Compose(in); err == nil || !strings.Contains(err.Error(), `"" does not match`) {
		t.Fatalf("an empty answer was taken: %v", err)
	}
}

func withDefault(in Input, def any) Input {
	in.Resolved.Variant.Inputs[0].Default = def
	return in
}

// The input's default answers it in the derived layer when the site does not;
// the deploy's answer beats both.
func TestTheVariantsDefaultIsTheLowestAnswer(t *testing.T) {
	for name, c := range map[string]struct {
		site, form map[string]any
		want       string
		layer      string
	}{
		"the default alone":       {want: "tuned.svc:50051", layer: values.LayerDerived},
		"the site over it":        {site: map[string]any{masterKey: "site.svc:50051"}, want: "site.svc:50051", layer: values.LayerDerived},
		"the deploy over the two": {site: map[string]any{masterKey: "site.svc:50051"}, form: map[string]any{"master": "mine.svc:50051"}, want: "mine.svc:50051", layer: values.LayerForm},
	} {
		in := withDefault(inputsInput(), "tuned.svc:50051")
		in.Profile.Inputs = c.site
		if c.form != nil {
			in.Overrides = values.Tree{"storageKVOffload": map[string]any{"mooncake": c.form}}
		}
		p, err := Compose(in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got, _ := values.Get(p.Values(), masterKey); got != c.want || p.LayerOf(masterKey) != c.layer {
			t.Errorf("%s: %s = %v from %s, want %s from %s", name, masterKey, got, p.LayerOf(masterKey), c.want, c.layer)
		}
	}
}

// A value the variant's values give the key stands when nobody answers, and
// any answer overrides it.
func TestTheVariantsValueIsTheBaseAnAnswerOverrides(t *testing.T) {
	for name, c := range map[string]struct {
		site  map[string]any
		def   any
		want  string
		layer string
	}{
		"nobody answers":      {want: "mooncake-master.example:50051", layer: values.LayerCatalog},
		"the site answers":    {site: map[string]any{masterKey: "site.svc:50051"}, want: "site.svc:50051", layer: values.LayerDerived},
		"the default answers": {def: "tuned.svc:50051", want: "tuned.svc:50051", layer: values.LayerDerived},
	} {
		in := inputsInput()
		in.Resolved.Variant.Values["storageKVOffload"].(map[string]any)["mooncake"].(map[string]any)["master"] = "mooncake-master.example:50051"
		in.Resolved.Variant.Inputs[0].Default = c.def
		in.Profile.Inputs = c.site
		p, err := Compose(in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got, _ := values.Get(p.Values(), masterKey); got != c.want || p.LayerOf(masterKey) != c.layer {
			t.Errorf("%s: %s = %v from %s, want %s from %s", name, masterKey, got, p.LayerOf(masterKey), c.want, c.layer)
		}
	}
}

// extra sets the key too: extra wins, and the input answer does nothing.
func TestExtraOverridesTheSitesInputAnswer(t *testing.T) {
	in := inputsInput()
	in.Profile.Inputs = map[string]any{masterKey: "site.svc:50051"}
	in.Profile.Extra = values.Tree{"storageKVOffload": map[string]any{"mooncake": map[string]any{"master": "extra.svc:50051"}}}
	p, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := values.Get(p.Values(), masterKey); got != "extra.svc:50051" || p.LayerOf(masterKey) != values.LayerSite {
		t.Errorf("%s = %v from %s, want extra's", masterKey, got, p.LayerOf(masterKey))
	}
}

// The profile takes inputs as written; compose refuses one it cannot fill.
func TestASiteAnswerWithAnUnknownPlaceholderRefusesThePlan(t *testing.T) {
	in := inputsInput()
	in.Profile.Inputs = map[string]any{masterKey: "{{release}}.svc:50051"}
	if _, err := Compose(in); err == nil || !strings.Contains(err.Error(), "unknown placeholder") {
		t.Fatalf("want a refusal naming the placeholder, got %v", err)
	}
}

func TestAnAnswerTheInputDoesNotTakeRefusesThePlan(t *testing.T) {
	in := inputsInput()
	in.Profile.Inputs = map[string]any{masterKey: "no-port"}
	if _, err := Compose(in); err == nil || !strings.Contains(err.Error(), `"no-port" does not match`) {
		t.Fatalf("want a refusal quoting the answer, got %v", err)
	}
}

// The recorded answer is the composed value at the input key, so a form
// override wins over the site, and a chart value that is not an input is not
// copied in. A variant with no inputs records nothing and hashes as before.
func TestComposeRecordsTheEffectiveInputAnswer(t *testing.T) {
	plain, err := Compose(testInput())
	if err != nil {
		t.Fatal(err)
	}
	if plain.Inputs != nil {
		t.Fatalf("no inputs declared, recorded %v", plain.Inputs)
	}
	bare, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bare), `"inputs"`) {
		t.Fatalf("a plan with no inputs must omit the field: %s", bare)
	}

	in := inputsInput()
	in.Resolved.Variant.Values["storageKVOffload"].(map[string]any)["mooncake"].(map[string]any)["master"] = "from-catalog:50051"
	in.Profile.Inputs = map[string]any{masterKey: "site.svc:50051"}
	in.Overrides = values.Tree{"storageKVOffload": map[string]any{"mooncake": map[string]any{"master": "10.0.0.1:50051"}}}
	p, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	want := []plan.AnsweredInput{{Key: masterKey, KeyType: plan.KeyTypeDotted, Value: "10.0.0.1:50051"}}
	if !reflect.DeepEqual(p.Inputs, want) {
		t.Fatalf("recorded %v, want the form's answer only", p.Inputs)
	}
	const ann = `[{"key":"storageKVOffload.mooncake.master","keyType":"dotted","value":"10.0.0.1:50051"}]`
	if p.InputsJSON() != ann {
		t.Fatalf("annotation %s", p.InputsJSON())
	}
	cleared := *p
	cleared.Inputs = nil
	if err := cleared.ComputeHash(); err != nil {
		t.Fatal(err)
	}
	if cleared.Hash != p.Hash {
		t.Fatal("answered inputs must not change the plan hash")
	}
}

// An answer reaches only a variant that declares its key: unlike extra, a
// site answer never lands in a deploy that did not ask.
func TestASiteAnswerReachesOnlyAVariantThatDeclaresIt(t *testing.T) {
	in := testInput()
	in.Profile.Inputs = map[string]any{masterKey: "site.svc:50051"}
	p, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := values.Get(p.Values(), masterKey); ok {
		t.Error("a site answer landed in a variant that declares no input")
	}
}
