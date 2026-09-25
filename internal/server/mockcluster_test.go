package server

import (
	"testing"

	"github.com/modelsphere/swiss/internal/cluster"
)

func TestMockGPUClusterServesNodesAPI(t *testing.T) {
	srv := testServer(t, cluster.NewMockGPUKube())
	code, body := get(t, srv, "/api/nodes")
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}

	nodes := body["nodes"].([]any)
	byName := map[string]map[string]any{}
	for _, n := range nodes {
		m := n.(map[string]any)
		byName[m["Name"].(string)] = m
	}

	nvidia := byName["nvidia-b300"]
	if nvidia["GPUProduct"] != "NVIDIA-B300-SXM6-AC" || nvidia["GPUs"] != float64(8) {
		t.Fatalf("nvidia api: %v", nvidia)
	}
	if nvidia["gpusUsed"] != float64(6) || nvidia["gpusFree"] != float64(2) {
		t.Fatalf("nvidia usage: used=%v free=%v", nvidia["gpusUsed"], nvidia["gpusFree"])
	}

	fleet := byName["ascend910b-207"]
	if fleet["GPUProduct"] != "huawei-Ascend910" || fleet["GPUResource"] != "huawei.com/Ascend910" {
		t.Fatalf("ascend fleet api: %v", fleet)
	}
	if fleet["gpusUsed"] != float64(0) || fleet["gpusFree"] != float64(8) {
		t.Fatalf("ascend usage: %v", fleet)
	}

	affinity := byName["ascend-affinity"]
	if affinity["GPUProduct"] != "module-910b-8" {
		t.Fatalf("ascend affinity api: %v", affinity)
	}

	amd := byName["amd-mi300"]
	if amd["GPUProduct"] != "0x74a1" || amd["GPUResource"] != "amd.com/gpu" {
		t.Fatalf("amd api: %v", amd)
	}

	sum := body["summary"].(map[string]any)
	// 8+8+8+8 = 32 GPU capacity across four accelerator nodes
	if sum["gpus"] != float64(32) || sum["gpusUsed"] != float64(6) {
		t.Fatalf("summary: %v", sum)
	}
}
