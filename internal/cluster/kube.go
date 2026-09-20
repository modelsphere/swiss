package cluster

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Kube reads a live cluster. One implementation for both frontends: the CLI
// passes a kubeconfig, the server runs with an in-cluster ServiceAccount, and
// neither gets a separate code path -- a "local cluster" shortcut exercised in
// only one deployment is where the bugs would live.
type Kube struct {
	client kubernetes.Interface
	// SwissPlanPrefix names the ConfigMap holding a release's plan. It is read
	// alongside the release so a reconciliation view can tell a Swiss-managed
	// release from one installed by hand.
	SwissPlanPrefix string
	// Namespaces bounds every list. Empty means cluster-wide, which needs a
	// ClusterRole; a non-empty list lists each namespace in turn, which is what
	// namespace-scoped Roles can actually authorise. Getting this wrong is not a
	// degraded view -- a cluster-wide list is simply forbidden.
	Namespaces []string
}

// PlanConfigMapPrefix names the ConfigMap holding a release's plan.
const PlanConfigMapPrefix = "swiss-plan-"

const (
	planKey   = "plan.yaml"
	statusKey = "status.yaml"
)

// NewKube builds a probe. An empty kubeconfig path means in-cluster first,
// falling back to the usual loading rules (KUBECONFIG, ~/.kube/config).
// namespaces bounds what it reads; empty is cluster-wide.
func NewKube(kubeconfig, context_ string, namespaces ...string) (*Kube, error) {
	cfg, err := restConfig(kubeconfig, context_)
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Kube{client: cs, SwissPlanPrefix: PlanConfigMapPrefix, Namespaces: namespaces}, nil
}

// NewKubeWithClient is for tests, which supply a fake clientset.
func NewKubeWithClient(c kubernetes.Interface) *Kube {
	return &Kube{client: c, SwissPlanPrefix: PlanConfigMapPrefix}
}

