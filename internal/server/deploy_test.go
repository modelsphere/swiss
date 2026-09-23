package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aceforeverd/swiss/internal/cluster"
	"github.com/aceforeverd/swiss/internal/plan"
	"github.com/aceforeverd/swiss/internal/store"
	"github.com/aceforeverd/swiss/internal/values"
)

type fakeWriter struct {
	written map[string]map[string]string
	deleted []string
	secrets map[string]map[string]string
	labels  map[string]map[string]string
}

func (f *fakeWriter) PutConfigMap(_ context.Context, ref string, data map[string]string) error {
	if f.written == nil {
		f.written = map[string]map[string]string{}
	}
	f.written[ref] = data
	return nil
}

func (f *fakeWriter) PutSecret(_ context.Context, ref string, data, labels map[string]string) error {
	if f.secrets == nil {
		f.secrets, f.labels = map[string]map[string]string{}, map[string]map[string]string{}
	}
	f.secrets[ref], f.labels[ref] = data, labels
	return nil
}

func (f *fakeWriter) DeleteSecret(_ context.Context, ref string) error {
	f.deleted = append(f.deleted, ref)
	delete(f.secrets, ref)
	return nil
}

func (f *fakeWriter) DeleteConfigMap(_ context.Context, ref string) error {
	f.deleted = append(f.deleted, ref)
	delete(f.written, ref)
	return nil
}

func deployServer(t *testing.T, allow bool) (*httptest.Server, *Server) {
	t.Helper()
	return deployServerWith(t, liveProbe(), allow)
}

