package server

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/llmsvc"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/store"
)

type uninstallCase struct {
	Kind                 string `yaml:"kind"`
	Release              string `yaml:"release"`
	Namespace            string `yaml:"namespace"`
	Chart                string `yaml:"chart"`
	HelmExit             int    `yaml:"helmExit"`
	PodListError         bool   `yaml:"podListError"`
	DeleteConfigMapFails int    `yaml:"deleteConfigMapFails"`
	SeedArchive          bool   `yaml:"seedArchive"`
	SeedAutoscale        bool   `yaml:"seedAutoscale"`
	// HelmBlocks makes helm uninstall wait for a releaseHelm step, so a case
	// can act while an uninstall holds the release's lock.
	HelmBlocks    bool   `yaml:"helmBlocks"`
	OperatorPhase string `yaml:"operatorPhase"`
	// Deleting nil means the LLMService is created with a deletionTimestamp,
	// which is what the older cases describe. false creates it without one.
	Deleting          *bool          `yaml:"deleting"`
	Finalizer         bool           `yaml:"finalizer"`
	DeleteNotFound    bool           `yaml:"deleteNotFound"`
	Missing           bool           `yaml:"missing"`
	SeedPlanConfigMap bool           `yaml:"seedPlanConfigMap"`
	PlanPhase         string         `yaml:"planPhase"`
	Deletion          map[string]any `yaml:"deletion"`
	Nodes             []nodeSpec     `yaml:"nodes"`
	Pods              []podSpec      `yaml:"pods"`
	Steps             []unstep       `yaml:"steps"`
}

type nodeSpec struct {
	Name  string `yaml:"name"`
	Ready bool   `yaml:"ready"`
}

type podSpec struct {
	Name                          string            `yaml:"name"`
	Phase                         string            `yaml:"phase"`
	Namespace                     string            `yaml:"namespace"`
	Node                          string            `yaml:"node"`
	DeletionTimestamp             time.Time         `yaml:"deletionTimestamp"`
	DeletionGracePeriodSeconds    *int64            `yaml:"deletionGracePeriodSeconds"`
	TerminationGracePeriodSeconds *int64            `yaml:"terminationGracePeriodSeconds"`
	Labels                        map[string]string `yaml:"labels"`
}

type unstep struct {
	Do             string    `yaml:"do"`
	Status         int       `yaml:"status"`
	Present        *bool     `yaml:"present"`
	Phase          string    `yaml:"phase"`
	PlanPhase      string    `yaml:"planPhase"`
	ServicePhase   string    `yaml:"servicePhase"`
	ConfigMap      *bool     `yaml:"configMap"`
	PlanError      *bool     `yaml:"planError"`
	ErrorContains  string    `yaml:"errorContains"`
	OutputContains string    `yaml:"outputContains"`
	MinPods        *int      `yaml:"minPods"`
	TotalPods      *int      `yaml:"totalPods"`
	Backend        string    `yaml:"backend"`
	Pods           []podSpec `yaml:"pods"`
	Archives       *int      `yaml:"archives"`
	// Autoscale is whether swiss-autoscale-<release> exists after the step.
	Autoscale        *bool     `yaml:"autoscale"`
	Now              time.Time `yaml:"now"`
	HasRevision      *bool     `yaml:"hasRevision"`
	OperatorPhase    string    `yaml:"operatorPhase"`
	DeletionStage    string    `yaml:"deletionStage"`
	DeletionOutcome  string    `yaml:"deletionOutcome"`
	DeletionMessage  string    `yaml:"deletionMessage"`
	DeletionDeadline string    `yaml:"deletionDeadline"`
	PodsRemaining    *int      `yaml:"podsRemaining"`
	DeletionPods     *int      `yaml:"deletionPods"`
	ForceDeleted     *int      `yaml:"forceDeleted"`
	DriftContains    string    `yaml:"driftContains"`
	Audits           *int      `yaml:"audits"`
	LLMService       *bool     `yaml:"llmService"`
	StoredPhase      string    `yaml:"storedPhase"`
}

func runUninstallingCase(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cs uninstallCase
	if err := yaml.Unmarshal(raw, &cs); err != nil {
		t.Fatal(err)
	}
	if cs.Kind == "" {
		cs.Kind = "helm"
	}
	if cs.Namespace == "" {
		cs.Namespace = "models"
	}
	if cs.Release == "" {
		t.Fatal("release is required")
	}
	switch cs.Kind {
	case "helm":
		runHelmUninstallCase(t, &cs)
	case "llmsvc":
		runLLMUninstallCase(t, &cs)
	default:
		t.Fatalf("kind %q", cs.Kind)
	}
}

func runHelmUninstallCase(t *testing.T, cs *uninstallCase) {
	t.Helper()
	if cs.Chart == "" {
		cs.Chart = "vllm"
	}
	c := newUninstallCluster(t, cs)
	h := &helmHarness{cs: cs, c: c}
	t.Cleanup(func() {
		if h.srv != nil {
			h.srv.Close()
		}
	})
	h.open(t)
	for i, step := range cs.Steps {
		t.Logf("step %d %s", i, step.Do)
		code, body := h.do(t, step)
		assertUnstep(t, cs, c, step, code, body, h.s, nil)
	}
}

type helmHarness struct {
	cs  *uninstallCase
	c   *uninstallCluster
	srv *httptest.Server
	s   *Server
	now time.Time
	// gate is the file a blocked helm uninstall waits for.
	gate string
	// pending is the DELETE started by uninstallAsync.
	pending chan asyncResult
	locked  bool
}

