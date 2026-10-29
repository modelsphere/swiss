//go:build e2e

package e2e

import (
	"context"
	_ "embed"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/modelsphere/swiss/internal/plan"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
)

//go:embed testdata/disaggregatedset-crd.yaml
var dsCRD []byte

const e2ePDRelease = "e2e-pd"

var dsGVR = schema.GroupVersionResource{
	Group: "disaggregatedset.x-k8s.io", Version: "v1", Resource: "disaggregatedsets",
}

// Its own Ordered container so -ginkgo.label-filter=autoscale logs in without
// the helm/llmsvc specs. The DisaggregatedSet CRD has no controller behind it.
var _ = Describe("PD autoscaling", Label("autoscale"), Ordered, func() {
	AfterEach(func() {
		if !CurrentSpecReport().Failed() {
			return
		}
		dumpClusterState(CurrentSpecReport().FullText())
		dumpAutoscale(cfg.modelNS, e2ePDRelease)
	})

	BeforeAll(func() {
		user, pass := chartCredentials()
		var resp struct {
			Token         string `json:"token"`
			Authenticated bool   `json:"authenticated"`
		}
		api.must(http.MethodPost, "/api/login", loginBody{Username: user, Password: pass}, &resp, apiTimeout)
		Expect(resp.Authenticated).To(BeTrue())
		Expect(resp.Token).NotTo(BeEmpty())
		api.token = resp.Token
	})

	It("installs a disaggregated release and scales it down", func() {
		ns := cfg.modelNS
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		applyDSCRD()
		DeferCleanup(func() {
			cmd := exec.Command(cfg.kubectlBin, "--context", cfg.context, "delete", "crd",
				"disaggregatedsets.disaggregatedset.x-k8s.io", "--ignore-not-found")
			_, _ = cmd.CombinedOutput()
		})

		// active=1 against an install of 2 slices. Scale-down does not wait for
		// routers, which stay unready on a CPU cluster.
		decisionBody := fmt.Sprintf(`events {}
http {
  server {
    listen 80;
    location = /decisions {
      default_type application/json;
      return 200 '{"apiVersion":"llmscaling.inference.x-k8s.io/v1alpha1","decisions":[{"namespace":"%s","serviceId":"%s","replicas":{"active":1}}]}';
    }
  }
}
`, ns, e2ePDRelease)
		_, err := kube.CoreV1().ConfigMaps(ns).Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "e2e-decisions", Namespace: ns},
			Data:       map[string]string{"nginx.conf": decisionBody},
		}, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			_ = kube.CoreV1().ConfigMaps(ns).Delete(context.Background(), "e2e-decisions", metav1.DeleteOptions{})
		})

		_, err = kube.AppsV1().Deployments(ns).Create(ctx, nginxDeploy("e2e-decisions", "e2e-decisions", "e2e-decisions"), metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			_ = kube.AppsV1().Deployments(ns).Delete(context.Background(), "e2e-decisions", metav1.DeleteOptions{})
		})
		_, err = kube.CoreV1().Services(ns).Create(ctx, &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "e2e-decisions", Namespace: ns},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "e2e-decisions"},
				Ports:    []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt(80)}},
			},
		}, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			_ = kube.CoreV1().Services(ns).Delete(context.Background(), "e2e-decisions", metav1.DeleteOptions{})
		})

		text, _ := renderProfile()
		text += fmt.Sprintf("scaler:\n  serverAddress: http://e2e-decisions.%s.svc:80\n  autoscalePeriod: 2s\n", ns)
		api.must(http.MethodPut, "/api/profile", profilePut{YAML: text}, nil, apiTimeout)

		By("planning a 2-slice PD release")
		var planned plan.Plan
		api.must(http.MethodPost, "/api/plans", planBody{
			Model: cfg.model, Release: e2ePDRelease, Namespace: ns, ServiceID: e2ePDRelease,
			Overrides: map[string]any{"sloRequirement": map[string]any{"enabled": true}},
			EditsYAML: pdEdits(e2ePDRelease),
		}, &planned, apiTimeout)
		Expect(planned.Hash).NotTo(BeEmpty())

		DeferCleanup(func() {
			_ = api.call(http.MethodDelete, releasePath(e2ePDRelease), nil, nil, installTimeout)
			_ = dynClient.Resource(dsGVR).Namespace(ns).Delete(context.Background(), e2ePDRelease, metav1.DeleteOptions{})
		})

		By("installing through helm")
		var installed installResult
		api.must(http.MethodPost, "/api/install", installBody{PlanHash: planned.Hash, ApplyWith: "helm"}, &installed, installTimeout)
		_, _ = fmt.Fprintf(GinkgoWriter, "install release=%s revision=%d statusError=%s\n", installed.Release, installed.Revision, installed.StatusError)
		Expect(installed.Release).To(Equal(e2ePDRelease))
		Expect(installed.StatusError).To(BeEmpty())
		Expect(installed.Revision).To(BeNumerically(">", 0))

		By("waiting for the DisaggregatedSet at 2 slices")
		Eventually(func(g Gomega) {
			var st releaseStatusBody
			g.Expect(api.call(http.MethodGet, releasePath(e2ePDRelease)+"/status", nil, &st, apiTimeout)).To(Succeed())
			g.Expect(st.Exists).To(BeTrue())
			g.Expect(st.HelmStatus).To(Equal("deployed"))
			g.Expect(st.Revision).To(Equal(installed.Revision))
			obj, err := dynClient.Resource(dsGVR).Namespace(ns).Get(context.Background(), e2ePDRelease, metav1.GetOptions{})
			g.Expect(err).NotTo(HaveOccurred())
			slices, _, err := unstructured.NestedInt64(obj.Object, "spec", "slices")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(slices).To(Equal(int64(2)))
		}).WithTimeout(5 * time.Minute).WithPolling(2 * time.Second).Should(Succeed())

		// No controller is installed. A stale observedGeneration is a rollout
		// hold and would block the scale-down; unready roles would not.
		markDSCurrent(ns, e2ePDRelease)
		Eventually(func(g Gomega) {
			dep, err := kube.AppsV1().Deployments(ns).Get(context.Background(), "e2e-decisions", metav1.GetOptions{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(dep.Status.AvailableReplicas).To(BeNumerically(">=", 1))
		}).WithTimeout(4 * time.Minute).WithPolling(2 * time.Second).Should(Succeed())

		By("scaling 2 to 1")
		// The DS spec and helm's revision change while the scale's apply still
		// holds the release lock, and a DELETE then is a 409. The applied phase
		// at that revision is written last, under the lock.
		Eventually(func(g Gomega) {
			obj, err := dynClient.Resource(dsGVR).Namespace(ns).Get(context.Background(), e2ePDRelease, metav1.GetOptions{})
			g.Expect(err).NotTo(HaveOccurred())
			slices, _, err := unstructured.NestedInt64(obj.Object, "spec", "slices")
			g.Expect(err).NotTo(HaveOccurred())
			var st releaseStatusBody
			g.Expect(api.call(http.MethodGet, releasePath(e2ePDRelease)+"/status", nil, &st, apiTimeout)).To(Succeed())
			var cur plan.Plan
			g.Expect(api.call(http.MethodGet, releasePath(e2ePDRelease)+"/plan", nil, &cur, apiTimeout)).To(Succeed())
			layer := cur.Layers["autoscale"]
			g.Expect(slices).To(Equal(int64(1)), "helm=%s rev=%d autoscale=%v", st.HelmStatus, st.Revision, layer)
			g.Expect(st.Revision).To(BeNumerically(">", installed.Revision))
			g.Expect(layer).NotTo(BeNil())
			g.Expect(layer["replicaCount"]).To(BeNumerically("==", 1))
			g.Expect(st.Plan).NotTo(BeNil())
			g.Expect(st.Plan.Phase).To(Equal("applied"))
			g.Expect(st.Plan.Revision).To(Equal(st.Revision))
		}).WithTimeout(10 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())
		markDSCurrent(ns, e2ePDRelease)

		By("uninstalling")
		var removed uninstallResult
		api.must(http.MethodDelete, releasePath(e2ePDRelease), nil, &removed, installTimeout)
		Expect(removed.Release).To(Equal(e2ePDRelease))
		Expect(removed.PlanError).To(BeEmpty())
	})
})

