package version

import (
	"os"
	"regexp"
	"testing"
)

// Two files hold the version; this is what makes that safe.
func TestVersionMatchesChart(t *testing.T) {
	raw, err := os.ReadFile("../../helm/swiss/Chart.yaml")
	if err != nil {
		t.Skip("chart not present")
	}
	m := regexp.MustCompile(`(?m)^appVersion:\s*"?([^"\s]+)"?`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("no appVersion in Chart.yaml")
	}
	if got := string(m[1]); got != Version {
		t.Fatalf("version.Version is %q but Chart.yaml appVersion is %q -- run ./hack/bump.sh", Version, got)
	}
}
