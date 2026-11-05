package server

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/gpu"
)

// nodeSelector ANDs its labels, so it cannot express "any of these products".
// One term with one In expression is the OR, and the label key comes from the
// variant's vendor -- which is why the form sends product names, not affinity
// it had to build itself.
func TestGPUProductsBecomeNodeAffinity(t *testing.T) {
	srv, _ := deployServer(t, true)
	code, p := post(t, srv, "/api/plans", map[string]any{
		"model": "qwen3.6-35b-a3b", "serviceId": "r",
		"gpuProducts": []string{"NVIDIA-H100-80GB-HBM3", "NVIDIA-H800"},
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, p)
	}
	if _, present := planValues(p)["nodeSelector"]; present {
		t.Error("nodeSelector must not be written any more")
	}

	expr := matchExpr(t, p)
	if expr["key"] != "nvidia.com/gpu.product" || expr["operator"] != "In" {
		t.Fatalf("unexpected expression: %v", expr)
	}
	got := expr["values"].([]any)
	if len(got) != 2 || got[0] != "NVIDIA-H100-80GB-HBM3" || got[1] != "NVIDIA-H800" {
		t.Fatalf("both products must be offered as alternatives: %v", got)
	}
}

// Set at the nodeSelectorTerms path, so a preferred rule beside it survives.
func TestGPUProductsKeepAPreferredAffinityRule(t *testing.T) {
	srv, _ := deployServer(t, true)
	_, p := post(t, srv, "/api/plans", map[string]any{
		"model": "qwen3.6-35b-a3b", "serviceId": "r",
		"gpuProducts": []string{"NVIDIA-H800"},
		"overrides": map[string]any{
			"affinity": map[string]any{
				"nodeAffinity": map[string]any{
					"preferredDuringSchedulingIgnoredDuringExecution": []any{
						map[string]any{"weight": 1},
					},
				},
			},
		},
	})
	na := planValues(p)["affinity"].(map[string]any)["nodeAffinity"].(map[string]any)
	if na["preferredDuringSchedulingIgnoredDuringExecution"] == nil {
		t.Fatalf("a preferred rule beside the required one was dropped: %v", na)
	}
	if na["requiredDuringSchedulingIgnoredDuringExecution"] == nil {
		t.Fatal("the required rule is missing")
	}
}

func matchExpr(t *testing.T, p map[string]any) map[string]any {
	t.Helper()
	na := planValues(p)["affinity"].(map[string]any)["nodeAffinity"].(map[string]any)
	req := na["requiredDuringSchedulingIgnoredDuringExecution"].(map[string]any)
	terms := req["nodeSelectorTerms"].([]any)
	if len(terms) != 1 {
		t.Fatalf("want one term, got %d", len(terms))
	}
	exprs := terms[0].(map[string]any)["matchExpressions"].([]any)
	if len(exprs) != 1 {
		t.Fatalf("want one expression, got %d", len(exprs))
	}
	return exprs[0].(map[string]any)
}

// The profile is where "what this cluster schedules GPU work on" belongs, and
// an explicit form value still wins.
func TestSchedulingDefaultsComeFromTheProfile(t *testing.T) {
	srv, _ := deployServer(t, true)
	_, p := post(t, srv, "/api/plans", map[string]any{
		"model": "qwen3.6-35b-a3b", "serviceId": "r",
	})
	vals := planValues(p)

	// testProfile carries no schedule block, so nothing is forced.
	if _, set := vals["schedulerName"]; set && layerOf(p, "schedulerName") != "site" {
		t.Errorf("an unset profile must not invent a scheduler: %v", vals["schedulerName"])
	}

	_, withForm := post(t, srv, "/api/plans", map[string]any{
		"model": "qwen3.6-35b-a3b", "serviceId": "r",
		"overrides": map[string]any{"schedulerName": "volcano"},
	})
	fv := planValues(withForm)
	if fv["schedulerName"] != "volcano" {
		t.Fatalf("the form must win: %v", fv["schedulerName"])
	}
	if got := layerOf(withForm, "schedulerName"); got != "form" {
		t.Errorf("an explicit value is the form's, whatever the profile said: %v", got)
	}
}

// nodeSelector, affinity and tolerations arrive as a YAML fragment, parsed on
// the server so there is one parser for it.
func TestSchedulingYAMLIsAccepted(t *testing.T) {
	srv, _ := deployServer(t, true)
	for _, frag := range []string{
		"tolerations:\n  - key: gpu\n    operator: Exists\n",
		"extraArgs: [--tp-size=8]\n", // the model entry set this too; the form wins
	} {
		code, body := post(t, srv, "/api/plans", map[string]any{
			"model": "qwen3.6-35b-a3b", "serviceId": "r", "overridesYAML": frag,
		})
		if code != 200 {
			t.Fatalf("%q: status %d: %v", frag, code, body)
		}
	}
}