// pdEdits renders prefill/decode on a CPU cluster. commandOverride null clears
// the mock catalog's empty list, which the chart rejects beside pd. Pods may
// stay Pending; DirectoryOrCreate keeps the router hostPath valid.
func pdEdits(release string) string {
	return fmt.Sprintf(`replicaCount: 2
serviceId: %s
fullnameOverride: %s
commandOverride: null
model:
  localPath: /tmp/e2e-pd
  hostPathType: DirectoryOrCreate
pd:
  enabled: true
  colocate: false
  prefill:
    gpus: 0
  decode:
    gpus: 0
rdma:
  enabled: true
lws:
  enabled: false
scaler:
  enabled: false
modelRoute:
  enabled: false
cart:
  enabled: false
serviceMonitor:
  enabled: false
`, release, release)
}

func dumpAutoscale(ns, release string) {
	var st releaseStatusBody
	if err := api.call(http.MethodGet, releasePath(release)+"/status", nil, &st, apiTimeout); err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "status: %v\n", err)
	} else {
		_, _ = fmt.Fprintf(GinkgoWriter, "release exists=%v helm=%s rev=%d\n", st.Exists, st.HelmStatus, st.Revision)
	}
	var cur plan.Plan
	if err := api.call(http.MethodGet, releasePath(release)+"/plan", nil, &cur, apiTimeout); err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "plan: %v\n", err)
	} else {
		names := make([]string, 0, len(cur.Layers))
		for name := range cur.Layers {
			names = append(names, name)
		}
		_, _ = fmt.Fprintf(GinkgoWriter, "plan layers=%v autoscale=%v\n", names, cur.Layers["autoscale"])
	}
	if dynClient == nil {
		return
	}
	obj, err := dynClient.Resource(dsGVR).Namespace(ns).Get(context.Background(), release, metav1.GetOptions{})
	if err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "ds: %v\n", err)
		return
	}
	slices, _, _ := unstructured.NestedInt64(obj.Object, "spec", "slices")
	obs, _, _ := unstructured.NestedInt64(obj.Object, "status", "observedGeneration")
	_, _ = fmt.Fprintf(GinkgoWriter, "ds generation=%d observed=%d slices=%d\n", obj.GetGeneration(), obs, slices)
}

