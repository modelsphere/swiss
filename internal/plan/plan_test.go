package plan

import (
	"strings"
	"testing"

	"github.com/aceforeverd/swiss/internal/values"
)

func testPlan() *Plan {
	return &Plan{
		APIVersion: APIVersion,
		Release:    Release{Name: "glm-53", Namespace: "modelforge"},
		Chart:      ChartRef{Name: "sglang", Version: "0.8.0"},
		Values:     values.Tree{"replicaCount": 2},
	}
}

func TestHelmfileIsNotInTheHash(t *testing.T) {
	a, b := testPlan(), testPlan()
	a.Chart.Repo = "oci://harbor.4pd.io/hardcore-tech"
	b.Chart.Repo = "oci://harbor.4pd.io/hardcore-tech"

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

func TestHelmfileCarriesTheDeployDeclaration(t *testing.T) {
	p := testPlan()
	p.Chart.Repo = "https://harbor.4pd.io/chartrepo/hardcore-tech"
	doc, err := p.RenderHelmfile("")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"wait: false", "atomic: false", "historyMax: 20", "--three-way-merge",
		"chart: charts/sglang", "version: 0.8.0",
		"namespace: modelforge", "values:", "- values.yaml",
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
	p.Chart.Repo = "oci://harbor.4pd.io/hardcore-tech"

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
