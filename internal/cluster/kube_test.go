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

// The same storage decode as before, now reached one release at a time: helm
// labels each of its secrets with the release name, so this asks for one
// release's revisions rather than every release in scope.
func TestReleaseDecodesHelmStorage(t *testing.T) {
	cs := fake.NewSimpleClientset(
		helmSecret("modelforge", "glm-53", 1, "superseded", "sglang", "0.7.0"),
		helmSecret("modelforge", "glm-53", 4, "deployed", "sglang", "0.8.0"),
		helmSecret("kimi", "kimi-k25", 2, "pending-upgrade", "sglang", "0.8.0"),
	)
	k := NewKubeWithClient(cs)

	glm, err := k.Release(context.Background(), "modelforge", "glm-53")
	if err != nil {
		t.Fatal(err)
	}
	if glm.Revision != 4 || glm.Chart != "sglang-0.8.0" || glm.Status != "deployed" {
		t.Errorf("want revision 4 of sglang-0.8.0, got %+v", glm)
	}
	if glm.Updated.IsZero() {
		t.Error("last_deployed was not parsed")
	}

	// A release in another namespace is not reachable by name alone, which is
	// the point: two namespaces may hold the same release name.
	other, err := k.Release(context.Background(), "modelforge", "kimi-k25")
	if err != nil {
		t.Fatal(err)
	}
	if other != nil {
		t.Errorf("kimi-k25 is in another namespace: %+v", other)
	}
}

// A release swiss did not deploy has no plan ConfigMap. That absence must not be
// an error: it is what makes a name collision a conflict rather than an adoption.
func TestReleaseWithoutAPlanIsStillLive(t *testing.T) {
	cs := fake.NewSimpleClientset(
		helmSecret("modelforge", "glm-53", 1, "deployed", "sglang", "0.8.0"),
		helmSecret("modelforge", "by-hand", 1, "deployed", "sglang", "0.8.0"),
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: "modelforge", Name: "swiss-plan-glm-53"},
			Data:       map[string]string{"plan.yaml": "release:\n  name: glm-53\n"},
		},
	)
	k := NewKubeWithClient(cs)

	glm, err := k.Release(context.Background(), "modelforge", "glm-53")
	if err != nil {
		t.Fatal(err)
	}
	if glm == nil || glm.SwissFiles == nil {
		t.Error("glm-53 has a plan ConfigMap and should carry it")
	}

	hand, err := k.Release(context.Background(), "modelforge", "by-hand")
	if err != nil {
		t.Fatal(err)
	}
	if hand == nil || !hand.Updated.IsZero() && false {
		t.Fatalf("a hand-installed release must come back live: %+v", hand)
	}
	if hand.SwissFiles != nil {
		t.Errorf("it has no plan beside it: %+v", hand.SwissFiles)
	}
	if hand.Status != "deployed" {
		t.Errorf("its live state must still be read: %+v", hand)
	}
}

func TestNoReleaseAndNoPlanIsNil(t *testing.T) {
	rel, err := NewKubeWithClient(fake.NewSimpleClientset()).
		Release(context.Background(), "modelforge", "nothing-here")
	if err != nil {
		t.Fatal(err)
	}
	if rel != nil {
		t.Errorf("neither half exists, want nil, got %+v", rel)
	}
}

