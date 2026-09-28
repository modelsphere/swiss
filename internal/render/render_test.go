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
