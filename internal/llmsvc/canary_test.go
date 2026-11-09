package llmsvc

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCanaryOwnershipReportsIncompleteMetadata(t *testing.T) {
	controller := true
	ref := metav1.OwnerReference{APIVersion: "canary.modelsphere.dev/v1alpha1", Kind: "LLMCanary", Name: "demo", UID: "old-uid", Controller: &controller}
	labels := map[string]string{CanaryPrefix + "canary-name": "demo", CanaryPrefix + "canary-namespace": "canary", CanaryPrefix + "canary-uid": "old-uid"}
	for _, tc := range []struct {
		name                string
		refs                []metav1.OwnerReference
		labels              map[string]string
		protected, verified bool
	}{
		{"ordinary", nil, nil, false, false},
		{"matching", []metav1.OwnerReference{ref}, labels, true, true},
		{"removed labels", []metav1.OwnerReference{ref}, nil, true, false},
		{"removed reference", nil, labels, true, false},
		{"different UID", []metav1.OwnerReference{ref}, map[string]string{CanaryPrefix + "canary-uid": "new-uid"}, true, false},
		{"missing namespace label", nil, map[string]string{CanaryPrefix + "canary-name": "demo"}, true, false},
		{"empty marker", nil, map[string]string{CanaryPrefix + "canary-name": ""}, true, false},
		{"unrelated owner", []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "demo", UID: "old-uid"}}, nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := &unstructured.Unstructured{}
			u.SetNamespace("canary")
			u.SetOwnerReferences(tc.refs)
			u.SetLabels(tc.labels)
			owner := CanaryOwnership(u)
			if owner != nil && owner.Namespace != "canary" {
				t.Fatalf("owner namespace %q, want canary", owner.Namespace)
			}
			if (owner != nil) != tc.protected || (owner != nil && owner.Verified != tc.verified) {
				t.Fatalf("%+v", owner)
			}
		})
	}
}

func TestCanarySystemLayerIsEffectiveInReadPlan(t *testing.T) {
	u, err := ToUnstructured(&LLMService{Spec: Spec{Chart: ChartSpec{Name: "vllm", Repo: "oci://example/charts", Version: "1.0.0"}, Layers: []Layer{
		{Name: "form", Values: map[string]any{"replicaCount": 1, "scaler": map[string]any{"enabled": true}, "modelRoute": map[string]any{"nginx": map[string]any{"route": "arm-name"}}}},
		{Name: "canary-system", Values: map[string]any{"scaler": map[string]any{"enabled": false}, "modelRoute": map[string]any{"nginx": map[string]any{"route": "production-route"}}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := ToPlan(u)
	if err != nil {
		t.Fatal(err)
	}
	v := p.Values()
	if v["scaler"].(map[string]any)["enabled"] != false || v["modelRoute"].(map[string]any)["nginx"].(map[string]any)["route"] != "production-route" {
		t.Fatalf("system policy ignored: %v", v)
	}
	if p.LayerOf("scaler.enabled") != "canary-system" {
		t.Fatal("wrong provenance")
	}
}

func TestCanaryAssignedReportsArmAndMalformedMetadata(t *testing.T) {
	for _, tc := range []struct {
		name              string
		raw               string
		present, verified bool
	}{
		{"none", "", false, false},
		{"valid arm", `{"apiVersion":"canary.modelsphere.dev/v1alpha1","kind":"LLMCanary","namespace":"canary","name":"demo","uid":"parent-uid","role":"arm","arm":"baseline","configDigest":"sha256:abc","configVersion":"7"}`, true, true},
		{"valid production", `{"apiVersion":"canary.modelsphere.dev/v1alpha1","kind":"LLMCanary","namespace":"canary","name":"demo","uid":"parent-uid","role":"production"}`, true, true},
		{"empty annotation", " ", true, false},
		{"malformed json", "{not-json", true, false},
		{"missing uid", `{"apiVersion":"canary.modelsphere.dev/v1alpha1","kind":"LLMCanary","namespace":"canary","name":"demo","role":"arm","arm":"baseline"}`, true, false},
		{"arm without arm name", `{"apiVersion":"canary.modelsphere.dev/v1alpha1","kind":"LLMCanary","namespace":"canary","name":"demo","uid":"parent-uid","role":"arm"}`, true, false},
		{"production with arm name", `{"apiVersion":"canary.modelsphere.dev/v1alpha1","kind":"LLMCanary","namespace":"canary","name":"demo","uid":"parent-uid","role":"production","arm":"baseline"}`, true, false},
		{"cross namespace production", `{"apiVersion":"canary.modelsphere.dev/v1alpha1","kind":"LLMCanary","namespace":"other","name":"demo","uid":"parent-uid","role":"production"}`, true, true},
		{"cross namespace arm", `{"apiVersion":"canary.modelsphere.dev/v1alpha1","kind":"LLMCanary","namespace":"other","name":"demo","uid":"parent-uid","role":"arm","arm":"baseline"}`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := &unstructured.Unstructured{}
			u.SetNamespace("canary")
			if tc.present {
				u.SetAnnotations(map[string]string{AssignmentKey: tc.raw})
			}
			got := CanaryAssigned(u)
			if (got != nil) != tc.present || (got != nil && got.Verified != tc.verified) {
				t.Fatalf("got %+v", got)
			}
		})
	}
}
