package llmsvc

import (
	"context"
	"fmt"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// Client reads and writes LLMServices. dyn and disco are required for the
// resource; kube is required only for ControllerRevisions.
type Client struct {
	dyn   dynamic.Interface
	disco discovery.DiscoveryInterface
	kube  kubernetes.Interface

	// Interval is how often WaitApplied and WaitGone poll. Zero means one second.
	Interval time.Duration
}

func New(dyn dynamic.Interface, disco discovery.DiscoveryInterface, kube kubernetes.Interface) *Client {
	return &Client{dyn: dyn, disco: disco, kube: kube, Interval: time.Second}
}

// Served reports whether API discovery lists llmservices in this group version.
// NotFound is false: the CRD is simply not installed.
func (c *Client) Served(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if c.disco == nil {
		return false, fmt.Errorf("no discovery client")
	}
	list, err := c.disco.ServerResourcesForGroupVersion(APIVersion)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if list == nil {
		return false, nil
	}
	for _, r := range list.APIResources {
		if r.Name == Resource {
			return true, nil
		}
	}
	return false, nil
}

func (c *Client) Get(ctx context.Context, ns, name string) (*unstructured.Unstructured, error) {
	u, err := c.resource(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return u.DeepCopy(), nil
}

func (c *Client) Create(ctx context.Context, u *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	got, err := c.resource(u.GetNamespace()).Create(ctx, u, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	return got.DeepCopy(), nil
}

// Update writes the object, resourceVersion included, so a stale write is a Conflict.
func (c *Client) Update(ctx context.Context, u *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	got, err := c.resource(u.GetNamespace()).Update(ctx, u, metav1.UpdateOptions{})
	if err != nil {
		return nil, err
	}
	return got.DeepCopy(), nil
}

func (c *Client) Delete(ctx context.Context, ns, name string) error {
	return c.resource(ns).Delete(ctx, name, metav1.DeleteOptions{})
}

// Patch applies a patch and returns the updated object.
func (c *Client) Patch(ctx context.Context, ns, name string, pt types.PatchType, data []byte) (*unstructured.Unstructured, error) {
	got, err := c.resource(ns).Patch(ctx, name, pt, data, metav1.PatchOptions{})
	if err != nil {
		return nil, err
	}
	return got.DeepCopy(), nil
}

// List reads LLMServices. An empty namespaces list is cluster-wide; otherwise
// each namespace is listed on its own, which is what a namespaced Role can authorise.
func (c *Client) List(ctx context.Context, namespaces []string) ([]*unstructured.Unstructured, error) {
	scopes := namespaces
	if len(scopes) == 0 {
		scopes = []string{metav1.NamespaceAll}
	}
	var out []*unstructured.Unstructured
	for _, ns := range scopes {
		list, err := c.resource(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list llmservices in %s: %w", scopeName(ns), err)
		}
		for i := range list.Items {
			out = append(out, list.Items[i].DeepCopy())
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GetNamespace() != out[j].GetNamespace() {
			return out[i].GetNamespace() < out[j].GetNamespace()
		}
		return out[i].GetName() < out[j].GetName()
	})
	return out, nil
}

// WaitApplied returns the object once observedGeneration >= generation and
// phase is Applied or Failed. The context is the budget.
func (c *Client) WaitApplied(ctx context.Context, ns, name string, generation int64) (*unstructured.Unstructured, error) {
	var got *unstructured.Unstructured
	err := c.poll(ctx, func() (bool, error) {
		u, err := c.Get(ctx, ns, name)
		if err != nil {
			return false, err
		}
		done, err := reached(u, generation)
		if err != nil || !done {
			return false, err
		}
		got = u
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return got, nil
}

// WaitGone returns once the object is NotFound. A finalizer keeps it visible
// until the operator drops it, so a successful Delete is not yet gone.
func (c *Client) WaitGone(ctx context.Context, ns, name string) error {
	return c.poll(ctx, func() (bool, error) {
		_, err := c.Get(ctx, ns, name)
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
}

// ControllerRevisions reads history snapshots by name, in the order given.
func (c *Client) ControllerRevisions(ctx context.Context, ns string, names []string) ([]*appsv1.ControllerRevision, error) {
	if c.kube == nil {
		return nil, fmt.Errorf("no typed client for controllerrevisions")
	}
	out := make([]*appsv1.ControllerRevision, 0, len(names))
	for _, name := range names {
		rev, err := c.kube.AppsV1().ControllerRevisions(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("controllerrevision %s/%s: %w", ns, name, err)
		}
		out = append(out, rev)
	}
	return out, nil
}

func (c *Client) resource(ns string) dynamic.ResourceInterface {
	r := c.dyn.Resource(GVR)
	// Empty and NamespaceAll are the cluster-wide list. Namespace("all") would
	// ask for a namespace named all.
	if ns == "" || ns == metav1.NamespaceAll {
		return r
	}
	return r.Namespace(ns)
}

func (c *Client) poll(ctx context.Context, fn func() (bool, error)) error {
	every := c.Interval
	if every <= 0 {
		every = time.Second
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		done, err := fn()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func reached(u *unstructured.Unstructured, generation int64) (bool, error) {
	obj, err := FromUnstructured(u)
	if err != nil {
		return false, err
	}
	if obj.Status == nil || obj.Status.ObservedGeneration < generation {
		return false, nil
	}
	switch obj.Status.Phase {
	case PhaseApplied, PhaseFailed:
		return true, nil
	default:
		return false, nil
	}
}

func scopeName(ns string) string {
	if ns == "" || ns == metav1.NamespaceAll {
		return "the cluster scope"
	}
	return "namespace " + ns
}
