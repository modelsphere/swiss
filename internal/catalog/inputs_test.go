package catalog

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/values"
)

var mooncakeMaster = Input{
	Key:     "storageKVOffload.mooncake.master",
	Title:   "Mooncake master",
	Pattern: `[^\s:/]+:[0-9]+`,
	Example: "mooncake-master.example:50051",
}

func inputVariant(inputs []Input, vals values.Tree) Variant {
	return Variant{
		ID:       "a",
		Engine:   "sglang",
		Chart:    Chart{Name: "sglang", Version: "0.9.32"},
		Requires: Requires{GPUs: 8},
		Inputs:   inputs,
		Values:   vals,
	}
}

func quietLog(t *testing.T) {
	t.Helper()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	t.Cleanup(func() { slog.SetDefault(prev) })
}

// An input this swissd can use is kept. swiss does not evaluate the key against
// variant values. One it cannot use is dropped; validate does not fail the variant.
func TestAnInputsDefaultAndExampleAreAnswers(t *testing.T) {
	quietLog(t)
	withDefault := mooncakeMaster
	withDefault.Default = "mc.svc:50051"
	for _, in := range []Input{mooncakeMaster, withDefault} {
		ok := inputVariant([]Input{in}, values.Tree{"storageKVOffload": map[string]any{"enabled": true, "mooncake": map[string]any{"protocol": "rdma"}}})
		if err := ok.validate(); err != nil {
			t.Errorf("default %v: %v", in.Default, err)
		}
		if got := usableInputs("m", ok.ID, ok.Inputs); len(got) != 1 || got[0].Key != in.Key || got[0].Default != in.Default {
			t.Errorf("default %v: kept %+v", in.Default, got)
		}
	}
	// values may set an input's key, or a non-map above it: swiss does not look.
	for _, base := range []any{"mooncake-master.example:50051", ""} {
		ok := inputVariant([]Input{mooncakeMaster}, values.Tree{"storageKVOffload": map[string]any{"mooncake": map[string]any{"master": base}}})
		if err := ok.validate(); err != nil {
			t.Errorf("values base %q: %v", base, err)
		}
		if got := usableInputs("m", ok.ID, ok.Inputs); len(got) != 1 {
			t.Errorf("values base %q dropped the input", base)
		}
	}
	nonMap := inputVariant([]Input{mooncakeMaster}, values.Tree{"storageKVOffload": map[string]any{"mooncake": "off"}})
	if err := nonMap.validate(); err != nil {
		t.Errorf("non-map ancestor: %v", err)
	}
	one := func(in Input) []Input { return []Input{in} }
	at := func(in Input, edit func(*Input)) Input { edit(&in); return in }
	blankTitle := mooncakeMaster
	blankTitle.Title = " "
	second := mooncakeMaster
	second.Title = "Second"
	if got := usableInputs("m", "a", []Input{mooncakeMaster, second}); len(got) != 1 || got[0].Key != mooncakeMaster.Key || got[0].Title != mooncakeMaster.Title {
		t.Errorf("duplicate kept %+v", got)
	}
	later := mooncakeMaster
	later.Title = "Later"
	if got := usableInputs("m", "a", []Input{blankTitle, later}); len(got) != 1 || got[0].Key != later.Key || got[0].Title != later.Title {
		t.Errorf("a dropped key blocked the next: %+v", got)
	}
	for _, c := range []struct {
		name string
		v    Variant
		want []string
	}{
		{"default off pattern", inputVariant(one(at(mooncakeMaster, func(i *Input) { i.Default = "no-port" })), nil), nil},
		{"a blank default", inputVariant(one(at(mooncakeMaster, func(i *Input) { i.Default = "" })), nil), nil},
		{"default of a type", inputVariant(one(Input{Key: "a.b", Title: "t", Type: InputInteger, Example: 1, Default: "8"}), nil), nil},
		{"the model path", inputVariant(one(Input{Key: "model.localPath", Title: "p", Example: "/m"}), nil), nil},
		{"no title", inputVariant(one(blankTitle), nil), nil},
		{"no example", inputVariant(one(at(mooncakeMaster, func(i *Input) { i.Example = nil })), nil), nil},
		{"example off pattern", inputVariant(one(at(mooncakeMaster, func(i *Input) { i.Example = "no-port" })), nil), nil},
		{"pattern compiles", inputVariant(one(at(mooncakeMaster, func(i *Input) { i.Pattern = "(" })), nil), nil},
		{"pattern on integer", inputVariant(one(Input{Key: "a.b", Title: "t", Type: InputInteger, Pattern: "[0-9]+", Example: 1}), nil), nil},
		{"example of a type", inputVariant(one(Input{Key: "a.b", Title: "t", Type: InputInteger, Example: "8"}), nil), nil},
		{"unknown type", inputVariant(one(Input{Key: "a.b", Title: "t", Type: "list", Example: "x"}), nil), nil},
		{"unknown type beside a keeper", inputVariant([]Input{mooncakeMaster, {Key: "a.b", Title: "t", Type: "list", Example: "x"}}, nil), []string{mooncakeMaster.Key}},
	} {
		if err := c.v.validate(); err != nil {
			t.Errorf("%s: validate failed the variant: %v", c.name, err)
		}
		got := usableInputs("m", c.v.ID, c.v.Inputs)
		if len(got) != len(c.want) {
			t.Errorf("%s: kept %+v, want keys %v", c.name, got, c.want)
			continue
		}
		for i, k := range c.want {
			if got[i].Key != k {
				t.Errorf("%s: inputs[%d].key = %q, want %q", c.name, i, got[i].Key, k)
			}
		}
	}
}

