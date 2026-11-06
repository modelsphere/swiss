package server

import (
	"testing"

	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/testutil/cases"
	"github.com/modelsphere/swiss/internal/values"
)

// nameCase expects the name fn returns.
type nameCase struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Input       nameInput `json:"input"`
	Expected    string    `json:"expected"`
}

type nameInput struct {
	Fn               string `json:"fn"`
	Release          string `json:"release"`
	ServiceID        string `json:"serviceId,omitempty"`
	FullnameOverride string `json:"fullnameOverride,omitempty"`
}

func TestAutoscaleNames(t *testing.T) {
	cases.Run(t, "testdata/autoscale-names", func(t *testing.T, tc nameCase) {
		p := &plan.Plan{
			Release: plan.Release{Name: tc.Input.Release, Namespace: "models"},
			Layers:  map[string]values.Tree{values.LayerForm: {}},
		}
		if tc.Input.ServiceID != "" {
			p.Layers[values.LayerForm]["serviceId"] = tc.Input.ServiceID
		}
		if tc.Input.FullnameOverride != "" {
			p.Layers[values.LayerForm]["fullnameOverride"] = tc.Input.FullnameOverride
		}
		var got string
		switch tc.Input.Fn {
		case "fullname":
			got = fullnameOf(p, tc.Input.Release)
		case "serviceId":
			got = serviceIDOf(p)
		case "autoscaleServiceId":
			got = autoscaleServiceID(p)
		default:
			t.Fatalf("fn %q", tc.Input.Fn)
		}
		if got != tc.Expected {
			t.Fatalf("%s = %q, want %q", tc.Input.Fn, got, tc.Expected)
		}
	})
}
