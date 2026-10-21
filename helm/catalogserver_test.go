package swisschart_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// catalog-server is vendored and off by default; on, it renders beside swissd
// under names of its own, at the vendored chart's image.
func TestChartCatalogServer(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not on PATH")
	}
	off, err := helmTemplate(t)
	if err != nil {
		t.Fatal(off)
	}
	if strings.Contains(off, "catalog-server") {
		t.Fatal("catalog-server rendered while off")
	}

	on, err := helmTemplate(t, "catalogServer.enabled=true")
	if err != nil {
		t.Fatal(on)
	}
	ids := objectIDs(t, on, "default")
	for _, want := range []string{
		"ConfigMap/default/swiss-catalog-server",
		"Deployment/default/swiss-catalog-server",
		"Service/default/swiss-catalog-server",
		"PersistentVolumeClaim/default/swiss-catalog-server-data",
	} {
		if !slices.Contains(ids, want) {
			t.Errorf("missing %s in %v", want, ids)
		}
	}
	slices.Sort(ids)
	if len(slices.Compact(slices.Clone(ids))) != len(ids) {
		t.Errorf("an object is rendered twice: %v", ids)
	}
	if !strings.Contains(on, `image: "ghcr.io/modelsphere/catalog-server:0.1.0"`) {
		t.Error("the image is not the pinned one")
	}

	routed, err := helmTemplate(t, "catalogServer.enabled=true", "catalogServer.httpRoute.enabled=true", "catalogServer.httpRoute.parentRefs[0].name=gw")
	if err != nil {
		t.Fatal(routed)
	}
	if !slices.Contains(objectIDs(t, routed, "default"), "HTTPRoute/default/swiss-catalog-server") {
		t.Error("no HTTPRoute with httpRoute on")
	}
	if out, err := helmTemplate(t, "catalogServer.enabled=true", "catalogServer.httpRoute.enabled=true"); err == nil {
		t.Errorf("an HTTPRoute attached to no Gateway rendered:\n%s", out)
	}
}
