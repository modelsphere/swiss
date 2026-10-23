//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilnet "k8s.io/apimachinery/pkg/util/net"

	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/llmsvc"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/site"
)

const (
	apiTimeout     = 60 * time.Second
	installTimeout = 16 * time.Minute
	goneTimeout    = 180 * time.Second
	readyPoll      = 5 * time.Second
	gonePoll       = 3 * time.Second
)

// Setup specs carry both backend labels so -ginkgo.label-filter=helm still
// logs in, writes the profile and reads the catalog before that deploy.
var _ = Describe("swissd", Ordered, func() {
	AfterEach(func() {
		if CurrentSpecReport().Failed() {
			dumpClusterState(CurrentSpecReport().FullText())
		}
	})

	It("answers /healthz and /readyz", Label("helm", "llmsvc"), func() {
		var health struct {
			Status string `json:"status"`
		}
		api.must(http.MethodGet, "/healthz", nil, &health, apiTimeout)
		Expect(health.Status).To(Equal("ok"))

		var ready struct {
			Ready bool `json:"ready"`
		}
		api.must(http.MethodGet, "/readyz", nil, &ready, apiTimeout)
		Expect(ready.Ready).To(BeTrue())
	})

	It("logs in with the swiss-auth Secret", Label("helm", "llmsvc"), func() {
		user, pass := chartCredentials()
		var resp struct {
			Token         string `json:"token"`
			Authenticated bool   `json:"authenticated"`
			User          string `json:"user"`
		}
		api.must(http.MethodPost, "/api/login", loginBody{Username: user, Password: pass}, &resp, apiTimeout)
		Expect(resp.Authenticated).To(BeTrue())
		Expect(resp.Token).NotTo(BeEmpty())
		Expect(resp.User).To(Equal(user))
		api.token = resp.Token
	})

	It("saves the site profile and reads the cluster", Label("helm", "llmsvc"), func() {
		yamlText, prof := renderProfile()
		api.must(http.MethodPut, "/api/profile", profilePut{YAML: yamlText}, nil, apiTimeout)

		var info clusterBody
		api.must(http.MethodGet, "/api/cluster", nil, &info, apiTimeout)
		_, _ = fmt.Fprintf(GinkgoWriter, "cluster name=%s llmservice=%+v\n", info.Name, info.LLMService)
		Expect(info.Name).To(Equal(prof.Name))
		Expect(info.LLMService).NotTo(BeNil())
	})

	It("reads the catalog model", Label("helm", "llmsvc"), func() {
		var list struct {
			Models []catalog.Model `json:"models"`
		}
		api.must(http.MethodGet, "/api/catalog", nil, &list, apiTimeout)
		var names []string
		found := false
		for _, m := range list.Models {
			names = append(names, m.Name)
			if m.Name == cfg.model {
				found = true
			}
		}
		Expect(found).To(BeTrue(), "%s is not in the catalog at %s (names: %s)", cfg.model, cfg.catalog, strings.Join(names, ", "))

		var one struct {
			Schema  string          `json:"schema"`
			Model   catalog.Model   `json:"model"`
			Variant catalog.Variant `json:"variant"`
		}
		api.must(http.MethodGet, "/api/catalog/"+url.PathEscape(cfg.model), nil, &one, apiTimeout)
		_, _ = fmt.Fprintf(GinkgoWriter, "catalog model=%s schema=%s variant=%s\n", one.Model.Name, one.Schema, one.Variant.ID)
		Expect(one.Model.Name).To(Equal(cfg.model))
		Expect(one.Schema).NotTo(BeEmpty())
		Expect(one.Variant.ID).NotTo(BeEmpty())
	})

	for _, backend := range []string{"helm", "llmsvc"} {
		deploySpec(backend)
	}
})

func deploySpec(backend string) {
	It("deploys and removes a "+backend+" release", Label(backend), func() {
		deployCycle(backend)
	})
}

