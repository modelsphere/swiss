package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/llmsvc"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/site"
	"github.com/modelsphere/swiss/internal/testutil/cases"
	"github.com/modelsphere/swiss/internal/values"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
)

// masterLayers is the layer list from before the autoscale layer existed.
var masterLayers = []string{
	values.LayerDefaults, values.LayerCatalog, values.LayerSite,
	values.LayerDerived, values.LayerForm, values.LayerEdit,
}

type noAutoscaleCase struct {
	Name             string `json:"name"`
	Description      string `json:"description,omitempty"`
	Kind             string `json:"kind"`
	Backend          string `json:"backend,omitempty"`
	ServiceID        string `json:"serviceId,omitempty"`
	FullnameOverride string `json:"fullnameOverride,omitempty"`
	WantReplicaCount int    `json:"wantReplicaCount,omitempty"`
	Keep             int    `json:"keep,omitempty"`
	LiveRevision     int    `json:"liveRevision,omitempty"`
	ArchiveRevisions []int  `json:"archiveRevisions,omitempty"`
	WantDeleted      []int  `json:"wantDeleted,omitempty"`
	WantKept         []int  `json:"wantKept,omitempty"`
}

func TestNoAutoscale(t *testing.T) {
	cases.Run(t, "testdata/no-autoscale", func(t *testing.T, tc noAutoscaleCase) {
		switch tc.Kind {
		case "hash":
			runNoAutoscaleHash(t, tc)
		case "upgrade":
			runNoAutoscaleUpgrade(t, tc)
		case "rollback":
			runNoAutoscaleRollback(t, tc)
		case "prune":
			runNoAutoscalePrune(t, tc)
		case "uninstall":
			runNoAutoscaleUninstall(t, tc)
		case "layers":
			runNoAutoscaleLayers(t, tc)
		case "loop":
			runNoAutoscaleLoop(t, tc)
		case "profile":
			runNoAutoscaleProfile(t)
		default:
			t.Fatalf("kind %q", tc.Kind)
		}
	})
}

func runNoAutoscaleHash(t *testing.T, tc noAutoscaleCase) {
	t.Helper()
	probe, req := noAutoscaleRequest(t, tc.Backend)
	srv, s := deployServerWith(t, probe, true)
	code, body := post(t, srv, "/api/plans", noAutoscaleBody(req))
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	p, err := s.compose(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if body["hash"] != p.Hash {
		t.Fatalf("api hash %v, compose %s", body["hash"], p.Hash)
	}
	layers, _ := body["layers"].(map[string]any)
	if _, ok := layers[values.LayerAutoscale]; ok {
		t.Fatalf("api plan includes an autoscale layer: %v", layers[values.LayerAutoscale])
	}
	assertNoAutoscalePlan(t, p)
}

func runNoAutoscaleUpgrade(t *testing.T, tc noAutoscaleCase) {
	t.Helper()
	switch tc.Backend {
	case backendHelm:
		runNoAutoscaleUpgradeHelm(t, tc)
	case backendLLMSVC:
		runNoAutoscaleUpgradeLLM(t, tc)
	default:
		t.Fatalf("backend %q", tc.Backend)
	}
}

func runNoAutoscaleUpgradeHelm(t *testing.T, tc noAutoscaleCase) {
	t.Helper()
	overrides := values.Tree{"replicaCount": tc.WantReplicaCount}
	if tc.FullnameOverride != "" {
		overrides["fullnameOverride"] = tc.FullnameOverride
	}
	p, files := livePlan(t, planRequest{
		Model: "qwen3.6-35b-a3b", Release: tc.ServiceID, Namespace: "models",
		Overrides: overrides,
	})
	if _, ok := p.Layers[values.LayerAutoscale]; ok {
		t.Fatal("seed plan has an autoscale layer")
	}
	if _, ok := values.Get(p.Values(), "serviceId"); ok {
		t.Fatal("seed plan sets serviceId, so the upgrade check is not the release-name fallback")
	}
	probe := liveProbe()
	probe.Rel = append(probe.Rel, cluster.Release{
		Name: tc.ServiceID, Namespace: "models", Status: "deployed", Revision: 2,
		Chart: "sglang-0.7.0", SwissFiles: files,
	})
	probe.Maps[planRef("models", tc.ServiceID)] = files
	srv, _ := deployServerWith(t, probe, true)
	// The chart fullname is not the deployed identity. Accepting it would mean
	// serviceIDOf fell back to fullnameOf.
	code, bad := post(t, srv, "/api/plans", map[string]any{
		"fromRelease": tc.ServiceID,
		"serviceId":   tc.FullnameOverride,
		"overrides":   map[string]any{"replicaCount": tc.WantReplicaCount},
	})
	if code == http.StatusOK || !strings.Contains(errText(bad), "serviceId cannot change") {
		t.Fatalf("fullname as serviceId: status %d %v", code, bad)
	}
	code, up := post(t, srv, "/api/plans", map[string]any{
		"fromRelease": tc.ServiceID,
		"serviceId":   tc.ServiceID,
		"overrides":   map[string]any{"replicaCount": tc.WantReplicaCount},
	})
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, up)
	}
	layers, _ := up["layers"].(map[string]any)
	if _, ok := layers[values.LayerAutoscale]; ok {
		t.Fatalf("autoscale layer %v", layers[values.LayerAutoscale])
	}
	if got := jsonInt(planValues(up)["replicaCount"]); got != tc.WantReplicaCount {
		t.Fatalf("replicaCount %d, want %d", got, tc.WantReplicaCount)
	}
}

