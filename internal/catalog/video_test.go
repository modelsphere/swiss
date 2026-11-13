package catalog

import (
	"strings"
	"testing"
)

func videoVariant() Variant {
	return Variant{
		ID:       "h3-h100",
		Engine:   EngineVideo,
		Chart:    Chart{Name: "minimax-h3", Version: ">=0.3.0"},
		Requires: Requires{GPUs: 4},
	}
}

func TestVideoVariantRunsOnItsOwnChart(t *testing.T) {
	if err := videoVariant().validate(); err != nil {
		t.Fatalf("a video variant on its own chart is valid, got %v", err)
	}
}

func TestVideoVariantRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Variant)
		want string
	}{
		{"engine chart", func(v *Variant) { v.Chart.Name = "sglang" }, "not the sglang engine chart"},
		{"no chart", func(v *Variant) { v.Chart.Name = "" }, "needs its own chart"},
		{"bad chart name", func(v *Variant) { v.Chart.Name = "MiniMax_H3" }, "needs its own chart"},
		{"lws", func(v *Variant) { v.Requires.Topology = TopologyLWS; v.Requires.Nodes = 2 }, "topology single-node"},
		{"pd", func(v *Variant) { v.Requires.Topology = "pd" }, "topology single-node"},
		{"nodes", func(v *Variant) { v.Requires.Nodes = 2 }, "no requires.nodes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := videoVariant()
			tc.edit(&v)
			err := v.validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error containing %q, got %v", tc.want, err)
			}
		})
	}
}

// The LLM engines keep their rule: a chart of their own name, and only that.
func TestLLMEngineStillNeedsItsChart(t *testing.T) {
	v := videoVariant()
	v.Engine = "sglang"
	if err := v.validate(); err == nil || !strings.Contains(err.Error(), "does not match engine") {
		t.Fatalf("got %v", err)
	}
}
