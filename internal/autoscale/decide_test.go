package autoscale

import (
	"testing"

	"github.com/modelsphere/swiss/internal/testutil/cases"
)

type decideCase struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Live        Live     `json:"live"`
	Decision    Decision `json:"decision"`
	WantHold    string   `json:"wantHold"`
	WantScale   bool     `json:"wantScale"`
	WantCurrent int      `json:"wantCurrent"`
	Want        int      `json:"want"`
	HasDecision bool     `json:"hasDecision"`
}

func TestDecide(t *testing.T) {
	cases.Run(t, "testdata/decide", func(t *testing.T, tc decideCase) {
		got := Decide(tc.Live, tc.Decision)
		if got.HoldReason != tc.WantHold {
			t.Errorf("hold %q, want %q", got.HoldReason, tc.WantHold)
		}
		if got.Scale != tc.WantScale {
			t.Errorf("scale %v, want %v", got.Scale, tc.WantScale)
		}
		if got.Current != tc.WantCurrent {
			t.Errorf("current %d, want %d", got.Current, tc.WantCurrent)
		}
		if got.Want != tc.Want {
			t.Errorf("want %d, want %d", got.Want, tc.Want)
		}
		if tc.HasDecision != (got.Decision != nil) {
			t.Errorf("decision present %v, want %v", got.Decision != nil, tc.HasDecision)
		}
		if got.Decision != nil && tc.Decision.OK && *got.Decision != tc.Decision.Replicas {
			t.Errorf("decision %d, want %d", *got.Decision, tc.Decision.Replicas)
		}
	})
}