// An undecodable payload still means the release is there. Reporting it as
// absent would make install offer to create a release that already exists.
func TestAnUnreadableReleaseIsStillReported(t *testing.T) {
	broken := helmSecret("ns", "broken", 1, "deployed", "sglang", "0.8.0")
	broken.Data["release"] = []byte("!!! not base64 gzip json")
	cs := fake.NewSimpleClientset(broken, helmSecret("ns", "fine", 1, "deployed", "sglang", "0.8.0"))

	got, err := NewKubeWithClient(cs).Release(context.Background(), "ns", "broken")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Name != "broken" {
		t.Fatalf("want the release reported despite the payload, got %+v", got)
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
			Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: "10.0.0.8"},
				{Type: corev1.NodeExternalIP, Address: "203.0.113.8"},
			},
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionFalse, Reason: "KubeletNotReady", Message: "node is shutting down"},
				{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue, Reason: "KubeletHasInsufficientMemory"},
			},
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
	if n.Ready || len(n.Conditions) != 2 || n.Conditions[0].Type != "Ready" || n.Conditions[0].Status != "False" || n.Conditions[0].Reason != "KubeletNotReady" {
		t.Fatalf("conditions not read: ready=%v %+v", n.Ready, n.Conditions)
	}
	if n.Conditions[1].Type != "MemoryPressure" || n.Conditions[1].Status != "True" {
		t.Fatalf("pressure condition: %+v", n.Conditions[1])
	}
	if n.InternalIP != "10.0.0.8" || n.ExternalIP != "203.0.113.8" {
		t.Fatalf("addresses: internal=%q external=%q", n.InternalIP, n.ExternalIP)
	}
}

func TestNodesReadAscendAndOtherAccelerators(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "ascend-1",
				Labels: map[string]string{"accelerator/huawei-ascend910": "module-910b-8"},
			},
			Status: corev1.NodeStatus{
				Allocatable: corev1.ResourceList{"huawei.com/Ascend910": resource.MustParse("8")},
			},
		},
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "ascend-accelerator-label",
				Labels: map[string]string{"accelerator": "huawei-Ascend910"},
			},
			Status: corev1.NodeStatus{
				Allocatable: corev1.ResourceList{"huawei.com/Ascend910": resource.MustParse("8")},
			},
		},
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "ascend-unlabelled"},
			Status: corev1.NodeStatus{
				Allocatable: corev1.ResourceList{"huawei.com/Ascend910": resource.MustParse("8")},
			},
		},
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "custom-1",
				Labels: map[string]string{"custom.com/npu.sku": "NPU-Pro-100"},
			},
			Status: corev1.NodeStatus{
				Allocatable: corev1.ResourceList{"custom.com/npu": resource.MustParse("4")},
			},
		},
	)
	k := NewKubeWithClient(cs)
	k.GPUProductLabels = map[string][]string{"custom.com/npu": {"custom.com/npu.sku"}}
	got, err := k.Nodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Node{}
	for _, n := range got {
		byName[n.Name] = n
	}
	ascend := byName["ascend-1"]
	if ascend.GPUs != 8 || ascend.GPUProduct != "module-910b-8" || ascend.GPUResource != "huawei.com/Ascend910" {
		t.Fatalf("unexpected ascend node: %+v", ascend)
	}
	ascendAccel := byName["ascend-accelerator-label"]
	if ascendAccel.GPUs != 8 || ascendAccel.GPUProduct != "huawei-Ascend910" || ascendAccel.GPUResource != "huawei.com/Ascend910" {
		t.Fatalf("unexpected ascend accelerator-label node: %+v", ascendAccel)
	}
	ascendUnlabelled := byName["ascend-unlabelled"]
	if ascendUnlabelled.GPUs != 8 || ascendUnlabelled.GPUProduct != "" || ascendUnlabelled.GPUResource != "huawei.com/Ascend910" {
		t.Fatalf("unexpected ascend unlabelled node: %+v", ascendUnlabelled)
	}
	custom := byName["custom-1"]
	if custom.GPUs != 4 || custom.GPUProduct != "NPU-Pro-100" || custom.GPUResource != "custom.com/npu" {
		t.Fatalf("unexpected custom node: %+v", custom)
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
	md := metaClient(
		planMeta("modelforge", PlanConfigMapPrefix+"glm-53"),
		planMeta("kimi", PlanConfigMapPrefix+"kimi-k25"),
		planMeta("other", PlanConfigMapPrefix+"not-ours"),
	)
	k := NewKubeWithClients(fake.NewSimpleClientset(), md)
	k.Namespaces = []string{"modelforge", "kimi"}

	got, err := k.ManagedRefs(context.Background())
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
	got, err = k.ManagedRefs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("cluster-wide should see all three, got %d", len(got))
	}
}
