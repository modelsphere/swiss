package site

import (
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/modelsphere/swiss/internal/values"
)

func TestPresetsRoundTrip(t *testing.T) {
	p, err := Parse([]byte(inputsHead+`presets:
  storage-net:
    description: on storage-network nodes
    values:
      nodeSelector:
        ebof.modelsphere.io/storage-net: "true"
      tolerations:
        - key: ebof-test
          operator: Exists
          effect: NoSchedule
`), "test")
	if err != nil {
		t.Fatal(err)
	}
	got := p.Presets["storage-net"]
	if got.Description != "on storage-network nodes" {
		t.Errorf("description = %q", got.Description)
	}
	if sel, _ := values.Get(got.Values, "nodeSelector"); !reflect.DeepEqual(sel, values.Tree{"ebof.modelsphere.io/storage-net": "true"}) {
		t.Errorf("nodeSelector = %#v", sel)
	}
	out, err := yaml.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(out, "again")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.Presets, p.Presets) {
		t.Errorf("presets changed on the way back:\n%v\n%v", p.Presets, again.Presets)
	}
}

func TestPresetValuesMustBeAMap(t *testing.T) {
	if _, err := Parse([]byte(inputsHead+"presets:\n  x:\n    values: [a]\n"), "test"); err == nil {
		t.Error("a preset whose values are a list parsed")
	}
}
