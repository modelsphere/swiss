package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelsphere/swiss/internal/autoscale"
	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/llmsvc"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/store"
	"github.com/modelsphere/swiss/internal/testutil/cases"
	"github.com/modelsphere/swiss/internal/values"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"
)

type loopCase struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Input       loopInput    `json:"input"`
	Expected    loopExpected `json:"expected"`
}

type loopInput struct {
	Backend    string `json:"backend"`
	Decision   int    `json:"decision"`
	Slices     int    `json:"slices"`
	Revision   int    `json:"revision"`
	Conflict   bool   `json:"conflict,omitempty"`
	HelmStatus string `json:"helmStatus,omitempty"`
	// PlanPhase seeds status.yaml in the helm plan ConfigMap.
	PlanPhase string `json:"planPhase,omitempty"`
	// Deleting gives the LLMService a deletionTimestamp.
	Deleting bool `json:"deleting,omitempty"`
	// LLMPhase overrides status.phase. Deleting holds without a deletionTimestamp,
	// and the rig operator stays off so it does not rewrite that phase.
	LLMPhase string `json:"llmPhase,omitempty"`
	// DecisionPD adds shape.pd to the decision row, with decision as its
	// slices. Engines is the DisaggregatedSet's spec.roles and PlanEngines the
	// form's pd.<role>.replicas, each omitted when nil.
	DecisionPD  *autoscale.Engines `json:"decisionPD,omitempty"`
	Engines     *autoscale.Engines `json:"engines,omitempty"`
	PlanEngines *autoscale.Engines `json:"planEngines,omitempty"`
}

type loopExpected struct {
	Applies       int    `json:"applies"`
	From          *int   `json:"from,omitempty"`
	To            *int   `json:"to,omitempty"`
	Hold          string `json:"hold,omitempty"`
	ErrorContains string `json:"errorContains,omitempty"`
	// PD is the autoscale layer's pd.prefill and pd.decode replicas.
	PD *autoscale.Engines `json:"pd,omitempty"`
}

func TestAutoscaleLoop(t *testing.T) {
	cases.Run(t, "testdata/autoscale-loop", func(t *testing.T, tc loopCase) {
		switch tc.Input.Backend {
		case backendHelm:
			runHelmLoop(t, tc)
		case backendLLMSVC:
			runLLMLoop(t, tc)
		default:
			t.Fatalf("backend %q", tc.Input.Backend)
		}
	})
}

func runHelmLoop(t *testing.T, tc loopCase) {
	t.Helper()
	const ns, release = "models", "pd"
	dec := decisionServerPD(t, ns, release, tc.Input.Decision, tc.Input.DecisionPD)
	p, seed := pdPlan(t, ns, release, release, tc.Input.Slices, true, false, boolPtr(true), "")
	if tc.Input.PlanEngines != nil {
		seed = withPlanEngines(t, p, *tc.Input.PlanEngines)
	}

	probe := fakeProbe()
	setDecisionProfile(probe, dec.URL)
	probe.Deps = readyRouters(ns, release, tc.Input.Slices)
	probe.DSs = map[string]cluster.DisaggregatedSet{ns + "/" + release: loopDS(tc)}
	if tc.Input.PlanPhase != "" {
		seed[statusFile] = "phase: " + tc.Input.PlanPhase + "\naction: uninstall\nrevision: 2\n"
	}
	probe.Maps[planRef(ns, release)] = seed
	rev := tc.Input.Revision
	if rev < 1 {
		rev = 2
	}
	helmStatus := "deployed"
	if tc.Input.HelmStatus != "" {
		helmStatus = tc.Input.HelmStatus
	}
	probe.Rel = []cluster.Release{{
		Name: release, Namespace: ns, Status: helmStatus, Revision: rev,
		Chart: "sglang-0.7.0", SwissFiles: seed,
	}}

	logPath := filepath.Join(t.TempDir(), "helmfile.log")
	bin := countBin(t, logPath)
	cp := &revProbe{Fake: probe}
	if tc.Input.Conflict {
		cp.flipAt = 4
	}
	s, w := newLoopServer(t, cp, bin, bin)
	logs := captureLog(s)
	s.autoscaleTick(t.Context())

	applies := countLines(t, logPath)
	if applies != tc.Expected.Applies {
		t.Fatalf("helmfile calls %d, want %d (Release calls %d) log=%s", applies, tc.Expected.Applies, cp.calls.Load(), logs.String())
	}
	assertLoopLog(t, logs, tc)
	if tc.Input.Conflict {
		if cp.calls.Load() != 4 {
			t.Errorf("Release calls %d, want 4", cp.calls.Load())
		}
		if w.written[planRef(ns, release)] != nil {
			t.Fatal("conflict wrote a plan")
		}
		return
	}
	if tc.Expected.Hold != "" || tc.Expected.Applies == 0 {
		if applies != 0 {
			t.Fatal("held or unchanged tick applied")
		}
		if len(w.written) != 0 {
			t.Fatalf("held or unchanged tick wrote %v", w.written)
		}
		return
	}
	assertHelmScale(t, tc, w.written[planRef(ns, release)], seed)
	assertLoopState(t, s, tc)
}

