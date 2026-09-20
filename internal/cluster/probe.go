// Package cluster is the read side of a live cluster, behind one interface.
//
// One interface, three implementations: a kubeconfig for the CLI, an in-cluster
// ServiceAccount for the server, and a fake for tests. Preflight is the only
// consumer, and the reason it is worth abstracting at all is that preflight
// rules which cannot be unit tested are preflight rules that get discussed
// rather than written.
package cluster

import "context"

// Release is a live helm release, enough to answer "would this collide".
type Release struct {
	Name      string
	Namespace string
	Chart     string
	Status    string
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
	// ConfigMapKeys lists the keys of a ConfigMap given as "ns/name", so a route
	// collision can be detected before it is written rather than after two models
	// are fighting over one openresty key.
	ConfigMapKeys(ctx context.Context, ref string) ([]string, error)
}

// Fake is an in-memory Probe.
type Fake struct {
	Rel  []Release
	Nod  []Node
	Keys map[string][]string
}

func (f Fake) Releases(context.Context) ([]Release, error) { return f.Rel, nil }
func (f Fake) Nodes(context.Context) ([]Node, error)       { return f.Nod, nil }
func (f Fake) ConfigMapKeys(_ context.Context, ref string) ([]string, error) {
	return f.Keys[ref], nil
}
