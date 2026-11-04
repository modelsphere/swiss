package site

import (
	"strings"
	"testing"
)

const migrateHead = "# the site\nname: prod # this cluster\nmodel:\n  pathTemplate: /models/{{name}}\n"

// Missing or null gets the well-known resources, comments kept; the result is
// a profile Parse accepts.
func TestMigrateFillsAMissingGPUTable(t *testing.T) {
	for name, nodes := range map[string]string{
		"no nodes":   "",
		"null nodes": "nodes:\n",
		"missing":    "nodes:\n  gpusPerNode: 8 # a full node\n",
		"null":       "nodes:\n  gpusPerNode: 8 # a full node\n  gpus:\n",
	} {
		t.Run(name, func(t *testing.T) {
			out, changes, err := Migrate([]byte(migrateHead + nodes))
			if err != nil || len(changes) != 1 || !strings.Contains(changes[0], "nodes.gpus") {
				t.Fatalf("changes = %v, err = %v", changes, err)
			}
			for _, want := range []string{"# the site", "# this cluster", "pathTemplate: /models/{{name}}"} {
				if !strings.Contains(string(out), want) {
					t.Errorf("%q was lost:\n%s", want, out)
				}
			}
			if strings.Contains(nodes, "gpusPerNode") && !strings.Contains(string(out), "gpusPerNode: 8 # a full node") {
				t.Errorf("the rest of nodes was lost:\n%s", out)
			}
			p, err := Parse(out, "migrated")
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if gpus := p.Nodes.GPUTable(); len(gpus) != 5 || gpus.Default("ascend") != "huawei.com/Ascend910" {
				t.Errorf("want the five well-known resources, got %+v", gpus)
			}
		})
	}
}

// Anything present is the operator's, however wrong: left as written.
func TestMigrateLeavesAnyGPUTablePresent(t *testing.T) {
	for name, nodes := range map[string]string{
		"empty list": "nodes:\n  gpus: []\n",
		"empty map":  "nodes:\n  gpus: {}\n",
		"wrong type": "nodes:\n  gpus: nvidia\n",
		"bad nodes":  "nodes: [a]\n",
		"one":        "nodes:\n  gpus: [{vendor: nvidia, resource: nvidia.com/gpu, labelKey: k}]\n",
	} {
		raw := migrateHead + nodes
		out, changes, err := Migrate([]byte(raw))
		if err != nil || len(changes) != 0 || string(out) != raw {
			t.Errorf("%s: changes = %v, err = %v:\n%s", name, changes, err, out)
		}
	}
}

func TestTemplateCarriesTheWellKnownGPUs(t *testing.T) {
	p, err := Parse([]byte(DefaultYAML("")), "template")
	if err != nil {
		t.Fatal(err)
	}
	if gpus := p.Nodes.GPUTable(); len(gpus) != 5 || gpus.Default("nvidia") != "nvidia.com/gpu" {
		t.Errorf("want the five well-known resources, got %+v", gpus)
	}
	if _, changes, _ := Migrate([]byte(DefaultYAML(""))); len(changes) != 0 {
		t.Errorf("a new profile has nothing to migrate: %v", changes)
	}
}