// markDSCurrent sets observedGeneration to the live generation and leaves the
// roles unready. Scale-down is allowed to proceed; a rollout hold is not.
func markDSCurrent(ns, name string) {
	obj, err := dynClient.Resource(dsGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())
	obj.Object["status"] = map[string]any{
		"observedGeneration": obj.GetGeneration(),
		"roleStatuses": []any{
			map[string]any{"name": "prefill", "replicas": int64(1), "readyReplicas": int64(0), "updatedReplicas": int64(0)},
			map[string]any{"name": "decode", "replicas": int64(1), "readyReplicas": int64(0), "updatedReplicas": int64(0)},
		},
	}
	_, err = dynClient.Resource(dsGVR).Namespace(ns).UpdateStatus(context.Background(), obj, metav1.UpdateOptions{})
	Expect(err).NotTo(HaveOccurred())
}

func applyDSCRD() {
	f, err := os.CreateTemp("", "disaggregatedset-crd-*.yaml")
	Expect(err).NotTo(HaveOccurred())
	_, err = f.Write(dsCRD)
	Expect(err).NotTo(HaveOccurred())
	Expect(f.Close()).To(Succeed())
	defer os.Remove(f.Name())
	cmd := exec.Command(cfg.kubectlBin, "--context", cfg.context, "apply", "-f", f.Name())
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))
	Eventually(func(g Gomega) {
		established, err := kubectl("get", "crd", "disaggregatedsets.disaggregatedset.x-k8s.io",
			"-o", `jsonpath={.status.conditions[?(@.type=="Established")].status}`)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(established).To(Equal("True"))
	}).WithTimeout(time.Minute).WithPolling(2 * time.Second).Should(Succeed())
	// Established comes before the apiserver serves the schema to server-side
	// apply, which is what helm uses; probe it with the chart's shape.
	Eventually(func(g Gomega) {
		cmd := exec.Command(cfg.kubectlBin, "--context", cfg.context, "apply", "--server-side",
			"--dry-run=server", "--field-manager=e2e-probe", "-f", "-")
		cmd.Stdin = strings.NewReader(dsProbe)
		out, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred(), string(out))
	}).WithTimeout(time.Minute).WithPolling(2 * time.Second).Should(Succeed())
}

const dsProbe = `apiVersion: disaggregatedset.x-k8s.io/v1
kind: DisaggregatedSet
metadata:
  name: e2e-probe
  namespace: default
spec:
  slices: 1
  roles:
    - name: prefill
`

func nginxDeploy(name, app, configMap string) *appsv1.Deployment {
	pod := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:  "nginx",
			Image: "nginx:1.27-alpine",
			Ports: []corev1.ContainerPort{{ContainerPort: 80}},
		}},
	}
	if configMap != "" {
		pod.Containers[0].VolumeMounts = []corev1.VolumeMount{{
			Name: "cfg", MountPath: "/etc/nginx/nginx.conf", SubPath: "nginx.conf",
		}}
		pod.Volumes = []corev1.Volume{{
			Name: "cfg",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: configMap},
				},
			},
		}}
	}
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": app}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": app}},
				Spec:       pod,
			},
		},
	}
}