// openGate lets a blocked helm return and waits for the DELETE it was part of.
func (h *helmHarness) openGate() (asyncResult, bool) {
	if h.gate != "" {
		_ = os.WriteFile(h.gate, nil, 0o644)
	}
	if h.pending == nil {
		return asyncResult{}, true
	}
	select {
	case r := <-h.pending:
		h.pending = nil
		return r, true
	case <-time.After(10 * time.Second):
		return asyncResult{}, false
	}
}

type asyncResult struct {
	code int
	body map[string]any
}

func (h *helmHarness) open(t *testing.T) {
	t.Helper()
	if h.srv != nil {
		h.srv.Close()
		h.srv = nil
	}
	cfg := testConfig("prod-b300")
	cfg.Server.AllowDeploy = true
	cfg.Server.HelmBin = stubHelm(t, h.cs.HelmExit)
	if h.cs.HelmBlocks {
		if h.gate == "" {
			h.gate = filepath.Join(t.TempDir(), "gate")
			// Runs before the server closes and before the temp dir goes: a
			// failed step must not leave helm, and with it the DELETE
			// handler, waiting forever.
			t.Cleanup(func() { h.openGate() })
		}
		cfg.Server.HelmBin = stubHelmGated(t, h.cs.HelmExit, h.gate)
	}
	s := New(cfg, h.c, discardLogger(), "test")
	db, err := store.Open(filepath.Join(t.TempDir(), "swiss.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s.SetStore(db)
	s.SetWriter(h.c)
	h.s = s
	if !h.now.IsZero() {
		frozen := h.now
		s.now = func() time.Time { return frozen }
	}
	h.srv = httptest.NewServer(s.Handler())
}

func (h *helmHarness) do(t *testing.T, step unstep) (int, map[string]any) {
	t.Helper()
	ns, rel := h.cs.Namespace, h.cs.Release
	switch step.Do {
	case "uninstall":
		return del(t, h.srv, "/api/releases/"+ns+"/"+rel)
	case "uninstallAsync":
		// Returns once helm is running: the lock is held, the plan is marked.
		if !h.cs.HelmBlocks {
			t.Fatal("uninstallAsync needs helmBlocks")
		}
		h.pending = make(chan asyncResult, 1)
		srv := h.srv
		go func() {
			code, body := delNoFatal(srv, "/api/releases/"+ns+"/"+rel)
			h.pending <- asyncResult{code, body}
		}()
		deadline := time.Now().Add(10 * time.Second)
		for !h.s.uninstallInflight(ns, rel) {
			if time.Now().After(deadline) {
				t.Fatal("uninstall did not start")
			}
			time.Sleep(5 * time.Millisecond)
		}
		return 0, nil
	case "releaseHelm":
		// Lets the blocked helm return; the step's status is the first DELETE's.
		if h.pending == nil {
			t.Fatal("releaseHelm without uninstallAsync")
		}
		r, ok := h.openGate()
		if !ok {
			t.Fatal("uninstall did not finish")
		}
		return r.code, r.body
	case "holdLock":
		// An apply in progress on this release, as far as the lock knows.
		if !h.s.tryApplyLock(ns, rel) {
			t.Fatal("lock already held")
		}
		h.locked = true
		t.Cleanup(func() {
			if h.locked {
				h.s.unlockApply(ns, rel)
			}
		})
		return 0, nil
	case "releaseLock":
		if !h.locked {
			t.Fatal("releaseLock without holdLock")
		}
		h.locked = false
		h.s.unlockApply(ns, rel)
		return 0, nil
	case "list":
		return get(t, h.srv, "/api/deployments?perPage=100")
	case "status":
		return get(t, h.srv, "/api/releases/"+ns+"/"+rel+"/status")
	case "setPods":
		h.c.setPods(ns, step.Pods)
		return 0, nil
	case "setNow":
		if step.Now.IsZero() {
			t.Fatal("setNow requires now")
		}
		h.now = step.Now
		if h.s != nil {
			frozen := h.now
			h.s.now = func() time.Time { return frozen }
		}
		return 0, nil
	case "seedArchives":
		if err := h.c.seedArchive(ns, rel); err != nil {
			t.Fatal(err)
		}
		return 0, nil
	case "dropHelm":
		h.c.dropHelm(ns, rel)
		return 0, nil
	case "restart":
		h.open(t)
		return 0, nil
	case "install":
		return h.mutate(t, "/api/install")
	case "upgrade", "apply":
		return h.mutate(t, "/api/apply")
	case "rollback":
		return post(t, h.srv, "/api/releases/"+ns+"/"+rel+"/rollback", map[string]any{
			"toRevision": 1, "expectRevision": 4,
		})
	default:
		t.Fatalf("unknown step %q", step.Do)
		return 0, nil
	}
}

func (h *helmHarness) mutate(t *testing.T, path string) (int, map[string]any) {
	t.Helper()
	code, body := post(t, h.srv, "/api/plans", map[string]any{
		"model": "qwen3.6-35b-a3b", "release": h.cs.Release,
		"namespace": h.cs.Namespace, "serviceId": h.cs.Release,
	})
	if code != http.StatusOK {
		return code, body
	}
	hash, _ := body["hash"].(string)
	return post(t, h.srv, path, map[string]any{"planHash": hash})
}

func runLLMUninstallCase(t *testing.T, cs *uninstallCase) {
	t.Helper()
	if cs.OperatorPhase == "" {
		cs.OperatorPhase = llmsvc.PhaseApplied
	}
	probe := llmProbe()
	if probe.MapLabels == nil {
		probe.MapLabels = map[string]map[string]string{}
	}
	if cs.SeedPlanConfigMap {
		seedLLMPlan(t, &probe, cs)
	}
	if cs.SeedArchive {
		seedLLMArchive(&probe, cs)
	}
	if cs.SeedAutoscale {
		seedLLMAutoscale(&probe, cs)
	}
	// The rig operator would rewrite status, including status.deletion.
	rig := startRig(t, probe, true, false, "")
	base := rig.s.probe.(*cluster.Fake)
	rig.s.probe = llmPlanProbe{Fake: base}
	rig.s.SetWriter(&llmMirrorWriter{fakeWriter: rig.w, probe: base})
	h := &llmUninstallHarness{cs: cs, rig: rig, base: base, srv: rig.srv}
	h.arm(t)
	if !cs.Missing {
		h.create(t)
	}
	ns, rel := cs.Namespace, cs.Release
	for i, step := range cs.Steps {
		t.Logf("step %d %s", i, step.Do)
		var code int
		var body map[string]any
		switch step.Do {
		case "list":
			code, body = get(t, h.srv, "/api/deployments?perPage=100")
		case "status":
			code, body = get(t, h.srv, "/api/releases/"+ns+"/"+rel+"/status")
		case "uninstall":
			code, body = del(t, h.srv, "/api/releases/"+ns+"/"+rel)
		case "reap":
			h.rig.s.reapUninstalling(context.Background())
		case "operatorRelease":
			if err := h.rig.dyn.Tracker().Delete(llmsvc.GVR, ns, rel); err != nil && !apierrors.IsNotFound(err) {
				t.Fatal(err)
			}
		case "clearDeletion":
			h.clearDeletion(t)
		case "restart":
			h.restart(t)
		case "install":
			code, body = llmMutate(t, h.rig, cs, "/api/install")
		case "upgrade", "apply":
			code, body = llmMutate(t, h.rig, cs, "/api/apply")
		case "rollback":
			code, body = post(t, h.srv, "/api/releases/"+ns+"/"+rel+"/rollback", map[string]any{
				"toRevision": 1, "expectRevision": 4,
			})
		default:
			t.Fatalf("unknown step %q", step.Do)
		}
		assertUnstep(t, cs, h.base, step, code, body, h.rig.s, h.rig.c)
	}
}

func llmMutate(t *testing.T, rig *llmRig, cs *uninstallCase, path string) (int, map[string]any) {
	t.Helper()
	hash := rig.storePlan(t, map[string]any{
		"release": cs.Release, "namespace": cs.Namespace,
	})
	return post(t, rig.srv, path, map[string]any{"planHash": hash})
}

func assertUnstep(t *testing.T, cs *uninstallCase, p cluster.Probe, step unstep, code int, body map[string]any, srv *Server, llms *llmsvc.Client) {
	t.Helper()
	if step.Status != 0 && code != step.Status {
		t.Fatalf("%s: status %d, body %v", step.Do, code, body)
	}
	if step.ErrorContains != "" && !strings.Contains(fmt.Sprint(body["error"]), step.ErrorContains) {
		t.Fatalf("%s: error %q, want it to contain %q", step.Do, body["error"], step.ErrorContains)
	}
	if step.OutputContains != "" && !strings.Contains(fmt.Sprint(body["output"]), step.OutputContains) {
		t.Fatalf("%s: output %q, want it to contain %q", step.Do, body["output"], step.OutputContains)
	}
	if step.PlanError != nil {
		pe, _ := body["planError"].(string)
		if *step.PlanError && pe == "" {
			t.Fatalf("%s: expected planError, body %v", step.Do, body)
		}
		if !*step.PlanError && pe != "" {
			t.Fatalf("%s: unexpected planError %q", step.Do, pe)
		}
	}
	if step.Present != nil || step.Phase != "" || step.Backend != "" || step.HasRevision != nil {
		if step.Do != "list" {
			t.Fatalf("%s sets present, phase, backend, or hasRevision; those are read from a list step", step.Do)
		}
		row, found := deploymentRowIn(body, cs.Namespace, cs.Release)
		if step.Present != nil && *step.Present != found {
			t.Fatalf("present=%v, want %v, rows %v", found, *step.Present, body["deployments"])
		}
		if step.Phase != "" {
			if !found {
				t.Fatalf("no row for %s/%s in %v", cs.Namespace, cs.Release, body["deployments"])
			}
			if row["phase"] != step.Phase {
				t.Fatalf("phase %v, want %s (%v)", row["phase"], step.Phase, row)
			}
		}
		if step.Backend != "" && (!found || row["backend"] != step.Backend) {
			t.Fatalf("backend %v, want %s", row, step.Backend)
		}
		if step.HasRevision != nil {
			if !found {
				t.Fatalf("no row for %s/%s in %v", cs.Namespace, cs.Release, body["deployments"])
			}
			_, ok := row["revision"]
			if ok != *step.HasRevision {
				t.Fatalf("revision key present=%v, want %v, row %v", ok, *step.HasRevision, row)
			}
		}
	}
	if step.PlanPhase != "" || step.ServicePhase != "" || step.MinPods != nil || step.TotalPods != nil {
		if step.Do != "status" {
			t.Fatalf("%s sets a status field; those are read from a status step", step.Do)
		}
		if step.PlanPhase != "" {
			ps, _ := body["planStatus"].(map[string]any)
			if ps["phase"] != step.PlanPhase {
				t.Fatalf("planStatus.phase %v, want %s (%v)", ps["phase"], step.PlanPhase, body["planStatus"])
			}
		}
		if step.ServicePhase != "" {
			svc, _ := body["llmservice"].(map[string]any)
			if svc["phase"] != step.ServicePhase {
				t.Fatalf("llmservice.phase %v, want %s (%v)", svc["phase"], step.ServicePhase, body["llmservice"])
			}
		}
		total := asNum(body["total"])
		if step.MinPods != nil && total < *step.MinPods {
			t.Fatalf("pods total %d, want at least %d (%v)", total, *step.MinPods, body["pods"])
		}
		if step.TotalPods != nil && total != *step.TotalPods {
			t.Fatalf("pods total %d, want %d (%v)", total, *step.TotalPods, body["pods"])
		}
	}
	if step.ConfigMap != nil || step.Autoscale != nil || step.Archives != nil || step.StoredPhase != "" {
		if p == nil {
			t.Fatal("resource assertions need a probe")
		}
	}
	if step.ConfigMap != nil {
		got := probeHasConfigMap(p, planRef(cs.Namespace, cs.Release))
		if got != *step.ConfigMap {
			t.Fatalf("configMap present=%v, want %v", got, *step.ConfigMap)
		}
	}
	if step.Autoscale != nil {
		got := probeHasConfigMap(p, autoscaleRef(cs.Namespace, cs.Release))
		if got != *step.Autoscale {
			t.Fatalf("autoscale configmap present=%v, want %v", got, *step.Autoscale)
		}
	}
	if step.Archives != nil {
		names, err := p.SecretNames(context.Background(), cs.Namespace, archiveSelector(cs.Release))
		if err != nil {
			t.Fatal(err)
		}
		if len(names) != *step.Archives {
			t.Fatalf("archives %v, want %d", names, *step.Archives)
		}
	}
	if step.StoredPhase != "" {
		data, err := p.ConfigMap(context.Background(), planRef(cs.Namespace, cs.Release))
		if err != nil {
			t.Fatal(err)
		}
		if got := parseStatus([]byte(data[statusFile])).Phase; got != step.StoredPhase {
			t.Fatalf("stored phase %q, want %q", got, step.StoredPhase)
		}
	}
	if step.OperatorPhase != "" {
		if step.Do != "status" {
			t.Fatalf("%s sets operatorPhase; that is read from a status step", step.Do)
		}
		svc, _ := body["llmservice"].(map[string]any)
		if svc["operatorPhase"] != step.OperatorPhase {
			t.Fatalf("operatorPhase %v, want %s (%v)", svc["operatorPhase"], step.OperatorPhase, svc)
		}
	}
	if step.DeletionStage != "" || step.DeletionOutcome != "" || step.DeletionMessage != "" || step.DeletionDeadline != "" || step.PodsRemaining != nil || step.DeletionPods != nil || step.ForceDeleted != nil {
		del := deletionBlock(t, cs, step, body)
		if step.DeletionStage != "" && del["stage"] != step.DeletionStage {
			t.Fatalf("deletion.stage %v, want %s (%v)", del["stage"], step.DeletionStage, del)
		}
		if step.DeletionOutcome != "" && del["outcome"] != step.DeletionOutcome {
			t.Fatalf("deletion.outcome %v, want %s (%v)", del["outcome"], step.DeletionOutcome, del)
		}
		if step.DeletionMessage != "" && del["message"] != step.DeletionMessage {
			t.Fatalf("deletion.message %v, want %s", del["message"], step.DeletionMessage)
		}
		if step.DeletionDeadline != "" && del["deadline"] != step.DeletionDeadline {
			t.Fatalf("deletion.deadline %v, want %s", del["deadline"], step.DeletionDeadline)
		}
		if step.PodsRemaining != nil && asNum(del["podsRemaining"]) != *step.PodsRemaining {
			t.Fatalf("podsRemaining %v, want %d (%v)", del["podsRemaining"], *step.PodsRemaining, del)
		}
		if step.DeletionPods != nil {
			pods, _ := del["pods"].([]any)
			if len(pods) != *step.DeletionPods {
				t.Fatalf("deletion pods %d, want %d (%v)", len(pods), *step.DeletionPods, del["pods"])
			}
		}
		if step.ForceDeleted != nil {
			names, _ := del["forceDeleted"].([]any)
			if len(names) != *step.ForceDeleted {
				t.Fatalf("forceDeleted %d, want %d (%v)", len(names), *step.ForceDeleted, del["forceDeleted"])
			}
		}
	}
	if step.DriftContains != "" {
		if step.Do != "list" {
			t.Fatalf("%s sets driftContains; that is read from a list step", step.Do)
		}
		row, found := deploymentRowIn(body, cs.Namespace, cs.Release)
		if !found || !strings.Contains(fmt.Sprint(row["drift"]), step.DriftContains) {
			t.Fatalf("drift %v, want it to contain %q", row["drift"], step.DriftContains)
		}
	}
	if step.Audits != nil {
		if srv == nil || srv.store == nil {
			t.Fatal("audits need a store")
		}
		runs, err := srv.store.RunsFiltered(context.Background(), store.RunFilter{
			Namespace: cs.Namespace, Release: cs.Release, Action: "uninstall", Limit: 100,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) != *step.Audits {
			t.Fatalf("uninstall audits %d, want %d", len(runs), *step.Audits)
		}
	}
	if step.LLMService != nil {
		if llms == nil {
			t.Fatal("llmService needs a client")
		}
		_, err := llms.Get(context.Background(), cs.Namespace, cs.Release)
		exists := err == nil
		if err != nil && !apierrors.IsNotFound(err) {
			t.Fatal(err)
		}
		if exists != *step.LLMService {
			t.Fatalf("llmservice exists=%v, want %v", exists, *step.LLMService)
		}
	}
}

func probeHasConfigMap(p cluster.Probe, ref string) bool {
	data, err := p.ConfigMap(context.Background(), ref)
	if err != nil {
		return false
	}
	return len(data) > 0
}

func deletionBlock(t *testing.T, cs *uninstallCase, step unstep, body map[string]any) map[string]any {
	t.Helper()
	switch step.Do {
	case "status":
		svc, _ := body["llmservice"].(map[string]any)
		del, _ := svc["deletion"].(map[string]any)
		if del == nil {
			t.Fatalf("no llmservice.deletion in %v", svc)
		}
		return del
	case "list":
		row, found := deploymentRowIn(body, cs.Namespace, cs.Release)
		if !found {
			t.Fatalf("no row for %s/%s", cs.Namespace, cs.Release)
		}
		del, _ := row["deletion"].(map[string]any)
		if del == nil {
			t.Fatalf("no deletion on %v", row)
		}
		return del
	default:
		t.Fatalf("%s sets a deletion field; those are read from a list or status step", step.Do)
		return nil
	}
}

func deploymentRowIn(body map[string]any, ns, release string) (map[string]any, bool) {
	rows, _ := body["deployments"].([]any)
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		if row["namespace"] == ns && row["release"] == release {
			return row, true
		}
	}
	return nil, false
}

// uninstallCluster is one object behind both Probe and Writer, so a write the
// server makes is what the next read sees. Fake splits those, and its Pods
// ignores the selector, which is the difference these cases exist to pin.
type uninstallCluster struct {
	cluster.Fake
	mu            sync.Mutex
	cms           map[string]*cmRec
	pods          []podFix
	helm          map[string]*helmRec
	secs          map[string]*secretRec
	podErr        error
	deleteCMFails int
}

type cmRec struct {
	data   map[string]string
	labels map[string]string
}

type podFix struct {
	name, phase, namespace, node string
	deletion                     time.Time
	deletionGrace                *int64
	terminationGrace             *int64
	labels                       map[string]string
}

type helmRec struct {
	revision int
	status   string
	chart    string
}

type secretRec struct {
	data   map[string]string
	labels map[string]string
}

func newUninstallCluster(t *testing.T, cs *uninstallCase) *uninstallCluster {
	t.Helper()
	c := &uninstallCluster{Fake: fakeProbe()}
	c.Nod = []cluster.Node{
		{Name: "gpu-1", GPUProduct: "NVIDIA-B300-SXM6-AC", GPUs: 8, Schedulable: true, Ready: true},
	}
	if len(cs.Nodes) > 0 {
		c.Nod = nil
		for _, n := range cs.Nodes {
			c.Nod = append(c.Nod, cluster.Node{Name: n.Name, Ready: n.Ready, Schedulable: true})
		}
	}
	c.cms = map[string]*cmRec{}
	c.helm = map[string]*helmRec{}
	c.secs = map[string]*secretRec{}
	if cs.PodListError {
		c.podErr = fmt.Errorf("pods list forbidden")
	}
	c.deleteCMFails = cs.DeleteConfigMapFails

	p := &plan.Plan{
		APIVersion: plan.APIVersion,
		Release:    plan.Release{Name: cs.Release, Namespace: cs.Namespace},
		Source:     plan.SourceRef{Model: "qwen3.6-35b-a3b", Variant: "sglang-tp2-h100"},
		Chart:      plan.ChartRef{Name: cs.Chart, Version: "0.7.1"},
	}
	if err := p.ComputeHash(); err != nil {
		t.Fatal(err)
	}
	files, err := p.Files("")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := yaml.Marshal(planStatus{
		Phase: phaseApplied, Action: "install", Revision: 4, StartedAt: stamp(metav1.Now().Time),
	})
	if err != nil {
		t.Fatal(err)
	}
	files[statusFile] = string(raw)
	ctx := context.Background()
	if err := c.PutConfigMap(ctx, planRef(cs.Namespace, cs.Release), files, nil); err != nil {
		t.Fatal(err)
	}
	c.helm[cs.Namespace+"/"+cs.Release] = &helmRec{revision: 4, status: "deployed", chart: cs.Chart + "-0.7.1"}
	c.setPods(cs.Namespace, cs.Pods)
	if cs.SeedArchive {
		if err := c.seedArchive(cs.Namespace, cs.Release); err != nil {
			t.Fatal(err)
		}
	}
	if cs.SeedAutoscale {
		ref := autoscaleRef(cs.Namespace, cs.Release)
		data := map[string]string{autoscaleConfigKey: "enabled: true\nminSlices: 1\nmaxSlices: 4\n"}
		if err := c.PutLabeledConfigMap(ctx, ref, data, autoscaleLabels(cs.Release)); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

// stubHelmGated is stubHelm whose uninstall waits until gate exists.
func stubHelmGated(t *testing.T, exitCode int, gate string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "helm")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = uninstall ]; then\n" +
		"  while [ ! -f " + gate + " ]; do sleep 0.02; done\n" +
		"fi\n" +
		"echo \"helm $*\"\n" +
		"exit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// delNoFatal is del for a goroutine, where t.Fatal is not allowed.
func delNoFatal(srv *httptest.Server, path string) (int, map[string]any) {
	req, err := http.NewRequest(http.MethodDelete, srv.URL+path, nil)
	if err != nil {
		return 0, map[string]any{"error": err.Error()}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, map[string]any{"error": err.Error()}
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (c *uninstallCluster) seedArchive(ns, release string) error {
	ctx := context.Background()
	data, err := c.ConfigMap(ctx, planRef(ns, release))
	if err != nil {
		return err
	}
	return c.PutSecret(ctx, archiveRef(ns, release, 3), data, archiveLabels(release, 3))
}

func (c *uninstallCluster) setPods(ns string, pods []podSpec) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pods = nil
	for _, p := range pods {
		n := p.Namespace
		if n == "" {
			n = ns
		}
		phase := p.Phase
		if phase == "" {
			phase = "Running"
		}
		c.pods = append(c.pods, podFix{
			name: p.Name, phase: phase, namespace: n, node: p.Node,
			deletion: p.DeletionTimestamp, deletionGrace: cloneInt64(p.DeletionGracePeriodSeconds),
			terminationGrace: cloneInt64(p.TerminationGracePeriodSeconds), labels: maps.Clone(p.Labels),
		})
	}
}

func cloneInt64(p *int64) *int64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func (c *uninstallCluster) dropHelm(ns, release string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.helm, ns+"/"+release)
}

func (c *uninstallCluster) hasConfigMap(ref string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.cms[ref]
	return ok
}

func (c *uninstallCluster) ConfigMap(_ context.Context, ref string) (map[string]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cm, ok := c.cms[ref]; ok {
		return maps.Clone(cm.data), nil
	}
	if data, ok := c.Maps[ref]; ok && len(data) > 0 {
		return maps.Clone(data), nil
	}
	_, name, err := cluster.SplitRef(ref)
	if err != nil {
		name = ref
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, name)
}

func (c *uninstallCluster) PutConfigMap(_ context.Context, ref string, data, labels map[string]string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	labs := map[string]string{"app.kubernetes.io/managed-by": "swiss"}
	for k, v := range labels {
		if v != "" {
			labs[k] = v
		}
	}
	if c.cms == nil {
		c.cms = map[string]*cmRec{}
	}
	c.cms[ref] = &cmRec{data: maps.Clone(data), labels: labs}
	return nil
}

func (c *uninstallCluster) PutLabeledConfigMap(ctx context.Context, ref string, data, labels map[string]string) error {
	return c.PutConfigMap(ctx, ref, data, labels)
}

func (c *uninstallCluster) UpdateConfigMapKey(_ context.Context, ref, key, value, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cm, ok := c.cms[ref]
	if !ok {
		return apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, ref)
	}
	if cm.data == nil {
		cm.data = map[string]string{}
	}
	cm.data[key] = value
	return nil
}

func (c *uninstallCluster) DeleteConfigMap(_ context.Context, ref string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.deleteCMFails > 0 {
		c.deleteCMFails--
		return fmt.Errorf("configmap delete forbidden")
	}
	delete(c.cms, ref)
	return nil
}

func (c *uninstallCluster) ManagedRefs(context.Context) ([]cluster.ManagedRef, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []cluster.ManagedRef
	for ref, cm := range c.cms {
		ns, name, err := cluster.SplitRef(ref)
		if err != nil {
			continue
		}
		rel, ok := strings.CutPrefix(name, cluster.PlanConfigMapPrefix)
		if !ok || rel == "" {
			continue
		}
		out = append(out, cluster.ManagedRef{
			Namespace: ns, Name: rel, Labels: maps.Clone(cm.labels),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (c *uninstallCluster) Release(_ context.Context, namespace, name string) (*cluster.Release, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var files map[string]string
	var status []byte
	if cm, ok := c.cms[namespace+"/"+cluster.PlanConfigMapPrefix+name]; ok {
		files = maps.Clone(cm.data)
		status = []byte(cm.data[statusFile])
	}
	h, ok := c.helm[namespace+"/"+name]
	if !ok {
		if files == nil {
			return nil, nil
		}
		return &cluster.Release{Namespace: namespace, Name: name, SwissFiles: files, SwissStatus: status}, nil
	}
	return &cluster.Release{
		Name: name, Namespace: namespace,
		Chart: h.chart, Status: h.status, Revision: h.revision,
		SwissFiles: files, SwissStatus: status,
	}, nil
}

func (c *uninstallCluster) Pods(_ context.Context, namespace, selector string) ([]cluster.Pod, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.podErr != nil {
		return nil, c.podErr
	}
	var out []cluster.Pod
	for _, p := range c.pods {
		if p.namespace != namespace || !selectorMatches(p.labels, selector) {
			continue
		}
		pod := cluster.Pod{Name: p.name, Phase: p.phase, Node: p.node}
		if !p.deletion.IsZero() {
			ts := p.deletion
			pod.DeletionTimestamp = &ts
		}
		pod.DeletionGracePeriodSeconds = p.deletionGrace
		pod.TerminationGracePeriodSeconds = p.terminationGrace
		out = append(out, pod)
	}
	return out, nil
}

func (c *uninstallCluster) PutSecret(_ context.Context, ref string, data, labels map[string]string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.secs == nil {
		c.secs = map[string]*secretRec{}
	}
	c.secs[ref] = &secretRec{data: maps.Clone(data), labels: maps.Clone(labels)}
	return nil
}

func (c *uninstallCluster) DeleteSecret(_ context.Context, ref string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.secs, ref)
	return nil
}

func (c *uninstallCluster) Secret(_ context.Context, ref string) (map[string]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.secs[ref]
	if !ok {
		_, name, err := cluster.SplitRef(ref)
		if err != nil {
			name = ref
		}
		return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, name)
	}
	return maps.Clone(s.data), nil
}

func (c *uninstallCluster) SecretNames(_ context.Context, namespace, selector string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for ref, s := range c.secs {
		ns, name, err := cluster.SplitRef(ref)
		if err != nil || ns != namespace || !selectorMatches(s.labels, selector) {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func (c *uninstallCluster) EnsureNamespace(context.Context, string) error { return nil }

func selectorMatches(labels map[string]string, selector string) bool {
	if selector == "" {
		return true
	}
	for _, term := range strings.Split(selector, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(term), "=")
		if !ok || labels[k] != v {
			return false
		}
	}
	return true
}

var (
	_ cluster.Probe  = (*uninstallCluster)(nil)
	_ cluster.Writer = (*uninstallCluster)(nil)
)

// llmPlanProbe is the fake plus plan ConfigMaps labeled uninstalling. Fake's
// managed set comes from helm releases, which an llmsvc test does not have.
type llmPlanProbe struct {
	*cluster.Fake
}

func (p llmPlanProbe) ManagedRefs(ctx context.Context) ([]cluster.ManagedRef, error) {
	out, err := p.Fake.ManagedRefs(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, ref := range out {
		seen[ref.Namespace+"/"+ref.Name] = true
	}
	for ref, labels := range p.MapLabels {
		if labels[cluster.LabelPhase] != phaseUninstalling {
			continue
		}
		ns, name, err := cluster.SplitRef(ref)
		if err != nil {
			continue
		}
		rel, ok := strings.CutPrefix(name, cluster.PlanConfigMapPrefix)
		if !ok || rel == "" {
			continue
		}
		key := ns + "/" + rel
		if seen[key] {
			continue
		}
		out = append(out, cluster.ManagedRef{Namespace: ns, Name: rel, Labels: maps.Clone(labels)})
		seen[key] = true
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// llmMirrorWriter copies writes onto the probe the server reads. The stock
// fake writer only remembers them for the test.
type llmMirrorWriter struct {
	*fakeWriter
	probe *cluster.Fake
}

func (m *llmMirrorWriter) PutConfigMap(ctx context.Context, ref string, data, labels map[string]string) error {
	if err := m.fakeWriter.PutConfigMap(ctx, ref, data, labels); err != nil {
		return err
	}
	if m.probe.Maps == nil {
		m.probe.Maps = map[string]map[string]string{}
	}
	m.probe.Maps[ref] = cloneData(data)
	if m.probe.MapLabels == nil {
		m.probe.MapLabels = map[string]map[string]string{}
	}
	labs := map[string]string{"app.kubernetes.io/managed-by": "swiss"}
	for k, v := range labels {
		if v != "" {
			labs[k] = v
		}
	}
	m.probe.MapLabels[ref] = labs
	return nil
}

func (m *llmMirrorWriter) DeleteConfigMap(ctx context.Context, ref string) error {
	if err := m.fakeWriter.DeleteConfigMap(ctx, ref); err != nil {
		return err
	}
	delete(m.probe.Maps, ref)
	delete(m.probe.MapLabels, ref)
	return nil
}

func (m *llmMirrorWriter) PutSecret(ctx context.Context, ref string, data, labels map[string]string) error {
	if err := m.fakeWriter.PutSecret(ctx, ref, data, labels); err != nil {
		return err
	}
	if m.probe.Secrets == nil {
		m.probe.Secrets = map[string]map[string]string{}
	}
	if m.probe.SecretLabels == nil {
		m.probe.SecretLabels = map[string]map[string]string{}
	}
	m.probe.Secrets[ref] = cloneData(data)
	m.probe.SecretLabels[ref] = cloneData(labels)
	return nil
}

func (m *llmMirrorWriter) DeleteSecret(ctx context.Context, ref string) error {
	if err := m.fakeWriter.DeleteSecret(ctx, ref); err != nil {
		return err
	}
	delete(m.probe.Secrets, ref)
	delete(m.probe.SecretLabels, ref)
	return nil
}

type llmUninstallHarness struct {
	cs   *uninstallCase
	rig  *llmRig
	base *cluster.Fake
	srv  *httptest.Server
}

func (h *llmUninstallHarness) arm(t *testing.T) {
	t.Helper()
	if h.cs.Finalizer {
		h.rig.dyn.PrependReactor("delete", "llmservices", func(action k8stesting.Action) (bool, runtime.Object, error) {
			del := action.(k8stesting.DeleteAction)
			obj, err := h.rig.dyn.Tracker().Get(llmsvc.GVR, del.GetNamespace(), del.GetName())
			if err != nil {
				return true, nil, err
			}
			u := obj.(*unstructured.Unstructured).DeepCopy()
			if !u.GetDeletionTimestamp().IsZero() {
				return true, nil, nil
			}
			ts := metav1.Now()
			u.SetDeletionTimestamp(&ts)
			if len(u.GetFinalizers()) == 0 {
				u.SetFinalizers([]string{llmsvc.Group + "/cleanup"})
			}
			if err := h.rig.dyn.Tracker().Update(llmsvc.GVR, u, del.GetNamespace()); err != nil {
				return true, nil, err
			}
			return true, nil, nil
		})
	}
	// Prepended last so it runs first. A NotFound delete still removes the
	// object, which is what makes the plan-restore path observable.
	if h.cs.DeleteNotFound {
		h.rig.dyn.PrependReactor("delete", "llmservices", func(action k8stesting.Action) (bool, runtime.Object, error) {
			del := action.(k8stesting.DeleteAction)
			_ = h.rig.dyn.Tracker().Delete(llmsvc.GVR, del.GetNamespace(), del.GetName())
			gr := schema.GroupResource{Group: llmsvc.Group, Resource: llmsvc.Resource}
			return true, nil, apierrors.NewNotFound(gr, del.GetName())
		})
	}
}

func (h *llmUninstallHarness) create(t *testing.T) {
	t.Helper()
	u := llmObject(h.cs.Namespace, h.cs.Release, map[string]string{llmsvc.AnnProfile: "prod"})
	st := &llmsvc.Status{
		Phase: h.cs.OperatorPhase,
		Helm:  &llmsvc.HelmStatus{Revision: 4, Status: "deployed"},
	}
	if len(h.cs.Deletion) > 0 {
		raw, err := json.Marshal(h.cs.Deletion)
		if err != nil {
			t.Fatal(err)
		}
		var del llmsvc.DeletionStatus
		if err := json.Unmarshal(raw, &del); err != nil {
			t.Fatal(err)
		}
		st.Deletion = &del
	}
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(st)
	if err != nil {
		t.Fatal(err)
	}
	u.Object["status"] = m
	if h.cs.Deleting == nil || *h.cs.Deleting {
		ts := metav1.Now()
		u.SetDeletionTimestamp(&ts)
	}
	if _, err := h.rig.c.Create(t.Context(), u); err != nil {
		t.Fatal(err)
	}
}

func (h *llmUninstallHarness) clearDeletion(t *testing.T) {
	t.Helper()
	obj, err := h.rig.dyn.Tracker().Get(llmsvc.GVR, h.cs.Namespace, h.cs.Release)
	if err != nil {
		t.Fatal(err)
	}
	u := obj.(*unstructured.Unstructured).DeepCopy()
	u.SetDeletionTimestamp(nil)
	u.SetFinalizers(nil)
	if err := h.rig.dyn.Tracker().Update(llmsvc.GVR, u, h.cs.Namespace); err != nil {
		t.Fatal(err)
	}
}

func (h *llmUninstallHarness) restart(t *testing.T) {
	t.Helper()
	s := New(h.rig.s.cfg, h.rig.s.probe, discardLogger(), "test")
	s.SetStore(h.rig.s.store)
	s.SetWriter(h.rig.s.writer)
	s.SetLLMServices(h.rig.s.llms)
	h.rig.s = s
	if h.srv != nil {
		h.srv.Close()
	}
	h.srv = httptest.NewServer(s.Handler())
	h.rig.srv = h.srv
	t.Cleanup(h.srv.Close)
}

func seedLLMPlan(t *testing.T, probe *cluster.Fake, cs *uninstallCase) {
	t.Helper()
	phase := cs.PlanPhase
	if phase == "" {
		phase = phaseApplied
	}
	raw, err := yaml.Marshal(planStatus{Phase: phase, Action: "install", Revision: 4, StartedAt: stamp(time.Now())})
	if err != nil {
		t.Fatal(err)
	}
	ref := planRef(cs.Namespace, cs.Release)
	if probe.Maps == nil {
		probe.Maps = map[string]map[string]string{}
	}
	probe.Maps[ref] = map[string]string{
		"helmfile.yaml": "releases: []\n",
		statusFile:      string(raw),
	}
	if phase == phaseUninstalling {
		if probe.MapLabels == nil {
			probe.MapLabels = map[string]map[string]string{}
		}
		probe.MapLabels[ref] = map[string]string{
			"app.kubernetes.io/managed-by": "swiss",
			cluster.LabelPhase:             phaseUninstalling,
		}
	}
}

func seedLLMArchive(probe *cluster.Fake, cs *uninstallCase) {
	ref := archiveRef(cs.Namespace, cs.Release, 1)
	if probe.Secrets == nil {
		probe.Secrets = map[string]map[string]string{}
	}
	if probe.SecretLabels == nil {
		probe.SecretLabels = map[string]map[string]string{}
	}
	probe.Secrets[ref] = map[string]string{"helmfile.yaml": "releases: []\n"}
	probe.SecretLabels[ref] = archiveLabels(cs.Release, 1)
}

func seedLLMAutoscale(probe *cluster.Fake, cs *uninstallCase) {
	ref := autoscaleRef(cs.Namespace, cs.Release)
	if probe.Maps == nil {
		probe.Maps = map[string]map[string]string{}
	}
	probe.Maps[ref] = map[string]string{autoscaleConfigKey: "enabled: true\nminSlices: 1\nmaxSlices: 4\n"}
}
