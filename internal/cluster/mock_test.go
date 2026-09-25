package cluster

import (
	"context"
	"testing"
)

func TestMockGPUClusterInventory(t *testing.T) {
	k := NewMockGPUKube()
	nodes, err := k.Nodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Node{}
	for _, n := range nodes {
		byName[n.Name] = n
	}

	nvidia := byName["nvidia-b300"]
	if nvidia.GPUs != 8 || nvidia.GPUProduct != "NVIDIA-B300-SXM6-AC" || nvidia.GPUResource != "nvidia.com/gpu" || !nvidia.Ready {
		t.Fatalf("nvidia: %+v", nvidia)
	}

	affinity := byName["ascend-affinity"]
	if affinity.GPUs != 8 || affinity.GPUProduct != "module-910b-8" || affinity.GPUResource != "huawei.com/Ascend910" {
		t.Fatalf("ascend affinity key: %+v", affinity)
	}

	fleet := byName["ascend910b-207"]
	if fleet.GPUs != 8 || fleet.GPUProduct != "huawei-Ascend910" || fleet.GPUResource != "huawei.com/Ascend910" {
		t.Fatalf("ascend fleet fallback: %+v", fleet)
	}

	amd := byName["amd-mi300"]
	if amd.GPUs != 8 || amd.GPUProduct != "0x74a1" || amd.GPUResource != "amd.com/gpu" {
		t.Fatalf("amd: %+v", amd)
	}

	cpu := byName["cpu-1"]
	if cpu.GPUs != 0 || cpu.GPUProduct != "" || cpu.GPUResource != "" {
		t.Fatalf("cpu: %+v", cpu)
	}

	alloc, err := k.GPUAllocations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pods := alloc["nvidia-b300"]
	if len(pods) != 2 {
		t.Fatalf("want 2 pods on nvidia-b300, got %+v", pods)
	}
	used := 0
	for _, p := range pods {
		used += p.GPUs
	}
	if used != 6 {
		t.Fatalf("nvidia-b300 used=%d, want 6", used)
	}
	if len(alloc["ascend910b-207"]) != 0 {
		t.Fatalf("ascend node should be idle: %+v", alloc["ascend910b-207"])
	}
}