func deployCycle(backend string) {
	rel := strings.ReplaceAll(fmt.Sprintf("e2e-%s-%s", cfg.model, backend), ".", "-")

	By(fmt.Sprintf("[%s] planning %s as %s/%s", backend, cfg.model, cfg.modelNS, rel))
	var planned plan.Plan
	api.must(http.MethodPost, "/api/plans", planBody{
		Model: cfg.model, Release: rel, Namespace: cfg.modelNS, ServiceID: rel,
	}, &planned, apiTimeout)
	Expect(planned.Hash).NotTo(BeEmpty())
	_, _ = fmt.Fprintf(GinkgoWriter, "plan hash=%s source=%+v\n", planned.Hash, planned.Source)

	By("[" + backend + "] installing")
	var installed installResult
	api.must(http.MethodPost, "/api/install", installBody{PlanHash: planned.Hash, ApplyWith: backend}, &installed, installTimeout)
	_, _ = fmt.Fprintf(GinkgoWriter, "install release=%s revision=%d statusError=%s\n", installed.Release, installed.Revision, installed.StatusError)
	Expect(installed.Release).To(Equal(rel))

	By(fmt.Sprintf("[%s] waiting for %s to be ready", backend, rel))
	var st releaseStatusBody
	Eventually(func(g Gomega) {
		g.Expect(api.call(http.MethodGet, releasePath(rel)+"/status", nil, &st, apiTimeout)).To(Succeed())
		g.Expect(st.Exists).To(BeTrue())
		g.Expect(st.Total).To(BeNumerically(">", 0))
		g.Expect(st.Ready).To(Equal(st.Total))
	}).WithTimeout(cfg.readyTimeout).WithPolling(readyPoll).Should(Succeed())
	_, _ = fmt.Fprintf(GinkgoWriter, "status backend=%s revision=%d helmStatus=%s ready=%d total=%d model=%s\n",
		st.Backend, st.Revision, st.HelmStatus, st.Ready, st.Total, st.Model)

	By("[" + backend + "] asking the engine")
	engineAnswers(rel)

	var objects objectBody
	api.must(http.MethodGet, releasePath(rel)+"/objects", nil, &objects, apiTimeout)
	_, _ = fmt.Fprintf(GinkgoWriter, "objects: %s\n", strings.Join(objectKinds(objects), ", "))

	By("[" + backend + "] checking the deployment list")
	var deps deploymentList
	api.must(http.MethodGet, "/api/deployments?perPage=100", nil, &deps, apiTimeout)
	listed := false
	for _, d := range deps.Deployments {
		if d.Release == rel && d.Namespace == cfg.modelNS && d.Backend == backend {
			listed = true
		}
	}
	Expect(listed).To(BeTrue(), "%s is not listed under backend %s", rel, backend)

	By("[" + backend + "] uninstalling")
	var removed uninstallResult
	api.must(http.MethodDelete, releasePath(rel), nil, &removed, installTimeout)
	_, _ = fmt.Fprintf(GinkgoWriter, "uninstall release=%s planError=%s\n", removed.Release, removed.PlanError)
	Expect(removed.Release).To(Equal(rel))

	By(fmt.Sprintf("[%s] waiting for %s to be gone", backend, rel))
	Eventually(func(g Gomega) {
		pods, err := vllmPods(rel)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(pods).To(BeEmpty())
		exists, err := llmServiceExists(rel)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(exists).To(BeFalse())
		g.Expect(helmReleasePresent(cfg.modelNS, rel)).To(BeFalse())
	}).WithTimeout(goneTimeout).WithPolling(gonePoll).Should(Succeed())
}

