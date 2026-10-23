package server

import (
	"testing"

	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/testutil/cases"
	"github.com/modelsphere/swiss/internal/values"
)

type nameCase struct {
	Name             string `json:"name"`
	Description      string `json:"description,omitempty"`
	Fn               string `json:"fn"`
	Release          string `json:"release"`
	ServiceID        string `json:"serviceId,omitempty"`
	FullnameOverride string `json:"fullnameOverride,omitempty"`
	Want             string `json:"want"`
}

func TestAutoscaleNames(t *testing.T) {
	cases.Run(t, "testdata/autoscale-names", func(t *testing.T, tc nameCase) {
		p := &plan.Plan{
			Release: plan.Release{Name: tc.Release, Namespace: "models"},
			Layers:  map[string]values.Tree{values.LayerForm: {}},
		}
		if tc.ServiceID != "" {
			p.Layers[values.LayerForm]["serviceId"] = tc.ServiceID
		}
		if tc.FullnameOverride != "" {
			p.Layers[values.LayerForm]["fullnameOverride"] = tc.FullnameOverride
		}
		var got string
		switch tc.Fn {
		case "fullname":
			got = fullnameOf(p, tc.Release)
		case "serviceId":
			got = serviceIDOf(p)
		case "autoscaleServiceId":
			got = autoscaleServiceID(p)
		default:
			t.Fatalf("fn %q", tc.Fn)
		}
		if got != tc.Want {
			t.Fatalf("%s = %q, want %q", tc.Fn, got, tc.Want)
		}
	})
}
