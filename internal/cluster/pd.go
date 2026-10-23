package cluster

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
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

func (k *Kube) ConfigMapList(ctx context.Context, namespace, selector string) ([]ConfigMapObj, error) {
	if namespace == "" {
		namespace = metav1.NamespaceAll
	}
	list, err := k.client.CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("list configmaps: %w", err)
	}
	out := make([]ConfigMapObj, 0, len(list.Items))
	for i := range list.Items {
		cm := &list.Items[i]
		out = append(out, ConfigMapObj{
			Namespace: cm.Namespace,
			Name:      cm.Name,
			Data:      cloneStringMap(cm.Data),
			Labels:    cloneStringMap(cm.Labels),
		})
	}
	return out, nil
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

func (k *Kube) PutLabeledConfigMap(ctx context.Context, ref string, data, labels map[string]string) error {
	ns, name, err := SplitRef(ref)
	if err != nil {
		return err
	}
	lbs := map[string]string{"app.kubernetes.io/managed-by": "swiss"}
	for key, v := range labels {
		lbs[key] = v
	}
	copied := cloneStringMap(data)
	existing, err := k.client.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = k.client.CoreV1().ConfigMaps(ns).Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: lbs},
			Data:       copied,
		}, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	existing.Data = copied
	existing.Labels = lbs
	_, err = k.client.CoreV1().ConfigMaps(ns).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}
