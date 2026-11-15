package site

import "testing"

const inputsHead = "name: prod\nmodel:\n  pathTemplate: /models/{{name}}\nnodes:\n  gpus: []\n"

// Parse keeps profile inputs as written. swiss does not fill placeholders.
func TestParseKeepsProfileInputsAsWritten(t *testing.T) {
	p, err := Parse([]byte(inputsHead+`inputs:
  storageKVOffload.mooncake.master: mooncake-master.svc:50051
  engineCache.hostPath: /data/cache/{{model}}/{{org}}
  service.port: 8050
  shm.enabled: true
`), "test")
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"storageKVOffload.mooncake.master": "mooncake-master.svc:50051",
		"engineCache.hostPath":             "/data/cache/{{model}}/{{org}}",
		"service.port":                     8050,
		"shm.enabled":                      true,
	} {
		got, ok := p.Inputs[key]
		if !ok || got != want {
			t.Errorf("%s = %v (ok %v), want %v", key, got, ok, want)
		}
	}
	if len(p.Inputs) != 4 {
		t.Errorf("inputs = %#v", p.Inputs)
	}
}
