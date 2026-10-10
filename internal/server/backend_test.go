package server

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/llmsvc"
	"github.com/modelsphere/swiss/internal/store"
)

func TestChooseBackend(t *testing.T) {
	// served, LLMService present, plan ConfigMap present.
	cases := []struct {
		name    string
		served  bool
		svc     bool
		cm      bool
		want    string
		cleanup bool
	}{
		{"unserved, nothing", false, false, false, "", false},
		{"unserved, service only", false, true, false, "", false},
		{"unserved, configmap", false, false, true, backendHelm, false},
		{"unserved, both", false, true, true, backendHelm, false},
		{"served, nothing", true, false, false, "", false},
		{"served, configmap", true, false, true, backendHelm, false},
		{"served, service", true, true, false, backendLLMSVC, false},
		{"served, both", true, true, true, backendLLMSVC, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, cleanup := chooseBackend(tc.served, tc.svc, tc.cm)
			if got != tc.want || cleanup != tc.cleanup {
				t.Fatalf("chooseBackend(%v, %v, %v) = %q cleanup %v, want %q cleanup %v",
					tc.served, tc.svc, tc.cm, got, cleanup, tc.want, tc.cleanup)
			}
		})
	}
}

// TestBackendFor reads the same table off a fake cluster: an unserved CRD
// hides an LLMService, and a plan ConfigMap still selects helm.
func TestBackendFor(t *testing.T) {
	ctx := t.Context()
	cases := []struct {
		name    string
		client  bool
		served  bool
		svc     bool
		cm      bool
		want    string
		cleanup bool
	}{
		{"no client, configmap", false, false, false, true, backendHelm, false},
		{"no client, nothing", false, false, false, false, "", false},
		{"unserved, service only", true, false, true, false, "", false},
		{"unserved, both", true, false, true, true, backendHelm, false},
		{"served, nothing", true, true, false, false, "", false},
		{"served, configmap", true, true, false, true, backendHelm, false},
		{"served, service", true, true, true, false, backendLLMSVC, false},
		{"served, both", true, true, true, true, backendLLMSVC, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := fakeProbe()
			if tc.cm {
				probe.Maps[planRef("models", "qwen")] = map[string]string{"plan.yaml": "source: {}\n"}
			}
			s := New(testConfig("prod"), probe, discardLogger(), "test")
			if tc.client {
				dyn := newDyn()
				disco := emptyDisco()
				if tc.served {
					disco = servedDisco()
				}
				c := llmsvc.New(dyn, disco, nil)
				s.SetLLMServices(c)
				if tc.svc {
					u := &unstructured.Unstructured{Object: map[string]any{
						"apiVersion": llmsvc.APIVersion,
						"kind":       llmsvc.Kind,
						"metadata": map[string]any{
							"name":      "qwen",
							"namespace": "models",
						},
						"spec": map[string]any{"chart": map[string]any{"name": "sglang"}},
					}}
					if _, err := c.Create(ctx, u); err != nil {
						t.Fatal(err)
					}
				}
			}
			b, cleanup, err := s.backendFor(ctx, "models", "qwen")
			if err != nil {
				t.Fatal(err)
			}
			got := ""
			if b != nil {
				got = b.Name()
			}
			if got != tc.want || cleanup != tc.cleanup {
				t.Fatalf("backend %q cleanup %v, want %q cleanup %v", got, cleanup, tc.want, tc.cleanup)
			}
		})
	}
}

// A swissd with no LLMService client keeps today's deployment list, plus the
// backend badge. external and cleanupPending stay absent.
func TestNoLLMClientKeepsHelmBehaviour(t *testing.T) {
	probe := fakeProbe()
	probe.Rel = []cluster.Release{{
		Name: "rel-00", Namespace: "models", Chart: "sglang-0.7.1",
		Status: "deployed", Revision: 1,
		SwissFiles: map[string]string{"plan.yaml": "source:\n  model: qwen3.6-35b-a3b\n"},
	}}
	srv := testServer(t, probe)

	code, body := get(t, srv, "/api/deployments")
	if code != 200 {
		t.Fatalf("deployments %d %v", code, body)
	}
	rows := body["deployments"].([]any)
	if len(rows) != 1 {
		t.Fatalf("rows %d", len(rows))
	}
	row := rows[0].(map[string]any)
	if row["backend"] != "helm" || row["release"] != "rel-00" {
		t.Fatalf("row %v", row)
	}
	if _, ok := row["external"]; ok {
		t.Fatalf("external must stay absent: %v", row)
	}
	if _, ok := row["cleanupPending"]; ok {
		t.Fatalf("cleanupPending must stay absent: %v", row)
	}

	code, body = get(t, srv, "/api/cluster")
	if code != 200 {
		t.Fatalf("cluster %d %v", code, body)
	}
	info := body["llmservice"].(map[string]any)
	if info["served"] != false || info["applyWith"] != "helm" || info["newInstalls"] != "helm" {
		t.Fatalf("llmservice %v", info)
	}
	for _, w := range warningsOf(body) {
		if strings.Contains(w, "LLMService CRD is not served") {
			t.Fatalf("default applyWith must not warn: %v", body["warnings"])
		}
	}
}

