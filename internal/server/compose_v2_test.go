package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/values"
	"gopkg.in/yaml.v3"
)

const chartDefaults = "# Engine replicas.\nreplicaCount: 1\nimage:\n  tag: chart\n"

// stubShowValues prints a chart's values.yaml on stdout and an OCI pull's
// report on stderr, as helm does, and logs its arguments.
func stubShowValues(t *testing.T, exitCode int) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	bin, log = filepath.Join(dir, "helm"), filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + log + "\n" +
		"echo 'Pulled: ghcr.io/modelsphere/charts/sglang:0.8.0' >&2\n" +
		"printf '" + strings.ReplaceAll(chartDefaults, "\n", "\\n") + "'\n" +
		"exit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

// composeV2Server has a profile naming an OCI chart repo, so a plan names its chart.
func composeV2Server(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	probe := liveProbe()
	probe.Maps["swiss/site-profile"] = map[string]string{
		"profile.yaml": profileYAML + "chartRepo: oci://ghcr.io/modelsphere/charts\n",
	}
	return deployServerWith(t, probe, true)
}

var composeV2Body = map[string]any{
	"model": "qwen3.6-35b-a3b", "release": "glm-53",
	"overrides": map[string]any{"replicaCount": 2},
}

func TestComposeV2DumpsHelmValuesAndStoresNothing(t *testing.T) {
	srv, s := composeV2Server(t)
	bin, log := stubShowValues(t, 0)
	s.cfg.Server.HelmBin = bin
	code, body := post(t, srv, "/api/v2/compose?dryRun=true&dumpHelmValues=true", composeV2Body)
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	if _, ok := body["plan"]; ok {
		t.Fatal("a dry run returned a plan")
	}
	hv, _ := body["helmValues"].(map[string]any)
	if hv["defaults"] != chartDefaults {
		t.Fatalf("defaults %q, want the chart's values.yaml verbatim", hv["defaults"])
	}
	var overrides values.Tree
	if err := yaml.Unmarshal([]byte(hv["overrides"].(string)), &overrides); err != nil {
		t.Fatal(err)
	}
	if overrides["replicaCount"] != 2 {
		t.Fatalf("overrides %v", overrides)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if want := "show values oci://ghcr.io/modelsphere/charts/"; !strings.HasPrefix(string(calls), want) || !strings.Contains(string(calls), " --version ") {
		t.Fatalf("helm called with %q", calls)
	}
	p, err := s.compose(context.Background(), planRequest{Model: "qwen3.6-35b-a3b", Release: "glm-53", Overrides: values.Tree{"replicaCount": 2}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.Plan(context.Background(), p.Hash); err == nil {
		t.Fatal("a dry run stored its plan")
	}
}

func TestComposeV2DryRunAloneReadsNoChart(t *testing.T) {
	srv, s := composeV2Server(t)
	s.cfg.Server.HelmBin = failBin(t, "helm")
	code, body := post(t, srv, "/api/v2/compose?dryRun=true", composeV2Body)
	if code != http.StatusOK || len(body) != 0 {
		t.Fatalf("status %d: %v", code, body)
	}
}

func TestComposeV2Errors(t *testing.T) {
	srv, s := composeV2Server(t)
	s.cfg.Server.HelmBin, _ = stubShowValues(t, 1)
	for _, tc := range []struct {
		query  string
		body   map[string]any
		status int
		code   string
	}{
		{"", composeV2Body, http.StatusBadRequest, "INVALID_QUERY"},
		{"?dryRun=false&dumpHelmValues=true", composeV2Body, http.StatusBadRequest, "INVALID_QUERY"},
		{"?dryRun=yes", composeV2Body, http.StatusBadRequest, "INVALID_QUERY"},
		{"?dryRun=true&dumpHelmValues=1", composeV2Body, http.StatusBadRequest, "INVALID_QUERY"},
		{"?dryRun=true", map[string]any{"release": "glm-53"}, http.StatusBadRequest, "INVALID_REQUEST"},
		{"?dryRun=true&dumpHelmValues=true", composeV2Body, http.StatusBadGateway, "CHART_VALUES_UNAVAILABLE"},
	} {
		code, body := post(t, srv, "/api/v2/compose"+tc.query, tc.body)
		if code != tc.status || body["code"] != tc.code {
			t.Errorf("%s: status %d %v, want %d %s", tc.query, code, body, tc.status, tc.code)
		}
	}
}
