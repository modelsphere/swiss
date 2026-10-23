package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelsphere/swiss/internal/autoscale"
	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/llmsvc"
	"github.com/modelsphere/swiss/internal/store"
	"github.com/modelsphere/swiss/internal/testutil/cases"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"
)

type loopCase struct {
	Name               string            `json:"name"`
	Description        string            `json:"description,omitempty"`
	Backend            string            `json:"backend"`
	Decision           int               `json:"decision"`
	Slices             int               `json:"slices"`
	Min                int               `json:"min"`
	Max                int               `json:"max"`
	Revision           int               `json:"revision"`
	Conflict           bool              `json:"conflict,omitempty"`
	WantApplies        int               `json:"wantApplies"`
	WantFrom           *int              `json:"wantFrom,omitempty"`
	WantTo             *int              `json:"wantTo,omitempty"`
	ErrorContains      string            `json:"errorContains,omitempty"`
	HelmStatus         string            `json:"helmStatus,omitempty"`
	FailApply          bool              `json:"failApply,omitempty"`
	SeedStatus         *autoscale.Status `json:"seedStatus,omitempty"`
	WantHold           string            `json:"wantHold,omitempty"`
	WantFailures       *int              `json:"wantFailures,omitempty"`
	WantRetryDelta     string            `json:"wantRetryDelta,omitempty"`
	WantLastFailedWant *int              `json:"wantLastFailedWant,omitempty"`
	WantCleared        bool              `json:"wantCleared,omitempty"`
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
	_, seed := pdPlan(t, ns, release, release, tc.Slices, true, false, "")

	probe := fakeProbe()
	setDecisionProfile(probe, dec.URL)
	probe.MapLabels = map[string]map[string]string{}
	probe.Deps = readyRouters(ns, release, tc.Slices)
	probe.DSs = map[string]cluster.DisaggregatedSet{ns + "/" + release: readyDS(tc.Slices)}
	seedAutoscaleCM(&probe, ns, release, true, tc.Min, tc.Max)
	if tc.SeedStatus != nil {
		b, err := yaml.Marshal(tc.SeedStatus)
		if err != nil {
			t.Fatal(err)
		}
		probe.Maps[autoscaleRef(ns, release)][autoscaleStatusKey] = string(b)
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
	if tc.FailApply {
		bin = failCountBin(t, logPath)
	}
	cp := &revProbe{Fake: probe}
	if tc.Conflict {
		cp.flipAt = 4
	}
	s, w := newLoopServer(t, cp, bin, bin)
	s.autoscaleTick(t.Context())

	st := readAutoscaleStatus(t, w, autoscaleRef(ns, release))
	applies := countLines(t, logPath)
	if applies != tc.WantApplies {
		t.Fatalf("helmfile calls %d, want %d (Release calls %d) status=%+v", applies, tc.WantApplies, cp.calls.Load(), st)
	}
	if tc.Conflict {
		if cp.calls.Load() != 4 {
			t.Errorf("Release calls %d, want 4", cp.calls.Load())
		}
		if st.LastError == "" || !strings.Contains(st.LastError, tc.ErrorContains) {
			t.Fatalf("lastError %q, want substring %q (calls %d)", st.LastError, tc.ErrorContains, cp.calls.Load())
		}
		assertLoopBackoff(t, st, tc)
		return
	}
	if tc.FailApply || tc.WantHold != "" {
		if tc.ErrorContains != "" && (st.LastError == "" || !strings.Contains(st.LastError, tc.ErrorContains)) {
			t.Fatalf("lastError %q, want substring %q", st.LastError, tc.ErrorContains)
		}
		if st.HoldReason != tc.WantHold {
			t.Fatalf("hold %q, want %q", st.HoldReason, tc.WantHold)
		}
		assertLoopBackoff(t, st, tc)
		return
	}
	assertScale(t, st, tc, w.written[planRef(ns, release)], seed)
	assertLoopBackoff(t, st, tc)
}

func runLLMLoop(t *testing.T, tc loopCase) {
	t.Helper()
	const ns, release = "models", "pd"
	dec := decisionServer(t, ns, release, tc.Decision)
	p, _ := pdPlan(t, ns, release, release, tc.Slices, true, false, "v2")

	probe := fakeProbe()
	setDecisionProfile(probe, dec.URL)
	probe.MapLabels = map[string]map[string]string{}
	probe.Deps = readyRouters(ns, release, tc.Slices)
	probe.DSs = map[string]cluster.DisaggregatedSet{ns + "/" + release: readyDS(tc.Slices)}
	probe.Secrets = map[string]map[string]string{}
	probe.SecretLabels = map[string]map[string]string{}
	seedAutoscaleCM(&probe, ns, release, true, tc.Min, tc.Max)

	rig := startRig(t, probe, true, !tc.Conflict, "")
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
		"phase":              llmsvc.PhaseApplied,
		"helm":               map[string]any{"revision": int64(rev), "status": "deployed"},
	}, "status"); err != nil {
		t.Fatal(err)
	}
	u.SetGeneration(1)
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
	if obj.Status == nil || obj.Status.Phase != llmsvc.PhaseApplied || obj.Status.Helm == nil || obj.Status.Helm.Revision != int64(rev) {
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

	rig.s.autoscaleTick(t.Context())
	st := readAutoscaleStatus(t, rig.w, autoscaleRef(ns, release))
	gotGets := gets.Load()
	if int(updates.Load()) != tc.WantApplies {
		t.Fatalf("updates %d, want %d (gets %d) status=%+v", updates.Load(), tc.WantApplies, gotGets, st)
	}
	got, err := rig.c.Get(t.Context(), ns, release)
	if err != nil {
		t.Fatal(err)
	}
	if tc.Conflict {
		if gotGets != 7 {
			t.Errorf("Get calls %d, want 7", gotGets)
		}
		if st.LastError == "" || !strings.Contains(st.LastError, tc.ErrorContains) {
			t.Fatalf("lastError %q, want substring %q (gets %d)", st.LastError, tc.ErrorContains, gets.Load())
		}
		if got.GetAnnotations()[llmsvc.AnnAction] == "autoscale" {
			t.Fatalf("conflict updated the object: %v", got.GetAnnotations())
		}
		assertLoopBackoff(t, st, tc)
		return
	}
	if got.GetAnnotations()[llmsvc.AnnAction] != "autoscale" {
		t.Fatalf("action %q", got.GetAnnotations()[llmsvc.AnnAction])
	}
	note := fmt.Sprintf("autoscale %d -> %d (decision-gen)", *tc.WantFrom, *tc.WantTo)
	if got.GetAnnotations()[llmsvc.AnnNote] != note {
		t.Fatalf("note %q, want %q", got.GetAnnotations()[llmsvc.AnnNote], note)
	}
	layers, _, _ := unstructured.NestedSlice(got.Object, "spec", "layers")
	if len(layers) == 0 {
		t.Fatal("no layers")
	}
	last, _ := layers[len(layers)-1].(map[string]any)
	if last["name"] != "autoscale" {
		t.Fatalf("last layer %v", last["name"])
	}
	vals, _ := last["values"].(map[string]any)
	if asNumber(vals["replicaCount"]) != *tc.WantTo {
		t.Fatalf("autoscale replicaCount %v, want %d", vals["replicaCount"], *tc.WantTo)
	}
	for _, raw := range layers[:len(layers)-1] {
		layer, _ := raw.(map[string]any)
		if layer["name"] != "form" {
			continue
		}
		fv, _ := layer["values"].(map[string]any)
		if asNumber(fv["replicaCount"]) != tc.Slices {
			t.Fatalf("form replicaCount changed to %v", fv["replicaCount"])
		}
	}
	if st.LastError != "" || st.HoldReason != "" {
		t.Fatalf("clean scale recorded hold %q error %q", st.HoldReason, st.LastError)
	}
	if st.LastDecision == nil || *st.LastDecision != tc.Decision {
		t.Fatalf("lastDecision %v, want %d", st.LastDecision, tc.Decision)
	}
	if st.LastScaleFrom == nil || *st.LastScaleFrom != *tc.WantFrom || st.LastScaleTo == nil || *st.LastScaleTo != *tc.WantTo {
		t.Fatalf("scale %v -> %v, want %d -> %d", st.LastScaleFrom, st.LastScaleTo, *tc.WantFrom, *tc.WantTo)
	}
	assertLoopBackoff(t, st, tc)
}

