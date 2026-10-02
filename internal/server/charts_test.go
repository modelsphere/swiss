package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/modelsphere/swiss/internal/chart"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/site"
)

// chartRepo serves an index.yaml holding these sglang versions, counting reads.
func chartRepo(t *testing.T, versions ...string) (*site.Profile, *atomic.Int32) {
	t.Helper()
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		fmt.Fprint(w, "entries:\n  sglang:\n")
		for _, v := range versions {
			fmt.Fprintf(w, "    - version: %s\n", v)
		}
	}))
	t.Cleanup(srv.Close)
	return &site.Profile{ChartRepo: srv.URL}, &reads
}

var sglangRange = catalog.Chart{Name: "sglang", Version: ">=0.7.1"}

// A plan for a range records the newest version in it, and the listing is
// reused for the next plan rather than read again.
func TestARangeResolvesToTheNewestInIt(t *testing.T) {
	s := New(testConfig("c"), fakeProbe(), discardLogger(), "test")
	prof, reads := chartRepo(t, "0.7.0", "0.7.1", "0.8.2", "0.9.0-rc1")
	for range 2 {
		got, err := s.resolveChart(t.Context(), prof, sglangRange, "", plan.ChartRef{})
		if err != nil || got != "0.8.2" {
			t.Fatalf("got %q, %v; want 0.8.2", got, err)
		}
	}
	if n := reads.Load(); n != 1 {
		t.Errorf("the repository was read %d times, want once", n)
	}
}

// An upgrade keeps the chart a release runs while the range allows it: the
// chart moves when asked to, not because the registry gained a version.
func TestAnUpgradeKeepsTheRunningChartInRange(t *testing.T) {
	s := New(testConfig("c"), fakeProbe(), discardLogger(), "test")
	prof, reads := chartRepo(t, "0.7.1", "0.8.2")
	got, err := s.resolveChart(t.Context(), prof, sglangRange, "", plan.ChartRef{Name: "sglang", Version: "0.7.1"})
	if err != nil || got != "0.7.1" {
		t.Fatalf("got %q, %v; want the running 0.7.1", got, err)
	}
	if reads.Load() != 0 {
		t.Error("keeping the running chart needs no registry read")
	}
	// Asked to, it moves -- in place, the model version unchanged.
	if got, err := s.resolveChart(t.Context(), prof, sglangRange, "0.8.2", plan.ChartRef{Name: "sglang", Version: "0.7.1"}); err != nil || got != "0.8.2" {
		t.Errorf("got %q, %v; want the named 0.8.2", got, err)
	}
	// Another chart's version means nothing to this one.
	if got, err := s.resolveChart(t.Context(), prof, sglangRange, "", plan.ChartRef{Name: "vllm", Version: "0.7.1"}); err != nil || got != "0.8.2" {
		t.Errorf("got %q, %v; want the newest, 0.8.2", got, err)
	}
}

// An upgrade request carries chartVersion through to the plan, and one the
// catalog does not allow is refused rather than rendered.
func TestUpgradeTakesAChartVersion(t *testing.T) {
	probe := deployed(t, planRequest{Model: "qwen3.6-35b-a3b", Release: "r", ServiceID: "r"})
	srv, s := deployServerWith(t, probe, true)
	prev, err := s.currentPlan(t.Context(), "models", "r")
	if err != nil {
		t.Fatal(err)
	}

	out, err := s.carryForward(t.Context(), planRequest{FromRelease: "r", Namespace: "models", ChartVersion: prev.Chart.Version})
	if err != nil || out.ChartVersion != prev.Chart.Version || out.runningChart != prev.Chart {
		t.Fatalf("got %q running %+v, %v; want the request's version and the release's chart", out.ChartVersion, out.runningChart, err)
	}

	code, body := post(t, srv, "/api/plans", map[string]any{"fromRelease": "r", "chartVersion": "99.0.0"})
	if code == 200 || !strings.Contains(fmt.Sprint(body), "not in the catalog's") {
		t.Errorf("got %d %v; want a refusal naming the catalog's range", code, body)
	}
}

// A variant pinned to one version lists that version, without a registry.
func TestChartVersionsOfAPinnedVariant(t *testing.T) {
	srv, _ := deployServerWith(t, fakeProbe(), false)
	code, body := get(t, srv, "/api/catalog/qwen3.6-35b-a3b/chart-versions")
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	vs, _ := body["versions"].([]any)
	if len(vs) != 1 || vs[0] != body["range"] || body["chart"] == "" {
		t.Errorf("got %v; want the one pinned version", body)
	}
}

// A repository that cannot be listed fails the plan as the registry's problem,
// which the plan endpoint answers with 502 rather than 400.
func TestAnUnlistableRepositoryIsAListError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	s := New(testConfig("c"), fakeProbe(), discardLogger(), "test")
	_, err := s.resolveChart(t.Context(), &site.Profile{ChartRepo: srv.URL}, sglangRange, "", plan.ChartRef{})
	if !errors.As(err, new(*chart.ListError)) || !strings.Contains(err.Error(), "needs a login") || !strings.Contains(err.Error(), "Name a chart version") {
		t.Errorf("got %v", err)
	}
}