func runNoAutoscaleUpgradeLLM(t *testing.T, tc noAutoscaleCase) {
	t.Helper()
	rig := startRig(t, llmProbe(), true, true, backendLLMSVC)
	hash := rig.storePlan(t, map[string]any{
		"release":   llmRelease,
		"overrides": map[string]any{"replicaCount": tc.WantReplicaCount},
	})
	if code, out := post(t, rig.srv, "/api/install", map[string]any{"planHash": hash}); code != http.StatusOK {
		t.Fatalf("install %d %v", code, out)
	}
	code, up := post(t, rig.srv, "/api/plans", map[string]any{"fromRelease": llmRelease})
	if code != http.StatusOK {
		t.Fatalf("plan %d %v", code, up)
	}
	layers, _ := up["layers"].(map[string]any)
	if _, ok := layers[values.LayerAutoscale]; ok {
		t.Fatalf("autoscale layer %v", layers[values.LayerAutoscale])
	}
	if got := jsonInt(planValues(up)["replicaCount"]); got != tc.WantReplicaCount {
		t.Fatalf("replicaCount %d, want %d", got, tc.WantReplicaCount)
	}
}

func runNoAutoscaleRollback(t *testing.T, tc noAutoscaleCase) {
	t.Helper()
	switch tc.Backend {
	case backendHelm:
		runNoAutoscaleRollbackHelm(t)
	case backendLLMSVC:
		runNoAutoscaleRollbackLLM(t)
	default:
		t.Fatalf("backend %q", tc.Backend)
	}
}

func runNoAutoscaleRollbackHelm(t *testing.T) {
	t.Helper()
	probe, baseFiles := seedRelease(t, 6, values.Tree{"replicaCount": 1})
	base, err := plan.FromFiles(baseFiles)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := base.Layers[values.LayerAutoscale]; ok {
		t.Fatal("archive seed has an autoscale layer")
	}
	ref := archiveRef("models", "glm-53", 4)
	probe.Secrets = map[string]map[string]string{ref: baseFiles}
	probe.SecretLabels = map[string]map[string]string{ref: archiveLabels("glm-53", 4)}
	before := cloneData(probe.Secrets[ref])
	srv, s := deployServerWith(t, probe, true)
	s.cfg.Server.HelmBin, s.cfg.Server.HelmfileBin = stubHelm(t, 0), stubHelm(t, 0)
	code, out := post(t, srv, "/api/releases/models/glm-53/rollback", map[string]any{
		"toRevision": 4, "expectRevision": 6,
	})
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, out)
	}
	if !reflect.DeepEqual(probe.Secrets[ref], before) {
		t.Fatal("archived plan bytes changed")
	}
	archived, err := plan.FromFiles(probe.Secrets[ref])
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := out["planHash"].(string)
	if hash != archived.Hash {
		t.Fatalf("applied %s, archived %s", hash, archived.Hash)
	}
	if _, ok := archived.Layers[values.LayerAutoscale]; ok {
		t.Fatal("applied archive gained an autoscale layer")
	}
}

