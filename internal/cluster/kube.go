package cluster

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Kube reads a live cluster. One implementation for both frontends: the CLI
// passes a kubeconfig, the server runs with an in-cluster ServiceAccount, and
// neither gets a separate code path -- a "local cluster" shortcut exercised in
// only one deployment is where the bugs would live.
type Kube struct {
	client kubernetes.Interface
	// meta lists object metadata without object contents. Plan ConfigMaps carry
	// the whole deploy -- every values document -- so listing them through the
	// typed client to learn their names would pull megabytes to render a page of
	// twenty-five. Nil outside production; ManagedRefs says so rather than
	// silently falling back to the expensive path.
	meta metadata.Interface
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

// PlanConfigMapPrefix names the ConfigMap holding a release's live plan.
const PlanConfigMapPrefix = "swiss-plan-"

// PlanSecretPrefix names the Secrets holding previous plans, one per revision:
// swiss.plan.v1.<release>.v<revision>, the shape helm uses for its own.
const PlanSecretPrefix = "swiss.plan.v1."

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
	md, err := metadata.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Kube{client: cs, meta: md, SwissPlanPrefix: PlanConfigMapPrefix, Namespaces: namespaces}, nil
}

// NewKubeWithClient is for tests, which supply a fake clientset.
func NewKubeWithClient(c kubernetes.Interface) *Kube {
	return &Kube{client: c, SwissPlanPrefix: PlanConfigMapPrefix}
}

// NewKubeWithClients is for tests that exercise the metadata-only listing.
func NewKubeWithClients(c kubernetes.Interface, md metadata.Interface) *Kube {
	return &Kube{client: c, meta: md, SwissPlanPrefix: PlanConfigMapPrefix}
}

