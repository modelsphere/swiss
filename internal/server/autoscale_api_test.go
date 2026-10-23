package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/autoscale"
	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/llmsvc"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/testutil/cases"
	"github.com/modelsphere/swiss/internal/values"
	"gopkg.in/yaml.v3"
)

type apiCase struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Backend     string    `json:"backend,omitempty"`
	AllowDeploy *bool     `json:"allowDeploy,omitempty"`
	Managed     *bool     `json:"managed,omitempty"`
	PD          *bool     `json:"pd,omitempty"`
	Colocate    *bool     `json:"colocate,omitempty"`
	WantLabels  bool      `json:"wantLabels,omitempty"`
	Uninstall   bool      `json:"uninstall,omitempty"`
	Release     string    `json:"release,omitempty"`
	Namespace   string    `json:"namespace,omitempty"`
	Steps       []apiStep `json:"steps,omitempty"`
}

type apiStep struct {
	Method        string         `json:"method"`
	Body          map[string]any `json:"body,omitempty"`
	WantStatus    int            `json:"wantStatus"`
	WantFound     *bool          `json:"wantFound,omitempty"`
	WantEnabled   *bool          `json:"wantEnabled,omitempty"`
	WantMin       *int           `json:"wantMin,omitempty"`
	WantMax       *int           `json:"wantMax,omitempty"`
	ErrorContains string         `json:"errorContains,omitempty"`
}

// mirrorWriter copies ConfigMap writes onto the probe the server reads.
// Production uses one client for both; the fakes are two values.
type mirrorWriter struct {
	*fakeWriter
	maps      map[string]map[string]string
	mapLabels map[string]map[string]string
}

func (m *mirrorWriter) PutConfigMap(ctx context.Context, ref string, data, labels map[string]string) error {
	if err := m.fakeWriter.PutConfigMap(ctx, ref, data, labels); err != nil {
		return err
	}
	m.remember(ref, m.written[ref], nil)
	return nil
}

func (m *mirrorWriter) PutLabeledConfigMap(ctx context.Context, ref string, data, labels map[string]string) error {
	if err := m.fakeWriter.PutLabeledConfigMap(ctx, ref, data, labels); err != nil {
		return err
	}
	m.remember(ref, m.written[ref], m.labels[ref])
	return nil
}

func (m *mirrorWriter) DeleteConfigMap(ctx context.Context, ref string) error {
	if err := m.fakeWriter.DeleteConfigMap(ctx, ref); err != nil {
		return err
	}
	delete(m.maps, ref)
	delete(m.mapLabels, ref)
	return nil
}

func (m *mirrorWriter) remember(ref string, data, labels map[string]string) {
	if m.maps != nil && data != nil {
		m.maps[ref] = cloneData(data)
	}
	if m.mapLabels != nil && labels != nil {
		m.mapLabels[ref] = cloneData(labels)
	}
}

func TestAutoscalingAPI(t *testing.T) {
	cases.Run(t, "testdata/autoscale-api", func(t *testing.T, tc apiCase) {
		if tc.Uninstall {
			runUninstallCase(t, tc)
			return
		}
		runAPICase(t, tc)
	})
}

