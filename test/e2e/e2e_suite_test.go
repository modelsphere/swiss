//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/modelsphere/swiss/internal/llmsvc"
	"github.com/modelsphere/swiss/test/utils"
)

const (
	swissRelease        = "swiss"
	operatorNamespace   = "llmscaleoperator-system"
	operatorDeployment  = "llmscaleoperator-controller-manager"
	scalerCRD           = "llmscalers.autoscaling.modelsphere.dev"
	serviceMonitorURL   = "https://raw.githubusercontent.com/prometheus-operator/prometheus-operator/%s/example/prometheus-operator-crd/monitoring.coreos.com_servicemonitors.yaml"
	llmOperatorURL      = "https://raw.githubusercontent.com/modelsphere/llm-operator/%s/dist/install.yaml"
	defaultReadySeconds = 300
	defaultLocalPort    = 18080
)

type suiteConfig struct {
	kindBin      string
	kubectlBin   string
	helmBin      string
	cluster      string
	context      string
	imageRepo    string
	imageTag     string
	skipBuild    bool
	catalog      string
	chartRepo    string
	llmOperator  string
	autoconfig   string
	sloChart     string
	prometheusOp string
	model        string
	modelNS      string
	swissNS      string
	readyTimeout time.Duration
	localPort    int
}

var (
	cfg             suiteConfig
	root            string
	restCfg         *rest.Config
	kube            *kubernetes.Clientset
	dynClient       dynamic.Interface
	api             *apiClient
	portForward     *exec.Cmd
	portForwardDone chan struct{}
	beforeSuiteOK   bool
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "swiss e2e")
}

var _ = BeforeSuite(func() {
	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	cfg = loadConfig()
	Expect(os.Setenv("KIND", cfg.kindBin)).To(Succeed())
	Expect(os.Setenv("KIND_CLUSTER", cfg.cluster)).To(Succeed())

	for _, bin := range []string{cfg.kindBin, cfg.kubectlBin, cfg.helmBin} {
		_, err := exec.LookPath(bin)
		Expect(err).NotTo(HaveOccurred(), "%s is not installed", bin)
	}

	var err error
	root, err = utils.GetProjectDir()
	Expect(err).NotTo(HaveOccurred())

	By("using kube context " + cfg.context)
	loading := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{CurrentContext: cfg.context}
	restCfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loading, overrides).ClientConfig()
	Expect(err).NotTo(HaveOccurred(), "kube context %s: is the kind cluster up? run make setup-test-e2e", cfg.context)
	kube, err = kubernetes.NewForConfig(restCfg)
	Expect(err).NotTo(HaveOccurred())
	dynClient, err = dynamic.NewForConfig(restCfg)
	Expect(err).NotTo(HaveOccurred())

	image := cfg.imageRepo + ":" + cfg.imageTag
	if cfg.skipBuild {
		_, _ = fmt.Fprintf(GinkgoWriter, "SKIP_BUILD set: not building %s\n", image)
	} else {
		By("building the swissd image")
		cmd := exec.Command("make", "docker-build", "IMG="+image)
		out, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), out)
	}
	By("loading the swissd image into kind")
	Expect(utils.LoadImageToKindClusterWithName(image)).To(Succeed())

	installDependencies()
	installSwiss(image)
	startPortForward()
	beforeSuiteOK = true
})

var _ = AfterSuite(func() {
	stopPortForward()
	if !beforeSuiteOK && kube != nil {
		dumpClusterState("BeforeSuite")
	}
})

