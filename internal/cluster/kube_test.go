package cluster

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// helmSecret builds a release Secret the way helm's storage driver does:
// base64(gzip(json)), labelled owner=helm, named <release>.v<revision>.
func helmSecret(ns, name string, rev int, status, chart, version string) *corev1.Secret {
	body := fmt.Sprintf(`{"name":%q,"namespace":%q,"version":%d,
	  "info":{"status":%q,"last_deployed":"2026-09-20T10:00:00Z"},
	  "chart":{"metadata":{"name":%q,"version":%q}}}`, name, ns, rev, status, chart, version)
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = w.Write([]byte(body))
	_ = w.Close()
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      fmt.Sprintf("sh.helm.release.v1.%s.v%d", name, rev),
			Labels:    map[string]string{"owner": "helm", "name": name},
		},
		Data: map[string][]byte{"release": []byte(base64.StdEncoding.EncodeToString(gz.Bytes()))},
	}
}

func TestReleasesDecodesHelmStorage(t *testing.T) {
	cs := fake.NewSimpleClientset(
		helmSecret("modelforge", "glm-53", 1, "superseded", "sglang", "0.7.0"),
		helmSecret("modelforge", "glm-53", 4, "deployed", "sglang", "0.8.0"),
		helmSecret("kimi", "kimi-k25", 2, "pending-upgrade", "sglang", "0.8.0"),
	)
	got, err := NewKubeWithClient(cs).Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want the latest revision of each release, got %d: %+v", len(got), got)
	}
	// Sorted by namespace then name.
	if got[0].Namespace != "kimi" || got[0].Status != "pending-upgrade" {
		t.Errorf("unexpected first release: %+v", got[0])
	}
	glm := got[1]
	if glm.Revision != 4 || glm.Chart != "sglang-0.8.0" || glm.Status != "deployed" {
		t.Errorf("want revision 4 of sglang-0.8.0, got %+v", glm)
	}
	if glm.Updated.IsZero() {
		t.Error("last_deployed was not parsed")
	}
}

// A release Swiss did not deploy has no plan ConfigMap. That absence is the
// "live but untracked" signal, so it must not be an error.
func TestReleasesMarksSwissManagedOnes(t *testing.T) {
	cs := fake.NewSimpleClientset(
		helmSecret("modelforge", "glm-53", 1, "deployed", "sglang", "0.8.0"),
		helmSecret("modelforge", "by-hand", 1, "deployed", "sglang", "0.8.0"),
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: "modelforge", Name: "swiss-plan-glm-53"},
			Data:       map[string]string{"plan.yaml": "release:\n  name: glm-53\n"},
		},
	)
	got, err := NewKubeWithClient(cs).Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Release{}
	for _, r := range got {
		byName[r.Name] = r
	}
	if byName["glm-53"].SwissPlan == nil {
		t.Error("glm-53 has a plan ConfigMap and should carry it")
	}
	if byName["by-hand"].SwissPlan != nil {
		t.Error("a hand-installed release must come back untracked, not fail")
	}
}

func TestOneUnreadableReleaseDoesNotHideTheRest(t *testing.T) {
	broken := helmSecret("ns", "broken", 1, "deployed", "sglang", "0.8.0")
	broken.Data["release"] = []byte("!!! not base64 gzip json")
	cs := fake.NewSimpleClientset(broken, helmSecret("ns", "fine", 1, "deployed", "sglang", "0.8.0"))
	got, err := NewKubeWithClient(cs).Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "fine" {
		t.Fatalf("want just the readable release, got %+v", got)
	}
}

func TestNodesReadGPUFacts(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "gpu-1",
			Labels: map[string]string{"nvidia.com/gpu.product": "NVIDIA-B300-SXM6-AC"},
		},
		Spec: corev1.NodeSpec{
			Unschedulable: true,
			Taints:        []corev1.Taint{{Key: "gpu", Value: "true", Effect: corev1.TaintEffectNoSchedule}},
		},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("8")},
		},
	})
	got, err := NewKubeWithClient(cs).Nodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	n := got[0]
	if n.GPUs != 8 || n.GPUProduct != "NVIDIA-B300-SXM6-AC" || n.Schedulable {
		t.Fatalf("unexpected node: %+v", n)
	}
	if len(n.Taints) != 1 || n.Taints[0] != "gpu=true:NoSchedule" {
		t.Fatalf("taints not read: %+v", n.Taints)
	}
}

func TestConfigMapKeysForRouteCollision(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "llm-route", Name: "openresty-conf"},
		Data:       map[string]string{"modelforge-0.1": "...", "kimi-k2.5": "..."},
	})
	keys, err := ConfigMapKeys(context.Background(), NewKubeWithClient(cs), "llm-route/openresty-conf")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != "kimi-k2.5" {
		t.Fatalf("want sorted route keys, got %v", keys)
	}
	if _, err := ConfigMapKeys(context.Background(), NewKubeWithClient(cs), "no-slash"); err == nil {
		t.Error("a reference without a namespace must be refused")
	}
}

// rbac.scope: namespaced grants Roles, which cannot authorise a cluster-wide
// list. Listing each namespace in turn is the only thing those Roles permit --
// getting this wrong is a 403, not a narrower view.
func TestScopedProbeListsPerNamespace(t *testing.T) {
	cs := fake.NewSimpleClientset(
		helmSecret("modelforge", "glm-53", 1, "deployed", "sglang", "0.8.0"),
		helmSecret("kimi", "kimi-k25", 1, "deployed", "sglang", "0.8.0"),
		helmSecret("other", "not-ours", 1, "deployed", "sglang", "0.8.0"),
	)
	k := NewKubeWithClient(cs)
	k.Namespaces = []string{"modelforge", "kimi"}

	got, err := k.Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want only the listed namespaces, got %d: %+v", len(got), got)
	}
	for _, r := range got {
		if r.Namespace == "other" {
			t.Errorf("read outside the granted namespaces: %+v", r)
		}
	}

	// Empty means cluster-wide, which is what a ClusterRole authorises.
	k.Namespaces = nil
	got, err = k.Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("cluster-wide should see all three, got %d", len(got))
	}
}
