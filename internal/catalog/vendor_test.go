package catalog

import "testing"

func TestVendorDefaultsToNvidia(t *testing.T) {
	r := Requires{GPUs: 2}
	if r.VendorOrDefault() != VendorNvidia {
		t.Fatalf("vendor = %q", r.VendorOrDefault())
	}
	if r.ResourceName() != "nvidia.com/gpu" {
		t.Fatalf("nvidia mapping wrong: %s", r.ResourceName())
	}
}

func TestVendorSelectsTheResource(t *testing.T) {
	r := Requires{GPUs: 8, Vendor: "ascend"}
	if r.ResourceName() != "huawei.com/Ascend910" {
		t.Fatalf("ascend must not request nvidia.com/gpu, got %q", r.ResourceName())
	}
}

// An unknown vendor renders a pod requesting an empty resource name, which
// schedules and then runs on no accelerator at all.
func TestUnknownVendorIsRefused(t *testing.T) {
	e := Entry{
		APIVersion: APIVersion, Name: "m", Version: "1.0.0",
		Source: Source{HF: "org/m"},
		Variants: []Variant{{
			ID: "v", Engine: "sglang", Chart: Chart{Name: "sglang", Version: "0.8.0"},
			Requires: Requires{GPUs: 1, Vendor: "moore-threads"},
		}},
	}
	if err := e.Validate(); err == nil {
		t.Fatal("an unknown vendor must be refused")
	}
	e.Variants[0].Requires.Vendor = "ascend"
	if err := e.Validate(); err != nil {
		t.Fatalf("a known vendor must pass: %v", err)
	}
}

func TestMockEngineRunsOnAnEnginesChart(t *testing.T) {
	e := Entry{
		APIVersion: APIVersion, Name: "m", Version: "1.0.0",
		Source:   Source{HF: "org/m"},
		Variants: []Variant{{ID: "v", Engine: EngineMock, Requires: Requires{GPUs: 0}}},
	}
	for chart, ok := range map[string]bool{"vllm": true, "sglang": true, "mock": false, "": false} {
		e.Variants[0].Chart = Chart{Name: chart, Version: "0.8.0"}
		if err := e.Validate(); (err == nil) != ok {
			t.Errorf("mock on chart %q: err = %v, want ok=%v", chart, err, ok)
		}
	}
}

// gpus 0 is a CPU-only variant, such as a mock server; anything describing an
// accelerator beside it contradicts it.
func TestZeroGPUsIsCPUOnly(t *testing.T) {
	e := Entry{
		APIVersion: APIVersion, Name: "m", Version: "1.0.0",
		Source: Source{HF: "org/m"},
		Variants: []Variant{{
			ID: "v", Engine: "vllm", Chart: Chart{Name: "vllm", Version: "0.8.0"},
			Requires: Requires{GPUs: 0},
		}},
	}
	if err := e.Validate(); err != nil {
		t.Fatalf("gpus 0 must pass as CPU only: %v", err)
	}
	for name, r := range map[string]Requires{
		"vendor":          {Vendor: "ascend"},
		"gpuProduct":      {GPUProduct: []string{"NVIDIA-H100-80GB-HBM3"}},
		"minGpuMemoryGiB": {MinGPUMemoryGiB: 80},
		"rdma":            {RDMA: true},
		"negative":        {GPUs: -1},
	} {
		e.Variants[0].Requires = r
		if err := e.Validate(); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// source lives in metadata.yaml and reaches a consumer through the index. A
// version file carrying its own is a split that drifted, not an override.
func TestVersionFileMayNotCarrySource(t *testing.T) {
	_, err := ParseEntry([]byte(`
apiVersion: catalog.swiss/v1
name: m
version: 1.0.0
source: {hf: org/m}
variants:
  - id: v
    engine: sglang
    chart: {name: sglang, version: "0.8.0"}
    requires: {gpus: 2}
`))
	if err == nil {
		t.Fatal("source in a version file must be refused")
	}
}
