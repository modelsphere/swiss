package server

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/values"
)

// qwen3.6-35b-a3b's default variant lists NVIDIA-H100-80GB-HBM3 and NVIDIA-H800;
// liveProbe's one GPU node is a B300.
const (
	offCatalogOnCluster  = "NVIDIA-B300-SXM6-AC"
	offCatalogOffCluster = "NVIDIA-A100-SXM4-80GB"
	listedOffCluster     = "NVIDIA-H800"
)

func warningsOf(p map[string]any) []string {
	raw, _ := p["warnings"].([]any)
	out := make([]string, 0, len(raw))
	for _, w := range raw {
		out = append(out, w.(string))
	}
	return out
}

func TestOffCatalogGPUProductIsRefusedByDefault(t *testing.T) {
	srv, _ := deployServer(t, true)
	code, body := post(t, srv, "/api/plans", map[string]any{
		"model": "qwen3.6-35b-a3b", "serviceId": "r",
		"gpuProducts": []string{offCatalogOnCluster},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %v", code, body)
	}
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, offCatalogOnCluster) || !strings.Contains(msg, "forceGpuProducts") {
		t.Errorf("the refusal must name the product and the way past it: %q", msg)
	}
}

// Forcing keeps the variant: its GPU count, image and values are what the
// catalog published, and the plan says so in a warning.
func TestForcedGPUProductOnTheClusterIsAccepted(t *testing.T) {
	srv, _ := deployServer(t, true)
	_, plain := post(t, srv, "/api/plans", map[string]any{"model": "qwen3.6-35b-a3b", "serviceId": "r"})
	code, p := post(t, srv, "/api/plans", map[string]any{
		"model": "qwen3.6-35b-a3b", "serviceId": "r",
		"gpuProducts":      []string{offCatalogOnCluster},
		"forceGpuProducts": true,
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, p)
	}
	got := matchExpr(t, p)["values"].([]any)
	if len(got) != 1 || got[0] != offCatalogOnCluster {
		t.Fatalf("the forced product must be the affinity: %v", got)
	}
	ws := warningsOf(p)
	if len(ws) != 1 || !strings.Contains(ws[0], offCatalogOnCluster) {
		t.Fatalf("want one warning naming the forced product, got %v", ws)
	}
	if p["source"].(map[string]any)["variant"] != plain["source"].(map[string]any)["variant"] {
		t.Errorf("forcing must not change the variant: %v", p["source"])
	}
	for _, path := range []string{"model.gpus", "image"} {
		a, _ := values.Get(values.Tree(planValues(p)), path)
		b, _ := values.Get(values.Tree(planValues(plain)), path)
		if a == nil || !reflect.DeepEqual(a, b) {
			t.Errorf("%s moved when forcing: %v vs %v", path, a, b)
		}
	}
}

func TestForcedGPUProductNotOnTheClusterIsRefused(t *testing.T) {
	srv, _ := deployServer(t, true)
	code, body := post(t, srv, "/api/plans", map[string]any{
		"model": "qwen3.6-35b-a3b", "serviceId": "r",
		"gpuProducts":      []string{offCatalogOffCluster},
		"forceGpuProducts": true,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %v", code, body)
	}
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, offCatalogOffCluster) || !strings.Contains(msg, offCatalogOnCluster) {
		t.Errorf("the refusal must name the product and what the cluster has: %q", msg)
	}
}

