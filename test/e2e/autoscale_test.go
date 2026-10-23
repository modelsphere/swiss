//go:build e2e

package e2e

import (
	"context"
	_ "embed"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/values"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

	It("records a decision for a seeded disaggregated release", func() {
		ns := cfg.modelNS
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		applyDSCRD()
		DeferCleanup(func() {
			cmd := exec.Command(cfg.kubectlBin, "--context", cfg.context, "delete", "crd",
				"disaggregatedsets.disaggregatedset.x-k8s.io", "--ignore-not-found")
			_, _ = cmd.CombinedOutput()
		})

		decisionBody := fmt.Sprintf(`events {}
http {
  server {
    listen 80;
    location = /decisions {
      default_type application/json;
      return 200 '{"apiVersion":"llmscaling.inference.x-k8s.io/v1alpha1","decisions":[{"namespace":"%s","serviceId":"%s","replicas":{"active":2}}]}';
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

		planCM := e2ePlanConfigMap(ns)
		_, err = kube.CoreV1().ConfigMaps(ns).Create(ctx, planCM, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			_ = kube.CoreV1().ConfigMaps(ns).Delete(context.Background(), planCM.Name, metav1.DeleteOptions{})
		})

		ds := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "disaggregatedset.x-k8s.io/v1",
			"kind":       "DisaggregatedSet",
			"metadata":   map[string]any{"name": e2ePDRelease},
			"spec":       map[string]any{"slices": int64(1)},
		}}
		created, err := dynClient.Resource(dsGVR).Namespace(ns).Create(ctx, ds, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			_ = dynClient.Resource(dsGVR).Namespace(ns).Delete(context.Background(), e2ePDRelease, metav1.DeleteOptions{})
		})
		created.Object["status"] = map[string]any{
			"observedGeneration": created.GetGeneration(),
			"roleStatuses": []any{map[string]any{
				"name": "prefill", "replicas": int64(1), "readyReplicas": int64(1), "updatedReplicas": int64(1),
			}},
		}
		_, err = dynClient.Resource(dsGVR).Namespace(ns).UpdateStatus(ctx, created, metav1.UpdateOptions{})
		Expect(err).NotTo(HaveOccurred())

		router := nginxDeploy(e2ePDRelease+"-router-s0", e2ePDRelease+"-router", "")
		_, err = kube.AppsV1().Deployments(ns).Create(ctx, router, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			_ = kube.AppsV1().Deployments(ns).Delete(context.Background(), router.Name, metav1.DeleteOptions{})
		})

		api.must(http.MethodPut, releasePath(e2ePDRelease)+"/autoscaling", map[string]any{
			"enabled": true, "minSlices": 1, "maxSlices": 4,
		}, nil, apiTimeout)
		DeferCleanup(func() {
			_ = api.call(http.MethodDelete, releasePath(e2ePDRelease)+"/autoscaling", nil, nil, apiTimeout)
		})

		var view struct {
			Found  bool `json:"found"`
			Status struct {
				LastDecision *int   `json:"lastDecision"`
				HoldReason   string `json:"holdReason"`
				LastError    string `json:"lastError"`
			} `json:"status"`
		}
		Eventually(func(g Gomega) {
			g.Expect(api.call(http.MethodGet, releasePath(e2ePDRelease)+"/autoscaling", nil, &view, apiTimeout)).To(Succeed())
			g.Expect(view.Found).To(BeTrue())
			g.Expect(view.Status.LastDecision).NotTo(BeNil())
			g.Expect(*view.Status.LastDecision).To(Equal(2))
		}).WithTimeout(90 * time.Second).WithPolling(2 * time.Second).Should(Succeed())
		_, _ = fmt.Fprintf(GinkgoWriter, "autoscale hold=%q err=%q decision=%d\n", view.Status.HoldReason, view.Status.LastError, *view.Status.LastDecision)

		got, err := dynClient.Resource(dsGVR).Namespace(ns).Get(ctx, e2ePDRelease, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		slices, _, err := unstructured.NestedInt64(got.Object, "spec", "slices")
		Expect(err).NotTo(HaveOccurred())
		Expect(slices).To(Equal(int64(1)))

		_, err = kube.AppsV1().Deployments(ns).Get(ctx, e2ePDRelease+"-router-s0", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(apierrors.IsNotFound(err)).To(BeFalse())
	})
})

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
}

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

func e2ePlanConfigMap(ns string) *corev1.ConfigMap {
	p := &plan.Plan{
		APIVersion: plan.APIVersion,
		Release:    plan.Release{Name: e2ePDRelease, Namespace: ns},
		Source:     plan.SourceRef{Model: e2ePDRelease, Variant: "pd"},
		Chart:      plan.ChartRef{Name: "sglang", Version: "0.0.0", Repo: "oci://example.invalid/charts"},
		Engine:     "sglang",
		Profile:    "kind-e2e",
		Layers: map[string]values.Tree{
			"form": {
				"replicaCount":     1,
				"serviceId":        e2ePDRelease,
				"fullnameOverride": e2ePDRelease,
				"pd":               values.Tree{"enabled": true, "colocate": false},
			},
		},
	}
	Expect(p.ComputeHash()).To(Succeed())
	files, err := p.Files("")
	Expect(err).NotTo(HaveOccurred())
	files["status.yaml"] = "phase: applied\n"
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "swiss-plan-" + e2ePDRelease, Namespace: ns},
		Data:       files,
	}
}
