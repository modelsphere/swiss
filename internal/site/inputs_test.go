package site

import (
	"strings"
	"testing"
)

const inputsHead = "name: prod\nmodel:\n  pathTemplate: /models/{{name}}\nnodes:\n  gpus: []\n"

func TestInputAnswersFillTheModelPlaceholders(t *testing.T) {
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
		"engineCache.hostPath":             "/data/cache/glm-5.3/zai-org",
		"service.port":                     8050,
		"shm.enabled":                      true,
	} {
		got, ok, err := p.InputAnswer(key, "zai-org/GLM-5.3", "glm-5.3")
		if err != nil || !ok || got != want {
			t.Errorf("%s = %v (ok %v, err %v), want %v", key, got, ok, err, want)
		}
	}
	if _, ok, _ := p.InputAnswer("not.answered", "zai-org/GLM-5.3", "glm-5.3"); ok {
		t.Error("an unanswered key has an answer")
	}
}

// A model with no weights has no repo id for a template to name.
func TestATemplateNamingTheRepoNeedsASource(t *testing.T) {
	p := Profile{Inputs: map[string]any{"a.b": "/data/{{name}}", "c.d": "/data/{{model}}"}}
	if _, _, err := p.InputAnswer("a.b", "", "mock"); err == nil || !strings.Contains(err.Error(), "no source.hf") {
		t.Errorf("want a refusal naming source.hf, got %v", err)
	}
	if got, _, err := p.InputAnswer("c.d", "", "mock"); err != nil || got != "/data/mock" {
		t.Errorf("{{model}} needs no source: %v, %v", got, err)
	}
}
