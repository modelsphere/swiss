package llmsvc

import (
	"context"
	"encoding/json"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (c *Client) LiveSLO(ctx context.Context, a *CanaryAssignment) (bool, error) {
	if a == nil {
		return true, nil
	}
	if !a.Verified {
		return false, nil
	}
	if a.Role != "arm" {
		return true, nil
	}
	parent, err := c.dyn.Resource(schema.GroupVersionResource{Group: "canary.modelsphere.dev", Version: "v1alpha1", Resource: "llmcanaries"}).Namespace(a.Namespace).Get(ctx, a.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	policy, _, _ := unstructured.NestedString(parent.Object, "spec", "existingSLOPolicy")
	return string(parent.GetUID()) == a.UID && parent.GetDeletionTimestamp() == nil && policy == "LiveParent", nil
}

func PreserveSLOBinding(cur, fresh *unstructured.Unstructured) error {
	if cur == nil {
		return nil
	}
	a := CanaryAssigned(cur)
	if a == nil || !a.Verified || !a.Mutable {
		return nil
	}
	raw := cur.GetAnnotations()["canary.modelsphere.dev/slo-binding"]
	if raw == "" {
		return nil
	}
	var binding struct {
		Assignment string `json:"assignment"`
		Target     struct {
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
			UID       string `json:"uid"`
		} `json:"target"`
	}
	if err := json.Unmarshal([]byte(raw), &binding); err != nil {
		return fmt.Errorf("invalid inherited SLO binding: %w", err)
	}
	if binding.Assignment != cur.GetAnnotations()[AssignmentKey] || binding.Target.Namespace == "" || binding.Target.Name == "" || binding.Target.UID == "" {
		return fmt.Errorf("inherited SLO binding identity mismatch")
	}
	layers, _, err := unstructured.NestedSlice(fresh.Object, "spec", "layers")
	if err != nil {
		return err
	}
	clean := make([]any, 0, len(layers)+1)
	for _, raw := range layers {
		layer, _ := raw.(map[string]any)
		if layer["name"] != "canary-slo" {
			clean = append(clean, raw)
		}
	}
	clean = append(clean, map[string]any{"name": "canary-slo", "values": map[string]any{"modelRoute": map[string]any{"slo": map[string]any{"enabled": true, "name": binding.Target.Namespace + "/" + binding.Target.Name}}}})
	return unstructured.SetNestedSlice(fresh.Object, clean, "spec", "layers")
}