func loadConfig() suiteConfig {
	c := suiteConfig{
		kindBin:      envOr("KIND", "kind"),
		kubectlBin:   envOr("KUBECTL", "kubectl"),
		helmBin:      envOr("HELM", "helm"),
		cluster:      envOr("KIND_CLUSTER", "swiss-test-e2e"),
		catalog:      envOr("CATALOG", "https://modelsphere.github.io/model-catalog/"),
		chartRepo:    envOr("CHART_REPO", "https://modelsphere.github.io/helm-charts"),
		llmOperator:  envOr("LLM_OPERATOR_VERSION", "v0.5.0"),
		autoconfig:   envOr("AUTOCONFIG_CHART_VERSION", "0.4.1"),
		sloChart:     envOr("SLO_CHART_VERSION", "0.3.5"),
		prometheusOp: envOr("PROMETHEUS_OPERATOR_VERSION", "v0.94.1"),
		model:        envOr("MODEL", "mock-llm"),
		modelNS:      envOr("MODEL_NS", "models"),
		swissNS:      envOr("SWISS_NS", "swiss"),
		readyTimeout: time.Duration(envInt("READY_TIMEOUT", defaultReadySeconds)) * time.Second,
		localPort:    envInt("LOCAL_PORT", defaultLocalPort),
	}
	c.context = "kind-" + c.cluster
	c.skipBuild = os.Getenv("SKIP_BUILD") == "true" || os.Getenv("SKIP_BUILD") == "1"
	c.imageRepo, c.imageTag = imageFromEnv()
	return c
}

func imageFromEnv() (string, string) {
	if repo, ok := os.LookupEnv("IMAGE_REPO"); ok && repo != "" {
		tag := os.Getenv("IMAGE_TAG")
		if tag == "" {
			tag = "e2e"
		}
		return repo, tag
	}
	return splitImage(envOr("IMG", "swissd:e2e"))
}

func splitImage(img string) (string, string) {
	slash := strings.LastIndex(img, "/")
	colon := strings.LastIndex(img, ":")
	if colon > slash {
		return img[:colon], img[colon+1:]
	}
	return img, "e2e"
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	Expect(err).NotTo(HaveOccurred(), "%s=%q is not an integer", key, v)
	return n
}

func installDependencies() {
	By("installing the ServiceMonitor CRD")
	mustKubectl("apply", "--server-side", "-f", fmt.Sprintf(serviceMonitorURL, cfg.prometheusOp))

	By("installing ModelRoute and LLMSLORequirement CRDs")
	applyChartCRDs("autoconfig", cfg.autoconfig)
	applyChartCRDs("llm-slo-decision-gen", cfg.sloChart)

	By("installing llm-operator " + cfg.llmOperator)
	mustKubectl("apply", "--server-side", "-f", fmt.Sprintf(llmOperatorURL, cfg.llmOperator))

	By("waiting for operator CRDs")
	mustKubectl("wait", "--for=condition=Established", "--timeout=60s",
		"crd/"+llmsvc.CRDName, "crd/"+scalerCRD)

	By("waiting for the operator deployment")
	mustKubectl("-n", operatorNamespace, "rollout", "status", "deploy/"+operatorDeployment, "--timeout=180s")

	By("creating namespace " + cfg.modelNS)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: cfg.modelNS},
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred())
	}
}

func installSwiss(image string) {
	By("installing swiss chart (" + image + ")")
	out, err := helm(
		"upgrade", "--install", swissRelease, "./helm/swiss",
		"-n", cfg.swissNS, "--create-namespace",
		"-f", filepath.Join(root, "test/e2e/testdata/values.yaml"),
		"--set", "image.repository="+cfg.imageRepo,
		"--set", "image.tag="+cfg.imageTag,
		"--set", "rbac.namespaces={"+cfg.modelNS+"}",
		"--wait", "--timeout", "180s",
	)
	Expect(err).NotTo(HaveOccurred(), out)

	// A reused cluster keeps the old pod when only the image content changed.
	By("restarting swiss")
	mustKubectl("-n", cfg.swissNS, "rollout", "restart", "deploy/"+swissRelease)
	mustKubectl("-n", cfg.swissNS, "rollout", "status", "deploy/"+swissRelease, "--timeout=120s")
}

func applyChartCRDs(chart, version string) {
	By(fmt.Sprintf("applying CRDs from %s %s", chart, version))
	show := exec.Command(cfg.helmBin, "--kube-context", cfg.context,
		"show", "crds", chart, "--repo", cfg.chartRepo, "--version", version)
	show.Dir = root
	var stderr bytes.Buffer
	show.Stderr = &stderr
	crds, err := show.Output()
	Expect(err).NotTo(HaveOccurred(), stderr.String())

	apply := exec.Command(cfg.kubectlBin, "--context", cfg.context, "apply", "--server-side", "-f", "-")
	apply.Stdin = bytes.NewReader(crds)
	out, err := utils.Run(apply)
	Expect(err).NotTo(HaveOccurred(), out)
}

