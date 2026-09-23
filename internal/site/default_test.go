package site

import (
	"strings"
	"testing"
)

// The setup form is pre-filled with this, and a default that does not parse
// would greet a first install with an error it did not cause.
func TestDefaultProfileParses(t *testing.T) {
	p, err := Parse([]byte(DefaultYAML("prod-b300")), "default")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "prod-b300" {
		t.Errorf("name = %q, want the cluster it was rendered for", p.Name)
	}
	// The template's own placeholders survive: they are expanded per model at
	// compose time, not here.
	if !strings.Contains(p.Model.PathTemplate, "{{name}}") {
		t.Errorf("pathTemplate lost its placeholder: %q", p.Model.PathTemplate)
	}
}

func TestDefaultProfileStillParsesWithoutAClusterName(t *testing.T) {
	if _, err := Parse([]byte(DefaultYAML("")), "default"); err != nil {
		t.Fatal(err)
	}
}
