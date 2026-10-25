package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	clientsetfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/exec"
	"github.com/modelsphere/swiss/internal/llmsvc"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/store"
	"github.com/modelsphere/swiss/internal/values"
)

const llmRelease = "qwen-new"

func llmProbe() cluster.Fake {
	root, err := filepath.Abs(filepath.Join("..", "..", "testdata", "catalog-v2"))
	if err != nil {
		root = filepath.Join("..", "..", "testdata", "catalog-v2")
	}
	p := fakeProbe()
	p.Maps["swiss/site-profile"] = map[string]string{
		"profile.yaml": profileYAML + "chartRepo: oci://ghcr.io/modelsphere/charts\ncatalogs:\n  - name: next\n    url: " + root + "\n    schemaVersion: v2\n",
	}
	p.Secrets = map[string]map[string]string{}
	p.SecretLabels = map[string]map[string]string{}
	return p
}

// v1ChartProbe is the public v1 catalog (schemaVersion unset, which is v1)
// with a chart repo, so a plan composes and helm can run.
func v1ChartProbe() cluster.Fake {
	p := fakeProbe()
	p.Maps["swiss/site-profile"] = map[string]string{
		"profile.yaml": profileYAML + "chartRepo: oci://ghcr.io/modelsphere/charts\n",
	}
	p.Secrets = map[string]map[string]string{}
	p.SecretLabels = map[string]map[string]string{}
	return p
}

func failBin(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	script := "#!/bin/sh\necho " + name + " \"$*\" >&2\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func newDyn(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{llmsvc.GVR: llmsvc.Kind + "List"}, objs...)
}

func emptyDisco() *fake.FakeDiscovery {
	return &fake.FakeDiscovery{Fake: &k8stesting.Fake{}}
}

func servedDisco() *fake.FakeDiscovery {
	d := emptyDisco()
	d.Resources = []*metav1.APIResourceList{{
		GroupVersion: llmsvc.APIVersion,
		APIResources: []metav1.APIResource{
			{Name: llmsvc.Resource, Kind: llmsvc.Kind, Namespaced: true},
		},
	}}
	return d
}

// llmRig is a swissd whose cluster client is fake, plus a tiny operator that
// stamps status when an LLMService's generation moves. The object tracker does
// not bump generation or honour resourceVersion; the reactors below do.
type llmRig struct {
	t        *testing.T
	srv      *httptest.Server
	s        *Server
	probe    cluster.Fake
	w        *fakeWriter
	dyn      *dynamicfake.FakeDynamicClient
	kube     kubernetes.Interface
	c        *llmsvc.Client
	fail     sync.Map
	conflict atomic.Bool
	// skew makes a spec-changing update land at generation+2, so the
	// force-conflicts annotation has to be patched onto the real generation.
	skew atomic.Bool
}

func startRig(t *testing.T, probe cluster.Fake, served, operator bool, applyWith string) *llmRig {
	t.Helper()
	if probe.Maps == nil {
		probe = llmProbe()
	}
	if probe.Secrets == nil {
		probe.Secrets = map[string]map[string]string{}
	}
	if probe.SecretLabels == nil {
		probe.SecretLabels = map[string]map[string]string{}
	}
	profileApplyWith(probe, applyWith)
	cfg := testConfig("prod-b300")
	cfg.Server.AllowDeploy = true
	cfg.Server.HelmBin = failBin(t, "helm")
	cfg.Server.HelmfileBin = failBin(t, "helmfile")
	s := New(cfg, &probe, discardLogger(), "test")
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
	if served {
		disco = servedDisco()
	}
	kube := clientsetfake.NewSimpleClientset()
	c := llmsvc.New(dyn, disco, kube)
	c.Interval = 5 * time.Millisecond
	s.SetLLMServices(c)

	rig := &llmRig{t: t, s: s, probe: probe, w: w, dyn: dyn, kube: kube, c: c}
	rig.react()

	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	rig.srv = srv
	if operator {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go rig.loop(ctx)
	}
	return rig
}

func (r *llmRig) react() {
	r.dyn.PrependReactor("create", "llmservices", func(action k8stesting.Action) (bool, runtime.Object, error) {
		u := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		if u.GetGeneration() == 0 {
			u.SetGeneration(1)
		}
		return false, nil, nil
	})
	r.dyn.PrependReactor("update", "llmservices", func(action k8stesting.Action) (bool, runtime.Object, error) {
		u := action.(k8stesting.UpdateAction).GetObject().(*unstructured.Unstructured)
		oldObj, err := r.dyn.Tracker().Get(llmsvc.GVR, u.GetNamespace(), u.GetName())
		if err != nil {
			return false, nil, nil
		}
		old, ok := oldObj.(*unstructured.Unstructured)
		if !ok {
			return false, nil, nil
		}
		changed := specCanon(old) != specCanon(u)
		gen := old.GetGeneration()
		if changed {
			gen++
			if r.skew.Load() {
				gen++
			}
		}
		if gen == 0 {
			gen = 1
		}
		if changed && u.GetAnnotations()[llmsvc.AnnAction] == "apply" && r.conflict.CompareAndSwap(true, false) {
			gr := schema.GroupResource{Group: llmsvc.Group, Resource: llmsvc.Resource}
			return true, nil, apierrors.NewConflict(gr, u.GetName(), fmt.Errorf("stale"))
		}
		u.SetGeneration(gen)
		return false, nil, nil
	})
}

func specCanon(u *unstructured.Unstructured) string {
	if u == nil || u.Object == nil {
		return ""
	}
	b, err := json.Marshal(u.Object["spec"])
	if err != nil {
		return ""
	}
	return string(b)
}

func (r *llmRig) loop(ctx context.Context) {
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			items, err := r.c.List(ctx, nil)
			if err != nil {
				continue
			}
			for _, item := range items {
				r.reconcile(ctx, item.GetNamespace(), item.GetName())
			}
		}
	}
}

func (r *llmRig) reconcile(ctx context.Context, ns, name string) {
	if ctx.Err() != nil {
		return
	}
	u, err := r.c.Get(ctx, ns, name)
	if err != nil {
		return
	}
	gen := u.GetGeneration()
	if gen == 0 {
		return
	}
	obj, err := llmsvc.FromUnstructured(u)
	if err != nil {
		return
	}
	if obj.Status != nil && obj.Status.ObservedGeneration >= gen && terminal(obj.Status.Phase) {
		return
	}
	var prev int64
	if obj.Status != nil && obj.Status.Helm != nil {
		prev = obj.Status.Helm.Revision
	}
	rev := prev + 1
	phase, msg := llmsvc.PhaseApplied, "applied"
	if v, ok := r.fail.Load(ns + "/" + name); ok {
		phase, msg = llmsvc.PhaseFailed, v.(string)
	}
	crName := fmt.Sprintf("%s-%d", name, rev)
	if err := r.ensureCR(ctx, u, ns, crName); err != nil || ctx.Err() != nil {
		return
	}
	status := map[string]any{
		"observedGeneration": gen,
		"phase":              phase,
		"message":            msg,
		"chart": map[string]any{
			"name":    nestedString(u, "spec", "chart", "name"),
			"version": nestedString(u, "spec", "chart", "version"),
		},
		"helm":       map[string]any{"revision": rev, "status": "deployed"},
		"history":    historyList(obj, u, rev, crName),
		"conditions": []any{condition(phase, msg)},
	}
	payload, err := json.Marshal([]map[string]any{{
		"op": "add", "path": "/status", "value": status,
	}})
	if err != nil {
		return
	}
	_, _ = r.c.Patch(ctx, ns, name, types.JSONPatchType, payload)
}

