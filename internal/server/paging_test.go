package server

import (
	"fmt"
	"testing"

	"github.com/modelsphere/swiss/internal/cluster"
)

// managed seeds n swiss-deployed releases, named so their sort order is obvious.
func managed(n int) []cluster.Release {
	out := make([]cluster.Release, 0, n)
	for i := range n {
		out = append(out, cluster.Release{
			Name: fmt.Sprintf("rel-%02d", i), Namespace: "modelforge",
			Chart: "sglang-0.7.1", Status: "deployed", Revision: 1,
			SwissFiles: map[string]string{"plan.yaml": "source:\n  model: modelforge\n"},
		})
	}
	return out
}

// The page is what the request pays for. total is the whole managed set, read
// from a name list; the rows are only the window, because every row costs a plan
// read and a helm read.
func TestDeploymentsPage(t *testing.T) {
	probe := fakeProbe()
	probe.Rel = managed(30)
	srv := testServer(t, probe)

	code, body := get(t, srv, "/api/deployments?page=2&perPage=10")
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	if body["page"].(float64) != 2 || body["perPage"].(float64) != 10 {
		t.Errorf("the page is echoed back: %v %v", body["page"], body["perPage"])
	}
	if total := body["summary"].(map[string]any)["total"].(float64); total != 30 {
		t.Errorf("total is the whole managed set, got %v", total)
	}
	rows := body["deployments"].([]any)
	if len(rows) != 10 {
		t.Fatalf("want one page of rows, got %d", len(rows))
	}
	// Second page of ten, in ref order -- the fan-out must not reorder them.
	if rows[0].(map[string]any)["release"] != "rel-10" {
		t.Errorf("page 2 starts at rel-10, got %v", rows[0])
	}
	if rows[9].(map[string]any)["release"] != "rel-19" {
		t.Errorf("page 2 ends at rel-19, got %v", rows[9])
	}
}

func TestDeploymentsPageDefaultsAndClamps(t *testing.T) {
	probe := fakeProbe()
	probe.Rel = managed(30)
	srv := testServer(t, probe)

	// No query: page 1 at the default size.
	_, body := get(t, srv, "/api/deployments")
	if body["page"].(float64) != 1 || body["perPage"].(float64) != defaultPerPage {
		t.Errorf("defaults: %v %v", body["page"], body["perPage"])
	}

	// Unparseable is the default rather than an error: a bad query string
	// should not cost the view.
	_, body = get(t, srv, "/api/deployments?page=banana&perPage=-3")
	if body["page"].(float64) != 1 || body["perPage"].(float64) != defaultPerPage {
		t.Errorf("garbage must fall back to the defaults: %v %v", body["page"], body["perPage"])
	}

	// Past the end is an empty page, not an error and not a wrapped one.
	_, body = get(t, srv, "/api/deployments?page=99&perPage=10")
	if rows := body["deployments"].([]any); len(rows) != 0 {
		t.Errorf("past the end is empty, got %d rows", len(rows))
	}
	if total := body["summary"].(map[string]any)["total"].(float64); total != 30 {
		t.Errorf("total still counts everything, got %v", total)
	}

	// perPage is capped: one request must not be able to ask for the cluster.
	_, body = get(t, srv, "/api/deployments?perPage=100000")
	if body["perPage"].(float64) != maxPerPage {
		t.Errorf("perPage must clamp to %d, got %v", maxPerPage, body["perPage"])
	}
}
