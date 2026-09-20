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

	"github.com/aceforeverd/swiss/internal/cluster"
	"github.com/aceforeverd/swiss/internal/store"
)

type fakeWriter struct{ written map[string]map[string]string }

func (f *fakeWriter) PutConfigMap(_ context.Context, ref string, data map[string]string) error {
	if f.written == nil {
		f.written = map[string]map[string]string{}
	}
	f.written[ref] = data
	return nil
}

func deployServer(t *testing.T, allow bool) (*httptest.Server, *Server) {
	t.Helper()
	cfg := testConfig("prod-b300")
	cfg.Server.AllowDeploy = allow
	s := New(cfg, liveProbe(), discardLogger(), "test")
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

// The form layer may not touch catalog-owned keys, and the server must enforce
// that rather than trusting the UI to.
func TestPlanEndpointRejectsCatalogOverrides(t *testing.T) {
	srv, _ := deployServer(t, true)
	code, body := post(t, srv, "/api/plans", map[string]any{
		"model":     "modelforge",
		"overrides": map[string]any{"extraArgs": []string{"--tp-size=2"}},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("status %d: %v", code, body)
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

func TestRunsAreRecorded(t *testing.T) {
	srv, s := deployServer(t, true)
	post(t, srv, "/api/plans", map[string]any{"model": "modelforge", "release": "glm-53"})
	// No helmfile in the test environment, so diff fails -- the audit entry must
	// still exist, carrying the error.
	post(t, srv, "/api/diff", map[string]any{"model": "modelforge", "release": "glm-53"})

	runs, err := s.store.Runs(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) == 0 {
		t.Fatal("a failed diff must still be audited")
	}
	if runs[0].Action != "diff" || runs[0].Error == "" {
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
	vals := body["values"].(map[string]any)
	if vals["serviceId"] != "fallback-modelforge-01" {
		t.Errorf("serviceId = %v", vals["serviceId"])
	}
	model := vals["model"].(map[string]any)
	if model["localPath"] != "/mnt/disk1/models/moved" {
		t.Errorf("localPath = %v", model["localPath"])
	}
	prov := body["provenance"].(map[string]any)
	if prov["serviceId"] != "form" || prov["model.localPath"] != "form" {
		t.Errorf("both must be attributed to the form: %v / %v", prov["serviceId"], prov["model.localPath"])
	}
}

func TestLocalPathFallsBackToTheSiteTemplate(t *testing.T) {
	srv, _ := deployServer(t, true)
	_, body := post(t, srv, "/api/plans", map[string]any{"model": "kimi-k2.5"})
	prov := body["provenance"].(map[string]any)
	if prov["model.localPath"] != "site" {
		t.Errorf("the template default must be attributed to the site, got %v", prov["model.localPath"])
	}
}

// A release whose last apply failed must not read as a healthy managed one.
func TestDeploymentsSurfaceTheApplyPhase(t *testing.T) {
	probe := fakeProbe()
	probe.Rel = []cluster.Release{{
		Name: "glm-53", Namespace: "modelforge", Chart: "sglang-0.8.0",
		Status: "deployed", Revision: 4,
		SwissPlan:   []byte("source:\n  model: modelforge\n"),
		SwissStatus: []byte("phase: failed\nrevision: 4\nerror: chart not found\n"),
	}}
	srv := testServer(t, probe)
	_, body := get(t, srv, "/api/deployments")
	row := body["deployments"].([]any)[0].(map[string]any)
	if row["phase"] != "failed" || row["drift"] == "" {
		t.Fatalf("a failed apply must be visible: %v", row)
	}
	if !row["managed"].(bool) {
		t.Error("it is still swiss-managed; the plan is right there")
	}
}

// An upgrade keeps the deploy inputs and moves only the catalog layer.
func TestUpgradeCarriesTheFormLayerForward(t *testing.T) {
	srv, s := deployServer(t, true)

	_, body := post(t, srv, "/api/plans", map[string]any{
		"model": "modelforge", "release": "fallback-modelforge-01",
		"serviceId": "fallback-modelforge-01",
		"overrides": map[string]any{"replicaCount": 3},
	})
	hash := body["hash"].(string)
	if err := s.store.RecordApply(context.Background(), store.Deployment{
		Namespace: "modelforge", Release: "fallback-modelforge-01", PlanHash: hash, Revision: 1,
	}, 0); err != nil {
		t.Fatal(err)
	}

	code, up := post(t, srv, "/api/plans", map[string]any{"fromRelease": "fallback-modelforge-01"})
	if code != 200 {
		t.Fatalf("status %d: %v", code, up)
	}
	vals := up["values"].(map[string]any)
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
	srv, s := deployServer(t, true)
	_, body := post(t, srv, "/api/plans", map[string]any{"model": "kimi-k2.5", "release": "kimi-k25"})
	hash := body["hash"].(string)
	if err := s.store.RecordApply(context.Background(), store.Deployment{
		Namespace: "modelforge", Release: "kimi-k25", PlanHash: hash, Revision: 2,
	}, 0); err != nil {
		t.Fatal(err)
	}

	code, cur := get(t, srv, "/api/releases/modelforge/kimi-k25/plan")
	if code != 200 || cur["hash"] != hash {
		t.Fatalf("status %d: %v", code, cur)
	}
	// A release swiss did not deploy has no plan to upgrade from.
	if code, _ := get(t, srv, "/api/releases/modelforge/by-hand/plan"); code != 404 {
		t.Errorf("want 404 for an unmanaged release, got %d", code)
	}
}