func runNoAutoscaleRollbackLLM(t *testing.T) {
	t.Helper()
	rig := startRig(t, llmProbe(), true, true, backendLLMSVC)
	hash := rig.storePlan(t, map[string]any{"release": llmRelease})
	if code, out := post(t, rig.srv, "/api/install", map[string]any{"planHash": hash}); code != http.StatusOK {
		t.Fatalf("install %d %v", code, out)
	}
	orig, err := rig.s.store.Plan(context.Background(), hash)
	if err != nil {
		t.Fatal(err)
	}
	next := rig.storePlan(t, map[string]any{
		"release": llmRelease, "overrides": map[string]any{"replicaCount": 7},
	})
	if code, out := post(t, rig.srv, "/api/apply", map[string]any{"planHash": next, "expectRevision": 1}); code != http.StatusOK {
		t.Fatalf("apply %d %v", code, out)
	}
	code, out := post(t, rig.srv, "/api/releases/models/"+llmRelease+"/rollback", map[string]any{
		"toRevision": 1, "expectRevision": 2,
	})
	if code != http.StatusOK {
		t.Fatalf("rollback %d %v", code, out)
	}
	_, obj := rig.mustGet(t, "models", llmRelease)
	if obj.Annotations[llmsvc.AnnPlanHash] != hash {
		t.Fatalf("plan hash %q, want %s", obj.Annotations[llmsvc.AnnPlanHash], hash)
	}
	var names []string
	for _, layer := range obj.Spec.Layers {
		names = append(names, layer.Name)
	}
	want := masterLayerNames(orig)
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("spec.layers %v, want %v", names, want)
	}
}

func runNoAutoscalePrune(t *testing.T, tc noAutoscaleCase) {
	t.Helper()
	if tc.Backend != backendHelm {
		t.Fatalf("backend %q", tc.Backend)
	}
	probe, files := seedRelease(t, tc.LiveRevision, values.Tree{"replicaCount": 2})
	if _, ok := files["autoscale.yaml"]; ok {
		t.Fatal("seed workspace has autoscale.yaml")
	}
	probe.Maps["swiss/site-profile"]["profile.yaml"] += "chartRepo: oci://ghcr.io/modelsphere/charts\n"
	if probe.Secrets == nil {
		probe.Secrets = map[string]map[string]string{}
		probe.SecretLabels = map[string]map[string]string{}
	}
	for _, rev := range tc.ArchiveRevisions {
		ref := archiveRef("models", "glm-53", rev)
		probe.Secrets[ref] = cloneData(files)
		probe.SecretLabels[ref] = archiveLabels("glm-53", rev)
	}
	srv, s := deployServerWith(t, &probe, true)
	s.cfg.Server.PlanHistory = tc.Keep
	s.cfg.Server.HelmBin, s.cfg.Server.HelmfileBin = stubHelm(t, 0), stubHelm(t, 0)
	base := s.writer.(*fakeWriter)
	s.SetWriter(&secretMirror{fakeWriter: base, probe: &probe})

	code, up := post(t, srv, "/api/plans", map[string]any{
		"fromRelease": "glm-53",
		"overrides":   map[string]any{"replicaCount": 9},
	})
	if code != http.StatusOK {
		t.Fatalf("plan %d %v", code, up)
	}
	layers, _ := up["layers"].(map[string]any)
	if _, ok := layers[values.LayerAutoscale]; ok {
		t.Fatalf("upgrade plan has an autoscale layer: %v", layers[values.LayerAutoscale])
	}
	code, out := post(t, srv, "/api/apply", map[string]any{
		"planHash": up["hash"], "expectRevision": tc.LiveRevision,
	})
	if code != http.StatusOK {
		t.Fatalf("apply %d %v", code, out)
	}
	var deleted []int
	for _, ref := range base.deleted {
		rev, ok := archiveRevision(ref)
		if !ok {
			t.Fatalf("deleted %s, want plan archives", ref)
		}
		deleted = append(deleted, rev)
	}
	if !reflect.DeepEqual(deleted, tc.WantDeleted) {
		t.Fatalf("deleted revisions %v, want %v", deleted, tc.WantDeleted)
	}
	for _, rev := range tc.WantKept {
		ref := archiveRef("models", "glm-53", rev)
		data := probe.Secrets[ref]
		if len(data) == 0 {
			t.Fatalf("revision %d was not kept", rev)
		}
		if _, ok := data["autoscale.yaml"]; ok {
			t.Fatalf("revision %d has autoscale.yaml", rev)
		}
	}
	for _, rev := range tc.WantDeleted {
		if _, ok := probe.Secrets[archiveRef("models", "glm-53", rev)]; ok {
			t.Fatalf("revision %d still stored", rev)
		}
	}

	code, again := post(t, srv, "/api/plans", map[string]any{
		"fromRelease": "glm-53",
		"overrides":   map[string]any{"replicaCount": 4},
	})
	if code != http.StatusOK {
		t.Fatalf("second plan %d %v", code, again)
	}
	code, out = post(t, srv, "/api/apply", map[string]any{
		"planHash": again["hash"], "expectRevision": tc.LiveRevision,
	})
	if code != http.StatusOK {
		t.Fatalf("sequential apply %d %v", code, out)
	}
}