// tolerations is a plain form-owned key, so the form sends the array straight
// through -- no vendor mapping, nothing for the server to translate.
func TestTolerationsFromTheFormLandInThePlan(t *testing.T) {
	srv, _ := deployServer(t, true)
	code, p := post(t, srv, "/api/plans", map[string]any{
		"model": "qwen3.6-35b-a3b", "serviceId": "r",
		"overrides": map[string]any{
			"tolerations": []any{
				map[string]any{"key": "gpu", "operator": "Exists", "effect": "NoSchedule"},
				map[string]any{"key": "tier", "operator": "Equal", "value": "spot"},
			},
		},
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, p)
	}
	tol, ok := planValues(p)["tolerations"].([]any)
	if !ok || len(tol) != 2 {
		t.Fatalf("both tolerations must survive: %v", planValues(p)["tolerations"])
	}
	first := tol[0].(map[string]any)
	if first["key"] != "gpu" || first["effect"] != "NoSchedule" {
		t.Errorf("unexpected toleration: %v", first)
	}
	if got := layerOf(p, "tolerations"); got != "form" {
		t.Errorf("tolerations are the form's: %v", got)
	}
}

// One term: the resource's labelKey in each requested product and its values.
func TestGPUProductTermsAddValuesToTheProduct(t *testing.T) {
	gpus := gpu.Table{{Vendor: "ascend", Resource: "huawei.com/Ascend910", LabelKey: "accelerator/huawei-ascend910", Products: []gpu.Product{
		{Product: "Ascend-910B3", Values: []string{"module-910b-8"}},
		{Product: "Ascend-910B4", Values: []string{"module-910b4-8"}},
		{Product: "unrequested", Values: []string{"x"}},
	}}}
	got, _ := json.Marshal(gpuProductTerms(gpus, "huawei.com/Ascend910", []string{"Ascend-910B3", "Ascend-910B4", "Ascend-910B3"}))
	want := `[{"matchExpressions":[{"key":"accelerator/huawei-ascend910","operator":"In","values":["Ascend-910B3","module-910b-8","Ascend-910B4","module-910b4-8"]}]}]`
	if string(got) != want {
		t.Fatalf("terms:\n got %s\nwant %s", got, want)
	}
	if terms := gpuProductTerms(gpus, "unknown.com/npu", []string{"A"}); terms != nil {
		t.Errorf("a resource the table does not list has no label to pin on: %v", terms)
	}
}

const gpusProfile = profileHead + "nodes:\n  gpus:\n" +
	"    - {vendor: nvidia, resource: nvidia.com/gpu, labelKey: nvidia.com/gpu.product, products: [{product: NVIDIA-H800, values: [h800]}]}\n" +
	ascendGPU

func TestProfileGPUValuesReachThePlan(t *testing.T) {
	probe := liveProbe()
	probe.Maps["swiss/site-profile"] = map[string]string{"profile.yaml": gpusProfile}
	srv, _ := deployServerWith(t, probe, true)
	code, p := post(t, srv, "/api/plans", map[string]any{
		"model": "qwen3.6-35b-a3b", "serviceId": "r",
		"gpuProducts": []string{"NVIDIA-H800"},
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, p)
	}
	expr := matchExpr(t, p)
	got := expr["values"].([]any)
	if expr["key"] != "nvidia.com/gpu.product" || len(got) != 2 || got[0] != "NVIDIA-H800" || got[1] != "h800" {
		t.Fatalf("want the product and its value under one key: %v", expr)
	}
}

// tableProbe records the GPU table each cluster-wide read is given.
type tableProbe struct {
	cluster.Fake
	got *[]gpu.Table
}

func (p tableProbe) Nodes(ctx context.Context, gpus gpu.Table) ([]cluster.Node, error) {
	*p.got = append(*p.got, gpus)
	return p.Fake.Nodes(ctx, gpus)
}

func (p tableProbe) GPUAllocations(ctx context.Context, gpus gpu.Table) (map[string][]cluster.GPUPod, error) {
	*p.got = append(*p.got, gpus)
	return p.Fake.GPUAllocations(ctx, gpus)
}

