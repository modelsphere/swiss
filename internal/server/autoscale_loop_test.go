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
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/llmsvc"
	"github.com/modelsphere/swiss/internal/store"
	"github.com/modelsphere/swiss/internal/testutil/cases"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
)

type loopCase struct {
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	Backend       string `json:"backend"`
	Decision      int    `json:"decision"`
	Slices        int    `json:"slices"`
	Revision      int    `json:"revision"`
	Conflict      bool   `json:"conflict,omitempty"`
	WantApplies   int    `json:"wantApplies"`
	WantFrom      *int   `json:"wantFrom,omitempty"`
	WantTo        *int   `json:"wantTo,omitempty"`
	ErrorContains string `json:"errorContains,omitempty"`
	HelmStatus    string `json:"helmStatus,omitempty"`
	WantHold      string `json:"wantHold,omitempty"`
	// PlanPhase seeds status.yaml in the helm plan ConfigMap.
	PlanPhase string `json:"planPhase,omitempty"`
	// Deleting gives the LLMService a deletionTimestamp.
	Deleting bool `json:"deleting,omitempty"`
	// LLMPhase overrides status.phase. Deleting holds without a deletionTimestamp,
	// and the rig operator stays off so it does not rewrite that phase.
	LLMPhase string `json:"llmPhase,omitempty"`
}

func TestAutoscaleLoop(t *testing.T) {
	cases.Run(t, "testdata/autoscale-loop", func(t *testing.T, tc loopCase) {
		switch tc.Backend {
		case backendHelm:
			runHelmLoop(t, tc)
		case backendLLMSVC:
			runLLMLoop(t, tc)
		default:
			t.Fatalf("backend %q", tc.Backend)
		}
	})
}

func runHelmLoop(t *testing.T, tc loopCase) {
	t.Helper()
	const ns, release = "models", "pd"
	dec := decisionServer(t, ns, release, tc.Decision)
	_, seed := pdPlan(t, ns, release, release, tc.Slices, true, false, boolPtr(true), "")

	probe := fakeProbe()
	setDecisionProfile(probe, dec.URL)
	probe.Deps = readyRouters(ns, release, tc.Slices)
	probe.DSs = map[string]cluster.DisaggregatedSet{ns + "/" + release: readyDS(tc.Slices)}
	if tc.PlanPhase != "" {
		seed[statusFile] = "phase: " + tc.PlanPhase + "\naction: uninstall\nrevision: 2\n"
	}
	probe.Maps[planRef(ns, release)] = seed
	rev := tc.Revision
	if rev < 1 {
		rev = 2
	}
	helmStatus := "deployed"
	if tc.HelmStatus != "" {
		helmStatus = tc.HelmStatus
	}
	probe.Rel = []cluster.Release{{
		Name: release, Namespace: ns, Status: helmStatus, Revision: rev,
		Chart: "sglang-0.7.0", SwissFiles: seed,
	}}

	logPath := filepath.Join(t.TempDir(), "helmfile.log")
	bin := countBin(t, logPath)
	cp := &revProbe{Fake: probe}
	if tc.Conflict {
		cp.flipAt = 4
	}
	s, w := newLoopServer(t, cp, bin, bin)
	logs := captureLog(s)
	s.autoscaleTick(t.Context())

	applies := countLines(t, logPath)
	if applies != tc.WantApplies {
		t.Fatalf("helmfile calls %d, want %d (Release calls %d) log=%s", applies, tc.WantApplies, cp.calls.Load(), logs.String())
	}
	assertLoopLog(t, logs, tc)
	if tc.Conflict {
		if cp.calls.Load() != 4 {
			t.Errorf("Release calls %d, want 4", cp.calls.Load())
		}
		if w.written[planRef(ns, release)] != nil {
			t.Fatal("conflict wrote a plan")
		}
		return
	}
	if tc.WantHold != "" {
		if applies != 0 {
			t.Fatal("held tick applied")
		}
		if len(w.written) != 0 {
			t.Fatalf("held tick wrote %v", w.written)
		}
		return
	}
	assertHelmScale(t, tc, w.written[planRef(ns, release)], seed)
}

