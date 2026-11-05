package server

import (
	"encoding/json"
	"github.com/modelsphere/swiss/internal/llmsvc"
	"github.com/modelsphere/swiss/internal/values"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"testing"
)

func TestLivePlanReusesSourceWithoutCanaryRestrictions(t *testing.T) {
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
	if _, err = rig.c.Create(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	req := planRequest{ReuseSource: true, Namespace: "models", FromRelease: "adaptive-arm", Overrides: values.Tree{"modelRoute": map[string]any{"nginx": map[string]any{"values": map[string]any{"adaptive_cc": "false"}}}}}
	tuned, err := rig.s.compose(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if tuned.Source != p.Source || tuned.Chart != p.Chart || tuned.Profile != p.Profile {
		t.Fatal("upgrade moved a frozen source")
	}
	if tuned.Values()["schedulerName"] != "retained" {
		t.Fatal("lost scheduler")
	}

	req.Overrides["replicaCount"] = int64(3)
	req.OverridesYAML = "schedulerName: changed\n"
	req.EditsYAML = "resources:\n  limits:\n    memory: 2Gi\n"
	tuned, err = rig.s.compose(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if tuned.Values()["replicaCount"] != int64(3) || tuned.Values()["schedulerName"] != "changed" {
		t.Fatalf("ordinary overrides were restricted: %v", tuned.Values())
	}
	if memory, _ := values.Get(tuned.Values(), "resources.limits.memory"); memory != "2Gi" {
		t.Fatal("ordinary edit values were restricted")
	}
	req.Overrides = values.Tree{"modelRoute": map[string]any{"nginx": map[string]any{"values": map[string]any{"adaptive_cc_min_frac": nil, "default_max": "99"}}}}
	tuned, err = rig.s.compose(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	route, _, _ := unstructured.NestedMap(map[string]any(tuned.Values()), "modelRoute", "nginx", "values")
	if route["adaptive_cc_min_frac"] != nil {
		t.Fatal("null did not clear a stored option")
	}
	if route["default_max"] != "99" {
		t.Fatal("non-adaptive route setting was restricted")
	}
}