func engineAnswers(rel string) {
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()
	list, err := kube.CoreV1().Services(cfg.modelNS).List(ctx, metav1.ListOptions{
		LabelSelector: "app=" + rel + "-vllm",
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(list.Items).NotTo(BeEmpty(), "no service labeled app=%s-vllm", rel)
	svc := list.Items[0]
	var port int32
	for _, p := range svc.Spec.Ports {
		if p.Name == "http-inference" {
			port = p.Port
		}
	}
	Expect(port).NotTo(BeZero(), "service %s has no http-inference port", svc.Name)
	portStr := strconv.Itoa(int(port))

	raw, err := kube.CoreV1().Services(cfg.modelNS).ProxyGet("http", svc.Name, portStr, "v1/models", nil).DoRaw(ctx)
	Expect(err).NotTo(HaveOccurred(), string(raw))
	var models modelList
	Expect(json.Unmarshal(raw, &models)).To(Succeed(), string(raw))
	var ids []string
	found := false
	for _, m := range models.Data {
		ids = append(ids, m.ID)
		if m.ID == cfg.model {
			found = true
		}
	}
	Expect(found).To(BeTrue(), "service %s does not list %s in /v1/models (ids: %s)", svc.Name, cfg.model, strings.Join(ids, ", "))

	body, err := json.Marshal(chatRequest{
		Model:     cfg.model,
		Messages:  []chatMessage{{Role: "user", Content: "ping"}},
		MaxTokens: 4,
	})
	Expect(err).NotTo(HaveOccurred())
	chatCtx, chatCancel := context.WithTimeout(context.Background(), apiTimeout)
	defer chatCancel()
	result := kube.CoreV1().RESTClient().Post().
		Namespace(cfg.modelNS).
		Resource("services").
		Name(utilnet.JoinSchemeNamePort("http", svc.Name, portStr)).
		SubResource("proxy").
		Suffix("v1/chat/completions").
		SetHeader("Content-Type", "application/json").
		Body(body).
		Do(chatCtx)
	raw, err = result.Raw()
	Expect(err).NotTo(HaveOccurred(), string(raw))
	var chat chatResponse
	Expect(json.Unmarshal(raw, &chat)).To(Succeed(), string(raw))
	_, _ = fmt.Fprintf(GinkgoWriter, "chat model=%s choices=%d\n", chat.Model, len(chat.Choices))
	Expect(chat.Choices).NotTo(BeEmpty(), "service %s did not answer a chat completion", svc.Name)
}

func chartCredentials() (string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sec, err := kube.CoreV1().Secrets(cfg.swissNS).Get(ctx, swissRelease+"-auth", metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())
	user := string(sec.Data["username"])
	pass := string(sec.Data["password"])
	Expect(user).NotTo(BeEmpty())
	Expect(pass).NotTo(BeEmpty())
	return user, pass
}

func renderProfile() (string, *site.Profile) {
	raw, err := os.ReadFile(filepath.Join(root, "test/e2e/testdata/profile.yaml"))
	Expect(err).NotTo(HaveOccurred())
	text := string(raw)
	text = strings.ReplaceAll(text, "@CATALOG@", cfg.catalog)
	text = strings.ReplaceAll(text, "@CHART_REPO@", cfg.chartRepo)
	text = strings.ReplaceAll(text, "@MODEL_NS@", cfg.modelNS)
	prof, err := site.Parse([]byte(text), "profile fixture")
	Expect(err).NotTo(HaveOccurred())
	Expect(prof.Namespace).To(Equal(cfg.modelNS))
	Expect(prof.ChartRepo).To(Equal(cfg.chartRepo))
	Expect(prof.Catalogs).NotTo(BeEmpty())
	Expect(prof.Catalogs[0].URL).To(Equal(cfg.catalog))
	return text, prof
}

func vllmPods(rel string) ([]corev1.Pod, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	list, err := kube.CoreV1().Pods(cfg.modelNS).List(ctx, metav1.ListOptions{
		LabelSelector: "app=" + rel + "-vllm",
	})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func llmServiceExists(name string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := dynClient.Resource(llmsvc.GVR).Namespace(cfg.modelNS).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func releasePath(rel string) string {
	return "/api/releases/" + cfg.modelNS + "/" + rel
}

func objectKinds(body objectBody) []string {
	seen := map[string]struct{}{}
	var kinds []string
	for _, o := range body.Objects {
		if o.Ref.Kind == "" {
			continue
		}
		if _, ok := seen[o.Ref.Kind]; ok {
			continue
		}
		seen[o.Ref.Kind] = struct{}{}
		kinds = append(kinds, o.Ref.Kind)
	}
	sort.Strings(kinds)
	return kinds
}

type apiClient struct {
	base  string
	token string
	http  *http.Client
}

type apiStatusError struct {
	method string
	path   string
	status int
	body   string
}

func (e *apiStatusError) Error() string {
	return fmt.Sprintf("%s %s -> %d\n%s", e.method, e.path, e.status, e.body)
}

func (c *apiClient) must(method, path string, in, out any, timeout time.Duration) {
	Expect(c.call(method, path, in, out, timeout)).To(Succeed())
}

func (c *apiClient) call(method, path string, in, out any, timeout time.Duration) error {
	var body io.Reader
	if in != nil {
		buf, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(buf)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &apiStatusError{method: method, path: path, status: resp.StatusCode, body: string(raw)}
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s %s: %w\n%s", method, path, err, raw)
		}
	}
	return nil
}

type loginBody struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type profilePut struct {
	YAML string `json:"yaml"`
}

type clusterBody struct {
	Name       string `json:"name"`
	LLMService *struct {
		Served      bool   `json:"served"`
		Available   bool   `json:"available"`
		ApplyWith   string `json:"applyWith"`
		NewInstalls string `json:"newInstalls"`
	} `json:"llmservice"`
}

type planBody struct {
	Model     string `json:"model"`
	Release   string `json:"release"`
	Namespace string `json:"namespace"`
	ServiceID string `json:"serviceId"`
	EditsYAML string `json:"editsYAML,omitempty"`
}

type installBody struct {
	PlanHash  string `json:"planHash"`
	ApplyWith string `json:"applyWith"`
}

type installResult struct {
	Release     string `json:"release"`
	Revision    int    `json:"revision"`
	StatusError string `json:"statusError"`
}

type releaseStatusBody struct {
	Exists     bool   `json:"exists"`
	Total      int    `json:"total"`
	Ready      int    `json:"ready"`
	Backend    string `json:"backend"`
	Revision   int    `json:"revision"`
	HelmStatus string `json:"helmStatus"`
	Model      string `json:"model"`
}

type objectBody struct {
	Objects []struct {
		Ref cluster.ObjectRef `json:"ref"`
	} `json:"objects"`
}

type deploymentList struct {
	Deployments []struct {
		Release   string `json:"release"`
		Namespace string `json:"namespace"`
		Backend   string `json:"backend"`
	} `json:"deployments"`
}

type uninstallResult struct {
	Release   string `json:"release"`
	PlanError string `json:"planError"`
}

type modelList struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

type chatRequest struct {
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
	} `json:"choices"`
}
