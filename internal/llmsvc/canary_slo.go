package llmsvc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelsphere/swiss/internal/values"

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

func SLOBindingValues(cur *unstructured.Unstructured) (values.Tree, error) {
	if cur == nil {
		return nil, nil
	}
	a := CanaryAssigned(cur)
	if a == nil || !a.Verified || !a.Mutable {
		return nil, nil
	}
	raw := cur.GetAnnotations()["canary.modelsphere.dev/slo-binding"]
	if raw == "" {
		return nil, nil
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
		return nil, fmt.Errorf("invalid inherited SLO binding: %w", err)
	}
	if binding.Assignment != cur.GetAnnotations()[AssignmentKey] || binding.Target.Namespace == "" || binding.Target.Name == "" || binding.Target.UID == "" {
		return nil, fmt.Errorf("inherited SLO binding identity mismatch")
	}
	return values.Tree{"modelRoute": map[string]any{"slo": map[string]any{"enabled": true, "name": binding.Target.Namespace + "/" + binding.Target.Name}}}, nil
}

func PreserveSLOBinding(cur, fresh *unstructured.Unstructured) error {
	tree, err := SLOBindingValues(cur)
	if err != nil || tree == nil {
		return err
	}
	layers, _, err := unstructured.NestedSlice(fresh.Object, "spec", "layers")
	if err != nil {
		return err
	}
	clean := make([]any, 0, len(layers)+1)
	for _, raw := range layers {
		layer, _ := raw.(map[string]any)
		if layer["name"] != values.LayerCanarySLO {
			clean = append(clean, raw)
		}
	}
	clean = append(clean, map[string]any{"name": values.LayerCanarySLO, "values": map[string]any(tree)})
	if err := unstructured.SetNestedSlice(fresh.Object, clean, "spec", "layers"); err != nil {
		return err
	}
	if anns := fresh.GetAnnotations(); anns[AnnPlanHash] != "" {
		p, err := ToPlan(fresh)
		if err != nil {
			return err
		}
		anns[AnnPlanHash] = p.Hash
		fresh.SetAnnotations(anns)
	}
	return nil
}
