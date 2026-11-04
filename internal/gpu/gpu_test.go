package gpu

import (
	"slices"
	"testing"
)

func TestDefaultIsTheVendorsFirstEntry(t *testing.T) {
	gpus := Table{
		{Vendor: "ascend", Resource: "volcano.sh/Ascend910B3", LabelKey: "volcano.sh/npu-name"},
		{Vendor: "ascend", Resource: "huawei.com/Ascend910", LabelKey: "accelerator/huawei-ascend910"},
		{Vendor: "nvidia", Resource: "nvidia.com/gpu", LabelKey: "nvidia.com/gpu.product"},
	}
	for vendor, want := range map[string]string{
		"nvidia": "nvidia.com/gpu",
		"ascend": "volcano.sh/Ascend910B3",
		"amd":    "",
	} {
		if got := gpus.Default(vendor); got != want {
			t.Errorf("%s: %q, want %q", vendor, got, want)
		}
	}
	if got := Table(nil).Default("nvidia"); got != "" {
		t.Errorf("no table, no resource: %q", got)
	}
}

// The product name always names the product, whether or not values are set,
// so inventory and affinity agree on which nodes carry it.
func TestProductAndValuesAgree(t *testing.T) {
	g := GPU{LabelKey: "k", Products: []Product{{Product: "Ascend-910B3", Values: []string{"Ascend910B3"}}}}
	for raw, want := range map[string]string{
		"Ascend910B3":  "Ascend-910B3",
		"Ascend-910B3": "Ascend-910B3",
		"other":        "other",
		"":             "",
	} {
		if got := g.Product(raw); got != want {
			t.Errorf("Product(%q) = %q, want %q", raw, got, want)
		}
	}
	if got := g.Values("Ascend-910B3"); !slices.Equal(got, []string{"Ascend-910B3", "Ascend910B3"}) {
		t.Errorf("values must start with the product itself: %v", got)
	}
	if got := g.Values("unlisted"); !slices.Equal(got, []string{"unlisted"}) {
		t.Errorf("a product without values is matched by its name: %v", got)
	}
}