func restConfig(kubeconfig, ctxName string) (*rest.Config, error) {
	if kubeconfig == "" && ctxName == "" {
		if cfg, err := rest.InClusterConfig(); err == nil {
			return cfg, nil
		}
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{}
	if ctxName != "" {
		overrides.CurrentContext = ctxName
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
}

// helmRelease is the subset of helm's stored release this needs. Helm owns the
// full schema and grows it; decoding only these fields means a helm release
// written by a newer version still reads here.
type helmRelease struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Version   int    `json:"version"`
	Info      struct {
		Status       string `json:"status"`
		LastDeployed string `json:"last_deployed"`
	} `json:"info"`
	Chart struct {
		Metadata struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"metadata"`
	} `json:"chart"`
}

// Releases lists the latest revision of every helm release in the cluster.
//
// Read from helm's own storage -- the Secrets it writes, labelled owner=helm --
// rather than by shelling out to `helm list`. That is the same source `helm list`
// reads, so the two cannot disagree, and it needs no helm binary in the server
// image.
func (k *Kube) Releases(ctx context.Context) ([]Release, error) {
	// Several revisions of one release are stored side by side; keep the highest.
	latest := map[string]Release{}
	for _, ns := range k.scopes() {
		secrets, err := k.client.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{
			LabelSelector: "owner=helm",
		})
		if err != nil {
			return nil, fmt.Errorf("list helm release secrets in %s: %w", scopeName(ns), err)
		}
		for i := range secrets.Items {
			s := &secrets.Items[i]
			rel, err := decodeRelease(s)
			if err != nil {
				// One unreadable release must not hide every other one.
				continue
			}
			key := rel.Namespace + "/" + rel.Name
			if prev, ok := latest[key]; ok && prev.Revision >= rel.Revision {
				continue
			}
			latest[key] = *rel
		}
	}

	out := make([]Release, 0, len(latest))
	for _, r := range latest {
		if doc, status, err := k.planFor(ctx, r.Namespace, r.Name); err == nil {
			r.SwissPlan, r.SwissStatus = doc, status
		}
		out = append(out, r)
	}
	sortReleases(out)
	return out, nil
}

func decodeRelease(s *corev1.Secret) (*Release, error) {
	raw, ok := s.Data["release"]
	if !ok {
		return nil, fmt.Errorf("secret %s/%s has no release key", s.Namespace, s.Name)
	}
	// Helm stores base64(gzip(json)); the API server layer has already undone
	// its own base64 by the time client-go hands it over.
	if decoded, err := base64.StdEncoding.DecodeString(string(raw)); err == nil {
		raw = decoded
	}
	if len(raw) > 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		if raw, err = io.ReadAll(zr); err != nil {
			return nil, err
		}
	}

	var hr helmRelease
	if err := json.Unmarshal(raw, &hr); err != nil {
		return nil, err
	}
	r := &Release{
		Name:      hr.Name,
		Namespace: hr.Namespace,
		Revision:  hr.Version,
		Status:    hr.Info.Status,
	}
	if r.Namespace == "" {
		r.Namespace = s.Namespace
	}
	if hr.Chart.Metadata.Name != "" {
		r.Chart = hr.Chart.Metadata.Name + "-" + hr.Chart.Metadata.Version
	}
	if t, err := time.Parse(time.RFC3339, hr.Info.LastDeployed); err == nil {
		r.Updated = t
	}
	// Fall back to the revision encoded in the secret name (….v3) when the
	// payload is from a helm version whose field layout differs.
	if r.Revision == 0 {
		if i := strings.LastIndex(s.Name, ".v"); i >= 0 {
			if n, err := strconv.Atoi(s.Name[i+2:]); err == nil {
				r.Revision = n
			}
		}
	}
	return r, nil
}

func (k *Kube) planFor(ctx context.Context, namespace, release string) (plan, status []byte, err error) {
	cm, err := k.client.CoreV1().ConfigMaps(namespace).Get(ctx, k.SwissPlanPrefix+release, metav1.GetOptions{})
	if err != nil {
		return nil, nil, err
	}
	v, ok := cm.Data[planKey]
	if !ok {
		return nil, nil, fmt.Errorf("configmap %s/%s%s has no %s", namespace, k.SwissPlanPrefix, release, planKey)
	}
	return []byte(v), []byte(cm.Data[statusKey]), nil
}

// scopes is the namespaces to list, or one cluster-wide scope.
func (k *Kube) scopes() []string {
	if len(k.Namespaces) == 0 {
		return []string{metav1.NamespaceAll}
	}
	return k.Namespaces
}

func scopeName(ns string) string {
	if ns == metav1.NamespaceAll {
		return "the cluster scope"
	}
	return "namespace " + ns
}

// Ping asks the API server for its version: no RBAC, tiny response.
func (k *Kube) Ping(ctx context.Context) error {
	_, err := k.client.Discovery().ServerVersion()
	return err
}

// Nodes lists nodes with the GPU facts a fit check needs.
func (k *Kube) Nodes(ctx context.Context) ([]Node, error) {
	list, err := k.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	out := make([]Node, 0, len(list.Items))
	for i := range list.Items {
		n := &list.Items[i]
		node := Node{
			Name: n.Name,
			// Set by GPU Feature Discovery. Absent on a node with no GPUs, and
			// absent on a GPU node where GFD is not running -- which a fit check
			// must report as unknown rather than as "no match".
			GPUProduct:  n.Labels["nvidia.com/gpu.product"],
			Labels:      n.Labels,
			Schedulable: !n.Spec.Unschedulable,
		}
		if q, ok := n.Status.Allocatable["nvidia.com/gpu"]; ok {
			node.GPUs = int(q.Value())
		}
		for _, t := range n.Spec.Taints {
			node.Taints = append(node.Taints, t.Key+"="+t.Value+":"+string(t.Effect))
		}
		out = append(out, node)
	}
	return out, nil
}

// ConfigMap reads a ConfigMap given as "namespace/name".
func (k *Kube) ConfigMap(ctx context.Context, ref string) (map[string]string, error) {
	ns, name, err := SplitRef(ref)
	if err != nil {
		return nil, err
	}
	cm, err := k.client.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return cm.Data, nil
}

func sortReleases(r []Release) {
	for i := 1; i < len(r); i++ {
		for j := i; j > 0 && less(r[j], r[j-1]); j-- {
			r[j], r[j-1] = r[j-1], r[j]
		}
	}
}

func less(a, b Release) bool {
	if a.Namespace != b.Namespace {
		return a.Namespace < b.Namespace
	}
	return a.Name < b.Name
}

var (
	_ Probe  = (*Kube)(nil)
	_ Writer = (*Kube)(nil)
)

// PutConfigMap creates or replaces a ConfigMap given as "namespace/name".
func (k *Kube) PutConfigMap(ctx context.Context, ref string, data map[string]string) error {
	ns, name, err := SplitRef(ref)
	if err != nil {
		return err
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "swiss"},
		},
		Data: data,
	}
	_, err = k.client.CoreV1().ConfigMaps(ns).Update(ctx, cm, metav1.UpdateOptions{})
	if apierrors.IsNotFound(err) {
		_, err = k.client.CoreV1().ConfigMaps(ns).Create(ctx, cm, metav1.CreateOptions{})
	}
	return err
}

// PlanRef is where a release's plan is stored.
func (k *Kube) PlanRef(namespace, release string) string {
	return namespace + "/" + k.SwissPlanPrefix + release
}
