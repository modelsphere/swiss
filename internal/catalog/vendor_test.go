package catalog

import "testing"

func TestVendorDefaultsToNvidia(t *testing.T) {
	r := Requires{GPUs: 2}
	if r.VendorOrDefault() != VendorNvidia {
		t.Fatalf("vendor = %q", r.VendorOrDefault())
	}
	if r.ResourceName() != "nvidia.com/gpu" || r.ProductLabel() != "nvidia.com/gpu.product" {
		t.Fatalf("nvidia mapping wrong: %s / %s", r.ResourceName(), r.ProductLabel())
	}
}

func TestVendorSelectsTheResourceAndLabel(t *testing.T) {
	r := Requires{GPUs: 8, Vendor: "ascend"}
	if r.ResourceName() != "huawei.com/Ascend910" {
		t.Fatalf("ascend must not request nvidia.com/gpu, got %q", r.ResourceName())
	}
	if r.ProductLabel() == "nvidia.com/gpu.product" {
		t.Fatal("ascend products are not published under the nvidia label")
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
