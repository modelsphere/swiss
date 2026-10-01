package cluster

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

const manifest = `---
# Source: sglang/templates/service.yaml
apiVersion: v1
kind: Service
metadata:
  name: glm-53
---
# Source: sglang/templates/modelroute.yaml
apiVersion: routing.modelsphere.dev/v1alpha1
kind: ModelRoute
metadata:
  name: glm-53
  namespace: models
---
# Source: sglang/templates/llmscaler.yaml
apiVersion: autoscaling.4pd.io/v1alpha1
kind: LLMScaler
metadata:
  name: glm-53
---
# Source: sglang/templates/llmslorequirement.yaml
apiVersion: inference.modelsphere.dev/v1alpha1
kind: LLMSLORequirement
metadata:
  name: glm-slo
  namespace: models
`

// The manifest is the only record of which group a chart version rendered and
// what an override renamed, so both come from it rather than from convention.
func TestManifestObjectsKeepsGroupNameAndNamespace(t *testing.T) {
	got := manifestObjects(manifest, "models")
	want := []ObjectRef{
		{APIVersion: "routing.modelsphere.dev/v1alpha1", Kind: "ModelRoute", Namespace: "models", Name: "glm-53"},
		{APIVersion: "autoscaling.4pd.io/v1alpha1", Kind: "LLMScaler", Namespace: "models", Name: "glm-53"},
		{APIVersion: "inference.modelsphere.dev/v1alpha1", Kind: "LLMSLORequirement", Namespace: "models", Name: "glm-slo"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestObjectReadsSpecAndStatusAndReportsAbsence(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "autoscaling.modelsphere.dev", Version: "v1alpha1", Resource: "llmscalers"}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "autoscaling.modelsphere.dev/v1alpha1",
		"kind":       "LLMScaler",
		"metadata":   map[string]any{"name": "glm-53", "namespace": "models", "generation": int64(3)},
		"spec":       map[string]any{"minReplicas": int64(1), "maxReplicas": int64(4)},
		"status":     map[string]any{"currentReplicas": int64(2), "desiredReplicas": int64(3)},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "LLMScalerList"}, u)
	k := NewKubeWithDynamic(fake.NewSimpleClientset(), dyn)

	ref := ObjectRef{APIVersion: "autoscaling.modelsphere.dev/v1alpha1", Kind: "LLMScaler", Namespace: "models", Name: "glm-53"}
	o, err := k.Object(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if o == nil || o.Generation != 3 || o.Spec["maxReplicas"] != int64(4) || o.Status["desiredReplicas"] != int64(3) {
		t.Fatalf("got %+v", o)
	}

	ref.Name = "gone"
	if o, err := k.Object(context.Background(), ref); err != nil || o != nil {
		t.Fatalf("a deleted object is absent, not an error: %+v %v", o, err)
	}
}
