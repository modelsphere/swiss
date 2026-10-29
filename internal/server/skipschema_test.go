package server

import (
	"testing"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/values"
)

// An upgrade without the form keeps the release's flag, as it keeps its edits;
// one with the form is told by it. carryForward only matches the release's
// catalog by name, so no catalog index is read.
func TestUpgradeCarriesSkipSchemaValidation(t *testing.T) {
	const url = "/nowhere/index.json"
	meta := "source:\n  catalog: " + url + "\n  catalogName: public\n  model: m\n  variant: v\n" +
		"release:\n  name: r\n  namespace: ns\nskipSchemaValidation: true\n"
	probe := cluster.Fake{
		Maps: map[string]map[string]string{
			"swiss/site-profile": {"profile.yaml": profileYAML + "catalogs:\n  - name: public\n    url: " + url + "\n"},
		},
		Rel: []cluster.Release{{
			Name: "r", Namespace: "ns", Status: "deployed", Revision: 1,
			SwissFiles: map[string]string{plan.MetaFile: meta},
		}},
	}
	s := New(testConfig("c"), probe, discardLogger(), "test")

	kept, err := s.carryForward(t.Context(), planRequest{FromRelease: "r", Namespace: "ns"})
	if err != nil {
		t.Fatal(err)
	}
	if !kept.SkipSchemaValidation {
		t.Error("an upgrade sending no form must keep the release's flag")
	}

	form := values.Tree{"replicaCount": 1}
	off, err := s.carryForward(t.Context(), planRequest{FromRelease: "r", Namespace: "ns", Overrides: form})
	if err != nil {
		t.Fatal(err)
	}
	if off.SkipSchemaValidation {
		t.Error("a form that leaves it off must turn it off")
	}
	on, err := s.carryForward(t.Context(), planRequest{FromRelease: "r", Namespace: "ns", Overrides: form, SkipSchemaValidation: true})
	if err != nil {
		t.Fatal(err)
	}
	if !on.SkipSchemaValidation {
		t.Error("a form that turns it on must keep it on")
	}
}