func assertScale(t *testing.T, st autoscale.Status, tc loopCase, written, seed map[string]string) {
	t.Helper()
	if st.LastError != "" || st.HoldReason != "" {
		t.Fatalf("clean scale recorded hold %q error %q", st.HoldReason, st.LastError)
	}
	if st.LastDecision == nil || *st.LastDecision != tc.Decision {
		t.Fatalf("lastDecision %v, want %d", st.LastDecision, tc.Decision)
	}
	if tc.WantFrom != nil && (st.LastScaleFrom == nil || *st.LastScaleFrom != *tc.WantFrom) {
		t.Fatalf("from %v, want %d", st.LastScaleFrom, *tc.WantFrom)
	}
	if tc.WantTo != nil && (st.LastScaleTo == nil || *st.LastScaleTo != *tc.WantTo) {
		t.Fatalf("to %v, want %d", st.LastScaleTo, *tc.WantTo)
	}
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
	if f := configMapFake(probe); f != nil {
		initMapVersions(f)
		w.cm = f
	}
	s.SetWriter(w)
	return s, w
}

func configMapFake(p cluster.Probe) *cluster.Fake {
	switch x := p.(type) {
	case *cluster.Fake:
		return x
	case *revProbe:
		return &x.Fake
	case *gateProbe:
		return &x.Fake
	default:
		return nil
	}
}