// A number read from JSON is a float64: a whole one answers an integer input.
func TestAnIntegerInputTakesAWholeNumberOfAnyKind(t *testing.T) {
	port := Input{Key: "a.port", Title: "Port", Type: InputInteger, Example: 1}
	for _, v := range []any{8080, int64(8080), float64(8080)} {
		if err := port.Check(v); err != nil {
			t.Errorf("%T %v: %v", v, v, err)
		}
	}
	if err := port.Check(80.5); err == nil {
		t.Error("80.5 answered an integer input")
	}
}

// A kyaml-style key and a patcher are stored as-is. An empty key is dropped.
func TestKeyAndPatcherAreStoredAsIs(t *testing.T) {
	quietLog(t)
	in := mooncakeMaster
	in.Key = "volumes.[name=draft-model].hostPath.path"
	in.Patcher = "http://{value}/v1"
	v := inputVariant([]Input{in}, nil)
	if err := v.validate(); err != nil {
		t.Errorf("kyaml key and patcher: %v", err)
	}
	if got := usableInputs("m", v.ID, v.Inputs); len(got) != 1 || got[0].Key != in.Key || got[0].Patcher != in.Patcher {
		t.Errorf("kyaml key and patcher were not kept: %+v", got)
	}
	in.Key = ""
	v = inputVariant([]Input{in}, nil)
	if err := v.validate(); err != nil {
		t.Errorf("empty key failed the variant: %v", err)
	}
	if got := usableInputs("m", v.ID, v.Inputs); len(got) != 0 {
		t.Errorf("empty key kept: %+v", got)
	}
}