func runNoAutoscaleUninstall(t *testing.T, tc noAutoscaleCase) {
	t.Helper()
	switch tc.Backend {
	case backendHelm:
		srv, s := deployServerWith(t, liveProbe(), true)
		s.cfg.Server.HelmBin = stubHelm(t, 0)
		w := s.writer.(*fakeWriter)
		code, out := del(t, srv, "/api/releases/models/glm-53")
		if code != http.StatusOK {
			t.Fatalf("status %d: %v", code, out)
		}
		if planErr, _ := out["planError"].(string); planErr != "" {
			t.Fatalf("planError %q", planErr)
		}
		if !reflect.DeepEqual(w.deleted, []string{planRef("models", "glm-53")}) {
			t.Fatalf("deleted %v", w.deleted)
		}
	case backendLLMSVC:
		rig := startRig(t, llmProbe(), true, true, backendLLMSVC)
		hash := rig.storePlan(t, map[string]any{"release": llmRelease})
		if code, out := post(t, rig.srv, "/api/install", map[string]any{"planHash": hash}); code != http.StatusOK {
			t.Fatalf("install %d %v", code, out)
		}
		beforeW, beforeD := len(rig.w.written), len(rig.w.deleted)
		code, out := del(t, rig.srv, "/api/releases/models/"+llmRelease)
		if code != http.StatusOK {
			t.Fatalf("uninstall %d %v", code, out)
		}
		if planErr, _ := out["planError"].(string); planErr != "" {
			t.Fatalf("planError %q", planErr)
		}
		if len(rig.w.written) != beforeW || len(rig.w.deleted) != beforeD {
			t.Fatalf("written %v deleted %v", rig.w.written, rig.w.deleted)
		}
	default:
		t.Fatalf("backend %q", tc.Backend)
	}
}

func runNoAutoscaleLayers(t *testing.T, tc noAutoscaleCase) {
	t.Helper()
	if tc.Backend != backendLLMSVC {
		t.Fatalf("backend %q", tc.Backend)
	}
	_, p := composeNoAutoscale(t, backendLLMSVC)
	u, err := llmsvc.FromPlan(p, "install", "")
	if err != nil {
		t.Fatal(err)
	}
	obj, err := llmsvc.FromUnstructured(u)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, layer := range obj.Spec.Layers {
		names = append(names, layer.Name)
	}
	want := masterLayerNames(p)
	if len(want) == 0 {
		t.Fatal("composed plan has no layers")
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("spec.layers %v, want %v", names, want)
	}
	back, err := llmsvc.ToPlan(u)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := back.Layers[values.LayerAutoscale]; ok {
		t.Fatal("ToPlan kept an autoscale layer")
	}
}