func loopDS(tc loopCase) cluster.DisaggregatedSet {
	ds := readyDS(tc.Input.Slices)
	if tc.Input.Engines != nil {
		ds.RoleReplicas = map[string]int{"prefill": tc.Input.Engines.Prefill, "decode": tc.Input.Engines.Decode}
	}
	return ds
}

// withPlanEngines puts per-slice role replicas in p's form layer and
// returns the plan files to seed with.
func withPlanEngines(t *testing.T, p *plan.Plan, e autoscale.Engines) map[string]string {
	t.Helper()
	pd := p.Layers[values.LayerForm]["pd"].(values.Tree)
	pd["prefill"] = values.Tree{"replicas": e.Prefill}
	pd["decode"] = values.Tree{"replicas": e.Decode}
	if err := p.ComputeHash(); err != nil {
		t.Fatal(err)
	}
	files, err := p.Files("")
	if err != nil {
		t.Fatal(err)
	}
	files["status.yaml"] = "phase: applied\n"
	return files
}

// loopNote is the status note a scale writes.
func loopNote(tc loopCase) string {
	note := fmt.Sprintf("autoscale %d -> %d", *tc.Expected.From, *tc.Expected.To)
	if tc.Expected.PD != nil {
		note += fmt.Sprintf(", prefill %d -> %d, decode %d -> %d",
			tc.Input.Engines.Prefill, tc.Expected.PD.Prefill, tc.Input.Engines.Decode, tc.Expected.PD.Decode)
	}
	return note + " (decision-gen)"
}

// assertLoopState checks the recorded pass of a scale: slices, and the roles
// in pd when the decision set them.
func assertLoopState(t *testing.T, s *Server, tc loopCase) {
	t.Helper()
	st, ok := s.autoscaleStateOf("models", "pd")
	if !ok {
		t.Fatal("no recorded pass")
	}
	if tc.Expected.PD == nil {
		if st.PD != nil {
			t.Fatalf("pd %+v on a slice decision", *st.PD)
		}
		return
	}
	if st.PD == nil || st.PD.Want == nil || st.PD.Current == nil || st.PD.Decision == nil {
		t.Fatalf("pd %+v, want current, want and decision", st.PD)
	}
	if *st.PD.Want != *tc.Expected.PD || *st.PD.Current != *tc.Input.Engines || *st.PD.Decision != *tc.Input.DecisionPD {
		t.Fatalf("pd current %+v want %+v decision %+v", *st.PD.Current, *st.PD.Want, *st.PD.Decision)
	}
	if st.Want == nil || *st.Want != *tc.Expected.To || st.Current != *tc.Expected.From {
		t.Fatalf("slices current %d want %v, want %d -> %d", st.Current, st.Want, *tc.Expected.From, *tc.Expected.To)
	}
}

