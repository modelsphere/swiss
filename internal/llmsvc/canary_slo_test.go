package llmsvc

import (
	"encoding/json"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"testing"
)

func TestLiveSLORequiresMatchingParentPolicyAndUID(t *testing.T) {
	for _, tc := range []struct {
		name, uid, policy string
		want              bool
	}{{"enabled", "parent-uid", "LiveParent", true}, {"legacy", "parent-uid", "", false}, {"replaced", "other-uid", "LiveParent", false}} {
		t.Run(tc.name, func(t *testing.T) {
			parent := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "canary.modelsphere.dev/v1alpha1", "kind": "LLMCanary", "metadata": map[string]any{"namespace": "canary", "name": "experiment", "uid": tc.uid}, "spec": map[string]any{"existingSLOPolicy": tc.policy}}}
			c := New(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), parent), nil, nil)
			a := &CanaryAssignment{APIVersion: "canary.modelsphere.dev/v1alpha1", Kind: "LLMCanary", Namespace: "canary", Name: "experiment", UID: "parent-uid", Role: "arm", Arm: "baseline", Verified: true}
			got, err := c.LiveSLO(t.Context(), a)
			if err != nil || got != tc.want {
				t.Fatal(got, err)
			}
			a.Verified = false
			got, err = c.LiveSLO(t.Context(), a)
			if err != nil || got {
				t.Fatal("unverified assignment allowed SLO edit", got, err)
			}
		})
	}
}

func TestUpgradePreservesInheritedSLO(t *testing.T) {
	t.Setenv("CANARY_ALLOW_MUTABLE_REFERENCES", "true")
	assignment := `{"apiVersion":"canary.modelsphere.dev/v1alpha1","kind":"LLMCanary","namespace":"canary","name":"experiment","uid":"parent-uid","role":"arm","arm":"baseline"}`
	cur := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{}}}
	binding := map[string]any{"assignment": assignment, "target": map[string]any{"namespace": "production", "name": "slo", "uid": "slo-uid"}}
	raw, _ := json.Marshal(binding)
	cur.SetAnnotations(map[string]string{AssignmentKey: assignment, "canary.modelsphere.dev/slo-binding": string(raw)})
	fresh := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"layers": []any{map[string]any{"name": "form", "values": map[string]any{"replicaCount": int64(2)}}}}}}
	if err := PreserveSLOBinding(cur, fresh); err != nil {
		t.Fatal(err)
	}
	layers, _, _ := unstructured.NestedSlice(fresh.Object, "spec", "layers")
	if len(layers) != 2 {
		t.Fatal(layers)
	}
	last := layers[1].(map[string]any)
	name, _, _ := unstructured.NestedString(last, "values", "modelRoute", "slo", "name")
	if name != "production/slo" {
		t.Fatal(last)
	}
	if err := PreserveSLOBinding(cur, fresh); err != nil {
		t.Fatal(err)
	}
	layers, _, _ = unstructured.NestedSlice(fresh.Object, "spec", "layers")
	if len(layers) != 2 {
		t.Fatal("binding duplicated")
	}
}