func runNoAutoscaleLoop(t *testing.T, tc noAutoscaleCase) {
	t.Helper()
	switch tc.Backend {
	case backendHelm:
		runNoAutoscaleLoopHelm(t)
	case backendLLMSVC:
		runNoAutoscaleLoopLLM(t)
	default:
		t.Fatalf("backend %q", tc.Backend)
	}
}

func runNoAutoscaleLoopHelm(t *testing.T) {
	t.Helper()
	const ns, release = "models", "plain"
	_, files := livePlan(t, planRequest{
		Model: "qwen3.6-35b-a3b", Release: release, Namespace: ns, ServiceID: release,
	})
	probe := fakeProbe()
	probe.Rel = []cluster.Release{{
		Name: release, Namespace: ns, Status: "deployed", Revision: 2, SwissFiles: files,
	}}
	probe.Maps[planRef(ns, release)] = files
	logPath := filepath.Join(t.TempDir(), "calls.log")
	bin := countBin(t, logPath)
	s, w := newLoopServer(t, &probe, bin, bin)
	s.autoscaleTick(context.Background())
	if n := countLines(t, logPath); n != 0 {
		t.Fatalf("helm calls %d", n)
	}
	if len(w.written) != 0 || len(w.deleted) != 0 {
		t.Fatalf("written %v deleted %v", w.written, w.deleted)
	}
}

