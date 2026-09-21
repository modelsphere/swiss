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
	SwissPlan   []byte
	SwissStatus []byte
}

// Node is what a fit check needs, plus what a node view shows.
type Node struct {
	Name       string
	GPUProduct string // nvidia.com/gpu.product, as labelled by GFD
	// GPUs is allocatable, which is what a fit check must compare against --
	// capacity counts GPUs the kubelet has reserved away.
	GPUs        int
	Labels      map[string]string
	Taints      []string
	Schedulable bool
	Ready       bool
	Kubelet     string
}

// GPUPod is one pod holding GPUs on a node. Kubernetes publishes no "allocated"
// field, so this is summed from pod requests the way `kubectl describe node`
// does -- and it is also the answer to the question a free-GPU count raises,
// which is who is holding the rest.
type GPUPod struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	GPUs      int    `json:"gpus"`
	Node      string `json:"node,omitempty"`
}

// Pod is enough to tell loading from broken: on this workload a pod that is
// scheduled and not ready has usually been reading weights for twenty minutes.
type Pod struct {
	Name      string `json:"name"`
	Phase     string `json:"phase"`
	Ready     bool   `json:"ready"`
	Restarts  int32  `json:"restarts"`
	Node      string `json:"node,omitempty"`
	Message   string `json:"message,omitempty"`
	AgeSecond int64  `json:"ageSeconds"`
}

type Probe interface {
	// Pods in a namespace matching a label selector.
	Pods(ctx context.Context, namespace, selector string) ([]Pod, error)
	// Ping is a cheap reachability check, for readiness probes.
	Ping(ctx context.Context) error
	Releases(ctx context.Context) ([]Release, error)
	Nodes(ctx context.Context) ([]Node, error)
	// ConfigMap reads a ConfigMap given as "ns/name". Route collisions are
	// detected from its key set before a write, rather than after two models are
	// fighting over one openresty key; site profiles are read from one whole.
	ConfigMap(ctx context.Context, ref string) (map[string]string, error)
	// Secret reads a Secret given as "ns/name". The site profile is a ConfigMap,
	// so a credential it needs -- the entrypoint's API key -- is named there and
	// held here. Values come back already base64-decoded.
	Secret(ctx context.Context, ref string) (map[string]string, error)
	// GPUAllocations is the GPU-holding pods on every node, keyed by node name.
	// Separate from Nodes because it needs a cluster-wide pod list, a grant a
	// cluster may withhold: a node view without it shows capacity and says the
	// usage is unknown, rather than showing an undercount as if it were true.
	GPUAllocations(ctx context.Context) (map[string][]GPUPod, error)
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
	PingErr  error
	Pod      []Pod
	Rel      []Release
	Nod      []Node
	Maps     map[string]map[string]string
	Secrets  map[string]map[string]string
	Alloc    map[string][]GPUPod
	AllocErr error
}

func (f Fake) Ping(context.Context) error                          { return f.PingErr }
func (f Fake) Pods(context.Context, string, string) ([]Pod, error) { return f.Pod, nil }
func (f Fake) Releases(context.Context) ([]Release, error)         { return f.Rel, nil }
func (f Fake) Nodes(context.Context) ([]Node, error)               { return f.Nod, nil }
func (f Fake) ConfigMap(_ context.Context, ref string) (map[string]string, error) {
	return f.Maps[ref], nil
}

func (f Fake) GPUAllocations(context.Context) (map[string][]GPUPod, error) {
	if f.AllocErr != nil {
		return nil, f.AllocErr
	}
	return f.Alloc, nil
}

func (f Fake) Secret(_ context.Context, ref string) (map[string]string, error) {
	s, ok := f.Secrets[ref]
	if !ok {
		return nil, fmt.Errorf("no secret %s", ref)
	}
	return s, nil
}

// Writer is the write half, kept separate so a read-only swissd can hold a
// Probe and nothing else.
type Writer interface {
	PutConfigMap(ctx context.Context, ref string, data map[string]string) error
	// DeleteConfigMap removes one, and succeeds when it is already gone.
	// Uninstall is a cleanup path: it has to be safe to run twice.
	DeleteConfigMap(ctx context.Context, ref string) error
}
