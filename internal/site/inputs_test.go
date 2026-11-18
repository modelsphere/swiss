package site

import (
	"strings"
	"testing"
)

const inputsHead = "name: prod\nmodel:\n  pathTemplate: /models/{{name}}\nnodes:\n  gpus: []\n"

// Parse keeps profile inputs as written. swiss does not fill placeholders.
func TestParseKeepsProfileInputsAsWritten(t *testing.T) {
	p, err := Parse([]byte(inputsHead+`inputs:
  - key: storageKVOffload.mooncake.master
    value: mooncake-master.svc:50051
  - key: engineCache.hostPath
    value: /data/cache/{{model}}/{{org}}
  - key: service.port
    value: 8050
  - key: shm.enabled
    value: true
  - key: volumes.[name=draft-model].hostPath.path
    value: /data/models/Kimi-K3-DSpark
`), "test")
	if err != nil {
		t.Fatal(err)
	}
	want := []Input{
		{"storageKVOffload.mooncake.master", "mooncake-master.svc:50051"},
		{"engineCache.hostPath", "/data/cache/{{model}}/{{org}}"},
		{"service.port", 8050},
		{"shm.enabled", true},
		{"volumes.[name=draft-model].hostPath.path", "/data/models/Kimi-K3-DSpark"},
	}
	if len(p.Inputs) != len(want) {
		t.Fatalf("inputs = %#v", p.Inputs)
	}
	for i, w := range want {
		if p.Inputs[i] != w {
			t.Errorf("inputs[%d] = %#v, want %#v", i, p.Inputs[i], w)
		}
	}
}

func TestParseRefusesABadInputsList(t *testing.T) {
	for name, tc := range map[string]struct{ inputs, want string }{
		"duplicate": {"  - key: a.b\n    value: 1\n  - key: a.b\n    value: 2\n", `key "a.b" is listed twice`},
		"no key":    {"  - value: 1\n", "inputs[0]: key is required"},
		"a map":     {"  a.b: 1\n", "cannot unmarshal"},
	} {
		_, err := Parse([]byte(inputsHead+"inputs:\n"+tc.inputs), "test")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}