// inputs are read from the v2 variant file. The index carries none: nothing
// lists or picks a variant by them.
func TestV2InputsAreReadFromTheFile(t *testing.T) {
	raw := []byte("apiVersion: catalog.swiss/v2\nkind: Variant\nmodel: m\nid: a\nversion: 1.0.0\nengine: sglang\nchart: {name: sglang, version: \"0.9.32\"}\n" +
		"requires:\n  gpus: 1\ninputs:\n  - {key: storageKVOffload.mooncake.master, title: Mooncake master, example: \"h:1\", default: \"d:1\", patcher: \"http://{value}\"}\n")
	f := memFetcher{"models/m/a.yaml": raw}
	f["index-v2.json"] = []byte(fmt.Sprintf(`{"apiVersion":"catalog.swiss/v2","count":1,"models":[{"name":"m","source":{"hf":"org/m"},"variants":[{"id":"a","version":"1.0.0","path":"models/m/a.yaml","digest":%q,"engine":"sglang","chart":{"name":"sglang","version":"0.9.32"},"requires":{"gpus":1}}]}]}`, contentRef(raw)))
	c, err := OpenFetcher(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Resolve(context.Background(), "m", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if in := r.Variant.Inputs; len(in) != 1 || in[0].Key != "storageKVOffload.mooncake.master" || in[0].Example != "h:1" || in[0].Default != "d:1" || in[0].Patcher != "http://{value}" {
		t.Errorf("inputs = %+v", in)
	}
}

// A bad input is dropped and warned. The variant, and its other inputs, load.
func TestDroppedInputDoesNotBlockTheVariant(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const inputs = `inputs:
      - {key: a.keep, title: Keep, example: ok}
      - {key: a.drop, title: Drop, type: list, example: x}
      - {key: a.keep, title: Later, example: "no"}
`
	body := `
apiVersion: catalog.swiss/v1
name: m
version: 1.1.0
variants:
  - id: v
    engine: sglang
    chart: {name: sglang, version: "0.8.0"}
    requires: {gpus: 2}
    ` + inputs + `    values:
      extraArgs: [--tp-size=2]
`
	srv := serve(t, map[string]string{
		"catalog/index.json":          index(map[string]string{"1.1.0": body}),
		"catalog/models/m/1.1.0.yaml": body,
	})
	c, err := Open(context.Background(), srv.URL+"/catalog")
	if err != nil {
		t.Fatal(err)
	}
	e, err := c.(*V1).Entry(context.Background(), "m", "1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if e.Variants[0].Engine != "sglang" || e.Variants[0].Values["extraArgs"] == nil {
		t.Fatalf("variant did not load: %+v", e.Variants[0])
	}
	if in := e.Variants[0].Inputs; len(in) != 1 || in[0].Key != "a.keep" || in[0].Title != "Keep" {
		t.Fatalf("v1 inputs = %+v", in)
	}
	r, err := c.Resolve(context.Background(), "m", "v", "1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Variant.Inputs) != 1 || r.Variant.Inputs[0].Key != "a.keep" {
		t.Fatalf("v1 resolve inputs = %+v", r.Variant.Inputs)
	}
	warn := buf.String()
	for _, part := range []string{`"model":"m"`, `"variant":"v"`, `"key":"a.drop"`, "list", "declared twice"} {
		if !strings.Contains(warn, part) {
			t.Errorf("v1 warning missing %q: %s", part, warn)
		}
	}

	buf.Reset()
	vc, _ := v2Catalog(t, "", nil, v2Variant{id: "a", version: "1.0.0", extra: strings.ReplaceAll(inputs, "\n      ", "\n")})
	vr, err := vc.Resolve(context.Background(), "m", "a", "")
	if err != nil {
		t.Fatal(err)
	}
	if vr.Variant.Engine != "sglang" || vr.Variant.Requires.GPUs != 1 {
		t.Fatalf("v2 variant did not load: %+v", vr.Variant)
	}
	if in := vr.Variant.Inputs; len(in) != 1 || in[0].Key != "a.keep" || in[0].Title != "Keep" {
		t.Fatalf("v2 inputs = %+v", in)
	}
	warn = buf.String()
	for _, part := range []string{`"model":"m"`, `"variant":"a"`, `"key":"a.drop"`, "list", "declared twice"} {
		if !strings.Contains(warn, part) {
			t.Errorf("v2 warning missing %q: %s", part, warn)
		}
	}
}
