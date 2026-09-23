package server

import (
	"context"
	"fmt"
	"testing"

	"github.com/aceforeverd/swiss/internal/cluster"
)

// noScan fails the cluster-wide reads.
//
// Probe has exactly two left, and both belong to the nodes page: Nodes, and
// GPUAllocations, which lists every pod in the cluster to sum GPU usage. Nothing
// about one release may reach for either -- that is the shape of slowness that
// only shows up on a full cluster, so it is checked here rather than noticed in
// production. There is no full release scan to guard any more: Probe has no such
// method, which is the stronger version of this test.
type noScan struct {
	cluster.Probe
	t *testing.T
}

func (p noScan) Nodes(context.Context) ([]cluster.Node, error) {
	p.t.Helper()
	p.t.Error("a release path must not list every node in the cluster")
	return nil, fmt.Errorf("cluster-wide node list")
}

func (p noScan) GPUAllocations(context.Context) (map[string][]cluster.GPUPod, error) {
	p.t.Helper()
	p.t.Error("a release path must not list every pod in the cluster")
	return nil, fmt.Errorf("cluster-wide pod list")
}

// The detail page polls both of these every fifteen seconds, and the deploy
// pipeline polls status again while it is open.
func TestReleaseDetailReadsOnlyItsOwnRelease(t *testing.T) {
	_, doc := livePlan(t, planRequest{Model: "modelforge", Release: "r", ServiceID: "r"})
	probe := liveProbe()
	probe.Rel = append(probe.Rel, cluster.Release{
		Name: "r", Namespace: "modelforge", Chart: "sglang-0.7.1",
		Status: "deployed", Revision: 3, SwissFiles: doc,
	})
	srv := testServer(t, noScan{Probe: probe, t: t})

	for _, path := range []string{
		"/api/releases/modelforge/r/status",
		"/api/releases/modelforge/r/plan",
	} {
		code, body := get(t, srv, path)
		if code != 200 {
			t.Errorf("%s: status %d: %v", path, code, body)
		}
	}
}

// The reconciliation view enumerates plan ConfigMaps and resolves each release
// by name, so it touches neither cluster-wide read either.
func TestDeploymentsReadOnlyTheManagedSet(t *testing.T) {
	probe := fakeProbe()
	probe.Rel = managed(5)
	srv := testServer(t, noScan{Probe: probe, t: t})

	code, body := get(t, srv, "/api/deployments")
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	if rows := body["deployments"].([]any); len(rows) != 5 {
		t.Errorf("want every managed release, got %d", len(rows))
	}
}
