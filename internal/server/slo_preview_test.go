package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/llmsvc"
	"github.com/modelsphere/swiss/internal/values"
)

func TestDiffAndApplyUseSameInheritedSLO(t *testing.T) {
	t.Setenv("CANARY_ALLOW_MUTABLE_REFERENCES", "true")
	rig := startRig(t, llmProbe(), true, true, backendLLMSVC)
	hash := rig.storePlan(t, map[string]any{"release": "arm", "serviceId": "arm"})
	p, err := rig.s.store.Plan(t.Context(), hash)
	if err != nil {
		t.Fatal(err)
	}
	u, err := llmsvc.FromPlan(p, "install", "")
	if err != nil {
		t.Fatal(err)
	}
	assignment := `{"apiVersion":"canary.modelsphere.dev/v1alpha1","kind":"LLMCanary","namespace":"canary","name":"experiment","uid":"parent-uid","role":"arm","arm":"baseline"}`
	binding, _ := json.Marshal(map[string]any{"assignment": assignment, "target": map[string]any{"namespace": "parent", "name": "shared-slo", "uid": "slo-uid"}})
	anns := u.GetAnnotations()
	anns[llmsvc.AssignmentKey] = assignment
	anns[llmsvc.CanaryPrefix+"slo-binding"] = string(binding)
	u.SetAnnotations(anns)
	if _, err := rig.c.Create(t.Context(), u); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(t.TempDir(), "helmfile")
	script := "#!/bin/sh\nset -eu\ncat \"$2\"\ncat \"$(dirname \"$2\")/canary-slo.yaml\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	rig.s.cfg.Server.HelmfileBin = bin
	code, body := post(t, rig.srv, "/api/diff", map[string]any{"planHash": hash})
	if code != http.StatusOK || body["planHash"] != hash {
		t.Fatalf("diff %d %v", code, body)
	}
	output, _ := body["output"].(string)
	if !strings.Contains(output, "canary-slo.yaml") || !strings.Contains(output, "parent/shared-slo") {
		t.Fatal("diff omitted inherited requirement", body)
	}
	code, body = post(t, rig.srv, "/api/apply", map[string]any{"planHash": hash})
	if code != http.StatusOK {
		t.Fatalf("apply %d %v", code, body)
	}
	live, _ := rig.mustGet(t, "models", "arm")
	applied, err := llmsvc.ToPlan(live)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := values.Get(applied.Values(), "modelRoute.slo.name"); got != "parent/shared-slo" {
		t.Fatal("apply disagrees with preview", applied.Values())
	}
	if llmsvc.EditedInPlace(live, applied) {
		t.Fatal("preserved SLO does not match the annotated plan hash")
	}
	stored, err := rig.s.store.Plan(t.Context(), hash)
	if err != nil || stored.Layers[values.LayerCanarySLO] != nil {
		t.Fatal("preview modified the stored user plan", stored, err)
	}

	liveAnns := live.GetAnnotations()
	liveAnns[llmsvc.CanaryPrefix+"slo-binding"] = `{"assignment":"other-experiment"}`
	live.SetAnnotations(liveAnns)
	if _, err := rig.c.Update(t.Context(), live); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/diff", "/api/apply"} {
		code, body = post(t, rig.srv, path, map[string]any{"planHash": hash})
		if code != http.StatusConflict || !strings.Contains(body["error"].(string), "binding identity mismatch") {
			t.Fatalf("%s accepted unrelated binding: %d %v", path, code, body)
		}
	}
}