func runAPICase(t *testing.T, tc apiCase) {
	t.Helper()
	ns, release := tc.Namespace, tc.Release
	if ns == "" {
		ns = "models"
	}
	if release == "" {
		release = "pd"
	}
	allow := boolOr(tc.AllowDeploy, true)
	managed := boolOr(tc.Managed, true)
	pdOn := boolOr(tc.PD, true)
	colocate := boolOr(tc.Colocate, false)
	backend := tc.Backend
	if backend == "" {
		backend = backendHelm
	}

	var (
		srv *httptest.Server
		s   *Server
		w   *fakeWriter
	)
	if backend == backendLLMSVC {
		srv, s, w = llmAutoscaleServer(t, ns, release, managed, pdOn, colocate, allow)
	} else {
		srv, s, w = helmAutoscaleServer(t, ns, release, managed, pdOn, colocate, allow)
	}

	path := "/api/releases/" + ns + "/" + release + "/autoscaling"
	for i, step := range tc.Steps {
		code, body := callJSON(t, srv, step.Method, path, step.Body)
		if code != step.WantStatus {
			t.Fatalf("step %d %s: status %d, want %d: %v", i, step.Method, code, step.WantStatus, body)
		}
		if step.ErrorContains != "" && !strings.Contains(errText(body), step.ErrorContains) {
			t.Fatalf("step %d error %q, want substring %q", i, errText(body), step.ErrorContains)
		}
		if step.WantFound != nil && body["found"] != *step.WantFound {
			t.Fatalf("step %d found %v, want %v (%v)", i, body["found"], *step.WantFound, body)
		}
		cfg, _ := body["config"].(map[string]any)
		if step.WantEnabled != nil {
			if cfg["enabled"] != *step.WantEnabled {
				t.Fatalf("step %d enabled %v, want %v", i, cfg["enabled"], *step.WantEnabled)
			}
		}
		if step.WantMin != nil && jsonInt(cfg["minSlices"]) != *step.WantMin {
			t.Fatalf("step %d min %v, want %d", i, cfg["minSlices"], *step.WantMin)
		}
		if step.WantMax != nil && jsonInt(cfg["maxSlices"]) != *step.WantMax {
			t.Fatalf("step %d max %v, want %d", i, cfg["maxSlices"], *step.WantMax)
		}
	}
	if tc.WantLabels && w != nil {
		ref := autoscaleRef(ns, release)
		labels := w.labels[ref]
		if labels["owner"] != "swiss" || labels["name"] != release {
			t.Fatalf("labels %v, want owner=swiss name=%s", labels, release)
		}
	}
	_ = s
}

func runUninstallCase(t *testing.T, tc apiCase) {
	t.Helper()
	ns, release := tc.Namespace, tc.Release
	if ns == "" {
		ns = "models"
	}
	if release == "" {
		release = "glm-53"
	}
	ref := autoscaleRef(ns, release)
	var srv *httptest.Server
	var w *fakeWriter
	if tc.Backend == backendLLMSVC {
		probe := llmProbe()
		probe.MapLabels = map[string]map[string]string{}
		probe.Deps = map[string]cluster.Deployment{}
		probe.DSs = map[string]cluster.DisaggregatedSet{}
		probe.Maps[ref] = map[string]string{"config.yaml": "enabled: true\nminSlices: 1\nmaxSlices: 2\n"}
		probe.MapLabels[ref] = map[string]string{"owner": "swiss", "name": release}
		rig := startRig(t, probe, true, false, "")
		p, _ := pdPlan(t, ns, release, release, 1, true, false, "v2")
		u, err := llmsvc.FromPlan(p, "install", "seed")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rig.c.Create(t.Context(), u); err != nil {
			t.Fatal(err)
		}
		srv, w = rig.srv, rig.w
	} else {
		probe := liveProbe()
		probe.Maps[ref] = map[string]string{"config.yaml": "enabled: true\nminSlices: 1\nmaxSlices: 2\n"}
		if probe.MapLabels == nil {
			probe.MapLabels = map[string]map[string]string{}
		}
		probe.MapLabels[ref] = map[string]string{"owner": "swiss", "name": release}
		var s *Server
		srv, s = deployServerWith(t, probe, true)
		s.cfg.Server.HelmBin = stubHelm(t, 0)
		w = s.writer.(*fakeWriter)
	}
	code, out := del(t, srv, "/api/releases/"+ns+"/"+release)
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, out)
	}
	found := false
	for _, d := range w.deleted {
		if d == ref {
			found = true
		}
	}
	if !found {
		t.Fatalf("autoscale ConfigMap was not deleted: %v", w.deleted)
	}
}

func helmAutoscaleServer(t *testing.T, ns, release string, managed, pdOn, colocate, allow bool) (*httptest.Server, *Server, *fakeWriter) {
	t.Helper()
	probe := fakeProbe()
	probe.MapLabels = map[string]map[string]string{}
	probe.Deps = map[string]cluster.Deployment{}
	probe.DSs = map[string]cluster.DisaggregatedSet{}
	if managed {
		_, files := pdPlan(t, ns, release, release, 1, pdOn, colocate, "")
		ref := planRef(ns, release)
		probe.Maps[ref] = files
		probe.Rel = []cluster.Release{{
			Name: release, Namespace: ns, Status: "deployed", Revision: 2,
			Chart: "sglang-0.7.0", SwissFiles: files,
		}}
	}
	srv, s := deployServerWith(t, probe, allow)
	if !allow {
		return srv, s, nil
	}
	w := s.writer.(*fakeWriter)
	s.SetWriter(&mirrorWriter{fakeWriter: w, maps: probe.Maps, mapLabels: probe.MapLabels})
	return srv, s, w
}

