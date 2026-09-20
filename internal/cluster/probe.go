// Package cluster is the read side of a live cluster, behind one interface.
//
// One interface, three implementations: a kubeconfig for the CLI, an in-cluster
// ServiceAccount for the server, and a fake for tests. Preflight is the only
// consumer, and the reason it is worth abstracting at all is that preflight
// rules which cannot be unit tested are preflight rules that get discussed
// rather than written.
package cluster

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Release is a live helm release, enough to answer "would this collide" and
// "has it moved since I diffed it".
type Release struct {
	Name      string
	Namespace string
	Chart     string // "sglang-0.8.0"
	Status    string // deployed, pending-upgrade, failed, ...
	Revision  int
	Updated   time.Time
	// SwissPlan is the plan recorded alongside the release, when one is present.
	// Absent means the release was not deployed by Swiss -- the "live but
	// untracked" row that matters most in a reconciliation view.
	SwissPlan []byte
}

// Node is what a fit check needs.
type Node struct {
	Name        string
	GPUProduct  string // nvidia.com/gpu.product, as labelled by GFD
	GPUs        int
	Labels      map[string]string
	Taints      []string
	Schedulable bool
}

type Probe interface {
	Releases(ctx context.Context) ([]Release, error)
	Nodes(ctx context.Context) ([]Node, error)
	// ConfigMap reads a ConfigMap given as "ns/name". Route collisions are
	// detected from its key set before a write, rather than after two models are
	// fighting over one openresty key; site profiles are read from one whole.
	ConfigMap(ctx context.Context, ref string) (map[string]string, error)
}

// ConfigMapKeys is the key set of a ConfigMap, sorted.
func ConfigMapKeys(ctx context.Context, p Probe, ref string) ([]string, error) {
	m, err := p.ConfigMap(ctx, ref)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

// SplitRef splits a "namespace/name" reference.
func SplitRef(ref string) (namespace, name string, err error) {
	ns, n, ok := strings.Cut(ref, "/")
	if !ok || ns == "" || n == "" {
		return "", "", fmt.Errorf("reference %q is not namespace/name", ref)
	}
	return ns, n, nil
}

// Fake is an in-memory Probe.
type Fake struct {
	Rel  []Release
	Nod  []Node
	Maps map[string]map[string]string
}

func (f Fake) Releases(context.Context) ([]Release, error) { return f.Rel, nil }
func (f Fake) Nodes(context.Context) ([]Node, error)       { return f.Nod, nil }
func (f Fake) ConfigMap(_ context.Context, ref string) (map[string]string, error) {
	return f.Maps[ref], nil
}

// Writer is the write half, kept separate so a read-only swissd can hold a
// Probe and nothing else.
type Writer interface {
	PutConfigMap(ctx context.Context, ref string, data map[string]string) error
}