// SelfNamespace is the namespace this process runs in: POD_NAMESPACE when the
// deployment sets it, otherwise the ServiceAccount the pod mounts. Empty off
// cluster, where there is no own namespace to speak of.
func SelfNamespace() string {
	if ns := strings.TrimSpace(os.Getenv("POD_NAMESPACE")); ns != "" {
		return ns
	}
	b, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
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
		if files, status, err := k.planFor(ctx, r.Namespace, r.Name); err == nil {
			r.SwissFiles, r.SwissStatus = files, status
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

func (k *Kube) planFor(ctx context.Context, namespace, release string) (files map[string]string, status []byte, err error) {
	cm, err := k.client.CoreV1().ConfigMaps(namespace).Get(ctx, k.SwissPlanPrefix+release, metav1.GetOptions{})
	if err != nil {
		return nil, nil, err
	}
	if _, ok := cm.Data[planKey]; !ok {
		return nil, nil, fmt.Errorf("configmap %s/%s%s has no %s", namespace, k.SwissPlanPrefix, release, planKey)
	}
	return cm.Data, []byte(cm.Data[statusKey]), nil
}

var configMapGVR = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}

// ManagedRefs lists the plan ConfigMaps and reads the release name out of each
// one's name. Metadata only: a plan ConfigMap holds every values document the
// release was applied with, and none of that is needed to answer "which
// releases did swiss deploy".
//
// The label is what swiss stamps on everything it writes, so the site profile
// carries it too -- the name prefix is what separates a plan from it.
func (k *Kube) ManagedRefs(ctx context.Context) ([]ManagedRef, error) {
	if k.meta == nil {
		return nil, fmt.Errorf("no metadata client: ManagedRefs needs one")
	}
	var out []ManagedRef
	for _, ns := range k.scopes() {
		list, err := k.meta.Resource(configMapGVR).Namespace(ns).List(ctx, metav1.ListOptions{
			LabelSelector: "app.kubernetes.io/managed-by=swiss",
		})
		if err != nil {
			return nil, fmt.Errorf("list plan configmaps in %s: %w", scopeName(ns), err)
		}
		for _, item := range list.Items {
			name, ok := strings.CutPrefix(item.Name, k.SwissPlanPrefix)
			if !ok || name == "" {
				continue
			}
			out = append(out, ManagedRef{Namespace: item.Namespace, Name: name})
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

// ManagedRelease reads one release's plan and the live helm state beside it.
//
// The helm read is one labelled list in one namespace rather than a scan of
// every release secret in scope: helm labels each of its secrets with the
// release name, so the server sends back this release's revisions and nothing
// else. Only the highest is decoded -- the older ones are whole rendered
// manifests, gzipped, and nothing here reads them.
func (k *Kube) ManagedRelease(ctx context.Context, namespace, name string) (*Release, error) {
	files, status, err := k.planFor(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	rel := &Release{Namespace: namespace, Name: name, SwissFiles: files, SwissStatus: status}

	secrets, err := k.client.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "owner=helm,name=" + name,
	})
	if err != nil {
		return nil, fmt.Errorf("list helm release secrets for %s/%s: %w", namespace, name, err)
	}
	var newest *corev1.Secret
	for i := range secrets.Items {
		s := &secrets.Items[i]
		if newest == nil || revisionOf(s) > revisionOf(newest) {
			newest = s
		}
	}
	if newest == nil {
		// A plan with no release: an uninstall that did not finish cleaning up.
		// The row still belongs in the view, which is how it gets noticed.
		return rel, nil
	}
	live, err := decodeRelease(newest)
	if err != nil {
		// One unreadable release must not cost the row: the plan is what names
		// it, and that decoded fine.
		return rel, nil
	}
	live.SwissFiles, live.SwissStatus = files, status
	return live, nil
}

// revisionOf reads the revision helm labels its secret with, falling back to
// the ….v3 suffix on the name.
func revisionOf(s *corev1.Secret) int {
	if v, ok := s.Labels["version"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	if i := strings.LastIndex(s.Name, ".v"); i >= 0 {
		if n, err := strconv.Atoi(s.Name[i+2:]); err == nil {
			return n
		}
	}
	return 0
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
		node.Kubelet = n.Status.NodeInfo.KubeletVersion
		for _, c := range n.Status.Conditions {
			if c.Type == corev1.NodeReady {
				node.Ready = c.Status == corev1.ConditionTrue
			}
		}
		for _, t := range n.Spec.Taints {
			node.Taints = append(node.Taints, t.Key+"="+t.Value+":"+string(t.Effect))
		}
		out = append(out, node)
	}
	return out, nil
}

// GPUAllocations sums GPU requests per node from the pods actually bound to
// them, because Kubernetes publishes capacity and allocatable but never
// allocated -- `kubectl describe node` computes it the same way.
//
// Terminal pods are skipped: a Succeeded or Failed pod holds no GPU, and
// counting one is how a node reads as full when it is empty. Limits are
// preferred over requests only in that extended resources require them to be
// equal, so either is the same number.
func (k *Kube) GPUAllocations(ctx context.Context) (map[string][]GPUPod, error) {
	pods, err := k.client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: "status.phase!=Succeeded,status.phase!=Failed",
	})
	if err != nil {
		return nil, fmt.Errorf("list pods for GPU allocation: %w", err)
	}
	out := map[string][]GPUPod{}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Spec.NodeName == "" {
			continue
		}
		gpus := 0
		for _, c := range p.Spec.Containers {
			if q, ok := c.Resources.Limits["nvidia.com/gpu"]; ok {
				gpus += int(q.Value())
			} else if q, ok := c.Resources.Requests["nvidia.com/gpu"]; ok {
				gpus += int(q.Value())
			}
		}
		if gpus == 0 {
			continue
		}
		out[p.Spec.NodeName] = append(out[p.Spec.NodeName], GPUPod{
			Namespace: p.Namespace, Name: p.Name, GPUs: gpus, Node: p.Spec.NodeName,
		})
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

// Secret reads a Secret given as "namespace/name". client-go has already
// base64-decoded Data by the time it arrives here.
func (k *Kube) Secret(ctx context.Context, ref string) (map[string]string, error) {
	ns, name, err := SplitRef(ref)
	if err != nil {
		return nil, err
	}
	sec, err := k.client.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(sec.Data))
	for k, v := range sec.Data {
		out[k] = string(v)
	}
	return out, nil
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

// DeleteConfigMap removes a ConfigMap given as "namespace/name". Already gone
// counts as removed, so an uninstall that half-failed can simply be retried.
func (k *Kube) DeleteConfigMap(ctx context.Context, ref string) error {
	ns, name, err := SplitRef(ref)
	if err != nil {
		return err
	}
	err = k.client.CoreV1().ConfigMaps(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// SecretNames lists Secrets in a namespace by label selector.
func (k *Kube) SecretNames(ctx context.Context, namespace, selector string) ([]string, error) {
	list, err := k.client.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("list secrets in %s: %w", namespace, err)
	}
	out := make([]string, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, list.Items[i].Name)
	}
	sort.Strings(out)
	return out, nil
}

// PutSecret creates or replaces a Secret given as "namespace/name".
func (k *Kube) PutSecret(ctx context.Context, ref string, data, labels map[string]string) error {
	ns, name, err := SplitRef(ref)
	if err != nil {
		return err
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels},
		StringData: data,
	}
	_, err = k.client.CoreV1().Secrets(ns).Update(ctx, sec, metav1.UpdateOptions{})
	if apierrors.IsNotFound(err) {
		_, err = k.client.CoreV1().Secrets(ns).Create(ctx, sec, metav1.CreateOptions{})
	}
	return err
}

// DeleteSecret treats an absent Secret as removed: pruning history has to be
// safe to run twice.
func (k *Kube) DeleteSecret(ctx context.Context, ref string) error {
	ns, name, err := SplitRef(ref)
	if err != nil {
		return err
	}
	err = k.client.CoreV1().Secrets(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// PlanRef is where a release's plan is stored.
func (k *Kube) PlanRef(namespace, release string) string {
	return namespace + "/" + k.SwissPlanPrefix + release
}

// Pods lists pods matching a label selector.
func (k *Kube) Pods(ctx context.Context, namespace, selector string) ([]Pod, error) {
	list, err := k.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("list pods in %s: %w", namespace, err)
	}
	out := make([]Pod, 0, len(list.Items))
	for i := range list.Items {
		p := &list.Items[i]
		pod := Pod{
			Name:  p.Name,
			Phase: string(p.Status.Phase),
			Node:  p.Spec.NodeName,
		}
		if !p.CreationTimestamp.IsZero() {
			pod.AgeSecond = int64(time.Since(p.CreationTimestamp.Time).Seconds())
		}
		for _, c := range p.Status.ContainerStatuses {
			pod.Restarts += c.RestartCount
		}
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodReady {
				pod.Ready = c.Status == corev1.ConditionTrue
				if !pod.Ready && c.Message != "" {
					pod.Message = c.Message
				}
			}
		}
		out = append(out, pod)
	}
	return out, nil
}