func llmAutoscaleServer(t *testing.T, ns, release string, managed, pdOn, colocate, allow bool) (*httptest.Server, *Server, *fakeWriter) {
	t.Helper()
	probe := llmProbe()
	probe.MapLabels = map[string]map[string]string{}
	probe.Deps = map[string]cluster.Deployment{}
	probe.DSs = map[string]cluster.DisaggregatedSet{}
	rig := startRig(t, probe, true, false, "")
	if !allow {
		rig.s.cfg.Server.AllowDeploy = false
	}
	if managed {
		p, _ := pdPlan(t, ns, release, release, 1, pdOn, colocate, "v2")
		u, err := llmsvc.FromPlan(p, "install", "seed")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rig.c.Create(t.Context(), u); err != nil {
			t.Fatal(err)
		}
	}
	w := rig.w
	rig.s.SetWriter(&mirrorWriter{fakeWriter: w, maps: probe.Maps, mapLabels: probe.MapLabels})
	return rig.srv, rig.s, w
}

func pdPlan(t *testing.T, ns, release, serviceID string, replicas int, pdOn, colocate bool, schema string) (*plan.Plan, map[string]string) {
	t.Helper()
	p := &plan.Plan{
		APIVersion: plan.APIVersion,
		Release:    plan.Release{Name: release, Namespace: ns},
		Source:     plan.SourceRef{Model: "pd", Variant: "pd", Schema: schema},
		Chart:      plan.ChartRef{Name: "sglang", Version: "0.7.0", Repo: "https://modelsphere.github.io/helm-charts"},
		Engine:     "sglang",
		Profile:    "prod",
		Layers: map[string]values.Tree{
			values.LayerForm: {
				"replicaCount":     replicas,
				"serviceId":        serviceID,
				"fullnameOverride": release,
				"pd":               values.Tree{"enabled": pdOn, "colocate": colocate},
			},
		},
	}
	if err := p.ComputeHash(); err != nil {
		t.Fatal(err)
	}
	files, err := p.Files("")
	if err != nil {
		t.Fatal(err)
	}
	files["status.yaml"] = "phase: applied\n"
	return p, files
}

func callJSON(t *testing.T, srv *httptest.Server, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func jsonInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

func seedAutoscaleCM(probe *cluster.Fake, ns, release string, enabled bool, min, max int) {
	if probe.Maps == nil {
		probe.Maps = map[string]map[string]string{}
	}
	if probe.MapLabels == nil {
		probe.MapLabels = map[string]map[string]string{}
	}
	ref := autoscaleRef(ns, release)
	probe.Maps[ref] = map[string]string{
		autoscaleConfigKey: fmt.Sprintf("enabled: %t\nminSlices: %d\nmaxSlices: %d\n", enabled, min, max),
	}
	probe.MapLabels[ref] = map[string]string{"owner": "swiss", "name": release}
}

func setDecisionProfile(probe cluster.Fake, rawURL string) {
	m := probe.Maps["swiss/site-profile"]
	m["profile.yaml"] = profileYAML + "scaler:\n  serverAddress: " + rawURL + "\n"
}

func readyDS(slices int) cluster.DisaggregatedSet {
	n := slices
	return cluster.DisaggregatedSet{
		Generation: 1, ObservedGeneration: 1, Slices: &n,
		Roles: []cluster.RoleStatus{{Name: "prefill", Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1}},
	}
}

func readyRouters(ns, fullname string, n int) map[string]cluster.Deployment {
	out := map[string]cluster.Deployment{}
	for i := 0; i < n; i++ {
		out[fmt.Sprintf("%s/%s-router-s%d", ns, fullname, i)] = cluster.Deployment{
			Generation: 1, ObservedGeneration: 1, SpecReplicas: 1, ReadyReplicas: 1,
		}
	}
	return out
}

func asNumber(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return -1
	}
}

func readAutoscaleStatus(t *testing.T, w *fakeWriter, ref string) autoscale.Status {
	t.Helper()
	data := w.written[ref]
	if data == nil || data[autoscaleStatusKey] == "" {
		t.Fatalf("no autoscale status at %s (written %v)", ref, mapKeys(w.written))
	}
	var st autoscale.Status
	if err := yaml.Unmarshal([]byte(data[autoscaleStatusKey]), &st); err != nil {
		t.Fatalf("status: %v\n%s", err, data[autoscaleStatusKey])
	}
	return st
}

func mapKeys(m map[string]map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