func runNoAutoscaleLoopLLM(t *testing.T) {
	t.Helper()
	probe := llmProbe()
	rig := startRig(t, probe, true, false, "")
	p, err := rig.s.compose(context.Background(), planRequest{
		Model: "qwen3-8b", Variant: "vllm-tp1-910b", ChartVersion: "0.7.6",
		Release: llmRelease, Namespace: "models",
	})
	if err != nil {
		t.Fatal(err)
	}
	u, err := llmsvc.FromPlan(p, "install", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rig.c.Create(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	var updates atomic.Int32
	rig.dyn.PrependReactor("update", "llmservices", func(k8stesting.Action) (bool, runtime.Object, error) {
		updates.Add(1)
		return false, nil, nil
	})
	logPath := filepath.Join(t.TempDir(), "calls.log")
	bin := countBin(t, logPath)
	rig.s.cfg.Server.HelmBin = bin
	rig.s.cfg.Server.HelmfileBin = bin
	beforeW, beforeD := len(rig.w.written), len(rig.w.deleted)
	rig.s.autoscaleTick(context.Background())
	if updates.Load() != 0 || countLines(t, logPath) != 0 || len(rig.w.written) != beforeW || len(rig.w.deleted) != beforeD {
		t.Fatalf("updates %d calls %d written %v deleted %v", updates.Load(), countLines(t, logPath), rig.w.written, rig.w.deleted)
	}
}

func runNoAutoscaleProfile(t *testing.T) {
	t.Helper()
	p, err := site.Parse([]byte(profileYAML), "test")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "prod" || p.Namespace != "models" || p.Scaler.AutoscalePeriod != "" {
		t.Fatalf("profile name=%s namespace=%s period=%q", p.Name, p.Namespace, p.Scaler.AutoscalePeriod)
	}
	def, err := site.Parse([]byte(site.DefaultYAML("prod-b300")), "default")
	if err != nil {
		t.Fatal(err)
	}
	if def.Scaler.AutoscalePeriod != "" {
		t.Fatalf("default period %q", def.Scaler.AutoscalePeriod)
	}
	if def.Scaler.ServerAddress != "http://decision-gen.llm-scaler.svc:80" {
		t.Fatalf("serverAddress %q", def.Scaler.ServerAddress)
	}
	if def.Scaler.DecisionServer() != def.Scaler.ServerAddress {
		t.Fatalf("decision server %q", def.Scaler.DecisionServer())
	}
	_, composed := composeNoAutoscale(t, backendHelm)
	if _, ok := values.Get(composed.Values(), "scaler.autoscalePeriod"); ok {
		t.Fatal("autoscalePeriod was copied into chart values")
	}
}

func composeNoAutoscale(t *testing.T, backend string) (*Server, *plan.Plan) {
	t.Helper()
	probe, req := noAutoscaleRequest(t, backend)
	s := New(testConfig("prod-b300"), probe, discardLogger(), "test")
	p, err := s.compose(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return s, p
}

func noAutoscaleRequest(t *testing.T, backend string) (cluster.Fake, planRequest) {
	t.Helper()
	switch backend {
	case backendHelm:
		return fakeProbe(), planRequest{Model: "qwen3.6-35b-a3b", Release: "plain", Namespace: "models"}
	case backendLLMSVC:
		return llmProbe(), planRequest{
			Model: "qwen3-8b", Variant: "vllm-tp1-910b", ChartVersion: "0.7.6",
			Release: "plain", Namespace: "models",
		}
	default:
		t.Fatalf("backend %q", backend)
		return cluster.Fake{}, planRequest{}
	}
}

func noAutoscaleBody(req planRequest) map[string]any {
	body := map[string]any{"model": req.Model, "release": req.Release, "namespace": req.Namespace}
	if req.Variant != "" {
		body["variant"] = req.Variant
	}
	if req.ChartVersion != "" {
		body["chartVersion"] = req.ChartVersion
	}
	return body
}

func assertNoAutoscalePlan(t *testing.T, p *plan.Plan) {
	t.Helper()
	if _, ok := p.Layers[values.LayerAutoscale]; ok {
		t.Fatalf("autoscale layer %#v", p.Layers[values.LayerAutoscale])
	}
	for _, name := range p.ValuesFiles() {
		if name == "autoscale.yaml" {
			t.Fatalf("values files %v", p.ValuesFiles())
		}
	}
	if strings.Contains(p.Helmfile, "autoscale.yaml") {
		t.Fatalf("helmfile mentions autoscale.yaml:\n%s", p.Helmfile)
	}
	files, err := p.Files("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["autoscale.yaml"]; ok {
		t.Fatal("plan files include autoscale.yaml")
	}
	got, err := hashWithoutAutoscale(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != p.Hash {
		t.Fatalf("hash %s, master algorithm %s", p.Hash, got)
	}
}

// hashWithoutAutoscale is the hash ComputeHash had when Layers stopped at edit.
func hashWithoutAutoscale(p *plan.Plan) (string, error) {
	merged := values.Tree{}
	for _, layer := range masterLayers {
		if tree, ok := p.Layers[layer]; ok {
			values.Merge(merged, tree, layer, nil)
		}
	}
	src := p.Source
	src.CatalogName, src.HF, src.Schema = "", "", ""
	payload := struct {
		Release plan.Release   `json:"release"`
		Source  plan.SourceRef `json:"source"`
		Chart   plan.ChartRef  `json:"chart"`
		Values  values.Tree    `json:"values"`
	}{p.Release, src, p.Chart, merged}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func masterLayerNames(p *plan.Plan) []string {
	var out []string
	for _, name := range masterLayers {
		if len(p.Layers[name]) > 0 {
			out = append(out, name)
		}
	}
	return out
}

func archiveRevision(ref string) (int, bool) {
	_, name, err := cluster.SplitRef(ref)
	if err != nil {
		return 0, false
	}
	i := strings.LastIndex(name, ".v")
	if i < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(name[i+2:])
	if err != nil {
		return 0, false
	}
	return n, true
}

// secretMirror copies secret writes onto the probe prune lists. The production
// writer and the lister are one client; the fakes are not.
type secretMirror struct {
	*fakeWriter
	probe *cluster.Fake
}

func (m *secretMirror) PutSecret(ctx context.Context, ref string, data, labels map[string]string) error {
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

func (m *secretMirror) DeleteSecret(ctx context.Context, ref string) error {
	if err := m.fakeWriter.DeleteSecret(ctx, ref); err != nil {
		return err
	}
	delete(m.probe.Secrets, ref)
	delete(m.probe.SecretLabels, ref)
	return nil
}