func runLLMLoop(t *testing.T, tc loopCase) {
	t.Helper()
	const ns, release = "models", "pd"
	dec := decisionServer(t, ns, release, tc.Decision)
	p, _ := pdPlan(t, ns, release, release, tc.Slices, true, false, boolPtr(true), "v2")

	probe := fakeProbe()
	setDecisionProfile(probe, dec.URL)
	probe.Deps = readyRouters(ns, release, tc.Slices)
	probe.DSs = map[string]cluster.DisaggregatedSet{ns + "/" + release: readyDS(tc.Slices)}
	probe.Secrets = map[string]map[string]string{}
	probe.SecretLabels = map[string]map[string]string{}

	phase := llmsvc.PhaseApplied
	if tc.LLMPhase != "" {
		phase = tc.LLMPhase
	}
	rig := startRig(t, probe, true, !tc.Conflict && phase != llmsvc.PhaseDeleting, "")
	u, err := llmsvc.FromPlan(p, "install", "seed")
	if err != nil {
		t.Fatal(err)
	}
	rev := tc.Revision
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
	if tc.Deleting {
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
	if tc.Conflict {
		rig.dyn.PrependReactor("get", "llmservices", func(action k8stesting.Action) (bool, runtime.Object, error) {
			n := int(gets.Add(1))
			get := action.(k8stesting.GetAction)
			obj, err := rig.dyn.Tracker().Get(llmsvc.GVR, get.GetNamespace(), get.GetName())
			if err != nil {
				return true, nil, err
			}
			copied := obj.(*unstructured.Unstructured).DeepCopy()
			if n >= 7 {
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
	if int(updates.Load()) != tc.WantApplies {
		t.Fatalf("updates %d, want %d (gets %d) log=%s", updates.Load(), tc.WantApplies, gotGets, logs.String())
	}
	got, err := rig.c.Get(t.Context(), ns, release)
	if err != nil {
		t.Fatal(err)
	}
	assertLoopLog(t, logs, tc)
	if tc.Conflict {
		// applyLocked checks Canary ownership and deletion before applying.
		if gotGets != 9 {
			t.Errorf("Get calls %d, want 9", gotGets)
		}
		if got.GetAnnotations()[llmsvc.AnnAction] == "autoscale" {
			t.Fatalf("conflict updated the object: %v", got.GetAnnotations())
		}
		return
	}
	if tc.WantHold != "" {
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
	note := fmt.Sprintf("autoscale %d -> %d (decision-gen)", *tc.WantFrom, *tc.WantTo)
	if got.GetAnnotations()[llmsvc.AnnNote] != note {
		t.Fatalf("note %q, want %q", got.GetAnnotations()[llmsvc.AnnNote], note)
	}
	assertLLMAutoscaleLayer(t, got, tc.Slices, *tc.WantTo)
}

func assertHelmScale(t *testing.T, tc loopCase, written, seed map[string]string) {
	t.Helper()
	if written == nil {
		t.Fatal("plan was not written")
	}
	if !strings.Contains(written["autoscale.yaml"], fmt.Sprintf("replicaCount: %d", *tc.WantTo)) {
		t.Fatalf("autoscale.yaml:\n%s", written["autoscale.yaml"])
	}
	if seed["form.yaml"] != written["form.yaml"] {
		t.Fatalf("form.yaml changed\n got %s\n want %s", written["form.yaml"], seed["form.yaml"])
	}
	raw := written["status.yaml"]
	if !strings.Contains(raw, "action: autoscale") {
		t.Fatalf("status action:\n%s", raw)
	}
	note := fmt.Sprintf("autoscale %d -> %d (decision-gen)", *tc.WantFrom, *tc.WantTo)
	if !strings.Contains(raw, note) {
		t.Fatalf("status note missing %q:\n%s", note, raw)
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
	if tc.ErrorContains != "" && !strings.Contains(text, tc.ErrorContains) {
		t.Fatalf("log %q, want substring %q", text, tc.ErrorContains)
	}
	if tc.WantHold != "" && !strings.Contains(text, tc.WantHold) {
		t.Fatalf("log %q, want hold %q", text, tc.WantHold)
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
	body := fmt.Sprintf(`{"apiVersion":"llmscaling.inference.x-k8s.io/v1alpha1","decisions":[{"namespace":"%s","serviceId":"%s","replicas":{"active":%d}}]}`, ns, serviceID, active)
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