func initMapVersions(f *cluster.Fake) {
	if f == nil {
		return
	}
	if f.MapVersions == nil {
		f.MapVersions = map[string]string{}
	}
	for ref := range f.Maps {
		if f.MapVersions[ref] == "" {
			f.MapVersions[ref] = "1"
		}
	}
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

func assertLoopBackoff(t *testing.T, st autoscale.Status, tc loopCase) {
	t.Helper()
	if tc.WantFailures != nil && st.Failures != *tc.WantFailures {
		t.Fatalf("failures %d, want %d (%+v)", st.Failures, *tc.WantFailures, st)
	}
	if tc.WantLastFailedWant != nil && (st.LastFailedWant == nil || *st.LastFailedWant != *tc.WantLastFailedWant) {
		t.Fatalf("lastFailedWant %v, want %d", st.LastFailedWant, *tc.WantLastFailedWant)
	}
	if tc.WantRetryDelta != "" {
		d, err := time.ParseDuration(tc.WantRetryDelta)
		if err != nil {
			t.Fatal(err)
		}
		at, err := time.Parse(time.RFC3339, st.LastFailedAt)
		if err != nil {
			t.Fatalf("lastFailedAt %q: %v", st.LastFailedAt, err)
		}
		rt, err := time.Parse(time.RFC3339, st.RetryAfter)
		if err != nil {
			t.Fatalf("retryAfter %q: %v", st.RetryAfter, err)
		}
		if rt.Sub(at) != d {
			t.Fatalf("retry delta %s, want %s (at %s after %s)", rt.Sub(at), d, st.RetryAfter, st.LastFailedAt)
		}
	}
	if tc.WantCleared && (st.Failures != 0 || st.RetryAfter != "" || st.LastFailedAt != "" || st.LastFailedWant != nil) {
		t.Fatalf("backoff not cleared: %+v", st)
	}
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

func failCountBin(t *testing.T, logPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bin")
	script := "#!/bin/sh\necho called >> '" + logPath + "'\nexit 1\n"
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