// livePlan composes a plan and renders it the way an apply writes it beside the
// release. Tests seed the cluster with this rather than the database, because
// the ConfigMap is where a release's plan lives -- seeding a table would test a
// path nothing reads.
// livePlan composes a plan and renders the directory an apply leaves beside the
// release -- which is what the plan ConfigMap holds, so tests seed the cluster
// with the same thing the cluster would have.
func livePlan(t *testing.T, req planRequest) (*plan.Plan, map[string]string) {
	t.Helper()
	s := New(testConfig("prod-b300"), fakeProbe(), discardLogger(), "test")
	p, err := s.compose(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	files, err := p.Files("")
	if err != nil {
		t.Fatal(err)
	}
	return p, files
}

func deployServerWith(t *testing.T, probe cluster.Probe, allow bool) (*httptest.Server, *Server) {
	t.Helper()
	cfg := testConfig("prod-b300")
	cfg.Server.AllowDeploy = allow
	s := New(cfg, probe, discardLogger(), "test")
	if allow {
		db, err := store.Open(filepath.Join(t.TempDir(), "swiss.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		s.SetStore(db)
		s.SetWriter(&fakeWriter{})
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, s
}

func post(t *testing.T, srv *httptest.Server, path string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(srv.URL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// A read-only swissd must refuse every mutating endpoint, and say why.
func TestReadOnlyServerRefusesDeployEndpoints(t *testing.T) {
	srv, _ := deployServer(t, false)
	for _, p := range []string{"/api/plans", "/api/diff", "/api/apply", "/api/install"} {
		code, body := post(t, srv, p, map[string]any{"model": "modelforge"})
		if code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", p, code)
		}
		if body["error"] == "" {
			t.Errorf("%s: refusal should explain itself", p)
		}
	}
	// The read path is unaffected.
	if code, _ := get(t, srv, "/api/deployments"); code != 200 {
		t.Errorf("reads must keep working: %d", code)
	}
}

func TestPlanEndpointComposesAndStores(t *testing.T) {
	srv, s := deployServer(t, true)
	code, body := post(t, srv, "/api/plans", map[string]any{
		"model": "modelforge", "release": "glm-53",
		"overrides": map[string]any{"replicaCount": 2},
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	hash, _ := body["hash"].(string)
	if hash == "" {
		t.Fatal("no plan hash returned")
	}
	if _, err := s.store.Plan(context.Background(), hash); err != nil {
		t.Fatalf("plan was not stored: %v", err)
	}
}

// The form may set a key the model entry already set. It wins, because it
// merges later, and the plan reports it as shadowed rather than refusing it.
func TestPlanEndpointAcceptsCatalogOverrides(t *testing.T) {
	srv, _ := deployServer(t, true)
	code, body := post(t, srv, "/api/plans", map[string]any{
		"model":     "modelforge",
		"serviceId": "r",
		"overrides": map[string]any{"extraArgs": []string{"--tp-size=2"}},
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	if layerOf(body, "extraArgs") != "form" {
		t.Errorf("extraArgs should be attributed to the form, got %q", layerOf(body, "extraArgs"))
	}
}

func TestApplyNeedsAStoredPlan(t *testing.T) {
	srv, _ := deployServer(t, true)
	code, _ := post(t, srv, "/api/apply", map[string]any{"planHash": "sha256:nope"})
	if code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", code)
	}
}

// glm-53 is live at revision 4 in the fake probe; install must refuse it, and a
// stale expectRevision must be refused too.
func TestApplyPreconditionsAreEnforcedByTheServer(t *testing.T) {
	srv, _ := deployServer(t, true)
	code, body := post(t, srv, "/api/plans", map[string]any{"model": "modelforge", "release": "glm-53"})
	hash, _ := body["hash"].(string)
	if code != 200 || hash == "" {
		t.Fatalf("plan failed: %d %v", code, body)
	}

	code, out := post(t, srv, "/api/install", map[string]any{"planHash": hash})
	if code != http.StatusConflict {
		t.Errorf("install over a live release: status %d %v", code, out)
	}
	code, out = post(t, srv, "/api/apply", map[string]any{"planHash": hash, "expectRevision": 1})
	if code != http.StatusConflict {
		t.Errorf("stale revision: status %d %v", code, out)
	}
}

func TestApplyOnAMissingReleaseIsAConflict(t *testing.T) {
	srv, _ := deployServer(t, true)
	code, body := post(t, srv, "/api/plans", map[string]any{"model": "kimi-k2.5", "release": "not-live"})
	hash, _ := body["hash"].(string)
	if code != 200 || hash == "" {
		t.Fatalf("plan failed: %d %v", code, body)
	}
	code, out := post(t, srv, "/api/apply", map[string]any{"planHash": hash})
	if code != http.StatusConflict {
		t.Fatalf("status %d %v", code, out)
	}
}

func TestPlanConfigMapRefIsBesideTheRelease(t *testing.T) {
	cfg := testConfig("prod")
	cfg.Server.AllowDeploy = true
	s := New(cfg, liveProbe(), discardLogger(), "test")
	w := &fakeWriter{}
	s.SetWriter(w)

	p, err := s.compose(context.Background(), planRequest{Model: "modelforge", Release: "glm-53"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.writePlan(context.Background(), p, planStatus{Phase: phaseApplied, Revision: 4}); err != nil {
		t.Fatal(err)
	}
	ref := "modelforge/" + cluster.PlanConfigMapPrefix + "glm-53"
	data, ok := w.written[ref]
	if !ok {
		t.Fatalf("want a plan at %s, got %v", ref, w.written)
	}
	// Stored as YAML, and readable back by the reconciliation view.
	sum, err := parsePlanSummary([]byte(data["plan.yaml"]))
	if err != nil {
		t.Fatal(err)
	}
	if sum.Model != "modelforge" || sum.Hash != p.Hash {
		t.Fatalf("plan did not round trip: %+v", sum)
	}
	if !strings.Contains(data["status.yaml"], phaseApplied) {
		t.Fatalf("status not written alongside the plan: %q", data["status.yaml"])
	}
}

// Write-ahead: nothing reaches the cluster when the plan cannot be recorded.
func TestApplyRefusesWhenThePlanCannotBeRecorded(t *testing.T) {
	cfg := testConfig("prod")
	cfg.Server.AllowDeploy = true
	s := New(cfg, liveProbe(), discardLogger(), "test")
	db, err := store.Open(filepath.Join(t.TempDir(), "swiss.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s.SetStore(db)
	s.SetWriter(failingWriter{})

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	_, body := post(t, srv, "/api/plans", map[string]any{"model": "modelforge", "release": "glm-53"})
	hash, _ := body["hash"].(string)
	code, out := post(t, srv, "/api/apply", map[string]any{"planHash": hash})
	if code != http.StatusInternalServerError {
		t.Fatalf("status %d %v", code, out)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "nothing applied") {
		t.Fatalf("the refusal must say nothing was applied: %q", msg)
	}
}

type failingWriter struct{}

func (failingWriter) PutConfigMap(context.Context, string, map[string]string) error {
	return errors.New("forbidden")
}

func (failingWriter) DeleteConfigMap(context.Context, string) error {
	return errors.New("forbidden")
}

func (failingWriter) PutSecret(context.Context, string, map[string]string, map[string]string) error {
	return errors.New("forbidden")
}

func (failingWriter) DeleteSecret(context.Context, string) error {
	return errors.New("forbidden")
}

// A closed browser tab must not reach the cluster. helmfile runs under
// exec.CommandContext, so an apply on the request's context is killed
// mid-upgrade when the client goes away -- and a half-applied helm upgrade
// leaves the release pending-upgrade, which nothing but a hand rollback clears.
func TestApplyOutlivesTheRequest(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if cancelled.Err() == nil {
		t.Fatal("the parent should be cancelled")
	}

	ctx, done := detach(cancelled)
	defer done()

	if err := ctx.Err(); err != nil {
		t.Fatalf("the apply must survive the request: %v", err)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("detaching must not mean running forever")
	}
	if d := time.Until(deadline); d <= 0 || d > applyBudget {
		t.Fatalf("deadline %s is not within the apply budget %s", d, applyBudget)
	}
}

// A diff changes nothing, so it is not an operation and gets no row: one entry
// per preview buried the applies. The apply is audited, failure included --
// that entry is the only record that anything was attempted.
func TestRunsAreRecorded(t *testing.T) {
	srv, s := deployServer(t, true)
	_, p := post(t, srv, "/api/plans", map[string]any{"model": "modelforge", "release": "glm-53"})
	post(t, srv, "/api/diff", map[string]any{"model": "modelforge", "release": "glm-53"})

	runs, err := s.store.Runs(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("a diff must not be audited: %+v", runs)
	}

	// No chart source in the test profile, so the apply fails -- the audit
	// entry must still exist, carrying the error.
	post(t, srv, "/api/apply", map[string]any{"planHash": p["hash"]})

	runs, err = s.store.Runs(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) == 0 {
		t.Fatal("a failed apply must still be audited")
	}
	if runs[0].Action != "apply" || runs[0].Error == "" {
		t.Errorf("unexpected run: %+v", runs[0])
	}
}

func TestServiceIDAndLocalPathAreFirstClassFormFields(t *testing.T) {
	srv, _ := deployServer(t, true)
	code, body := post(t, srv, "/api/plans", map[string]any{
		"model":     "modelforge",
		"release":   "fallback-modelforge-01",
		"serviceId": "fallback-modelforge-01",
		"localPath": "/mnt/disk1/models/moved",
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	vals := planValues(body)
	if vals["serviceId"] != "fallback-modelforge-01" {
		t.Errorf("serviceId = %v", vals["serviceId"])
	}
	model := vals["model"].(map[string]any)
	if model["localPath"] != "/mnt/disk1/models/moved" {
		t.Errorf("localPath = %v", model["localPath"])
	}
	if layerOf(body, "serviceId") != "form" || layerOf(body, "model.localPath") != "form" {
		t.Errorf("both must be attributed to the form: %v / %v",
			layerOf(body, "serviceId"), layerOf(body, "model.localPath"))
	}
}

func TestLocalPathFallsBackToTheSiteTemplate(t *testing.T) {
	srv, _ := deployServer(t, true)
	_, body := post(t, srv, "/api/plans", map[string]any{"model": "kimi-k2.5"})
	if got := layerOf(body, "model.localPath"); got != "site" {
		t.Errorf("the template default must be attributed to the site, got %v", got)
	}
}

// A release whose last apply failed must not read as a healthy managed one.
func TestDeploymentsSurfaceTheApplyPhase(t *testing.T) {
	probe := fakeProbe()
	probe.Rel = []cluster.Release{{
		Name: "glm-53", Namespace: "modelforge", Chart: "sglang-0.8.0",
		Status: "deployed", Revision: 4,
		SwissFiles:  map[string]string{"plan.yaml": "source:\n  model: modelforge\n"},
		SwissStatus: []byte("phase: failed\nrevision: 4\nerror: chart not found\n"),
	}}
	srv := testServer(t, probe)
	_, body := get(t, srv, "/api/deployments")
	rows := body["deployments"].([]any)
	// A failed apply does not take the release out of the view: the plan is
	// right there beside it, which is the only thing listing depends on.
	if len(rows) != 1 {
		t.Fatalf("want the release listed, got %v", rows)
	}
	row := rows[0].(map[string]any)
	if row["phase"] != "failed" || row["drift"] == "" {
		t.Fatalf("a failed apply must be visible: %v", row)
	}
}

// An upgrade keeps the deploy inputs and moves only the catalog layer.
func TestUpgradeCarriesTheFormLayerForward(t *testing.T) {
	_, doc := livePlan(t, planRequest{
		Model: "modelforge", Release: "fallback-modelforge-01",
		ServiceID: "fallback-modelforge-01",
		Overrides: values.Tree{"replicaCount": 3},
	})
	probe := liveProbe()
	probe.Rel = append(probe.Rel, cluster.Release{
		Name: "fallback-modelforge-01", Namespace: "modelforge",
		Status: "deployed", Revision: 1, SwissFiles: doc,
	})
	srv, _ := deployServerWith(t, probe, true)

	code, up := post(t, srv, "/api/plans", map[string]any{"fromRelease": "fallback-modelforge-01"})
	if code != 200 {
		t.Fatalf("status %d: %v", code, up)
	}
	vals := planValues(up)
	if vals["serviceId"] != "fallback-modelforge-01" || vals["replicaCount"] != float64(3) {
		t.Fatalf("form layer not carried: %v", vals)
	}
	src := up["source"].(map[string]any)
	if src["model"] != "modelforge" || src["variant"] != "sglang-tp2" {
		t.Fatalf("model and variant should be carried too: %v", src)
	}
	if src["digest"] == "" {
		t.Error("the recomposed plan must carry the entry digest")
	}
}

func TestReleasePlanEndpoint(t *testing.T) {
	p, doc := livePlan(t, planRequest{Model: "kimi-k2.5", Release: "kimi-k25"})
	probe := liveProbe()
	probe.Rel = append(probe.Rel, cluster.Release{
		Name: "kimi-k25", Namespace: "modelforge",
		Status: "deployed", Revision: 2, SwissFiles: doc,
	})
	srv, _ := deployServerWith(t, probe, true)

	code, cur := get(t, srv, "/api/releases/modelforge/kimi-k25/plan")
	if code != 200 || cur["hash"] != p.Hash {
		t.Fatalf("status %d: %v", code, cur)
	}
	// A release swiss did not deploy has no plan to upgrade from.
	if code, _ := get(t, srv, "/api/releases/modelforge/by-hand/plan"); code != 404 {
		t.Errorf("want 404 for an unmanaged release, got %d", code)
	}
}

// The form sends every flag; nothing is decided by a chart default.
func TestWebFeatureTogglesLandInThePlan(t *testing.T) {
	srv, _ := deployServer(t, true)
	code, body := post(t, srv, "/api/plans", map[string]any{
		"model": "glm5.1", "release": "r", "serviceId": "r",
		"overrides": map[string]any{
			"cart":           map[string]any{"enabled": false},
			"sloRequirement": map[string]any{"enabled": true},
			"modelRoute":     map[string]any{"enabled": true, "nginx": map[string]any{"route": "glm-5"}},
			"scaler":         map[string]any{"enabled": true, "maxReplicas": 6},
		},
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	vals := planValues(body)
	for feature, want := range map[string]bool{
		"cart": false, "sloRequirement": true, "modelRoute": true, "scaler": true,
	} {
		got := vals[feature].(map[string]any)["enabled"]
		if got != want {
			t.Errorf("%s.enabled = %v, want %v", feature, got, want)
		}
	}
	if got := layerOf(body, "cart.enabled"); got != "form" {
		t.Errorf("an explicit choice belongs to the form, got %v", got)
	}
}

// The web sends a flag for every feature it shows, so each one must be
// form-settable. serviceMonitor was wholly site-owned and rejected the request.
func TestEveryToggledFeatureIsFormSettable(t *testing.T) {
	srv, _ := deployServer(t, true)
	code, body := post(t, srv, "/api/plans", map[string]any{
		"model": "glm5.1", "release": "r", "serviceId": "r",
		"overrides": map[string]any{
			"cart":           map[string]any{"enabled": true},
			"modelRoute":     map[string]any{"enabled": false},
			"sloRequirement": map[string]any{"enabled": true},
			"serviceMonitor": map[string]any{"enabled": true},
			"scaler":         map[string]any{"enabled": false},
		},
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	vals := planValues(body)
	if vals["serviceMonitor"].(map[string]any)["enabled"] != true {
		t.Errorf("serviceMonitor.enabled did not take: %v", vals["serviceMonitor"])
	}
}

// The advanced section is a values fragment typed by hand. It is parsed on the
// server so there is one parser for it.
func TestAdvancedOverridesYAML(t *testing.T) {
	srv, _ := deployServer(t, true)
	code, body := post(t, srv, "/api/plans", map[string]any{
		"model": "glm5.1", "release": "r", "serviceId": "r",
		"overridesYAML": "nodeSelector:\n  pool: gpu\ntolerations:\n  - key: gpu\n    operator: Exists\npriorityClassName: high\n",
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	vals := planValues(body)
	if vals["nodeSelector"].(map[string]any)["pool"] != "gpu" {
		t.Errorf("nodeSelector not merged: %v", vals["nodeSelector"])
	}
	if len(vals["tolerations"].([]any)) != 1 || vals["priorityClassName"] != "high" {
		t.Errorf("advanced fragment not merged: %v", vals)
	}
	if layerOf(body, "priorityClassName") != "form" {
		t.Error("hand-typed overrides belong to the form layer")
	}
}

func TestAdvancedOverridesMaySetACatalogKey(t *testing.T) {
	srv, _ := deployServer(t, true)
	code, body := post(t, srv, "/api/plans", map[string]any{
		"model": "glm5.1", "release": "r", "serviceId": "r",
		"overridesYAML": "extraArgs:\n  - --tp-size=8\n",
	})
	if code != 200 {
		t.Fatalf("a catalog key typed by hand is allowed now: %d %v", code, body)
	}
	if layerOf(body, "extraArgs") != "form" {
		t.Errorf("extraArgs should be attributed to the form, got %q", layerOf(body, "extraArgs"))
	}
}

func TestAdvancedOverridesRejectBadYAML(t *testing.T) {
	srv, _ := deployServer(t, true)
	if code, _ := post(t, srv, "/api/plans", map[string]any{
		"model": "glm5.1", "release": "r", "overridesYAML": "nodeSelector: [unclosed",
	}); code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", code)
	}
}

func TestStatusReportsPodReadiness(t *testing.T) {
	probe := liveProbe()
	probe.Pod = []cluster.Pod{
		{Name: "glm-53-abc", Phase: "Running", Ready: false, AgeSecond: 700},
		{Name: "glm-53-def", Phase: "Running", Ready: true},
	}
	srv := testServer(t, probe)
	code, body := get(t, srv, "/api/releases/modelforge/glm-53/status")
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	if body["ready"].(float64) != 1 || body["total"].(float64) != 2 {
		t.Fatalf("readiness not counted: %v", body)
	}
	if body["exists"] != true || body["revision"].(float64) != 4 {
		t.Errorf("helm state missing: %v", body)
	}
}

// The check goes through the entrypoint, not the pod: a ready pod behind an
// unpublished route serves nobody.
func TestProbeNeedsAnEntrypointAndARoute(t *testing.T) {
	_, doc := livePlan(t, planRequest{Model: "glm5.1", Release: "r", ServiceID: "r"})
	probe := liveProbe()
	probe.Rel = append(probe.Rel, cluster.Release{
		Name: "r", Namespace: "modelforge", Status: "deployed", Revision: 1, SwissFiles: doc,
	})
	srv, _ := deployServerWith(t, probe, true)
	// profileYAML names no route.nginxService.
	code, out := post(t, srv, "/api/releases/modelforge/r/probe", map[string]any{})
	if code != http.StatusPreconditionFailed {
		t.Fatalf("want 412, got %d %v", code, out)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "nginxService") {
		t.Errorf("the refusal should name what is missing: %q", msg)
	}
}

// Everything needed to see and upgrade a release sits beside it in the cluster,
// so a swissd with no database at all can still do both. Losing the database
// costs the operation log and nothing else.
func TestUpgradeNeedsNoDatabase(t *testing.T) {
	_, planYAML := livePlan(t, planRequest{
		Model: "glm5.1", Release: "glm-53", ServiceID: "glm-53",
		Overrides: values.Tree{"replicaCount": 3},
	})

	probe := fakeProbe()
	probe.Rel = []cluster.Release{{
		Name: "glm-53", Namespace: "modelforge", Chart: "sglang-0.7.0",
		Status: "deployed", Revision: 4, SwissFiles: planYAML,
	}}
	cfg := testConfig("prod-b300")
	cfg.Server.AllowDeploy = true
	s := New(cfg, probe, discardLogger(), "test") // no SetStore
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	code, cur := get(t, srv, "/api/releases/modelforge/glm-53/plan")
	if code != 200 {
		t.Fatalf("the plan beside the release must be readable: %d %v", code, cur)
	}
	if cur["source"].(map[string]any)["model"] != "glm5.1" {
		t.Fatalf("plan did not round trip from the ConfigMap: %v", cur["source"])
	}

	code, up := post(t, srv, "/api/plans", map[string]any{"fromRelease": "glm-53"})
	if code != 200 {
		t.Fatalf("upgrade must recompose with no database rows: %d %v", code, up)
	}
	vals := planValues(up)
	if vals["replicaCount"] != float64(3) || vals["serviceId"] != "glm-53" {
		t.Fatalf("the form layer did not survive the cluster round trip: %v", vals)
	}
}

// The database is not a record of what is deployed. When it holds a plan for a
// release anyway -- a staged plan that was never applied, or one left by an
// apply that failed after the cluster changed -- the ConfigMap beside the
// release is what every reader must report.
func TestTheClusterWinsOverTheDatabase(t *testing.T) {
	_, running := livePlan(t, planRequest{
		Model: "glm5.1", Release: "glm-53", ServiceID: "glm-53",
		Overrides: values.Tree{"replicaCount": 3},
	})
	probe := fakeProbe()
	probe.Rel = []cluster.Release{{
		Name: "glm-53", Namespace: "modelforge", Chart: "sglang-0.8.0",
		Status: "deployed", Revision: 4, SwissFiles: running,
	}}
	srv, s := deployServerWith(t, probe, true)

	// Stage a different plan for the same release, the way composing one does.
	_, staged := post(t, srv, "/api/plans", map[string]any{
		"model": "glm5.1", "release": "glm-53", "serviceId": "glm-53",
		"overrides": map[string]any{"replicaCount": 9},
	})
	if _, err := s.store.Plan(context.Background(), staged["hash"].(string)); err != nil {
		t.Fatalf("the staged plan should be in the database: %v", err)
	}

	code, cur := get(t, srv, "/api/releases/modelforge/glm-53/plan")
	if code != 200 {
		t.Fatalf("status %d: %v", code, cur)
	}
	if got := planValues(cur)["replicaCount"]; got != float64(3) {
		t.Fatalf("replicaCount = %v, want the cluster's 3 -- the database was read as truth", got)
	}

	// And the recompose an upgrade starts from must be the running plan too.
	_, up := post(t, srv, "/api/plans", map[string]any{"fromRelease": "glm-53"})
	if got := planValues(up)["replicaCount"]; got != float64(3) {
		t.Fatalf("upgrade carried forward %v, want the cluster's 3", got)
	}
}
