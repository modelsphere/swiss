package cluster

import (
	"maps"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestDisaggregatedSetRoleReplicas(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"slices": int64(1),
			"roles": []any{
				map[string]any{"name": "prefill", "spec": map[string]any{"replicas": int64(3)}},
				map[string]any{"name": "decode", "spec": map[string]any{"replicas": float64(1)}},
				map[string]any{"name": "router", "spec": map[string]any{}},
				map[string]any{"spec": map[string]any{"replicas": int64(2)}},
				"not a role",
			},
		},
	}}
	ds := disaggregatedSetFrom(u)
	want := map[string]int{"prefill": 3, "decode": 1}
	if !maps.Equal(ds.RoleReplicas, want) {
		t.Fatalf("role replicas %v, want %v", ds.RoleReplicas, want)
	}
	if ds.Slices == nil || *ds.Slices != 1 {
		t.Fatalf("slices %v, want 1", ds.Slices)
	}
}

func TestDisaggregatedSetNoRoles(t *testing.T) {
	ds := disaggregatedSetFrom(&unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{}}})
	if ds.RoleReplicas != nil {
		t.Fatalf("role replicas %v, want nil", ds.RoleReplicas)
	}
}
