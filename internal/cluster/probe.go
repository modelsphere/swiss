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
	// SwissFiles is the plan ConfigMap's contents: helmfile.yaml, one values
	// file per layer, and the metadata -- the directory the release was applied
	// from. Empty means the release was not deployed by Swiss, which is the
	// "live but untracked" row that matters most in a reconciliation view.
	SwissFiles  map[string]string
	SwissStatus []byte
	// Objects are the custom resources the release's manifest names, read off
	// the release record rather than found by label: the charts do not label
	// all of them, and an override can rename any of them.
	Objects []ObjectRef
}

// ManagedRef names one release swiss deployed. The plan ConfigMap is the link:
// swiss writes one per release, so the set of those ConfigMaps IS the set of
// managed releases -- no helm read required to enumerate them.
type ManagedRef struct {
	Namespace string
	Name      string // the release name, not the ConfigMap name
}

// Node is what a fit check needs, plus what a node view shows.
type Node struct {
	Name        string
	GPUProduct  string // node label for GPU SKU (e.g. nvidia.com/gpu.product, accelerator/huawei-ascend910)
	GPUResource string // the matching extended resource (e.g. nvidia.com/gpu, huawei.com/Ascend910)
	// GPUs is allocatable, which is what a fit check must compare against --
	// capacity counts GPUs the kubelet has reserved away.
	GPUs        int
	Labels      map[string]string
	Taints      []string
	Schedulable bool
	Ready       bool
	Kubelet     string
	// InternalIP is the node's primary address. ExternalIP is set when the
	// cluster publishes one; many nodes have only the internal address.
	InternalIP string
	ExternalIP string
	// Conditions are the kubelet's node conditions, Ready included. The node
	// view shows them; fit checks keep using Ready and Schedulable.
	Conditions []NodeCondition
}

// NodeCondition is one kubelet condition. Status is True, False, or Unknown.
type NodeCondition struct {
	Type    string
	Status  string
	Reason  string
	Message string
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
	// ManagedRefs names every release swiss deployed, sorted, reading metadata
	// only -- the plan ConfigMaps' names and nothing of their contents.
	//
	// This is the cheap half of the reconciliation view, and the reason it is
	// separate from Releases: listing helm's own storage means pulling and
	// gunzipping every release in scope, most of which swiss did not deploy.
	// Here the answer is a name list, and the expensive per-release reads happen
	// only for the page actually being shown.
	ManagedRefs(ctx context.Context) ([]ManagedRef, error)
	// Release is one release by name: the live helm state, and the plan beside
	// it when swiss deployed it. Nil when neither exists.
	//
	// Targeted, and that is the point: every question about ONE release -- does
	// it exist, what revision, what plan -- used to be answered by listing and
	// decoding every release in scope. helm labels its storage with the release
	// name, so this asks for the one.
	Release(ctx context.Context, namespace, name string) (*Release, error)
	Nodes(ctx context.Context) ([]Node, error)
	// ConfigMap reads a ConfigMap given as "ns/name". Route collisions are
	// detected from its key set before a write, rather than after two models are
	// fighting over one openresty key; site profiles are read from one whole.
	ConfigMap(ctx context.Context, ref string) (map[string]string, error)
	// SecretNames lists the Secrets in a namespace matching a label selector.
	// Plan history is one Secret per revision, labelled the way helm labels its
	// own, so enumerating what a release can roll back to is a cluster listing
	// and not a database query.
	SecretNames(ctx context.Context, namespace, selector string) ([]string, error)
	// Secret reads a Secret given as "ns/name". The site profile is a ConfigMap,
	// so a credential it needs -- the entrypoint's API key -- is named there and
	// held here. Values come back already base64-decoded.
	Secret(ctx context.Context, ref string) (map[string]string, error)
	// GPUAllocations is the GPU-holding pods on every node, keyed by node name.
	// Separate from Nodes because it needs a cluster-wide pod list, a grant a
	// cluster may withhold: a node view without it shows capacity and says the
	// usage is unknown, rather than showing an undercount as if it were true.
	GPUAllocations(ctx context.Context) (map[string][]GPUPod, error)
	// Object reads one custom resource a release rendered. Nil when it is gone.
	Object(ctx context.Context, ref ObjectRef) (*Object, error)
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
	PingErr error
	Pod     []Pod
	Rel     []Release
	Nod     []Node
	NodErr  error
	Maps    map[string]map[string]string
	Secrets map[string]map[string]string
	// SecretLabels is what SecretNames matches on, keyed like Secrets.
	SecretLabels map[string]map[string]string
	Alloc        map[string][]GPUPod
	AllocErr     error
	// Objs is keyed "Kind/namespace/name".
	Objs   map[string]Object
	ObjErr map[string]error
}

func (f Fake) Ping(context.Context) error                          { return f.PingErr }
func (f Fake) Pods(context.Context, string, string) ([]Pod, error) { return f.Pod, nil }

// ManagedRefs derives the managed set from the seeded releases the way Kube
// derives it from plan ConfigMaps: a release with a plan beside it is managed.
func (f Fake) ManagedRefs(context.Context) ([]ManagedRef, error) {
	var out []ManagedRef
	for _, r := range f.Rel {
		if len(r.SwissFiles) > 0 {
			out = append(out, ManagedRef{Namespace: r.Namespace, Name: r.Name})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (f Fake) Release(_ context.Context, namespace, name string) (*Release, error) {
	for _, r := range f.Rel {
		if r.Namespace == namespace && r.Name == name {
			found := r
			return &found, nil
		}
	}
	return nil, nil
}
func (f Fake) Nodes(context.Context) ([]Node, error) {
	if f.NodErr != nil {
		return nil, f.NodErr
	}
	return f.Nod, nil
}
func (f Fake) ConfigMap(_ context.Context, ref string) (map[string]string, error) {
	return f.Maps[ref], nil
}

// SecretNames matches on the labels the fake was seeded with. Good enough for
// the one selector swiss uses: every term must match.
func (f Fake) SecretNames(_ context.Context, namespace, selector string) ([]string, error) {
	var out []string
	for ref, labels := range f.SecretLabels {
		ns, name, err := SplitRef(ref)
		if err != nil || ns != namespace {
			continue
		}
		if matchSelector(labels, selector) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

func matchSelector(labels map[string]string, selector string) bool {
	for _, term := range strings.Split(selector, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(term), "=")
		if !ok || labels[k] != v {
			return false
		}
	}
	return true
}

func (f Fake) GPUAllocations(context.Context) (map[string][]GPUPod, error) {
	if f.AllocErr != nil {
		return nil, f.AllocErr
	}
	return f.Alloc, nil
}

func (f Fake) Object(_ context.Context, ref ObjectRef) (*Object, error) {
	key := ref.Kind + "/" + ref.Namespace + "/" + ref.Name
	if err := f.ObjErr[key]; err != nil {
		return nil, err
	}
	o, ok := f.Objs[key]
	if !ok {
		return nil, nil
	}
	return &o, nil
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
	// PutSecret writes one with labels, which is how plan history is stored:
	// one Secret per revision, the way helm stores its own.
	PutSecret(ctx context.Context, ref string, data, labels map[string]string) error
	DeleteSecret(ctx context.Context, ref string) error
	// EnsureNamespace creates a namespace, and succeeds when it already exists.
	//
	// swiss records a release's plan in the release's own namespace, before it
	// applies anything. With createNamespace that namespace is helm's to create
	// -- which happens after, so the write-ahead would land in a namespace that
	// does not exist yet and the apply would never run.
	EnsureNamespace(ctx context.Context, name string) error
}