// The node view reads GPUs by the profile's table, and by none before there is
// a profile to read: nodes are listed, without GPUs.
func TestNodeViewReadsByTheProfilesGPUTable(t *testing.T) {
	for name, tc := range map[string]struct {
		profile string
		want    gpu.Table
	}{
		"profile": {profile: gpusProfile, want: gpu.Table{
			{Vendor: "nvidia", Resource: "nvidia.com/gpu", LabelKey: "nvidia.com/gpu.product", Products: []gpu.Product{{Product: "NVIDIA-H800", Values: []string{"h800"}}}},
			{Vendor: "ascend", Resource: "huawei.com/Ascend910", LabelKey: "accelerator/huawei-ascend910"},
		}},
		"no profile": {},
	} {
		t.Run(name, func(t *testing.T) {
			probe := gpuProbe()
			delete(probe.Maps, "swiss/site-profile")
			if tc.profile != "" {
				probe.Maps["swiss/site-profile"] = map[string]string{"profile.yaml": tc.profile}
			}
			var got []gpu.Table
			if code, body := get(t, testServer(t, tableProbe{Fake: probe, got: &got}), "/api/nodes"); code != 200 {
				t.Fatalf("status %d: %v", code, body)
			}
			if len(got) != 2 || !reflect.DeepEqual(got[0], tc.want) || !reflect.DeepEqual(got[1], tc.want) {
				t.Fatalf("nodes and usage must both read by\n%+v\ngot %+v", tc.want, got)
			}
		})
	}
}

// An unsaved nodes.gpus entry reads the nodes by itself, not by the saved
// table. Each product comes back with the nodes behind every name for it, its
// own products first and listed even when no node carries them.
func TestNodeGPUsReadsByTheDraftEntry(t *testing.T) {
	probe := gpuProbe()
	probe.Nod = []cluster.Node{
		{Name: "a", GPUResource: "volcano.sh/Ascend910B4-1", GPUProduct: "Ascend-910B4", GPUs: 8, Labels: map[string]string{"volcano.sh/npu-name": "Ascend910B4-1"}},
		{Name: "b", GPUResource: "volcano.sh/Ascend910B4-1", GPUProduct: "Ascend-910B4", GPUs: 8, Labels: map[string]string{"volcano.sh/npu-name": "Ascend910B4-1"}},
		{Name: "c", GPUResource: "volcano.sh/Ascend910B4-1", GPUProduct: "Ascend-910B2", GPUs: 8, Labels: map[string]string{"volcano.sh/npu-name": "Ascend-910B2"}},
		{Name: "d", GPUResource: "volcano.sh/Ascend910B4-1", GPUs: 4},
		{Name: "e", GPUResource: "nvidia.com/gpu", GPUProduct: "NVIDIA-H800", GPUs: 8},
	}
	draft := gpu.GPU{Vendor: "ascend", Resource: "volcano.sh/Ascend910B4-1", LabelKey: "volcano.sh/npu-name", Products: []gpu.Product{
		{Product: "Ascend-910B4", Values: []string{"Ascend910B4-1"}},
		{Product: "Ascend-910B3", Values: []string{"Ascend910B3"}},
	}}
	var got []gpu.Table
	srv := testServer(t, tableProbe{Fake: probe, got: &got})
	code, body := post(t, srv, "/api/nodes/gpus", draft)
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0], gpu.Table{draft}) {
		t.Fatalf("want the nodes read by the draft alone, got %+v", got)
	}
	name := func(n string, nodes, gpus float64) map[string]any {
		return map[string]any{"name": n, "nodes": nodes, "gpus": gpus}
	}
	want := map[string]any{
		"resource": "volcano.sh/Ascend910B4-1", "nodes": 4.0, "gpus": 28.0,
		"products": []any{
			map[string]any{"product": "Ascend-910B4", "nodes": 2.0, "gpus": 16.0, "names": []any{name("Ascend-910B4", 0, 0), name("Ascend910B4-1", 2, 16)}},
			map[string]any{"product": "Ascend-910B3", "nodes": 0.0, "gpus": 0.0, "names": []any{name("Ascend-910B3", 0, 0), name("Ascend910B3", 0, 0)}},
			map[string]any{"product": "Ascend-910B2", "nodes": 1.0, "gpus": 8.0, "names": []any{name("Ascend-910B2", 1, 8)}},
		},
		"unlabelled": map[string]any{"nodes": 1.0, "gpus": 4.0},
	}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("got %v\nwant %v", body, want)
	}
	if code, _ := post(t, srv, "/api/nodes/gpus", map[string]string{"resource": "nvidia.com/gpu"}); code != 200 {
		t.Fatalf("a resource alone: status %d, want 200", code)
	}
	if code, _ := post(t, srv, "/api/nodes/gpus", gpu.GPU{Vendor: "ascend"}); code != 400 {
		t.Fatalf("an entry without a resource: status %d, want 400", code)
	}
}
