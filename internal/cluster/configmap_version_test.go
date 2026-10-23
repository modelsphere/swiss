package cluster

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestFakeConfigMapKeyVersion(t *testing.T) {
	f := &Fake{Maps: map[string]map[string]string{
		"models/cm": {"config.yaml": "enabled: true\n", "note": "keep"},
	}}
	ctx := context.Background()
	data, rv, err := f.ConfigMapWithVersion(ctx, "models/cm")
	if err != nil {
		t.Fatal(err)
	}
	if rv != "1" || data["note"] != "keep" {
		t.Fatalf("read %+v rv %s", data, rv)
	}
	if err := f.UpdateConfigMapKey(ctx, "models/cm", "status.yaml", "current: 1\n", rv); err != nil {
		t.Fatal(err)
	}
	got, rv2, err := f.ConfigMapWithVersion(ctx, "models/cm")
	if err != nil {
		t.Fatal(err)
	}
	if got["config.yaml"] != "enabled: true\n" || got["note"] != "keep" || got["status.yaml"] != "current: 1\n" {
		t.Fatalf("key write changed other keys: %+v", got)
	}
	if rv2 == rv {
		t.Fatalf("resourceVersion stayed %s", rv2)
	}
	if err := f.UpdateConfigMapKey(ctx, "models/cm", "status.yaml", "current: 2\n", rv); !apierrors.IsConflict(err) {
		t.Fatalf("stale resourceVersion: %v", err)
	}
	got, _, err = f.ConfigMapWithVersion(ctx, "models/cm")
	if err != nil {
		t.Fatal(err)
	}
	if got["status.yaml"] != "current: 1\n" {
		t.Fatalf("conflict wrote status: %+v", got)
	}
	if err := f.UpdateConfigMapKey(ctx, "models/missing", "status.yaml", "x\n", "1"); !apierrors.IsNotFound(err) {
		t.Fatalf("missing map: %v", err)
	}
	if _, ok := f.Maps["models/missing"]; ok {
		t.Fatal("UpdateConfigMapKey created a ConfigMap")
	}
	if _, _, err := f.ConfigMapWithVersion(ctx, "models/missing"); !apierrors.IsNotFound(err) {
		t.Fatalf("missing read: %v", err)
	}
}

func TestKubeUpdateConfigMapKey(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewSimpleClientset()
	if _, err := cs.CoreV1().ConfigMaps("models").Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "models", Name: "swiss-autoscale-pd"},
		Data:       map[string]string{"config.yaml": "enabled: true\n", "note": "keep"},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	k := NewKubeWithClient(cs)
	ref := "models/swiss-autoscale-pd"
	data, rv, err := k.ConfigMapWithVersion(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if data["config.yaml"] != "enabled: true\n" || data["note"] != "keep" {
		t.Fatalf("read %+v", data)
	}
	if err := k.UpdateConfigMapKey(ctx, ref, "status.yaml", "current: 1\n", "stale"); !apierrors.IsConflict(err) {
		t.Fatalf("stale resourceVersion %q: %v", rv, err)
	}
	still, err := cs.CoreV1().ConfigMaps("models").Get(ctx, "swiss-autoscale-pd", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := still.Data["status.yaml"]; ok {
		t.Fatalf("conflict wrote status: %+v", still.Data)
	}
	if err := k.UpdateConfigMapKey(ctx, ref, "status.yaml", "current: 1\n", rv); err != nil {
		t.Fatal(err)
	}
	got, err := cs.CoreV1().ConfigMaps("models").Get(ctx, "swiss-autoscale-pd", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Data["config.yaml"] != "enabled: true\n" || got.Data["note"] != "keep" || got.Data["status.yaml"] != "current: 1\n" {
		t.Fatalf("key write changed other keys: %+v", got.Data)
	}
	if err := k.UpdateConfigMapKey(ctx, "models/missing", "status.yaml", "x\n", rv); !apierrors.IsNotFound(err) {
		t.Fatalf("missing map: %v", err)
	}
	if _, err := cs.CoreV1().ConfigMaps("models").Get(ctx, "missing", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("created a ConfigMap: %v", err)
	}
}
