package catalog

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/values"
)

var mooncakeMaster = Input{
	Path:    "storageKVOffload.mooncake.master",
	Title:   "Mooncake master",
	Pattern: `[^\s:/]+:[0-9]+`,
	Example: "mooncake-master.example:50051",
}

func inputVariant(inputs []Input, vals values.Tree) Variant {
	return Variant{
		ID:       "a",
		Engine:   "sglang",
		Chart:    Chart{Name: "sglang", Version: "0.9.32"},
		Requires: Requires{GPUs: 8, Inputs: inputs},
		Values:   vals,
	}
}

// The same rules as the catalog's validator (hack/lib/inputs.js), because the
// catalog is untrusted where it is used.
func TestAnInputsDefaultAndExampleAreAnswers(t *testing.T) {
	for name, mooncake := range map[string]map[string]any{
		"no default":   {"protocol": "rdma"},
		"with default": {"protocol": "rdma", "master": "mc.svc:50051"},
	} {
		ok := inputVariant([]Input{mooncakeMaster}, values.Tree{"storageKVOffload": map[string]any{"enabled": true, "mooncake": mooncake}})
		if err := ok.validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	one := func(in Input) []Input { return []Input{in} }
	at := func(in Input, edit func(*Input)) Input { edit(&in); return in }
	for name, c := range map[string]struct {
		v    Variant
		want string
	}{
		"default off pattern": {inputVariant(one(mooncakeMaster), values.Tree{"storageKVOffload": map[string]any{"mooncake": map[string]any{"master": "no-port"}}}), `default "no-port" does not match`},
		"a blank default":     {inputVariant(one(mooncakeMaster), values.Tree{"storageKVOffload": map[string]any{"mooncake": map[string]any{"master": ""}}}), `default "" does not match`},
		"a null default":      {inputVariant(one(mooncakeMaster), values.Tree{"storageKVOffload": map[string]any{"mooncake": map[string]any{"master": nil}}}), "default <nil> is not a string"},
		"default of a type":   {inputVariant(one(Input{Path: "a.b", Title: "t", Type: InputInteger, Example: 1}), values.Tree{"a": map[string]any{"b": "8"}}), "default 8 is not an integer"},
		"a non-map above it":  {inputVariant(one(mooncakeMaster), values.Tree{"storageKVOffload": map[string]any{"mooncake": "off"}}), "values.storageKVOffload.mooncake is not a map"},
		"declared twice":      {inputVariant([]Input{mooncakeMaster, mooncakeMaster}, nil), "declared twice"},
		"the model path":      {inputVariant(one(Input{Path: "model.localPath", Title: "p", Example: "/m"}), nil), "never an input"},
		"not a path":          {inputVariant(one(at(mooncakeMaster, func(i *Input) { i.Path = "a..b" })), nil), "not a dotted chart path"},
		"no title":            {inputVariant(one(at(mooncakeMaster, func(i *Input) { i.Title = " " })), nil), "title is required"},
		"no example":          {inputVariant(one(at(mooncakeMaster, func(i *Input) { i.Example = nil })), nil), "example is required"},
		"example off pattern": {inputVariant(one(at(mooncakeMaster, func(i *Input) { i.Example = "no-port" })), nil), `example "no-port" does not match`},
		"pattern compiles":    {inputVariant(one(at(mooncakeMaster, func(i *Input) { i.Pattern = "(" })), nil), "does not compile"},
		"pattern on integer":  {inputVariant(one(Input{Path: "a.b", Title: "t", Type: InputInteger, Pattern: "[0-9]+", Example: 1}), nil), "only for a string input"},
		"example of a type":   {inputVariant(one(Input{Path: "a.b", Title: "t", Type: InputInteger, Example: "8"}), nil), "8 is not an integer"},
		"unknown type":        {inputVariant(one(Input{Path: "a.b", Title: "t", Type: "list", Example: "x"}), nil), `type "list" is not`},
	} {
		if err := c.v.validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want an error containing %q, got %v", name, c.want, err)
		}
	}
}

// A number read from JSON is a float64: a whole one answers an integer input.
func TestAnIntegerInputTakesAWholeNumberOfAnyKind(t *testing.T) {
	port := Input{Path: "a.port", Title: "Port", Type: InputInteger, Example: 1}
	for _, v := range []any{8080, int64(8080), float64(8080)} {
		if err := port.Check(v); err != nil {
			t.Errorf("%T %v: %v", v, v, err)
		}
	}
	if err := port.Check(80.5); err == nil {
		t.Error("80.5 answered an integer input")
	}
}

// requires.inputs is a known key in both places a v2 variant is read strictly:
// its file, and its entry in the index.
func TestV2InputsAreReadFromTheFileAndTheIndex(t *testing.T) {
	raw := []byte("apiVersion: catalog.swiss/v2\nkind: Variant\nmodel: m\nid: a\nversion: 1.0.0\nengine: sglang\nchart: {name: sglang, version: \"0.9.32\"}\n" +
		"requires:\n  gpus: 1\n  inputs:\n    - {path: storageKVOffload.mooncake.master, title: Mooncake master, example: \"h:1\"}\n")
	f := memFetcher{"models/m/a.yaml": raw}
	f["index-v2.json"] = []byte(fmt.Sprintf(`{"apiVersion":"catalog.swiss/v2","count":1,"models":[{"name":"m","source":{"hf":"org/m"},"variants":[{"id":"a","version":"1.0.0","path":"models/m/a.yaml","digest":%q,"engine":"sglang","chart":{"name":"sglang","version":"0.9.32"},"requires":{"gpus":1,"inputs":[{"path":"storageKVOffload.mooncake.master","title":"Mooncake master","example":"h:1"}]}}]}]}`, contentRef(raw)))
	c, err := OpenFetcher(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Resolve(context.Background(), "m", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if in := r.Variant.Requires.Inputs; len(in) != 1 || in[0].Path != "storageKVOffload.mooncake.master" || in[0].Example != "h:1" {
		t.Errorf("inputs = %+v", in)
	}
}
