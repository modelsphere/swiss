package server

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/llmsvc"
	"github.com/modelsphere/swiss/internal/testutil/cases"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
)

type inferCase struct {
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Backend      string `json:"backend"`
	PD           *bool  `json:"pd,omitempty"`
	Colocate     *bool  `json:"colocate,omitempty"`
	SLO          *bool  `json:"slo,omitempty"`
	NoDecision   bool   `json:"noDecision,omitempty"`
	WantQueries  int    `json:"wantQueries"`
	WantApplies  int    `json:"wantApplies"`
	WantReplicas *int   `json:"wantReplicas,omitempty"`
}

func TestAutoscaleInfer(t *testing.T) {
	cases.Run(t, "testdata/autoscale-infer", func(t *testing.T, tc inferCase) {
		switch tc.Backend {
		case backendHelm:
			runHelmInfer(t, tc)
		case backendLLMSVC:
			runLLMInfer(t, tc)
		default:
			t.Fatalf("backend %q", tc.Backend)
		}
	})
}

func runHelmInfer(t *testing.T, tc inferCase) {
	t.Helper()
	const ns, release = "models", "pd"
	dec, hits := countingDecisions(t, ns, release, 2, tc.NoDecision)
	_, seed := pdPlan(t, ns, release, release, 1, boolOr(tc.PD, false), boolOr(tc.Colocate, false), tc.SLO, "")

	probe := fakeProbe()
	setDecisionProfile(probe, dec.URL)
	probe.Deps = readyRouters(ns, release, 1)
	probe.DSs = map[string]cluster.DisaggregatedSet{ns + "/" + release: readyDS(1)}
	probe.Maps[planRef(ns, release)] = seed
	probe.Rel = []cluster.Release{{
		Name: release, Namespace: ns, Status: "deployed", Revision: 2,
		Chart: "sglang-0.7.0", SwissFiles: seed,
	}}

	logPath := filepath.Join(t.TempDir(), "helmfile.log")
	bin := countBin(t, logPath)
	s, w := newLoopServer(t, &probe, bin, bin)
	s.autoscaleTick(t.Context())

	if int(hits.Load()) != tc.WantQueries {
		t.Fatalf("decision queries %d, want %d", hits.Load(), tc.WantQueries)
	}
	applies := countLines(t, logPath)
	if applies != tc.WantApplies {
		t.Fatalf("helmfile calls %d, want %d", applies, tc.WantApplies)
	}
	written := w.written[planRef(ns, release)]
	if tc.WantApplies == 0 {
		if written != nil || len(w.written) != 0 {
			t.Fatalf("tick wrote %v", w.written)
		}
		return
	}
	if written == nil {
		t.Fatal("plan was not written")
	}
	if !strings.Contains(written["autoscale.yaml"], fmt.Sprintf("replicaCount: %d", *tc.WantReplicas)) {
		t.Fatalf("autoscale.yaml:\n%s", written["autoscale.yaml"])
	}
	if seed["form.yaml"] != written["form.yaml"] {
		t.Fatalf("form.yaml changed\n got %s\n want %s", written["form.yaml"], seed["form.yaml"])
	}
}

func runLLMInfer(t *testing.T, tc inferCase) {
	t.Helper()
	const ns, release = "models", "pd"
	dec, hits := countingDecisions(t, ns, release, 2, tc.NoDecision)
	p, _ := pdPlan(t, ns, release, release, 1, boolOr(tc.PD, false), boolOr(tc.Colocate, false), tc.SLO, "v2")

	probe := fakeProbe()
	setDecisionProfile(probe, dec.URL)
	probe.Deps = readyRouters(ns, release, 1)
	probe.DSs = map[string]cluster.DisaggregatedSet{ns + "/" + release: readyDS(1)}
	probe.Secrets = map[string]map[string]string{}
	probe.SecretLabels = map[string]map[string]string{}

	rig := startRig(t, probe, true, tc.WantApplies > 0, "")
	u, err := llmsvc.FromPlan(p, "install", "seed")
	if err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedMap(u.Object, map[string]any{
		"observedGeneration": int64(1),
		"phase":              llmsvc.PhaseApplied,
		"helm":               map[string]any{"revision": int64(2), "status": "deployed"},
	}, "status"); err != nil {
		t.Fatal(err)
	}
	u.SetGeneration(1)
	if _, err := rig.c.Create(t.Context(), u); err != nil {
		t.Fatal(err)
	}

	var updates atomic.Int32
	rig.dyn.PrependReactor("update", "llmservices", func(k8stesting.Action) (bool, runtime.Object, error) {
		updates.Add(1)
		return false, nil, nil
	})
	beforeW := len(rig.w.written)
	rig.s.autoscaleTick(t.Context())

	if int(hits.Load()) != tc.WantQueries {
		t.Fatalf("decision queries %d, want %d", hits.Load(), tc.WantQueries)
	}
	if int(updates.Load()) != tc.WantApplies {
		t.Fatalf("updates %d, want %d", updates.Load(), tc.WantApplies)
	}
	got, err := rig.c.Get(t.Context(), ns, release)
	if err != nil {
		t.Fatal(err)
	}
	if tc.WantApplies == 0 {
		if got.GetAnnotations()[llmsvc.AnnAction] == "autoscale" {
			t.Fatalf("tick updated the object: %v", got.GetAnnotations())
		}
		if len(rig.w.written) != beforeW {
			t.Fatalf("tick wrote %v", rig.w.written)
		}
		return
	}
	if got.GetAnnotations()[llmsvc.AnnAction] != "autoscale" {
		t.Fatalf("action %q", got.GetAnnotations()[llmsvc.AnnAction])
	}
	assertLLMAutoscaleLayer(t, got, 1, *tc.WantReplicas)
}

func countingDecisions(t *testing.T, ns, serviceID string, active int, empty bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	body := fmt.Sprintf(`{"apiVersion":"llmscaling.inference.x-k8s.io/v1alpha1","decisions":[{"namespace":"%s","serviceId":"%s","replicas":{"active":%d}}]}`, ns, serviceID, active)
	if empty {
		body = `{"apiVersion":"llmscaling.inference.x-k8s.io/v1alpha1","decisions":[]}`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}