func terminal(phase string) bool {
	return phase == llmsvc.PhaseApplied || phase == llmsvc.PhaseFailed
}

func condition(phase, msg string) map[string]any {
	return map[string]any{
		"type":               "Applied",
		"status":             "True",
		"reason":             phase,
		"message":            msg,
		"lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
	}
}

func historyList(obj *llmsvc.LLMService, u *unstructured.Unstructured, rev int64, crName string) []any {
	anns := u.GetAnnotations()
	entry := map[string]any{
		"revision":           rev,
		"hash":               anns[llmsvc.AnnPlanHash],
		"controllerRevision": crName,
		"action":             anns[llmsvc.AnnAction],
		"annotations":        anns,
	}
	if anns[llmsvc.AnnForceConflicts] != "" {
		entry["forceConflicts"] = true
	}
	out := []any{entry}
	if obj.Status == nil {
		return out
	}
	for _, h := range obj.Status.History {
		out = append(out, map[string]any{
			"revision":           h.Revision,
			"hash":               h.Hash,
			"controllerRevision": h.ControllerRevision,
			"action":             h.Action,
			"forceConflicts":     h.ForceConflicts,
			"annotations":        h.Annotations,
		})
	}
	return out
}

func (r *llmRig) ensureCR(ctx context.Context, u *unstructured.Unstructured, ns, crName string) error {
	_, err := r.kube.AppsV1().ControllerRevisions(ns).Get(ctx, crName, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	raw, err := json.Marshal(map[string]any{
		"spec":        u.Object["spec"],
		"annotations": u.GetAnnotations(),
	})
	if err != nil {
		return err
	}
	_, err = r.kube.AppsV1().ControllerRevisions(ns).Create(ctx, &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{Name: crName, Namespace: ns},
		Data:       runtime.RawExtension{Raw: raw},
	}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func nestedString(u *unstructured.Unstructured, fields ...string) string {
	s, _, _ := unstructured.NestedString(u.Object, fields...)
	return s
}

// profileApplyWith stamps the site profile's applyWith. Empty and helm leave
// the document alone: that is the default.
func profileApplyWith(p cluster.Fake, applyWith string) {
	if applyWith == "" || applyWith == backendHelm {
		return
	}
	m := p.Maps["swiss/site-profile"]
	if m == nil {
		return
	}
	m["profile.yaml"] += "applyWith: " + applyWith + "\n"
}

// rewriteRevisionSchema changes the catalog-schema annotation on an archived
// ControllerRevision, leaving the spec bytes alone. The plan hash does not
// cover source.schema, so the archive still verifies; ToPlan reads the
// annotation back as the plan's schema. Empty schema deletes the annotation,
// which is how a v1 plan is stored.
func rewriteRevisionSchema(t *testing.T, rig *llmRig, ns, release string, rev int, schema string) {
	t.Helper()
	crName := fmt.Sprintf("%s-%d", release, rev)
	cr, err := rig.kube.AppsV1().ControllerRevisions(ns).Get(t.Context(), crName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Spec        json.RawMessage   `json:"spec"`
		Annotations map[string]string `json:"annotations"`
	}
	if err := json.Unmarshal(cr.Data.Raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Annotations == nil {
		t.Fatal("revision has no annotations")
	}
	if schema == "" {
		delete(doc.Annotations, llmsvc.AnnCatalogSchema)
	} else {
		doc.Annotations[llmsvc.AnnCatalogSchema] = schema
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	cr.Data.Raw = raw
	if _, err := rig.kube.AppsV1().ControllerRevisions(ns).Update(t.Context(), cr, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func (r *llmRig) storePlan(t *testing.T, fields map[string]any) string {
	t.Helper()
	if fields["model"] == nil {
		fields["model"] = "qwen3-8b"
	}
	if fields["variant"] == nil {
		fields["variant"] = "vllm-tp1-910b"
	}
	if fields["chartVersion"] == nil {
		fields["chartVersion"] = "0.7.6"
	}
	code, out := post(t, r.srv, "/api/plans", fields)
	if code != 200 {
		t.Fatalf("plan %d %v", code, out)
	}
	h, _ := out["hash"].(string)
	if h == "" {
		t.Fatalf("plan has no hash: %v", out)
	}
	return h
}

func (r *llmRig) mustGet(t *testing.T, ns, name string) (*unstructured.Unstructured, *llmsvc.LLMService) {
	t.Helper()
	u, err := r.c.Get(t.Context(), ns, name)
	if err != nil {
		t.Fatal(err)
	}
	obj, err := llmsvc.FromUnstructured(u)
	if err != nil {
		t.Fatal(err)
	}
	return u, obj
}

func (r *llmRig) runs(t *testing.T) []store.Run {
	t.Helper()
	runs, err := r.s.store.Runs(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	return runs
}

func (r *llmRig) count(t *testing.T) int {
	t.Helper()
	items, err := r.c.List(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return len(items)
}

func asNum(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return -1
	}
}

func errText(body map[string]any) string {
	s, _ := body["error"].(string)
	return s
}

func llmObject(ns, name string, anns map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": llmsvc.APIVersion,
		"kind":       llmsvc.Kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": ns,
		},
		"spec": map[string]any{
			"chart": map[string]any{
				"name": "sglang", "repo": "oci://ghcr.io/modelsphere/charts", "version": "0.7.1",
			},
			"model": map[string]any{
				"name": "qwen3.6-35b-a3b", "version": "1.1.0", "variant": "sglang-tp2-h100", "engine": "sglang",
			},
		},
	}}
	if len(anns) > 0 {
		u.SetAnnotations(anns)
	}
	return u
}

func TestLLMInstall(t *testing.T) {
	rig := startRig(t, llmProbe(), true, true, backendLLMSVC)

	code, body := get(t, rig.srv, "/api/cluster")
	if code != 200 {
		t.Fatalf("cluster %d %v", code, body)
	}
	info := body["llmservice"].(map[string]any)
	if info["served"] != true || info["applyWith"] != "llmsvc" || info["newInstalls"] != "llmsvc" {
		t.Fatalf("llmservice %v", info)
	}
	for _, w := range warningsOf(body) {
		if strings.Contains(w, "CRD is not served") {
			t.Fatalf("served CRD must not warn: %v", body["warnings"])
		}
	}

	hash := rig.storePlan(t, map[string]any{"release": llmRelease})
	code, out := post(t, rig.srv, "/api/install", map[string]any{"planHash": hash, "note": "first"})
	if code != 200 {
		t.Fatalf("install %d %v", code, out)
	}
	if asNum(out["revision"]) != 1 || out["status"] != "applied" || out["planHash"] != hash {
		t.Fatalf("response %v", out)
	}
	if output, _ := out["output"].(string); !strings.Contains(output, "phase Applied") || !strings.Contains(output, "helm revision 1") {
		t.Fatalf("output %q", output)
	}
	if len(rig.w.written) != 0 {
		t.Fatalf("llmsvc install must not write a plan ConfigMap: %v", rig.w.written)
	}

	u, obj := rig.mustGet(t, "models", llmRelease)
	if obj.Spec.Model == nil || obj.Spec.Model.Name != "qwen3-8b" || obj.Spec.Model.Variant != "vllm-tp1-910b" {
		t.Fatalf("model %+v", obj.Spec.Model)
	}
	if obj.Spec.Chart.Repo != "oci://ghcr.io/modelsphere/charts" || obj.Spec.Chart.Version != "0.7.6" || obj.Spec.Chart.Name != "vllm" {
		t.Fatalf("chart %+v", obj.Spec.Chart)
	}
	anns := u.GetAnnotations()
	if anns[llmsvc.AnnAction] != "install" || anns[llmsvc.AnnNote] != "first" || anns[llmsvc.AnnPlanHash] != hash || anns[llmsvc.AnnCatalogSchema] != "v2" {
		t.Fatalf("annotations %v", anns)
	}
	if obj.Status == nil || obj.Status.Phase != llmsvc.PhaseApplied || obj.Status.Helm == nil || obj.Status.Helm.Revision != 1 {
		t.Fatalf("status %+v", obj.Status)
	}

	runs := rig.runs(t)
	if len(runs) != 1 || runs[0].Action != "install" || runs[0].Revision != 1 || runs[0].Error != "" || runs[0].Note != "first" {
		t.Fatalf("audit %+v", runs)
	}

	code, planBody := get(t, rig.srv, "/api/releases/models/"+llmRelease+"/plan")
	if code != 200 || planBody["hash"] != hash {
		t.Fatalf("current plan %d %v", code, planBody["hash"])
	}
	code, st := get(t, rig.srv, "/api/releases/models/"+llmRelease+"/status")
	if code != 200 {
		t.Fatalf("status %d %v", code, st)
	}
	if st["backend"] != "llmsvc" || asNum(st["revision"]) != 1 || st["helmStatus"] != "deployed" {
		t.Fatalf("release status %v", st)
	}
	if ps, _ := st["planStatus"].(map[string]any); ps["phase"] != "applied" || asNum(ps["revision"]) != 1 || ps["action"] != "install" || ps["note"] != "first" {
		t.Fatalf("planStatus %v", st["planStatus"])
	}
	svc := st["llmservice"].(map[string]any)
	if svc["phase"] != "Applied" || asNum(svc["revision"]) != 1 {
		t.Fatalf("llmservice view %v", svc)
	}
}

func TestLLMInstallRefusesExistingHelmRelease(t *testing.T) {
	probe := llmProbe()
	probe.Rel = []cluster.Release{{
		Name: llmRelease, Namespace: "models", Status: "deployed", Revision: 2, Chart: "sglang-0.7.1",
	}}
	rig := startRig(t, probe, true, true, backendLLMSVC)
	hash := rig.storePlan(t, map[string]any{"release": llmRelease})
	code, out := post(t, rig.srv, "/api/install", map[string]any{"planHash": hash})
	if code != http.StatusConflict {
		t.Fatalf("status %d %v", code, out)
	}
	want := "release models/" + llmRelease + " already exists at revision 2: use `swiss apply` to upgrade it"
	if errText(out) != want {
		t.Fatalf("got %q", errText(out))
	}
	if rig.count(t) != 0 {
		t.Fatal("refused install must not create an LLMService")
	}
	if len(rig.runs(t)) != 0 {
		t.Fatal("a refused install is not an audit row")
	}
}

func TestLLMInstallRefusesExistingService(t *testing.T) {
	rig := startRig(t, llmProbe(), true, false, backendLLMSVC)
	u := llmObject("models", llmRelease, map[string]string{llmsvc.AnnAction: "install"})
	u.Object["status"] = map[string]any{
		"phase": "Applied",
		"helm":  map[string]any{"revision": int64(5), "status": "deployed"},
	}
	if _, err := rig.c.Create(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	hash := rig.storePlan(t, map[string]any{"release": llmRelease})
	code, out := post(t, rig.srv, "/api/install", map[string]any{"planHash": hash})
	if code != http.StatusConflict {
		t.Fatalf("status %d %v", code, out)
	}
	want := "release models/" + llmRelease + " already exists at revision 5: use `swiss apply` to upgrade it"
	if errText(out) != want {
		t.Fatalf("got %q", errText(out))
	}
	if len(rig.runs(t)) != 0 {
		t.Fatal("a refused install is not an audit row")
	}
}

func TestLLMApplyWithFallsBackWhenCRDUnserved(t *testing.T) {
	rig := startRig(t, llmProbe(), false, false, backendLLMSVC)
	code, body := get(t, rig.srv, "/api/cluster")
	if code != 200 {
		t.Fatalf("cluster %d %v", code, body)
	}
	info := body["llmservice"].(map[string]any)
	if info["served"] != false || info["applyWith"] != "llmsvc" || info["newInstalls"] != "helm" {
		t.Fatalf("llmservice %v", info)
	}
	found := false
	for _, w := range warningsOf(body) {
		if w == "site profile applyWith is llmsvc but the LLMService CRD is not served; new installs use helm" {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings %v", body["warnings"])
	}

	hash := rig.storePlan(t, map[string]any{"release": llmRelease})
	code, out := post(t, rig.srv, "/api/install", map[string]any{"planHash": hash})
	if code != http.StatusInternalServerError {
		t.Fatalf("helm fallback still runs helmfile, which this test stubs to fail: %d %v", code, out)
	}
	if rig.count(t) != 0 {
		t.Fatal("fallback must not create an LLMService")
	}
	files, ok := rig.w.written[planRef("models", llmRelease)]
	if !ok || !strings.Contains(files["status.yaml"], "phase: failed") {
		t.Fatalf("helm write-ahead missing: %v", rig.w.written)
	}
}

func TestApplyWithHelmStaysOnHelmWhenCRDServed(t *testing.T) {
	rig := startRig(t, llmProbe(), true, false, backendHelm)
	code, body := get(t, rig.srv, "/api/cluster")
	if code != 200 {
		t.Fatal(code)
	}
	info := body["llmservice"].(map[string]any)
	if info["served"] != true || info["newInstalls"] != "helm" {
		t.Fatalf("llmservice %v", info)
	}
	hash := rig.storePlan(t, map[string]any{"release": llmRelease})
	code, out := post(t, rig.srv, "/api/install", map[string]any{"planHash": hash})
	if code != http.StatusInternalServerError {
		t.Fatalf("status %d %v", code, out)
	}
	if strings.Contains(errText(out), "catalog schema") {
		t.Fatalf("helm accepts schema v2: %v", out)
	}
	if _, ok := rig.w.written[planRef("models", llmRelease)]; !ok {
		t.Fatal("applyWith helm writes the plan ConfigMap")
	}
	if rig.count(t) != 0 {
		t.Fatal("applyWith helm must not create an LLMService")
	}
}

func TestLLMChartPathRefused(t *testing.T) {
	probe := llmProbe()
	probe.Maps["swiss/site-profile"]["profile.yaml"] += "chartPath: /tmp/charts-not-here\n"
	rig := startRig(t, probe, true, false, backendLLMSVC)
	hash := rig.storePlan(t, map[string]any{"release": llmRelease})
	code, out := post(t, rig.srv, "/api/install", map[string]any{"planHash": hash})
	if code != http.StatusBadRequest {
		t.Fatalf("status %d %v", code, out)
	}
	if errText(out) != "a chartPath plan cannot use llmsvc: the operator has no path" {
		t.Fatalf("got %q", errText(out))
	}
	if rig.count(t) != 0 || len(rig.w.written) != 0 {
		t.Fatalf("refused plan must not write: objects %d cms %v", rig.count(t), rig.w.written)
	}
}

func TestLLMUpgrade(t *testing.T) {
	rig := startRig(t, llmProbe(), true, true, backendLLMSVC)
	hash := rig.storePlan(t, map[string]any{"release": llmRelease})
	if code, out := post(t, rig.srv, "/api/install", map[string]any{"planHash": hash}); code != 200 {
		t.Fatalf("install %d %v", code, out)
	}
	next := rig.storePlan(t, map[string]any{
		"release": llmRelease, "overrides": map[string]any{"replicaCount": 2},
	})
	code, out := post(t, rig.srv, "/api/apply", map[string]any{"planHash": next, "expectRevision": 1})
	if code != 200 {
		t.Fatalf("apply %d %v", code, out)
	}
	if asNum(out["revision"]) != 2 || out["status"] != "applied" {
		t.Fatalf("response %v", out)
	}
	_, obj := rig.mustGet(t, "models", llmRelease)
	raw, _ := json.Marshal(obj.Spec)
	if !strings.Contains(string(raw), "replicaCount") {
		t.Fatalf("spec was not updated: %s", raw)
	}
	if obj.Annotations[llmsvc.AnnAction] != "apply" || obj.Annotations[llmsvc.AnnPlanHash] != next {
		t.Fatalf("annotations %v", obj.Annotations)
	}
	runs := rig.runs(t)
	if len(runs) < 1 || runs[0].Action != "apply" || runs[0].Revision != 2 || runs[0].Error != "" {
		t.Fatalf("audit %+v", runs)
	}
}

func TestLLMUpgradeOfMissingService(t *testing.T) {
	rig := startRig(t, llmProbe(), true, false, backendLLMSVC)
	hash := rig.storePlan(t, map[string]any{"release": llmRelease})
	p, err := rig.s.store.Plan(t.Context(), hash)
	if err != nil {
		t.Fatal(err)
	}
	_, err = (llmBackend{rig.s}).Apply(t.Context(), p, exec.Upgrade, applyOpts{Action: "apply"})
	var hf *httpFailure
	if !errors.As(err, &hf) || hf.code != http.StatusConflict {
		t.Fatalf("%v", err)
	}
	want := "no release models/" + llmRelease + ": use `swiss install` to create it"
	if hf.Error() != want {
		t.Fatalf("got %q", hf.Error())
	}
}

func TestLLMExpectRevisionConflict(t *testing.T) {
	rig := startRig(t, llmProbe(), true, true, backendLLMSVC)
	hash := rig.storePlan(t, map[string]any{"release": llmRelease})
	if code, out := post(t, rig.srv, "/api/install", map[string]any{"planHash": hash}); code != 200 {
		t.Fatalf("install %d %v", code, out)
	}
	next := rig.storePlan(t, map[string]any{
		"release": llmRelease, "overrides": map[string]any{"replicaCount": 2},
	})
	code, out := post(t, rig.srv, "/api/apply", map[string]any{"planHash": next, "expectRevision": 9})
	if code != http.StatusConflict {
		t.Fatalf("status %d %v", code, out)
	}
	want := "release moved from revision 9 to 1 since you looked; re-check it before applying"
	if errText(out) != want {
		t.Fatalf("got %q", errText(out))
	}
	if runs := rig.runs(t); len(runs) != 1 || runs[0].Action != "install" {
		t.Fatalf("mismatch is not audited: %+v", runs)
	}
}

func TestLLMApplyingIsRefused(t *testing.T) {
	rig := startRig(t, llmProbe(), true, false, backendLLMSVC)
	u := llmObject("models", llmRelease, map[string]string{llmsvc.AnnProfile: "prod"})
	u.SetGeneration(2)
	u.Object["status"] = map[string]any{
		"observedGeneration": int64(2),
		"phase":              llmsvc.PhaseApplying,
		"helm":               map[string]any{"revision": int64(1), "status": "pending-upgrade"},
	}
	if _, err := rig.c.Create(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	hash := rig.storePlan(t, map[string]any{"release": llmRelease})
	code, out := post(t, rig.srv, "/api/apply", map[string]any{"planHash": hash})
	if code != http.StatusConflict {
		t.Fatalf("status %d %v", code, out)
	}
	want := "release models/" + llmRelease + " is Applying: wait for it, or `helm rollback` first"
	if errText(out) != want {
		t.Fatalf("got %q", errText(out))
	}
}

func TestLLMResourceVersionConflict(t *testing.T) {
	rig := startRig(t, llmProbe(), true, true, backendLLMSVC)
	hash := rig.storePlan(t, map[string]any{"release": llmRelease})
	if code, out := post(t, rig.srv, "/api/install", map[string]any{"planHash": hash}); code != 200 {
		t.Fatalf("install %d %v", code, out)
	}
	rig.conflict.Store(true)
	next := rig.storePlan(t, map[string]any{
		"release": llmRelease, "overrides": map[string]any{"replicaCount": 3},
	})
	code, out := post(t, rig.srv, "/api/apply", map[string]any{"planHash": next, "expectRevision": 1})
	if code != http.StatusConflict {
		t.Fatalf("status %d %v", code, out)
	}
	if errText(out) != "the release moved under you; diff again" {
		t.Fatalf("got %q", errText(out))
	}
	if runs := rig.runs(t); len(runs) != 1 || runs[0].Action != "install" {
		t.Fatalf("conflict is not audited: %+v", runs)
	}
}

func TestLLMFailedPhase(t *testing.T) {
	rig := startRig(t, llmProbe(), true, true, backendLLMSVC)
	hash := rig.storePlan(t, map[string]any{"release": llmRelease})
	if code, out := post(t, rig.srv, "/api/install", map[string]any{"planHash": hash}); code != 200 {
		t.Fatalf("install %d %v", code, out)
	}
	rig.fail.Store("models/"+llmRelease, "chart pull denied")
	next := rig.storePlan(t, map[string]any{
		"release": llmRelease, "overrides": map[string]any{"replicaCount": 2},
	})
	code, out := post(t, rig.srv, "/api/apply", map[string]any{"planHash": next, "expectRevision": 1})
	if code != http.StatusInternalServerError {
		t.Fatalf("status %d %v", code, out)
	}
	if errText(out) != "chart pull denied" {
		t.Fatalf("got %q", errText(out))
	}
	runs := rig.runs(t)
	if len(runs) < 1 || runs[0].Action != "apply" || runs[0].Revision != 0 || runs[0].Error != "chart pull denied" {
		t.Fatalf("audit %+v", runs)
	}
}

func TestLLMForceConflicts(t *testing.T) {
	rig := startRig(t, llmProbe(), true, true, backendLLMSVC)
	hash := rig.storePlan(t, map[string]any{"release": llmRelease})
	code, out := post(t, rig.srv, "/api/install", map[string]any{"planHash": hash, "forceConflicts": true})
	if code != 200 {
		t.Fatalf("install %d %v", code, out)
	}
	u, _ := rig.mustGet(t, "models", llmRelease)
	if got, want := u.GetAnnotations()[llmsvc.AnnForceConflicts], strconv.FormatInt(u.GetGeneration(), 10); got != want {
		t.Fatalf("create annotation %q generation %s", got, want)
	}
	if u.GetGeneration() != 1 {
		t.Fatalf("create generation %d", u.GetGeneration())
	}

	rig.skew.Store(true)
	next := rig.storePlan(t, map[string]any{
		"release": llmRelease, "overrides": map[string]any{"replicaCount": 2},
	})
	code, out = post(t, rig.srv, "/api/apply", map[string]any{
		"planHash": next, "expectRevision": 1, "forceConflicts": true,
	})
	if code != 200 {
		t.Fatalf("apply %d %v", code, out)
	}
	u, _ = rig.mustGet(t, "models", llmRelease)
	if u.GetGeneration() != 3 {
		t.Fatalf("generation %d, want the skewed write", u.GetGeneration())
	}
	if got := u.GetAnnotations()[llmsvc.AnnForceConflicts]; got != "3" {
		t.Fatalf("annotation %q, want the produced generation", got)
	}
}

func TestLLMRollback(t *testing.T) {
	rig := startRig(t, llmProbe(), true, true, backendLLMSVC)
	hash := rig.storePlan(t, map[string]any{"release": llmRelease})
	if code, out := post(t, rig.srv, "/api/install", map[string]any{"planHash": hash}); code != 200 {
		t.Fatalf("install %d %v", code, out)
	}
	next := rig.storePlan(t, map[string]any{
		"release": llmRelease, "overrides": map[string]any{"replicaCount": 7},
	})
	if code, out := post(t, rig.srv, "/api/apply", map[string]any{"planHash": next, "expectRevision": 1}); code != 200 {
		t.Fatalf("apply %d %v", code, out)
	}
	_, mid := rig.mustGet(t, "models", llmRelease)
	raw, _ := json.Marshal(mid.Spec)
	if !strings.Contains(string(raw), "replicaCount") {
		t.Fatalf("upgrade did not change the spec: %s", raw)
	}

	code, out := post(t, rig.srv, "/api/releases/models/"+llmRelease+"/rollback", map[string]any{
		"toRevision": 1, "expectRevision": 2, "note": "back",
	})
	if code != 200 {
		t.Fatalf("rollback %d %v", code, out)
	}
	if asNum(out["revision"]) != 3 || out["status"] != "applied" {
		t.Fatalf("response %v", out)
	}
	_, obj := rig.mustGet(t, "models", llmRelease)
	if obj.Annotations[llmsvc.AnnAction] != "rollback" || obj.Annotations[llmsvc.AnnNote] != "back" || obj.Annotations[llmsvc.AnnPlanHash] != hash {
		t.Fatalf("annotations %v", obj.Annotations)
	}
	raw, _ = json.Marshal(obj.Spec)
	if strings.Contains(string(raw), "replicaCount") {
		t.Fatalf("rollback did not restore the archived spec: %s", raw)
	}
	runs := rig.runs(t)
	if len(runs) < 1 || runs[0].Action != "rollback" || runs[0].Revision != 3 || runs[0].Note != "back" {
		t.Fatalf("audit %+v", runs)
	}
}

func TestLLMRevisions(t *testing.T) {
	rig := startRig(t, llmProbe(), true, true, backendLLMSVC)
	hash := rig.storePlan(t, map[string]any{"release": llmRelease})
	if code, out := post(t, rig.srv, "/api/install", map[string]any{"planHash": hash}); code != 200 {
		t.Fatalf("install %d %v", code, out)
	}
	legacy, err := rig.s.compose(t.Context(), planRequest{
		Model: "qwen3-8b", Variant: "vllm-tp1-910b", ChartVersion: "0.7.6", Release: llmRelease,
		Overrides: values.Tree{"replicaCount": 9},
	})
	if err != nil {
		t.Fatal(err)
	}
	files, err := legacy.Files("")
	if err != nil {
		t.Fatal(err)
	}
	ref := archiveRef("models", llmRelease, 9)
	rig.probe.Secrets[ref] = files
	rig.probe.SecretLabels[ref] = archiveLabels(llmRelease, 9)

	code, out := get(t, rig.srv, "/api/releases/models/"+llmRelease+"/revisions")
	if code != 200 {
		t.Fatalf("revisions %d %v", code, out)
	}
	revs, _ := out["revisions"].([]any)
	if len(revs) != 2 {
		t.Fatalf("want history then the legacy archive, got %v", out["revisions"])
	}
	cur := revs[0].(map[string]any)
	old := revs[1].(map[string]any)
	if asNum(cur["revision"]) != 1 || cur["current"] != true || cur["model"] != "qwen3-8b" || cur["planHash"] != hash {
		t.Fatalf("history row %v", cur)
	}
	if cur["chart"] != "vllm-0.7.6" || cur["variant"] != "vllm-tp1-910b" || cur["version"] != "1.0.0" {
		t.Fatalf("controllerrevision did not fill the row: %v", cur)
	}
	if asNum(old["revision"]) != 9 || old["current"] == true || old["planHash"] != legacy.Hash {
		t.Fatalf("legacy row %v", old)
	}

	code, planBody := get(t, rig.srv, "/api/releases/models/"+llmRelease+"/revisions/1/plan")
	if code != 200 || planBody["hash"] != hash {
		t.Fatalf("archived history plan %d %v", code, planBody["hash"])
	}
	code, planBody = get(t, rig.srv, "/api/releases/models/"+llmRelease+"/revisions/9/plan")
	if code != 200 || planBody["hash"] != legacy.Hash {
		t.Fatalf("legacy archive plan %d %v", code, planBody["hash"])
	}
}

func TestLLMDeploymentsUnion(t *testing.T) {
	probe := llmProbe()
	planDoc := map[string]string{"plan.yaml": "source:\n  model: qwen3.6-35b-a3b\n"}
	probe.Rel = []cluster.Release{
		{Name: "helm-only", Namespace: "a", Chart: "sglang-0.7.1", Status: "deployed", Revision: 2, SwissFiles: planDoc},
		{Name: "both", Namespace: "models", Chart: "sglang-0.7.1", Status: "deployed", Revision: 3, SwissFiles: planDoc},
	}
	probe.Maps[planRef("models", "both")] = planDoc
	rig := startRig(t, probe, true, false, backendLLMSVC)

	both := llmObject("models", "both", map[string]string{llmsvc.AnnProfile: "prod"})
	both.Object["status"] = map[string]any{
		"phase": "Applied",
		"helm":  map[string]any{"revision": int64(8), "status": "deployed"},
		"chart": map[string]any{"name": "sglang", "version": "0.7.1"},
	}
	only := llmObject("models", "llm-only", map[string]string{llmsvc.AnnProfile: "prod"})
	only.Object["status"] = map[string]any{
		"phase": "Applied",
		"helm":  map[string]any{"revision": int64(4), "status": "deployed"},
	}
	ext := llmObject("models", "ext", nil)
	ext.Object["spec"].(map[string]any)["model"] = map[string]any{"name": "outside", "engine": "sglang"}
	ext.Object["status"] = map[string]any{
		"phase": "Applied",
		"helm":  map[string]any{"revision": int64(1), "status": "deployed"},
	}
	for _, u := range []*unstructured.Unstructured{both, only, ext} {
		if _, err := rig.c.Create(t.Context(), u); err != nil {
			t.Fatal(err)
		}
	}

	code, body := get(t, rig.srv, "/api/deployments")
	if code != 200 {
		t.Fatalf("status %d %v", code, body)
	}
	rows, _ := body["deployments"].([]any)
	if len(rows) != 4 {
		t.Fatalf("rows %v", body["deployments"])
	}
	want := []string{"a/helm-only", "models/both", "models/ext", "models/llm-only"}
	for i, row := range rows {
		m := row.(map[string]any)
		if m["namespace"].(string)+"/"+m["release"].(string) != want[i] {
			t.Fatalf("order[%d] = %s/%s, want %s", i, m["namespace"], m["release"], want[i])
		}
	}
	helm := rows[0].(map[string]any)
	if helm["backend"] != "helm" {
		t.Fatalf("helm row %v", helm)
	}
	if _, ok := helm["external"]; ok {
		t.Fatalf("external omitted when false: %v", helm)
	}
	if _, ok := helm["cleanupPending"]; ok {
		t.Fatalf("cleanupPending omitted when false: %v", helm)
	}
	bothRow := rows[1].(map[string]any)
	if bothRow["backend"] != "llmsvc" || bothRow["cleanupPending"] != true || asNum(bothRow["revision"]) != 8 {
		t.Fatalf("both %v", bothRow)
	}
	if drift, _ := bothRow["drift"].(string); !strings.Contains(drift, "clean-up is unfinished") {
		t.Fatalf("drift %q", drift)
	}
	extRow := rows[2].(map[string]any)
	if extRow["backend"] != "llmsvc" || extRow["external"] != true || extRow["model"] != "outside" {
		t.Fatalf("external %v", extRow)
	}
	onlyRow := rows[3].(map[string]any)
	if onlyRow["backend"] != "llmsvc" || asNum(onlyRow["revision"]) != 4 || onlyRow["phase"] != "Applied" {
		t.Fatalf("llmsvc row %v", onlyRow)
	}
	if _, ok := onlyRow["external"]; ok {
		t.Fatalf("a swiss LLMService is not external: %v", onlyRow)
	}
}

func TestLLMExternalUpgradeRefused(t *testing.T) {
	rig := startRig(t, llmProbe(), true, false, backendLLMSVC)
	u := llmObject("models", "ext", nil)
	u.Object["status"] = map[string]any{
		"phase": "Applied",
		"helm":  map[string]any{"revision": int64(1), "status": "deployed"},
	}
	if _, err := rig.c.Create(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	hash := rig.storePlan(t, map[string]any{"release": "ext"})
	code, out := post(t, rig.srv, "/api/apply", map[string]any{"planHash": hash, "expectRevision": 1})
	if code != http.StatusConflict {
		t.Fatalf("status %d %v", code, out)
	}
	want := "release models/ext is an external LLMService: swiss did not deploy it, so it cannot be upgraded"
	if errText(out) != want {
		t.Fatalf("got %q", errText(out))
	}

	code, st := get(t, rig.srv, "/api/releases/models/ext/status")
	if code != 200 || st["backend"] != "llmsvc" || st["exists"] != true {
		t.Fatalf("status still answers: %d %v", code, st)
	}
	for _, path := range []string{"/api/releases/models/ext/probe", "/api/releases/models/ext/chat"} {
		code, out = post(t, rig.srv, path, map[string]any{})
		if code != http.StatusPreconditionFailed {
			t.Fatalf("%s %d %v", path, code, out)
		}
		if !strings.Contains(errText(out), "nginxService") {
			t.Fatalf("%s should have reached the entrypoint check: %q", path, errText(out))
		}
	}
}

func TestLLMStatusReadsTheService(t *testing.T) {
	probe := llmProbe()
	probe.Maps[planRef("models", llmRelease)] = map[string]string{
		"plan.yaml": "source:\n  model: qwen3.6-35b-a3b\n", statusFile: "phase: applied\nnote: helm's leftover\n",
	}
	rig := startRig(t, probe, true, false, backendLLMSVC)
	u := llmObject("models", llmRelease, map[string]string{llmsvc.AnnProfile: "prod", llmsvc.AnnAction: "install", llmsvc.AnnNote: "first"})
	u.Object["status"] = map[string]any{
		"observedGeneration": int64(1),
		"phase":              "Failed",
		"message":            "boom",
		"helm":               map[string]any{"revision": int64(6), "status": "failed"},
		"conditions": []any{map[string]any{
			"type": "Applied", "status": "False", "reason": "ChartPull", "message": "boom",
			"lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
		}},
	}
	if _, err := rig.c.Create(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	code, st := get(t, rig.srv, "/api/releases/models/"+llmRelease+"/status")
	if code != 200 {
		t.Fatalf("status %d %v", code, st)
	}
	if st["backend"] != "llmsvc" || st["cleanupPending"] != true || asNum(st["revision"]) != 6 || st["helmStatus"] != "failed" {
		t.Fatalf("status %v", st)
	}
	// From the LLMService, never the leftover helm plan's status.yaml.
	ps, _ := st["planStatus"].(map[string]any)
	if ps["phase"] != "failed" || ps["error"] != "boom" || asNum(ps["revision"]) != 6 || ps["action"] != "install" || ps["note"] != "first" {
		t.Fatalf("planStatus %v", st["planStatus"])
	}
	if warn, _ := st["warning"].(string); !strings.Contains(warn, "clean-up is unfinished") {
		t.Fatalf("warning %q", warn)
	}
	svc := st["llmservice"].(map[string]any)
	if svc["phase"] != "Failed" || svc["message"] != "boom" || asNum(svc["revision"]) != 6 {
		t.Fatalf("llmservice %v", svc)
	}
	conds, _ := svc["conditions"].([]any)
	if len(conds) != 1 || conds[0].(map[string]any)["reason"] != "ChartPull" {
		t.Fatalf("conditions %v", svc["conditions"])
	}
}

// planStatus is what the web waits on before opening the SLO editor after an
// install, so an llmsvc release has to reach applied there too.
func TestLLMStatusPlanPhase(t *testing.T) {
	rig := startRig(t, llmProbe(), true, false, backendLLMSVC)
	cases := []struct {
		name       string
		generation int64
		status     map[string]any
		want       string
	}{
		{"not-observed", 1, nil, "applying"},
		{"pending", 1, map[string]any{"phase": "Pending"}, "applying"},
		{"applied", 2, map[string]any{"phase": "Applied", "observedGeneration": int64(2), "helm": map[string]any{"revision": int64(3)}}, "applied"},
		{"applied-stale", 3, map[string]any{"phase": "Applied", "observedGeneration": int64(2)}, "applying"},
		{"failed-stale", 3, map[string]any{"phase": "Failed", "observedGeneration": int64(2), "message": "old"}, "applying"},
		{"deleting", 2, map[string]any{"phase": "Deleting", "observedGeneration": int64(2)}, "uninstalling"},
	}
	for _, c := range cases {
		u := llmObject("models", c.name, nil)
		u.SetGeneration(c.generation)
		if c.status != nil {
			u.Object["status"] = c.status
		}
		if _, err := rig.c.Create(t.Context(), u); err != nil {
			t.Fatal(err)
		}
		code, st := get(t, rig.srv, "/api/releases/models/"+c.name+"/status")
		if code != 200 {
			t.Fatalf("%s: status %d %v", c.name, code, st)
		}
		ps, _ := st["planStatus"].(map[string]any)
		if ps["phase"] != c.want || ps["error"] != nil {
			t.Fatalf("%s: planStatus %v, want phase %s", c.name, st["planStatus"], c.want)
		}
		if c.name == "applied" && asNum(ps["revision"]) != 3 {
			t.Fatalf("%s: revision %v", c.name, ps["revision"])
		}
	}
}

func TestMergeReleaseAnnotations(t *testing.T) {
	cur := map[string]string{
		llmsvc.AnnNote:           "old note",
		llmsvc.AnnAction:         "install",
		llmsvc.AnnPlanHash:       "sha256:old",
		llmsvc.AnnForceConflicts: "3",
		llmsvc.AnnDeletionPolicy: "Orphan",
		"kubectl.kubernetes.io/last-applied-configuration": "{}",
		"example.com/owner": "alice",
	}
	fresh := map[string]string{
		llmsvc.AnnAction:   "apply",
		llmsvc.AnnPlanHash: "sha256:new",
		llmsvc.AnnProfile:  "prod",
	}
	got := mergeReleaseAnnotations(cur, fresh, "")
	if _, ok := got[llmsvc.AnnNote]; ok {
		t.Fatalf("stale note kept: %v", got)
	}
	if _, ok := got[llmsvc.AnnForceConflicts]; ok {
		t.Fatalf("force-conflicts kept: %v", got)
	}
	if got[llmsvc.AnnAction] != "apply" || got[llmsvc.AnnPlanHash] != "sha256:new" || got[llmsvc.AnnProfile] != "prod" {
		t.Fatalf("swiss set %v", got)
	}
	if got[llmsvc.AnnDeletionPolicy] != "Orphan" || got["example.com/owner"] != "alice" || got["kubectl.kubernetes.io/last-applied-configuration"] != "{}" {
		t.Fatalf("unowned annotations %v", got)
	}
	forced := mergeReleaseAnnotations(cur, fresh, "5")
	if forced[llmsvc.AnnForceConflicts] != "5" || forced[llmsvc.AnnDeletionPolicy] != "Orphan" {
		t.Fatalf("setForce %v", forced)
	}
	if _, ok := forced[llmsvc.AnnNote]; ok {
		t.Fatalf("stale note kept when force is set: %v", forced)
	}
	if mergeReleaseAnnotations(nil, nil, "") != nil {
		t.Fatal("empty merge should be nil")
	}
}

func TestPredictedGenerationIgnoresAnnotations(t *testing.T) {
	cur := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"chart": map[string]any{"name": "sglang", "version": "0.7.1"}},
	}}
	cur.SetGeneration(4)
	cur.SetAnnotations(map[string]string{
		llmsvc.AnnNote:           "old",
		llmsvc.AnnForceConflicts: "4",
		llmsvc.AnnDeletionPolicy: "Orphan",
	})
	next := cur.DeepCopy()
	next.SetAnnotations(map[string]string{llmsvc.AnnAction: "apply", llmsvc.AnnPlanHash: "sha256:new"})
	if got := predictedGeneration(cur, next); got != 4 {
		t.Fatalf("annotation-only predicted %d", got)
	}
	merged := cur.DeepCopy()
	merged.SetAnnotations(mergeReleaseAnnotations(cur.GetAnnotations(), next.GetAnnotations(), strconv.FormatInt(predictedGeneration(cur, next), 10)))
	if got := predictedGeneration(cur, merged); got != 4 {
		t.Fatalf("merged annotations predicted %d", got)
	}
	next.Object["spec"] = map[string]any{"chart": map[string]any{"name": "vllm", "version": "0.7.1"}}
	if got := predictedGeneration(cur, next); got != 5 {
		t.Fatalf("spec change predicted %d", got)
	}
	if got := predictedGeneration(nil, next); got != 1 {
		t.Fatalf("create predicted %d", got)
	}
}

func TestFromPlanStatusUsesSentinels(t *testing.T) {
	if fromPlanStatus(llmsvc.ErrChartPath) != http.StatusBadRequest {
		t.Fatal("chart path")
	}
	if fromPlanStatus(fmt.Errorf("while mapping: %w", llmsvc.ErrChartRepo)) != http.StatusBadRequest {
		t.Fatal("wrapped chart repo")
	}
	if fromPlanStatus(fmt.Errorf("chartPath mentioned in an internal failure")) != http.StatusInternalServerError {
		t.Fatal("substring must not classify the error")
	}
	if fromPlanStatus(fmt.Errorf("nil plan")) != http.StatusInternalServerError {
		t.Fatal("other errors are 500")
	}
}

func TestNoSuchReleaseIsASentinel(t *testing.T) {
	rig := startRig(t, llmProbe(), true, false, backendLLMSVC)
	_, err := (llmBackend{rig.s}).Current(t.Context(), "models", llmRelease)
	if !errors.Is(err, errNoSuchRelease) || err.Error() != "no such release" {
		t.Fatalf("current: %v", err)
	}
	_, err = (llmBackend{rig.s}).Uninstall(t.Context(), "models", llmRelease)
	var hf *httpFailure
	if !errors.As(err, &hf) || hf.code != http.StatusNotFound {
		t.Fatalf("uninstall: %v", err)
	}
	if runs := rig.runs(t); len(runs) != 0 {
		t.Fatalf("a missing release was audited: %+v", runs)
	}
}

func TestLLMChartRepoRefused(t *testing.T) {
	rig := startRig(t, llmProbe(), true, false, backendLLMSVC)
	_, err := (llmBackend{rig.s}).Apply(t.Context(), &plan.Plan{
		Release: plan.Release{Name: llmRelease, Namespace: "models"},
		Source:  plan.SourceRef{Schema: "v2"},
		Chart:   plan.ChartRef{Name: "sglang"},
	}, exec.Install, applyOpts{Action: "install"})
	var hf *httpFailure
	if !errors.As(err, &hf) || hf.code != http.StatusBadRequest || hf.Error() != llmsvc.ErrChartRepo.Error() {
		t.Fatalf("%v", err)
	}
	if rig.count(t) != 0 {
		t.Fatal("a refused plan created an LLMService")
	}
}

func TestLLMUpgradeKeepsUnownedAnnotations(t *testing.T) {
	rig := startRig(t, llmProbe(), true, true, backendLLMSVC)
	hash := rig.storePlan(t, map[string]any{"release": llmRelease})
	if code, out := post(t, rig.srv, "/api/install", map[string]any{"planHash": hash, "note": "first"}); code != 200 {
		t.Fatalf("install %d %v", code, out)
	}
	u, _ := rig.mustGet(t, "models", llmRelease)
	gen := u.GetGeneration()
	anns := u.GetAnnotations()
	anns[llmsvc.AnnDeletionPolicy] = "Orphan"
	anns["kubectl.kubernetes.io/last-applied-configuration"] = `{"kind":"LLMService"}`
	anns["example.com/owner"] = "alice"
	anns[llmsvc.AnnForceConflicts] = "9"
	u.SetAnnotations(anns)
	if _, err := rig.c.Update(t.Context(), u); err != nil {
		t.Fatal(err)
	}

	code, out := post(t, rig.srv, "/api/apply", map[string]any{
		"planHash": hash, "expectRevision": 1, "note": "second", "forceConflicts": true,
	})
	if code != 200 {
		t.Fatalf("annotation-only apply %d %v", code, out)
	}
	u, _ = rig.mustGet(t, "models", llmRelease)
	if u.GetGeneration() != gen {
		t.Fatalf("annotation-only bumped generation from %d to %d", gen, u.GetGeneration())
	}
	got := u.GetAnnotations()
	if got[llmsvc.AnnNote] != "second" || got[llmsvc.AnnAction] != "apply" {
		t.Fatalf("swiss annotations %v", got)
	}
	if got[llmsvc.AnnForceConflicts] != strconv.FormatInt(gen, 10) {
		t.Fatalf("force-conflicts %q generation %d", got[llmsvc.AnnForceConflicts], gen)
	}
	if got[llmsvc.AnnDeletionPolicy] != "Orphan" || got["example.com/owner"] != "alice" || got["kubectl.kubernetes.io/last-applied-configuration"] != `{"kind":"LLMService"}` {
		t.Fatalf("unowned annotations dropped: %v", got)
	}

	next := rig.storePlan(t, map[string]any{
		"release": llmRelease, "overrides": map[string]any{"replicaCount": 2},
	})
	code, out = post(t, rig.srv, "/api/apply", map[string]any{"planHash": next, "expectRevision": 1})
	if code != 200 {
		t.Fatalf("upgrade %d %v", code, out)
	}
	u, _ = rig.mustGet(t, "models", llmRelease)
	if u.GetGeneration() != gen+1 {
		t.Fatalf("spec change generation %d, want %d", u.GetGeneration(), gen+1)
	}
	got = u.GetAnnotations()
	if _, ok := got[llmsvc.AnnNote]; ok {
		t.Fatalf("stale note kept: %v", got)
	}
	if _, ok := got[llmsvc.AnnForceConflicts]; ok {
		t.Fatalf("force-conflicts kept without the flag: %v", got)
	}
	if got[llmsvc.AnnDeletionPolicy] != "Orphan" || got["example.com/owner"] != "alice" || got["kubectl.kubernetes.io/last-applied-configuration"] != `{"kind":"LLMService"}` {
		t.Fatalf("unowned annotations after upgrade: %v", got)
	}
	if got[llmsvc.AnnAction] != "apply" || got[llmsvc.AnnPlanHash] != next {
		t.Fatalf("fresh swiss set: %v", got)
	}
}

// An install naming its backend gets that one whatever the profile says; one
// naming none still gets the profile's.
func TestInstallNamesItsBackend(t *testing.T) {
	t.Run("llmsvc over a helm profile", func(t *testing.T) {
		rig := startRig(t, llmProbe(), true, true, backendHelm)
		_, body := get(t, rig.srv, "/api/cluster")
		info := body["llmservice"].(map[string]any)
		if info["available"] != true || info["newInstalls"] != "helm" {
			t.Fatalf("llmservice %v", info)
		}
		hash := rig.storePlan(t, map[string]any{"release": llmRelease})
		code, out := post(t, rig.srv, "/api/install", map[string]any{"planHash": hash, "applyWith": "llmsvc"})
		if code != 200 {
			t.Fatalf("install %d %v", code, out)
		}
		if len(rig.w.written) != 0 {
			t.Fatalf("llmsvc install must not write a plan ConfigMap: %v", rig.w.written)
		}
		rig.mustGet(t, "models", llmRelease)
	})
	t.Run("helm over an llmsvc profile", func(t *testing.T) {
		rig := startRig(t, llmProbe(), true, false, backendLLMSVC)
		hash := rig.storePlan(t, map[string]any{"release": llmRelease})
		code, out := post(t, rig.srv, "/api/install", map[string]any{"planHash": hash, "applyWith": "helm"})
		if code != http.StatusInternalServerError {
			t.Fatalf("helm runs helmfile, which this test stubs to fail: %d %v", code, out)
		}
		if _, ok := rig.w.written[planRef("models", llmRelease)]; !ok {
			t.Fatal("applyWith helm writes the plan ConfigMap")
		}
		if rig.count(t) != 0 {
			t.Fatal("applyWith helm must not create an LLMService")
		}
	})
}

func TestInstallWithLLMSVCRefusedWhenUnserved(t *testing.T) {
	rig := startRig(t, llmProbe(), false, false, backendHelm)
	_, body := get(t, rig.srv, "/api/cluster")
	if info := body["llmservice"].(map[string]any); info["available"] != false {
		t.Fatalf("llmservice %v", info)
	}
	hash := rig.storePlan(t, map[string]any{"release": llmRelease})
	code, out := post(t, rig.srv, "/api/install", map[string]any{"planHash": hash, "applyWith": "llmsvc"})
	if code != http.StatusConflict {
		t.Fatalf("status %d %v", code, out)
	}
	if want := "cannot install with llmsvc: the LLMService CRD is not served on this cluster"; errText(out) != want {
		t.Fatalf("got %q", errText(out))
	}
	if rig.count(t) != 0 || len(rig.w.written) != 0 {
		t.Fatalf("a refused install must not write: objects %d cms %v", rig.count(t), rig.w.written)
	}
}

func TestApplyWithUnknownRefused(t *testing.T) {
	rig := startRig(t, llmProbe(), true, false, backendHelm)
	hash := rig.storePlan(t, map[string]any{"release": llmRelease})
	code, out := post(t, rig.srv, "/api/install", map[string]any{"planHash": hash, "applyWith": "operator"})
	if code != http.StatusBadRequest || errText(out) != `applyWith "operator" is not helm or llmsvc` {
		t.Fatalf("%d %v", code, out)
	}
}

// An existing release stays on the backend that owns it: naming the other on
// an upgrade is refused, and naming none or its own upgrades it in place.
func TestExistingInstallKeepsItsBackend(t *testing.T) {
	t.Run("llmsvc", func(t *testing.T) {
		rig := startRig(t, llmProbe(), true, true, backendHelm)
		hash := rig.storePlan(t, map[string]any{"release": llmRelease})
		if code, out := post(t, rig.srv, "/api/install", map[string]any{"planHash": hash, "applyWith": "llmsvc"}); code != 200 {
			t.Fatalf("install %d %v", code, out)
		}
		next := rig.storePlan(t, map[string]any{
			"release": llmRelease, "overrides": map[string]any{"replicaCount": 2},
		})
		code, out := post(t, rig.srv, "/api/apply", map[string]any{"planHash": next, "expectRevision": 1, "applyWith": "helm"})
		if code != http.StatusConflict {
			t.Fatalf("status %d %v", code, out)
		}
		if want := "release models/" + llmRelease + " is installed with llmsvc, not helm: an existing install keeps its method"; errText(out) != want {
			t.Fatalf("got %q", errText(out))
		}
		code, out = post(t, rig.srv, "/api/apply", map[string]any{"planHash": next, "expectRevision": 1})
		if code != 200 || asNum(out["revision"]) != 2 {
			t.Fatalf("apply %d %v", code, out)
		}
		if len(rig.w.written) != 0 {
			t.Fatalf("an llmsvc upgrade must not write a plan ConfigMap: %v", rig.w.written)
		}
	})
	t.Run("helm", func(t *testing.T) {
		probe := llmProbe()
		probe.Rel = []cluster.Release{{
			Name: llmRelease, Namespace: "models", Status: "deployed", Revision: 2, Chart: "vllm-0.7.6",
		}}
		probe.Maps[planRef("models", llmRelease)] = map[string]string{"plan.yaml": "release: {}\n"}
		rig := startRig(t, probe, true, false, backendLLMSVC)
		hash := rig.storePlan(t, map[string]any{"release": llmRelease})
		code, out := post(t, rig.srv, "/api/apply", map[string]any{"planHash": hash, "applyWith": "llmsvc"})
		if code != http.StatusConflict {
			t.Fatalf("status %d %v", code, out)
		}
		if want := "release models/" + llmRelease + " is installed with helm, not llmsvc: an existing install keeps its method"; errText(out) != want {
			t.Fatalf("got %q", errText(out))
		}
		if rig.count(t) != 0 {
			t.Fatal("a helm release must not gain an LLMService")
		}
	})
}
