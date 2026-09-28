package render

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/values"
)

// fakeHelm prints the values file it was handed, so the test sees what helm would.
func fakeHelm(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "helm")
	script := "#!/bin/sh\nwhile [ $# -gt 0 ]; do [ \"$1\" = --values ] && cat \"$2\"; shift; done\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestTemplateHandsHelmThePlansValues(t *testing.T) {
	p := &plan.Plan{
		Release: plan.Release{Name: "q", Namespace: "models"},
		Chart:   plan.ChartRef{Name: "sglang", Version: "0.7.1", Repo: "http://charts.example"},
		Layers: map[string]values.Tree{
			plan.Layers[0]: {"progressDeadlineSeconds": 7200},
		},
	}
	out, err := Exec{Bin: fakeHelm(t)}.Template(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "progressDeadlineSeconds: 7200") {
		t.Fatalf("helm got values:\n%s", out)
	}
}

// argsHelm prints its arguments, one per line.
func argsHelm(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "helm")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nfor a; do echo \"$a\"; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// An http repo is an index, not a URL prefix: helm needs --repo and the chart's name.
// An oci:// repo is the other way round.
func TestTemplateChartReference(t *testing.T) {
	for _, tc := range []struct{ repo, want string }{
		{"http://charts.example/", "--repo\nhttp://charts.example/\nsglang\n"},
		{"oci://registry.example/charts", "oci://registry.example/charts/sglang\n"},
	} {
		p := &plan.Plan{
			Release: plan.Release{Name: "q", Namespace: "models"},
			Chart:   plan.ChartRef{Name: "sglang", Version: "0.7.1", Repo: tc.repo},
		}
		out, err := Exec{Bin: argsHelm(t)}.Template(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, tc.want) || !strings.Contains(out, "--version\n0.7.1\n") {
			t.Errorf("repo %s: helm got\n%s", tc.repo, out)
		}
	}
}
