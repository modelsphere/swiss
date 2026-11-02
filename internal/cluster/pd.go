package cluster

import (
	"context"
	"encoding/json"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// disaggregatedSetGVR is the DisaggregatedSet a prefill/decode chart renders.
var disaggregatedSetGVR = schema.GroupVersionResource{
	Group:    "disaggregatedset.x-k8s.io",
	Version:  "v1",
	Resource: "disaggregatedsets",
}

func (k *Kube) Deployment(ctx context.Context, ref string) (*Deployment, error) {
	ns, name, err := SplitRef(ref)
	if err != nil {
		return nil, err
	}
	d, err := k.client.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get deployment %s: %w", ref, err)
	}
	spec := 1
	if d.Spec.Replicas != nil {
		spec = int(*d.Spec.Replicas)
	}
	return &Deployment{
		Generation:         d.Generation,
		ObservedGeneration: d.Status.ObservedGeneration,
		SpecReplicas:       spec,
		ReadyReplicas:      int(d.Status.ReadyReplicas),
	}, nil
}

func (k *Kube) DisaggregatedSet(ctx context.Context, ref string) (*DisaggregatedSet, error) {
	if k.dyn == nil {
		return nil, fmt.Errorf("no dynamic client: DisaggregatedSet needs one")
	}
	ns, name, err := SplitRef(ref)
	if err != nil {
		return nil, err
	}
	u, err := k.dyn.Resource(disaggregatedSetGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get disaggregatedset %s: %w", ref, err)
	}
	return disaggregatedSetFrom(u), nil
}

func disaggregatedSetFrom(u *unstructured.Unstructured) *DisaggregatedSet {
	ds := &DisaggregatedSet{Generation: u.GetGeneration()}
	if spec, ok := u.Object["spec"].(map[string]any); ok {
		if n, ok := asInt(spec["slices"]); ok && n >= 0 {
			ds.Slices = &n
		}
	}
	st, _ := u.Object["status"].(map[string]any)
	if st == nil {
		return ds
	}
	if n, ok := asInt(st["observedGeneration"]); ok {
		ds.ObservedGeneration = int64(n)
	}
	roles, _ := st["roleStatuses"].([]any)
	for _, raw := range roles {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := RoleStatus{}
		role.Name, _ = m["name"].(string)
		role.Replicas, _ = asInt(m["replicas"])
		role.ReadyReplicas, _ = asInt(m["readyReplicas"])
		role.UpdatedReplicas, _ = asInt(m["updatedReplicas"])
		ds.Roles = append(ds.Roles, role)
	}
	return ds
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float64:
		if n != float64(int64(n)) {
			return 0, false
		}
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	default:
		return 0, false
	}
}
