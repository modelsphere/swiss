package server

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/modelsphere/swiss/internal/cluster"
)

func gpuProbe() cluster.Fake {
	p := liveProbe()
	p.Nod = []cluster.Node{
		{Name: "gpu-1", GPUProduct: "NVIDIA-B300-SXM6-AC", GPUs: 8, Schedulable: true, Ready: true},
		{Name: "gpu-2", GPUProduct: "NVIDIA-B300-SXM6-AC", GPUs: 8, Schedulable: true, Ready: true},
		{Name: "cpu-1", GPUs: 0, Schedulable: true, Ready: true},
	}
	p.Alloc = map[string][]cluster.GPUPod{
		"gpu-1": {
			{Namespace: "modelforge", Name: "glm-53-0", GPUs: 2},
			{Namespace: "modelforge", Name: "kimi-0", GPUs: 4},
		},
	}
	return p
}

func TestNodesReportGPUUsagePerNode(t *testing.T) {
	srv := testServer(t, gpuProbe())
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

	one := byName["gpu-1"]
	if one["gpusUsed"] != float64(6) || one["gpusFree"] != float64(2) {
		t.Fatalf("gpu-1 usage wrong: used=%v free=%v", one["gpusUsed"], one["gpusFree"])
	}
	if n := len(one["gpuPods"].([]any)); n != 2 {
		t.Errorf("the pods holding the GPUs should be listed, got %d", n)
	}

	// A node nothing is scheduled on is measured at zero, not left unknown.
	if two := byName["gpu-2"]; two["gpusUsed"] != float64(0) || two["gpusFree"] != float64(8) {
		t.Errorf("an idle node is 0 used, not unknown: %v", two)
	}

	sum := body["summary"].(map[string]any)
	if sum["gpus"] != float64(16) || sum["gpusUsed"] != float64(6) {
		t.Errorf("summary wrong: %v", sum)
	}
}

// A cluster may withhold the cluster-wide pod list. Usage is then absent rather
// than zero: an undercount presented as a fact is worse than saying "unknown".
func TestUsageIsOmittedWhenThePodListIsRefused(t *testing.T) {
	probe := gpuProbe()
	probe.AllocErr = errors.New("pods is forbidden: cannot list at the cluster scope")
	srv := testServer(t, probe)

	code, body := get(t, srv, "/api/nodes")
	if code != 200 {
		t.Fatalf("nodes must still be served without usage, got %d", code)
	}
	if body["usageError"] == nil {
		t.Fatal("the refusal must be reported, not swallowed")
	}
	for _, n := range body["nodes"].([]any) {
		m := n.(map[string]any)
		if _, present := m["gpusUsed"]; present {
			t.Fatalf("unknown usage must be absent, not zero: %v", m)
		}
		if m["GPUs"] == nil {
			t.Error("capacity is still known and should still be shown")
		}
	}
}

// Extended resources cannot be over-committed, but a node draining or a stale
// read can still put used above allocatable. Free must not go negative.
func TestFreeNeverGoesNegative(t *testing.T) {
	probe := gpuProbe()
	probe.Nod = []cluster.Node{{Name: "gpu-1", GPUs: 2, Schedulable: true, Ready: true}}
	probe.Alloc = map[string][]cluster.GPUPod{
		"gpu-1": {{Namespace: "ns", Name: "big", GPUs: 8}},
	}
	srv := testServer(t, probe)

	_, body := get(t, srv, "/api/nodes")
	n := body["nodes"].([]any)[0].(map[string]any)
	if n["gpusFree"] != float64(0) {
		t.Fatalf("free should clamp at 0, got %v", n["gpusFree"])
	}
}

func TestNodesEndpointFailsLoudlyWhenNodesCannotBeListed(t *testing.T) {
	srv := testServer(t, failingNodes{cluster.Fake{}})
	if code, _ := get(t, srv, "/api/nodes"); code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", code)
	}
}

type failingNodes struct{ cluster.Fake }

func (failingNodes) Nodes(context.Context) ([]cluster.Node, error) {
	return nil, errors.New("nodes is forbidden")
}
