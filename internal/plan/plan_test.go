package plan

import (
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/values"
)

func testPlan() *Plan {
	return &Plan{
		APIVersion: APIVersion,
		Release:    Release{Name: "glm-53", Namespace: "models"},
		Chart:      ChartRef{Name: "sglang", Version: "0.8.0"},
		Layers:     map[string]values.Tree{"form": {"replicaCount": 2}},
	}
}

func TestHelmfileIsNotInTheHash(t *testing.T) {
	a, b := testPlan(), testPlan()
	a.Chart.Repo = "oci://ghcr.io/modelsphere/charts"
	b.Chart.Repo = "oci://ghcr.io/modelsphere/charts"

	if err := a.ComputeHash(); err != nil {
		t.Fatal(err)
	}
	doc, err := b.RenderHelmfile("")
	if err != nil {
		t.Fatal(err)
	}
	b.Helmfile = doc
	if err := b.ComputeHash(); err != nil {
		t.Fatal(err)
	}
	if a.Hash != b.Hash {
		t.Fatal("the rendered declaration explains the values; it must not change the hash")
	}
}

// The catalog's name labels the location beside it and renders nothing, and a
// swissd older than the field drops it on read -- so it must not move the
// hash, or that swissd would refuse the plan as edited.
func TestCatalogNameIsNotInTheHash(t *testing.T) {
	a, b := testPlan(), testPlan()
	a.Source.Catalog = "https://models.example.com/swiss-catalog/index.json"
	b.Source = a.Source
	b.Source.CatalogName = "public"
	if err := a.ComputeHash(); err != nil {
		t.Fatal(err)
	}
	if err := b.ComputeHash(); err != nil {
		t.Fatal(err)
	}
	if a.Hash != b.Hash {
		t.Fatal("the catalog's name must not change the hash")
	}
	// Read back by a swissd that does not know the field, the plan still
	// verifies.
	b.Source.CatalogName = ""
	if err := b.VerifyHash(); err != nil {
		t.Fatalf("a plan with its name dropped must still verify: %v", err)
	}
}

func TestHelmfileCarriesTheDeployDeclaration(t *testing.T) {
	p := testPlan()
	p.Chart.Repo = "https://modelsphere.github.io/helm-charts"
	doc, err := p.RenderHelmfile("")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"wait: false", "atomic: false", "historyMax: 20",
		"--three-way-merge", "--server-side=true",
		"chart: charts/sglang", "version: 0.8.0",
		"namespace: models", "values:", "- form.yaml",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("declaration is missing %q:\n%s", want, doc)
		}
	}
}

func TestNoChartSourceLeavesTheDeclarationUnrenderable(t *testing.T) {
	if _, err := testPlan().RenderHelmfile(""); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := testPlan().RenderHelmfile("../charts"); err != nil {
		t.Fatalf("chartRoot is the fallback: %v", err)
	}
}

// helm v4 applies the Namespace object server-side, so --create-namespace needs
// patch on namespaces even when the namespace exists. swissd deploys into
// namespaces an admin already granted it, so this stays off unless asked for.
func TestCreateNamespaceIsOptIn(t *testing.T) {
	p := testPlan()
	p.Chart.Repo = "oci://ghcr.io/modelsphere/charts"

	doc, err := p.HelmfileDocument("")
	if err != nil {
		t.Fatal(err)
	}
	if doc.HelmDefaults.CreateNS {
		t.Error("createNamespace must default off")
	}

	p.CreateNamespace = true
	doc, err = p.HelmfileDocument("")
	if err != nil {
		t.Fatal(err)
	}
	if !doc.HelmDefaults.CreateNS {
		t.Error("the site profile must be able to turn it on")
	}
}
