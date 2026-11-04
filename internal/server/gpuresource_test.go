package server

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/config"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/values"
)

const volcano910B3 = "volcano.sh/Ascend910B3-1"

// ascendServer composes from the v2 fixture, whose vllm-tp2-910b is vendor
// ascend on Ascend-910B3, against nodes and product labels the test supplies.
func ascendServer(t *testing.T, nodes []cluster.Node, nodErr error, labels map[string]config.StringList) *Server {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "testdata", "catalog-v2"))
	if err != nil {
		t.Fatal(err)
	}
	probe := liveProbe()
	probe.Maps["swiss/site-profile"] = map[string]string{
		"profile.yaml": profileYAML + "catalogs:\n  - name: next\n    url: " + root + "\n    schemaVersion: v2\n",
	}
	probe.Nod, probe.NodErr = nodes, nodErr
	cfg := testConfig("prod-b300")
	cfg.Server.GPUProductLabels = labels
	return New(cfg, probe, discardLogger(), "test")
}

func ascendPlan(t *testing.T, s *Server, chartVersion string) (*plan.Plan, error) {
	t.Helper()
	return s.compose(context.Background(), planRequest{
		Model: "qwen3-8b", Variant: "vllm-tp2-910b", ServiceID: "q", ChartVersion: chartVersion,
	})
}

func requiredTerms(t *testing.T, p *plan.Plan) string {
	t.Helper()
	terms, ok := values.Get(p.Values(), "affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms")
	if !ok {
		t.Fatal("no required node affinity")
	}
	b, _ := json.Marshal(terms)
	return string(b)
}

// Inventory, not the vendor table, names the resource: a scheduler's own
// resource for the variant's product is what the pod requests, and the
// product affinity is keyed by it.
func TestGPUResourceFollowsInventory(t *testing.T) {
	s := ascendServer(t, []cluster.Node{
		{Name: "npu-1", GPUResource: volcano910B3, GPUProduct: "Ascend-910B3", GPUs: 8},
		{Name: "gpu-1", GPUResource: "nvidia.com/gpu", GPUProduct: "NVIDIA-H800", GPUs: 8},
	}, nil, map[string]config.StringList{volcano910B3: {"npu.example.com/product"}})
	p, err := ascendPlan(t, s, "0.9.1")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := values.Get(p.Values(), "model.gpuResource"); got != volcano910B3 {
		t.Fatalf("model.gpuResource = %v, want %s", got, volcano910B3)
	}
	if got := p.LayerOf("model.gpuResource"); got != values.LayerDerived {
		t.Errorf("model.gpuResource is %q's, want derived", got)
	}
	want := `[{"matchExpressions":[{"key":"npu.example.com/product","operator":"In","values":["Ascend-910B3"]}]}]`
	if got := requiredTerms(t, p); got != want {
		t.Errorf("terms:\n got %s\nwant %s", got, want)
	}
	if got := p.LayerOf("affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms"); got != values.LayerDerived {
		t.Errorf("catalog-product affinity is %q's, want derived", got)
	}
}

// No node carries the product, or the nodes are unreadable: the vendor's own
// resource, with a warning either way since the pods may not schedule.
func TestGPUResourceFallsBackToTheVendor(t *testing.T) {
	for name, tc := range map[string]struct {
		nodes   []cluster.Node
		err     error
		warning bool
	}{
		"no node has it": {nodes: []cluster.Node{{Name: "npu-1", GPUResource: volcano910B3, GPUProduct: "Ascend-910B4", GPUs: 8}}, warning: true},
		"unreadable":     {err: errors.New("nodes is forbidden"), warning: true},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := ascendPlan(t, ascendServer(t, tc.nodes, tc.err, nil), "0.9.1")
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := values.Get(p.Values(), "model.gpuResource"); got != "huawei.com/Ascend910" {
				t.Fatalf("model.gpuResource = %v, want the ascend default", got)
			}
			if got := len(p.Warnings) > 0; got != tc.warning {
				t.Errorf("warnings = %v", p.Warnings)
			}
			want := `[{"matchExpressions":[{"key":"accelerator/huawei-ascend910","operator":"In","values":["Ascend-910B3"]}]},` +
				`{"matchExpressions":[{"key":"accelerator","operator":"In","values":["Ascend-910B3"]}]}]`
			if got := requiredTerms(t, p); got != want {
				t.Errorf("terms:\n got %s\nwant %s", got, want)
			}
		})
	}
}

