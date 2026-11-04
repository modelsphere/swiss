package server

import (
	"encoding/json"
	"github.com/modelsphere/swiss/internal/exec"
	"github.com/modelsphere/swiss/internal/llmsvc"
	"github.com/modelsphere/swiss/internal/values"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"reflect"
	"testing"
)

func TestAssignedAdaptiveUpgradePinsEverythingElse(t *testing.T) {
	rig := startRig(t, llmProbe(), true, false, backendLLMSVC)
	hash := rig.storePlan(t, map[string]any{"release": "adaptive-arm"})
	p, err := rig.s.store.Plan(t.Context(), hash)
	if err != nil {
		t.Fatal(err)
	}
	p.Layers[values.LayerForm] = values.Tree{"schedulerName": "retained", "modelRoute": map[string]any{"nginx": map[string]any{"values": map[string]any{"adaptive_cc": "true", "adaptive_cc_min_frac": "0.4", "default_max": "42"}}}}
	if err = p.ComputeHash(); err != nil {
		t.Fatal(err)
	}
	u, err := llmsvc.FromPlan(p, "apply", "")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	wire, _, err := unstructured.UnstructuredJSONScheme.Decode(raw, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	u = wire.(*unstructured.Unstructured)
	anns := u.GetAnnotations()
	anns[llmsvc.AssignmentKey] = `{"apiVersion":"canary.modelsphere.dev/v1alpha1","kind":"LLMCanary","namespace":"models","name":"experiment","uid":"parent","role":"arm","arm":"baseline"}`
	anns[llmsvc.AdaptiveBaselineKey] = "ready"
	u.SetAnnotations(anns)
	if _, err = rig.c.Create(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	req := planRequest{AdaptiveOnly: true, Namespace: "models", FromRelease: "adaptive-arm", Overrides: values.Tree{"modelRoute": map[string]any{"nginx": map[string]any{"values": map[string]any{"adaptive_cc": "false"}}}}}
	tuned, err := rig.s.compose(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if tuned.Source != p.Source || tuned.Chart != p.Chart || tuned.Profile != p.Profile {
		t.Fatal("upgrade moved a frozen source")
	}
	if err = refuseCanaryPlan(u, tuned, exec.Upgrade, "apply"); err != nil {
		t.Fatal(err)
	}
	if tuned.Values()["schedulerName"] != "retained" {
		t.Fatal("lost scheduler")
	}
	if !reflect.DeepEqual(llmsvc.WithoutAdaptiveValues(map[string]any(p.Layers[values.LayerForm])), llmsvc.WithoutAdaptiveValues(map[string]any(tuned.Layers[values.LayerForm]))) {
		t.Fatal("changed non-adaptive values")
	}
	for _, action := range []string{"rollback", "scale", "install"} {
		if refuseCanaryPlan(u, tuned, exec.Upgrade, action) == nil {
			t.Fatalf("allowed %s", action)
		}
	}
	tuned.Layers[values.LayerForm]["replicaCount"] = int64(3)
	if refuseCanaryPlan(u, tuned, exec.Upgrade, "apply") == nil {
		t.Fatal("allowed scaling")
	}
	req.Overrides["replicaCount"] = int64(3)
	if _, err = rig.s.compose(t.Context(), req); err == nil {
		t.Fatal("accepted non-adaptive request")
	}
}