func startPortForward() {
	api = &apiClient{
		base: fmt.Sprintf("http://127.0.0.1:%d", cfg.localPort),
		http: &http.Client{},
	}
	By(fmt.Sprintf("port-forwarding svc/swiss to %d", cfg.localPort))
	cmd := exec.Command(cfg.kubectlBin, "--context", cfg.context,
		"-n", cfg.swissNS, "port-forward", "svc/"+swissRelease,
		fmt.Sprintf("%d:80", cfg.localPort))
	setPortForwardAttr(cmd)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	Expect(cmd.Start()).To(Succeed())
	portForward = cmd
	portForwardDone = make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(portForwardDone)
	}()

	Eventually(func(g Gomega) {
		select {
		case <-portForwardDone:
			g.Expect(true).To(BeFalse(), "port-forward exited; is port %d free? set LOCAL_PORT", cfg.localPort)
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, api.base+"/healthz", nil)
		g.Expect(err).NotTo(HaveOccurred())
		resp, err := http.DefaultClient.Do(req)
		g.Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		g.Expect(resp.StatusCode).To(Equal(http.StatusOK))
	}).WithTimeout(30 * time.Second).WithPolling(time.Second).Should(Succeed())
}

func stopPortForward() {
	if portForward == nil || portForward.Process == nil {
		return
	}
	_ = killPortForward(portForward)
	if portForwardDone != nil {
		select {
		case <-portForwardDone:
		case <-time.After(5 * time.Second):
		}
	}
	portForward = nil
}

func kubectl(args ...string) (string, error) {
	cmd := exec.Command(cfg.kubectlBin, append([]string{"--context", cfg.context}, args...)...)
	return utils.Run(cmd)
}

func mustKubectl(args ...string) {
	out, err := kubectl(args...)
	Expect(err).NotTo(HaveOccurred(), out)
}

func helm(args ...string) (string, error) {
	cmd := exec.Command(cfg.helmBin, append([]string{"--kube-context", cfg.context}, args...)...)
	return utils.Run(cmd)
}

func helmReleasePresent(namespace, name string) bool {
	cmd := exec.Command(cfg.helmBin, "--kube-context", cfg.context, "status", name, "-n", namespace)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

func dumpClusterState(spec string) {
	_, _ = fmt.Fprintf(GinkgoWriter, "\n==> dumping cluster state (%s)\n", spec)
	dumpCmd := func(title string, args ...string) {
		out, err := kubectl(args...)
		_, _ = fmt.Fprintf(GinkgoWriter, "\n--- %s\n", title)
		if out != "" {
			_, _ = fmt.Fprintf(GinkgoWriter, "%s\n", out)
		}
		if err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "error: %v\n", err)
		}
	}
	dumpCmd("pods", "get", "pods", "-A", "-o", "wide")
	out, err := kubectl("get", "events", "-A", "--sort-by=.lastTimestamp")
	_, _ = fmt.Fprintf(GinkgoWriter, "\n--- events\n%s\n", tail(out, 60))
	if err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "events error: %v\n", err)
	}
	dumpCmd("llmservices", "-n", cfg.modelNS, "get", "llmservices", "-o", "yaml")
	dumpCmd("swiss logs", "-n", cfg.swissNS, "logs", "deploy/"+swissRelease, "--tail=200")
	dumpCmd("operator logs", "-n", operatorNamespace, "logs", "deploy/"+operatorDeployment, "--tail=100")

	names, err := kubectl("-n", cfg.modelNS, "get", "pods", "-o", "name")
	if err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "list model pods: %v\n%s\n", err, names)
		return
	}
	for _, p := range utils.GetNonEmptyLines(names) {
		desc, derr := kubectl("-n", cfg.modelNS, "describe", p)
		_, _ = fmt.Fprintf(GinkgoWriter, "\n--- describe %s\n%s\n", p, tail(desc, 30))
		if derr != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "describe error: %v\n", derr)
		}
		logs, lerr := kubectl("-n", cfg.modelNS, "logs", p, "--all-containers", "--tail=50")
		_, _ = fmt.Fprintf(GinkgoWriter, "\n--- logs %s\n%s\n", p, logs)
		if lerr != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "logs error: %v\n", lerr)
		}
	}
}

func tail(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
