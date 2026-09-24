package server

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/modelsphere/swiss/internal/cluster"
)

// counting records which cluster reads a request actually makes, and with what
// arguments. Asserting the answer is not enough here: the same body comes back
// whether one release was read by name or every release in scope was listed and
// filtered, and only one of those stays fast on a full cluster.
type counting struct {
	cluster.Probe
	mu   sync.Mutex
	call map[string]int
	args []string
}

func newCounting(p cluster.Probe) *counting {
	return &counting{Probe: p, call: map[string]int{}}
}

func (c *counting) note(name, arg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.call[name]++
	c.args = append(c.args, name+"("+arg+")")
}

func (c *counting) Release(ctx context.Context, ns, name string) (*cluster.Release, error) {
	c.note("Release", ns+"/"+name)
	return c.Probe.Release(ctx, ns, name)
}

func (c *counting) Pods(ctx context.Context, ns, selector string) ([]cluster.Pod, error) {
	c.note("Pods", ns+" "+selector)
	return c.Probe.Pods(ctx, ns, selector)
}

func (c *counting) ManagedRefs(ctx context.Context) ([]cluster.ManagedRef, error) {
	c.note("ManagedRefs", "")
	return c.Probe.ManagedRefs(ctx)
}

func (c *counting) Nodes(ctx context.Context) ([]cluster.Node, error) {
	c.note("Nodes", "cluster-wide")
	return c.Probe.Nodes(ctx)
}

func (c *counting) GPUAllocations(ctx context.Context) (map[string][]cluster.GPUPod, error) {
	c.note("GPUAllocations", "cluster-wide")
	return c.Probe.GPUAllocations(ctx)
}

// Every cluster read the detail page makes must name the one release it is
// about. This pins the shape, not just the outcome.
func TestDetailPageReadsAreAllNameScoped(t *testing.T) {
	_, doc := livePlan(t, planRequest{Model: "modelforge", Release: "r", ServiceID: "r"})
	base := liveProbe()
	base.Rel = append(base.Rel, cluster.Release{
		Name: "r", Namespace: "modelforge", Chart: "sglang-0.7.1",
		Status: "deployed", Revision: 3, SwissFiles: doc,
	})
	probe := newCounting(base)
	srv := testServer(t, probe)

	// What the page fires on mount, minus /api/cluster, which reads no release.
	for _, path := range []string{
		"/api/releases/modelforge/r/status",
		"/api/releases/modelforge/r/plan",
	} {
		if code, body := get(t, srv, path); code != 200 {
			t.Fatalf("%s: status %d: %v", path, code, body)
		}
	}

	for _, banned := range []string{"Nodes", "GPUAllocations", "ManagedRefs"} {
		if probe.call[banned] > 0 {
			t.Errorf("the detail page must not call %s: %v", banned, probe.args)
		}
	}
	// Both reads name the release. Pods is a label selector on one namespace,
	// which is the chart's own app=<release>-<engine>.
	for _, arg := range probe.args {
		if !strings.Contains(arg, "modelforge") || !strings.Contains(arg, "r") {
			t.Errorf("a read that does not name the release: %q (all: %v)", arg, probe.args)
		}
	}
	t.Logf("detail page cluster reads: %v", probe.args)
}