func runLLMLoop(t *testing.T, tc loopCase) {
	t.Helper()
	const ns, release = "models", "pd"
	dec := decisionServerPD(t, ns, release, tc.Input.Decision, tc.Input.DecisionPD)
	p, _ := pdPlan(t, ns, release, release, tc.Input.Slices, true, false, boolPtr(true), "v2")

	probe := fakeProbe()
	setDecisionProfile(probe, dec.URL)
	probe.Deps = readyRouters(ns, release, tc.Input.Slices)
	probe.DSs = map[string]cluster.DisaggregatedSet{ns + "/" + release: loopDS(tc)}
	probe.Secrets = map[string]map[string]string{}
	probe.SecretLabels = map[string]map[string]string{}

	phase := llmsvc.PhaseApplied
	if tc.Input.LLMPhase != "" {
		phase = tc.Input.LLMPhase
	}
	rig := startRig(t, probe, true, !tc.Input.Conflict && phase != llmsvc.PhaseDeleting, "")
	u, err := llmsvc.FromPlan(p, "install", "seed")
	if err != nil {
		t.Fatal(err)
	}
	rev := tc.Input.Revision
	if rev < 1 {
		rev = 2
	}
	if err := unstructured.SetNestedMap(u.Object, map[string]any{
		"observedGeneration": int64(1),
		"phase":              phase,
		"helm":               map[string]any{"revision": int64(rev), "status": "deployed"},
	}, "status"); err != nil {
		t.Fatal(err)
	}
	u.SetGeneration(1)
	if tc.Input.Deleting {
		ts := metav1.Now()
		u.SetDeletionTimestamp(&ts)
	}
	if _, err := rig.c.Create(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	seeded, err := rig.c.Get(t.Context(), ns, release)
	if err != nil {
		t.Fatal(err)
	}
	obj, err := llmsvc.FromUnstructured(seeded)
	if err != nil {
		t.Fatal(err)
	}
	if obj.Status == nil || obj.Status.Phase != phase || obj.Status.Helm == nil || obj.Status.Helm.Revision != int64(rev) {
		t.Fatalf("seed status did not stick: %+v gen=%d", obj.Status, seeded.GetGeneration())
	}
	if obj.Status.ObservedGeneration < seeded.GetGeneration() {
		t.Fatalf("observed %d behind generation %d", obj.Status.ObservedGeneration, seeded.GetGeneration())
	}

	var updates atomic.Int32
	rig.dyn.PrependReactor("update", "llmservices", func(k8stesting.Action) (bool, runtime.Object, error) {
		updates.Add(1)
		return false, nil, nil
	})
	var gets atomic.Int32
	if tc.Input.Conflict {
		rig.dyn.PrependReactor("get", "llmservices", func(action k8stesting.Action) (bool, runtime.Object, error) {
			n := int(gets.Add(1))
			get := action.(k8stesting.GetAction)
			obj, err := rig.dyn.Tracker().Get(llmsvc.GVR, get.GetNamespace(), get.GetName())
			if err != nil {
				return true, nil, err
			}
			copied := obj.(*unstructured.Unstructured).DeepCopy()
			if n >= 6 {
				helm, _, _ := unstructured.NestedMap(copied.Object, "status", "helm")
				if helm == nil {
					helm = map[string]any{}
				}
				helm["revision"] = int64(99)
				helm["status"] = "deployed"
				_ = unstructured.SetNestedMap(copied.Object, helm, "status", "helm")
			}
			return true, copied, nil
		})
	}

	logs := captureLog(rig.s)
	rig.s.autoscaleTick(t.Context())
	gotGets := gets.Load()
	if int(updates.Load()) != tc.Expected.Applies {
		t.Fatalf("updates %d, want %d (gets %d) log=%s", updates.Load(), tc.Expected.Applies, gotGets, logs.String())
	}
	got, err := rig.c.Get(t.Context(), ns, release)
	if err != nil {
		t.Fatal(err)
	}
	assertLoopLog(t, logs, tc)
	if tc.Input.Conflict {
		// One more than the loop alone: applyLocked reads the LLMService
		// once to refuse a release that is being deleted.
		if gotGets != 8 {
			t.Errorf("Get calls %d, want 8", gotGets)
		}
		if got.GetAnnotations()[llmsvc.AnnAction] == "autoscale" {
			t.Fatalf("conflict updated the object: %v", got.GetAnnotations())
		}
		return
	}
	if tc.Expected.Hold != "" {
		if updates.Load() != 0 {
			t.Fatal("held tick applied")
		}
		if got.GetAnnotations()[llmsvc.AnnAction] == "autoscale" {
			t.Fatalf("held tick updated the object: %v", got.GetAnnotations())
		}
		return
	}
	if got.GetAnnotations()[llmsvc.AnnAction] != "autoscale" {
		t.Fatalf("action %q", got.GetAnnotations()[llmsvc.AnnAction])
	}
	note := loopNote(tc)
	if got.GetAnnotations()[llmsvc.AnnNote] != note {
		t.Fatalf("note %q, want %q", got.GetAnnotations()[llmsvc.AnnNote], note)
	}
	assertLLMAutoscaleLayer(t, got, tc.Input.Slices, *tc.Expected.To)
	if tc.Expected.PD != nil {
		assertLLMAutoscaleRoles(t, got, *tc.Expected.PD)
	}
	assertLoopState(t, rig.s, tc)
}

func assertHelmScale(t *testing.T, tc loopCase, written, seed map[string]string) {
	t.Helper()
	if written == nil {
		t.Fatal("plan was not written")
	}
	assertAutoscaleYAML(t, tc, written["autoscale.yaml"])
	if seed["form.yaml"] != written["form.yaml"] {
		t.Fatalf("form.yaml changed\n got %s\n want %s", written["form.yaml"], seed["form.yaml"])
	}
	raw := written["status.yaml"]
	if !strings.Contains(raw, "action: autoscale") {
		t.Fatalf("status action:\n%s", raw)
	}
	note := loopNote(tc)
	if !strings.Contains(raw, note) {
		t.Fatalf("status note missing %q:\n%s", note, raw)
	}
}

// assertAutoscaleYAML is the whole autoscale layer: replicaCount, and the
// role replicas when the decision set them.
func assertAutoscaleYAML(t *testing.T, tc loopCase, raw string) {
	t.Helper()
	var got map[string]any
	if err := yaml.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("autoscale.yaml: %v\n%s", err, raw)
	}
	want := map[string]any{"replicaCount": float64(*tc.Expected.To)}
	if tc.Expected.PD != nil {
		want["pd"] = map[string]any{
			"prefill": map[string]any{"replicas": float64(tc.Expected.PD.Prefill)},
			"decode":  map[string]any{"replicas": float64(tc.Expected.PD.Decode)},
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("autoscale.yaml %v, want %v\n%s", got, want, raw)
	}
}

// assertLLMAutoscaleRoles is the role replicas in the LLMService's last layer,
// which assertLLMAutoscaleLayer has checked is the autoscale one.
func assertLLMAutoscaleRoles(t *testing.T, got *unstructured.Unstructured, want autoscale.Engines) {
	t.Helper()
	layers, _, _ := unstructured.NestedSlice(got.Object, "spec", "layers")
	last, _ := layers[len(layers)-1].(map[string]any)
	vals, _ := last["values"].(map[string]any)
	for role, n := range map[string]int{"prefill": want.Prefill, "decode": want.Decode} {
		v, _, _ := unstructured.NestedFieldNoCopy(vals, "pd", role, "replicas")
		if asNumber(v) != n {
			t.Fatalf("autoscale pd.%s.replicas %v, want %d: %v", role, v, n, vals)
		}
	}
}

func assertLLMAutoscaleLayer(t *testing.T, got *unstructured.Unstructured, slices, want int) {
	t.Helper()
	layers, _, _ := unstructured.NestedSlice(got.Object, "spec", "layers")
	if len(layers) == 0 {
		t.Fatal("no layers")
	}
	last, _ := layers[len(layers)-1].(map[string]any)
	if last["name"] != "autoscale" {
		t.Fatalf("last layer %v", last["name"])
	}
	vals, _ := last["values"].(map[string]any)
	if asNumber(vals["replicaCount"]) != want {
		t.Fatalf("autoscale replicaCount %v, want %d", vals["replicaCount"], want)
	}
	for _, raw := range layers[:len(layers)-1] {
		layer, _ := raw.(map[string]any)
		if layer["name"] != "form" {
			continue
		}
		fv, _ := layer["values"].(map[string]any)
		if asNumber(fv["replicaCount"]) != slices {
			t.Fatalf("form replicaCount changed to %v", fv["replicaCount"])
		}
	}
}

func captureLog(s *Server) *bytes.Buffer {
	buf := &bytes.Buffer{}
	s.log = slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return buf
}

func assertLoopLog(t *testing.T, logs *bytes.Buffer, tc loopCase) {
	t.Helper()
	text := logs.String()
	if tc.Expected.ErrorContains != "" && !strings.Contains(text, tc.Expected.ErrorContains) {
		t.Fatalf("log %q, want substring %q", text, tc.Expected.ErrorContains)
	}
	if tc.Expected.Hold != "" && !strings.Contains(text, tc.Expected.Hold) {
		t.Fatalf("log %q, want hold %q", text, tc.Expected.Hold)
	}
}

type revProbe struct {
	cluster.Fake
	calls  atomic.Int32
	flipAt int
}

func (p *revProbe) Release(ctx context.Context, namespace, name string) (*cluster.Release, error) {
	n := int(p.calls.Add(1))
	rel, err := p.Fake.Release(ctx, namespace, name)
	if err != nil || rel == nil || p.flipAt == 0 || n < p.flipAt {
		return rel, err
	}
	cp := *rel
	cp.Revision = 99
	return &cp, nil
}

func newLoopServer(t *testing.T, probe cluster.Probe, helm, helmfile string) (*Server, *fakeWriter) {
	t.Helper()
	cfg := testConfig("prod-b300")
	cfg.Server.AllowDeploy = true
	cfg.Server.HelmBin = helm
	cfg.Server.HelmfileBin = helmfile
	s := New(cfg, probe, discardLogger(), "test")
	db, err := store.Open(filepath.Join(t.TempDir(), "swiss.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s.SetStore(db)
	w := &fakeWriter{}
	s.SetWriter(w)
	return s, w
}

func decisionServer(t *testing.T, ns, serviceID string, active int) *httptest.Server {
	t.Helper()
	return decisionServerPD(t, ns, serviceID, active, nil)
}

// decisionServerPD is decisionServer whose row carries shape.pd when pd is set,
// as a pdengine CR's does.
func decisionServerPD(t *testing.T, ns, serviceID string, active int, pd *autoscale.Engines) *httptest.Server {
	t.Helper()
	shape := ""
	if pd != nil {
		shape = fmt.Sprintf(`,"shape":{"pd":{"prefill":{"replicas":%d,"liveReplicas":1},"decode":{"replicas":%d,"liveReplicas":1},"slices":%d}}`,
			pd.Prefill, pd.Decode, active)
	}
	body := fmt.Sprintf(`{"apiVersion":"llmscaling.inference.x-k8s.io/v1alpha1","decisions":[{"namespace":"%s","serviceId":"%s","replicas":{"active":%d}%s}]}`, ns, serviceID, active, shape)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("namespace") == "" || r.URL.Query().Get("serviceId") == "" {
			http.Error(w, "missing query", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func countBin(t *testing.T, logPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bin")
	script := "#!/bin/sh\necho called >> '" + logPath + "'\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if line != "" {
			n++
		}
	}
	return n
}