// gateServer is a swissd with a real LLMService client whose discovery or
// RBAC can be broken, so a pure-helm install can be compared with "no CRD".
type gateServer struct {
	srv   *httptest.Server
	s     *Server
	w     *fakeWriter
	dyn   *dynamicfake.FakeDynamicClient
	disco *fake.FakeDiscovery
	calls atomic.Int32
	log   *bytes.Buffer
}

func newGateServer(t *testing.T, kind, applyWith, helmBin, helmfileBin string) *gateServer {
	t.Helper()
	var buf bytes.Buffer
	log := discardLogger()
	g := &gateServer{}
	if kind == "discovery" {
		g.log = &buf
		log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	cfg := testConfig("prod-b300")
	cfg.Server.AllowDeploy = true
	cfg.Server.ApplyWith = applyWith
	cfg.Server.HelmBin = helmBin
	cfg.Server.HelmfileBin = helmfileBin
	s := New(cfg, liveProbe(), log, "test")
	db, err := store.Open(filepath.Join(t.TempDir(), "swiss.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s.SetStore(db)
	w := &fakeWriter{}
	s.SetWriter(w)

	dyn := newDyn()
	disco := emptyDisco()
	switch kind {
	case "served", "forbidden":
		disco = servedDisco()
	}
	c := llmsvc.New(dyn, disco, nil)
	s.SetLLMServices(c)
	g.srv, g.s, g.w, g.dyn, g.disco = nil, s, w, dyn, disco
	switch kind {
	case "forbidden":
		react := func(k8stesting.Action) (bool, runtime.Object, error) {
			g.calls.Add(1)
			gr := schema.GroupResource{Group: llmsvc.Group, Resource: llmsvc.Resource}
			return true, nil, apierrors.NewForbidden(gr, "llmservices", errors.New("rbac"))
		}
		dyn.PrependReactor("get", "llmservices", react)
		dyn.PrependReactor("list", "llmservices", react)
	case "discovery":
		disco.PrependReactor("*", "*", func(k8stesting.Action) (bool, runtime.Object, error) {
			g.calls.Add(1)
			return true, nil, fmt.Errorf("discovery down")
		})
	case "listerr":
		disco = servedDisco()
		c = llmsvc.New(dyn, disco, nil)
		s.SetLLMServices(c)
		g.disco = disco
		dyn.PrependReactor("list", "llmservices", func(k8stesting.Action) (bool, runtime.Object, error) {
			g.calls.Add(1)
			return true, nil, fmt.Errorf("apiserver timeout")
		})
	}

	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	g.srv = srv
	return g
}

// A served CRD that this swissd cannot read must not turn the helm paths into
// 502s. Forbidden and a discovery error both behave as an unserved CRD.
func TestLLMInvisibleMatchesUnserved(t *testing.T) {
	helmBin := failBin(t, "helm")
	helmfileBin := failBin(t, "helmfile")
	base := newGateServer(t, "unserved", backendHelm, helmBin, helmfileBin)

	for _, kind := range []string{"forbidden", "discovery"} {
		t.Run(kind, func(t *testing.T) {
			other := newGateServer(t, kind, backendHelm, helmBin, helmfileBin)
			for _, path := range []string{
				"/api/deployments",
				"/api/releases/models/glm-53/status",
			} {
				bc, bb := get(t, base.srv, path)
				oc, ob := get(t, other.srv, path)
				if bc != 200 || oc != bc || !reflect.DeepEqual(bb, ob) {
					t.Fatalf("%s base %d %v other %d %v", path, bc, bb, oc, ob)
				}
			}
			// A second deployments read must not call the API again.
			if _, _ = get(t, other.srv, "/api/deployments"); kind == "forbidden" && other.calls.Load() != 1 {
				t.Fatalf("forbidden list calls %d, want 1 for the TTL", other.calls.Load())
			}
			if _, _ = get(t, other.srv, "/api/releases/models/glm-53/status"); kind == "forbidden" && other.calls.Load() != 1 {
				t.Fatalf("status called get after forbidden was cached: %d", other.calls.Load())
			}

			bHash := gatePlan(t, base)
			oHash := gatePlan(t, other)
			bc, bb := post(t, base.srv, "/api/install", map[string]any{"planHash": bHash})
			oc, ob := post(t, other.srv, "/api/install", map[string]any{"planHash": oHash})
			if bc != oc || bc == http.StatusBadGateway {
				t.Fatalf("install base %d %v other %d %v", bc, bb, oc, ob)
			}
			if _, ok := base.w.written[planRef("models", "qwen-new")]; !ok {
				t.Fatal("unserved install did not write the plan ConfigMap")
			}
			if _, ok := other.w.written[planRef("models", "qwen-new")]; !ok {
				t.Fatal("hidden install did not write the plan ConfigMap")
			}
			bc, bb = post(t, base.srv, "/api/apply", map[string]any{"planHash": bHash})
			oc, ob = post(t, other.srv, "/api/apply", map[string]any{"planHash": oHash})
			if bc != oc || bc == http.StatusBadGateway {
				t.Fatalf("upgrade base %d %v other %d %v", bc, bb, oc, ob)
			}
			if kind == "forbidden" && other.calls.Load() != 1 {
				t.Fatalf("apply called the API after forbidden was cached: %d", other.calls.Load())
			}

			_, body := get(t, other.srv, "/api/cluster")
			info := body["llmservice"].(map[string]any)
			if info["newInstalls"] != "helm" || info["applyWith"] != "helm" {
				t.Fatalf("llmservice %v", info)
			}
			warns := warningsOf(body)
			switch kind {
			case "forbidden":
				if info["served"] != true || !containsString(warns, "LLMService CRD is served but swissd has no llmservices grant; set rbac.applyWith") {
					t.Fatalf("cluster served %v warnings %v", info["served"], warns)
				}
			case "discovery":
				if info["served"] != false || !containsString(warns, "LLMService discovery failed: discovery down") {
					t.Fatalf("cluster served %v warnings %v", info["served"], warns)
				}
				if other.calls.Load() != 1 || strings.Count(other.log.String(), "LLMService discovery failed") != 1 {
					t.Fatalf("discovery calls %d log %q", other.calls.Load(), other.log.String())
				}
			}
		})
	}
}

// applyWith llmsvc falls back to helm when the CRD is served but not granted,
// and when discovery itself fails.
func TestLLMApplyWithHiddenFallsBackToHelm(t *testing.T) {
	helmBin := failBin(t, "helm")
	helmfileBin := failBin(t, "helmfile")
	for _, kind := range []string{"forbidden", "discovery"} {
		t.Run(kind, func(t *testing.T) {
			g := newGateServer(t, kind, backendLLMSVC, helmBin, helmfileBin)
			hash := gatePlan(t, g)
			code, out := post(t, g.srv, "/api/install", map[string]any{"planHash": hash})
			if code == http.StatusBadGateway {
				t.Fatalf("install %d %v", code, out)
			}
			if _, ok := g.w.written[planRef("models", "qwen-new")]; !ok {
				t.Fatal("fallback did not write the helm plan ConfigMap")
			}
			_, body := get(t, g.srv, "/api/cluster")
			info := body["llmservice"].(map[string]any)
			if info["newInstalls"] != "helm" || info["applyWith"] != "llmsvc" {
				t.Fatalf("llmservice %v", info)
			}
			warns := warningsOf(body)
			if kind == "forbidden" {
				if info["served"] != true || !containsString(warns, "LLMService CRD is served but swissd has no llmservices grant; set rbac.applyWith") {
					t.Fatalf("served %v warnings %v", info["served"], warns)
				}
			} else if !containsString(warns, "server.applyWith is llmsvc but the LLMService CRD is not served; new installs use helm: discovery down") {
				t.Fatalf("warnings %v", warns)
			}
		})
	}
}

// Errors other than Forbidden still fail the request.
func TestLLMListErrorIsStillBadGateway(t *testing.T) {
	g := newGateServer(t, "listerr", backendHelm, failBin(t, "helm"), failBin(t, "helmfile"))
	code, body := get(t, g.srv, "/api/deployments")
	if code != http.StatusBadGateway {
		t.Fatalf("status %d %v", code, body)
	}
	if _, _ = get(t, g.srv, "/api/deployments"); g.calls.Load() != 2 {
		t.Fatalf("a non-forbidden list error was cached: calls %d", g.calls.Load())
	}
}

func gatePlan(t *testing.T, g *gateServer) string {
	t.Helper()
	code, out := post(t, g.srv, "/api/plans", map[string]any{
		"model": "qwen3.6-35b-a3b", "release": "qwen-new",
	})
	if code != 200 {
		t.Fatalf("plan %d %v", code, out)
	}
	h, _ := out["hash"].(string)
	if h == "" {
		t.Fatalf("plan %v", out)
	}
	return h
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
