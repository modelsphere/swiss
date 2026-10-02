package catalog

import "testing"

// chart.version is one version or a range; anything else cannot be resolved.
func TestChartVersionIsAVersionOrARange(t *testing.T) {
	e := Entry{
		APIVersion: APIVersion, Name: "m", Version: "1.0.0",
		Source: Source{HF: "org/m"},
		Variants: []Variant{{
			ID: "v", Engine: "sglang", Chart: Chart{Name: "sglang"},
			Requires: Requires{GPUs: 1},
		}},
	}
	for _, v := range []string{"0.7.1", ">=0.7.1", "^0.7.1", ">=0.7.1 <0.9.0"} {
		e.Variants[0].Chart.Version = v
		if err := e.Validate(); err != nil {
			t.Errorf("%q: %v", v, err)
		}
	}
	for _, v := range []string{"", "latest"} {
		e.Variants[0].Chart.Version = v
		if err := e.Validate(); err == nil {
			t.Errorf("%q must be refused", v)
		}
	}
}
