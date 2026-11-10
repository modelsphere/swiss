package compose

import (
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/modelsphere/swiss/internal/values"
)

const masterPath = "storageKVOffload.mooncake.master"

func inputsInput() Input {
	in := testInput()
	in.Resolved.Variant.Requires.Inputs = []catalog.Input{{
		Path:    masterPath,
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
	in.Profile.Features.Inputs = map[string]any{masterPath: "mc-{{model}}.svc:50051"}
	p, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := values.Get(p.Values(), masterPath); got != "mc-glm-5.3.svc:50051" {
		t.Errorf("%s = %v", masterPath, got)
	}
	if got := p.LayerOf(masterPath); got != values.LayerDerived {
		t.Errorf("answered by %s, want derived", got)
	}
	if got, _ := values.Get(p.Values(), "storageKVOffload.mooncake.protocol"); got != "rdma" {
		t.Errorf("the variant's own mooncake values were lost: protocol = %v", got)
	}
}

// Level two: the deployer overrides any answer, and needs no site answer to.
func TestTheDeployOverridesTheSitesAnswer(t *testing.T) {
	for name, site := range map[string]map[string]any{
		"over the site's answer": {masterPath: "site.svc:50051"},
		"with no site answer":    nil,
	} {
		in := inputsInput()
		in.Profile.Features.Inputs = site
		in.Overrides = values.Tree{"storageKVOffload": map[string]any{"mooncake": map[string]any{"master": "mine.svc:50051"}}}
		p, err := Compose(in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got, _ := values.Get(p.Values(), masterPath); got != "mine.svc:50051" || p.LayerOf(masterPath) != values.LayerForm {
			t.Errorf("%s: %s = %v from %s, want the deploy's", name, masterPath, got, p.LayerOf(masterPath))
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
	in.Resolved.Variant.Requires.Inputs[0].Default = def
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
		"the site over it":        {site: map[string]any{masterPath: "site.svc:50051"}, want: "site.svc:50051", layer: values.LayerDerived},
		"the deploy over the two": {site: map[string]any{masterPath: "site.svc:50051"}, form: map[string]any{"master": "mine.svc:50051"}, want: "mine.svc:50051", layer: values.LayerForm},
	} {
		in := withDefault(inputsInput(), "tuned.svc:50051")
		in.Profile.Features.Inputs = c.site
		if c.form != nil {
			in.Overrides = values.Tree{"storageKVOffload": map[string]any{"mooncake": c.form}}
		}
		p, err := Compose(in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got, _ := values.Get(p.Values(), masterPath); got != c.want || p.LayerOf(masterPath) != c.layer {
			t.Errorf("%s: %s = %v from %s, want %s from %s", name, masterPath, got, p.LayerOf(masterPath), c.want, c.layer)
		}
	}
}

func TestAnAnswerTheInputDoesNotTakeRefusesThePlan(t *testing.T) {
	in := inputsInput()
	in.Profile.Features.Inputs = map[string]any{masterPath: "no-port"}
	if _, err := Compose(in); err == nil || !strings.Contains(err.Error(), `"no-port" does not match`) {
		t.Fatalf("want a refusal quoting the answer, got %v", err)
	}
}

// An answer reaches only a variant that declares its path: unlike extra, a
// site answer never lands in a deploy that did not ask.
func TestASiteAnswerReachesOnlyAVariantThatDeclaresIt(t *testing.T) {
	in := testInput()
	in.Profile.Features.Inputs = map[string]any{masterPath: "site.svc:50051"}
	p, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := values.Get(p.Values(), masterPath); ok {
		t.Error("a site answer landed in a variant that declares no input")
	}
}
