package cluster

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

// MockGPUObjects is a multi-vendor GPU cluster for inventory e2e tests:
// NVIDIA product label, Ascend affinity key, Ascend accelerator fallback
// (shaped like ascend910b-207), AMD device-id, and a CPU node.
func MockGPUObjects() []runtime.Object {
	return []runtime.Object{
		mockNode("nvidia-b300", map[string]string{
			"nvidia.com/gpu.product": "NVIDIA-B300-SXM6-AC",
			"kubernetes.io/hostname": "nvidia-b300",
		}, corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("8")}),
		mockNode("ascend-affinity", map[string]string{
			"accelerator/huawei-ascend910": "module-910b-8",
			"kubernetes.io/hostname":       "ascend-affinity",
		}, corev1.ResourceList{"huawei.com/Ascend910": resource.MustParse("8")}),
		// Real-fleet shape: accelerator=huawei-Ascend910, no affinity key.
		mockNode("ascend910b-207", map[string]string{
			"accelerator":                      "huawei-Ascend910",
			"node.kubernetes.io/npu.chip.name": "910B3",
			"node.4pd.io/accelerator":          "ascend-910b",
			"servertype":                       "Ascend910B-20",
			"kubernetes.io/hostname":           "ascend910b-207",
			"huawei.com/driver.version":        "25.5.0",
			"mind-cluster/npu-chip-memory":     "64G",
			"workerselector":                   "dls-worker-node",
		}, corev1.ResourceList{"huawei.com/Ascend910": resource.MustParse("8")}),
		mockNode("amd-mi300", map[string]string{
			"amd.com/gpu.device-id":  "0x74a1",
			"kubernetes.io/hostname": "amd-mi300",
		}, corev1.ResourceList{"amd.com/gpu": resource.MustParse("8")}),
		mockNode("cpu-1", map[string]string{
			"kubernetes.io/hostname": "cpu-1",
		}, nil),
		mockGPUPod("modelforge", "glm-53-0", "nvidia-b300", "nvidia.com/gpu", 2),
		mockGPUPod("modelforge", "kimi-0", "nvidia-b300", "nvidia.com/gpu", 4),
	}
}

// NewMockGPUKube returns a Kube backed by MockGPUObjects.
func NewMockGPUKube() *Kube {
	return NewKubeWithClient(MockGPUClient())
}

// MockGPUClient returns the fake clientset for MockGPUObjects.
func MockGPUClient() kubernetes.Interface {
	return fake.NewSimpleClientset(MockGPUObjects()...)
}

func mockNode(name string, labels map[string]string, allocatable corev1.ResourceList) *corev1.Node {
	if allocatable == nil {
		allocatable = corev1.ResourceList{}
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Status: corev1.NodeStatus{
			Allocatable: allocatable,
			Conditions: []corev1.NodeCondition{{
				Type:   corev1.NodeReady,
				Status: corev1.ConditionTrue,
			}},
			NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.31.0"},
		},
	}
}

func mockGPUPod(ns, name, node, res string, gpus int) *corev1.Pod {
	qty := resource.MustParse(fmt.Sprintf("%d", gpus))
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: corev1.PodSpec{
			NodeName: node,
			Containers: []corev1.Container{{
				Name: "engine",
				Resources: corev1.ResourceRequirements{
					Limits:   corev1.ResourceList{corev1.ResourceName(res): qty},
					Requests: corev1.ResourceList{corev1.ResourceName(res): qty},
				},
			}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}