// The product label and resource are the variant's vendor's, so a product that
// is only on another vendor's nodes could never be scheduled.
func TestForcedGPUProductOfAnotherVendorIsRefused(t *testing.T) {
	probe := liveProbe()
	probe.Nod = append(probe.Nod, cluster.Node{
		Name: "ascend-1", GPUProduct: "module-910b-8", GPUResource: "huawei.com/Ascend910", GPUs: 8, Schedulable: true,
	})
	srv, _ := deployServerWith(t, probe, true)
	code, body := post(t, srv, "/api/plans", map[string]any{
		"model": "qwen3.6-35b-a3b", "serviceId": "r",
		"gpuProducts":      []string{"module-910b-8"},
		"forceGpuProducts": true,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %v", code, body)
	}
}

// Forcing only widens: a listed product is still accepted without being on the
// cluster, and only the unlisted one warns.
func TestForceLeavesListedProductsAsTheyWere(t *testing.T) {
	srv, _ := deployServer(t, true)
	code, p := post(t, srv, "/api/plans", map[string]any{
		"model": "qwen3.6-35b-a3b", "serviceId": "r",
		"gpuProducts":      []string{listedOffCluster, offCatalogOnCluster},
		"forceGpuProducts": true,
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, p)
	}
	if got := matchExpr(t, p)["values"].([]any); len(got) != 2 {
		t.Fatalf("both products must be alternatives: %v", got)
	}
	if ws := warningsOf(p); len(ws) != 1 || strings.Contains(ws[0], "GPU product "+listedOffCluster) {
		t.Fatalf("only the unlisted product warns: %v", ws)
	}

	_, plain := post(t, srv, "/api/plans", map[string]any{
		"model": "qwen3.6-35b-a3b", "serviceId": "r",
		"gpuProducts":      []string{listedOffCluster},
		"forceGpuProducts": true,
	})
	if ws := warningsOf(plain); len(ws) != 0 {
		t.Errorf("a listed product needs no warning: %v", ws)
	}
}

// A variant listing no products takes any of its vendor's, as before.
func TestVariantListingNoProductsTakesAnyWithoutForce(t *testing.T) {
	srv, _ := deployServer(t, true)
	code, p := post(t, srv, "/api/plans", map[string]any{
		"model": "glm5.1", "serviceId": "r",
		"gpuProducts": []string{offCatalogOffCluster},
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, p)
	}
	if ws := warningsOf(p); len(ws) != 0 {
		t.Errorf("nothing was forced: %v", ws)
	}
}

func TestForcedGPUProductWithUnreadableNodesIsABadGateway(t *testing.T) {
	probe := liveProbe()
	probe.NodErr = errors.New("nodes is forbidden")
	srv, _ := deployServerWith(t, probe, true)
	code, body := post(t, srv, "/api/plans", map[string]any{
		"model": "qwen3.6-35b-a3b", "serviceId": "r",
		"gpuProducts":      []string{offCatalogOnCluster},
		"forceGpuProducts": true,
	})
	if code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502: %v", code, body)
	}
}

// The upgrade form sends gpuProducts with the rest of the form; force rides
// along with them.
func TestUpgradeCarriesForceWithTheProducts(t *testing.T) {
	probe := deployed(t, planRequest{Model: "qwen3.6-35b-a3b", Release: "r", ServiceID: "r"})
	srv, _ := deployServerWith(t, probe, true)
	form := map[string]any{
		"fromRelease": "r",
		"overrides":   map[string]any{"replicaCount": 1},
		"gpuProducts": []string{offCatalogOnCluster},
	}
	if code, body := post(t, srv, "/api/plans", form); code != http.StatusBadRequest {
		t.Fatalf("unforced upgrade: status %d, want 400: %v", code, body)
	}
	form["forceGpuProducts"] = true
	code, up := post(t, srv, "/api/plans", form)
	if code != 200 {
		t.Fatalf("status %d: %v", code, up)
	}
	if len(warningsOf(up)) != 1 {
		t.Errorf("the forced upgrade must warn: %v", up["warnings"])
	}
}

func TestGPUProductsEndpointMarksWhatTheCatalogSupports(t *testing.T) {
	srv := testServer(t, liveProbe())
	code, body := get(t, srv, "/api/catalog/qwen3.6-35b-a3b/gpu-products")
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	if body["label"] != "nvidia.com/gpu.product" || body["resource"] != "nvidia.com/gpu" {
		t.Errorf("unexpected vendor keys: %v", body)
	}
	byName := map[string]map[string]any{}
	for _, o := range body["products"].([]any) {
		m := o.(map[string]any)
		byName[m["product"].(string)] = m
	}
	want := map[string][2]bool{ // catalogSupported, onCluster
		"NVIDIA-H100-80GB-HBM3": {true, false},
		listedOffCluster:        {true, false},
		offCatalogOnCluster:     {false, true},
	}
	if len(byName) != len(want) {
		t.Fatalf("want %d products, got %v", len(want), byName)
	}
	for name, w := range want {
		o := byName[name]
		if o == nil || o["catalogSupported"] != w[0] || o["onCluster"] != w[1] {
			t.Errorf("%s: %v, want catalogSupported=%v onCluster=%v", name, o, w[0], w[1])
		}
	}
	if b := byName[offCatalogOnCluster]; b["nodes"] != float64(1) || b["gpus"] != float64(8) {
		t.Errorf("cluster counts: %v", b)
	}
}

func TestGPUProductsEndpointSurvivesAnUnreadableNodeList(t *testing.T) {
	probe := liveProbe()
	probe.NodErr = errors.New("nodes is forbidden")
	srv := testServer(t, probe)
	code, body := get(t, srv, "/api/catalog/qwen3.6-35b-a3b/gpu-products?variant=sglang-tp8-h100-baseline")
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	if body["inventoryError"] == nil || body["variant"] != "sglang-tp8-h100-baseline" {
		t.Errorf("want the variant named and the inventory error reported: %v", body)
	}
	if ps := body["products"].([]any); len(ps) != 1 {
		t.Errorf("the listed product must still be offered: %v", ps)
	}
}