// A pod requests one resource, so products that split across two are refused
// rather than left for the scheduler to narrow.
func TestGPUResourceRefusesTwoResourcesForTheProducts(t *testing.T) {
	s := ascendServer(t, []cluster.Node{
		{Name: "npu-1", GPUResource: volcano910B3, GPUProduct: "Ascend-910B3", GPUs: 8},
		{Name: "npu-2", GPUResource: "huawei.com/Ascend910", GPUProduct: "Ascend-910B3", GPUs: 8},
	}, nil, map[string]config.StringList{volcano910B3: {"accelerator"}})
	_, err := ascendPlan(t, s, "0.9.1")
	if err == nil || !strings.Contains(err.Error(), volcano910B3) || !strings.Contains(err.Error(), "huawei.com/Ascend910") {
		t.Fatalf("want a refusal naming both resources, got %v", err)
	}
}

// A chart from before model.gpuResource rejects the key in its schema; the
// plan says which version is needed instead of failing at install.
func TestGPUResourceRefusesAChartWithoutIt(t *testing.T) {
	_, err := ascendPlan(t, ascendServer(t, nil, nil, nil), "0.7.6")
	if err == nil || !strings.Contains(err.Error(), "0.9.1") {
		t.Fatalf("want a refusal naming vllm 0.9.1, got %v", err)
	}
}

// requires.gpuProduct places a deploy without the form naming products. The
// nvidia default resource is left to the chart, so the values are as before.
func TestCatalogGPUProductBecomesDerivedAffinity(t *testing.T) {
	srv, _ := deployServer(t, true)
	code, p := post(t, srv, "/api/plans", map[string]any{"model": "qwen3.6-35b-a3b", "serviceId": "r"})
	if code != 200 {
		t.Fatalf("status %d: %v", code, p)
	}
	expr := matchExpr(t, p)
	got := expr["values"].([]any)
	if expr["key"] != "nvidia.com/gpu.product" || len(got) != 2 || got[0] != "NVIDIA-H100-80GB-HBM3" || got[1] != "NVIDIA-H800" {
		t.Fatalf("unexpected expression: %v", expr)
	}
	if l := layerOf(p, "affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms"); l != "derived" {
		t.Errorf("catalog-product affinity is %q's, want derived", l)
	}
	if _, set := planValues(p)["model"].(map[string]any)["gpuResource"]; set {
		t.Error("nvidia.com/gpu is the chart default and must not be written")
	}
	if ws := warningsOf(p); len(ws) != 0 {
		t.Errorf("warnings: %v", ws)
	}
}

func TestInventoryResource(t *testing.T) {
	ascend := catalog.Requires{GPUs: 1, Vendor: "ascend"}
	for name, tc := range map[string]struct {
		nodes []cluster.Node
		res   string
		found bool
	}{
		"site resource":    {nodes: []cluster.Node{{GPUResource: volcano910B3, GPUProduct: "A"}}, res: volcano910B3, found: true},
		"another vendor's": {nodes: []cluster.Node{{GPUResource: "nvidia.com/gpu", GPUProduct: "A"}}},
		"unrecorded":       {nodes: []cluster.Node{{GPUProduct: "A"}}, found: true},
		"other product":    {nodes: []cluster.Node{{GPUResource: volcano910B3, GPUProduct: "B"}}},
	} {
		t.Run(name, func(t *testing.T) {
			res, found, err := inventoryResource(tc.nodes, ascend, []string{"A"})
			if err != nil || res != tc.res || found != tc.found {
				t.Fatalf("got %q %v %v, want %q %v", res, found, err, tc.res, tc.found)
			}
		})
	}
}
