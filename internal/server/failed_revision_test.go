package server

import (
	"strings"
	"testing"
	"time"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/llmsvc"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A failed latest helm attempt is the live revision. status.helm stays on the
// last success, and that number is not the apply lock.
func TestLLMFailedHelmRevisionIsTheApplyLock(t *testing.T) {
	rig, hash := seedLLMBaseline(t, llmBaseline{
		operator: true,
		secret:   &cluster.Release{Name: llmRelease, Namespace: "models", Status: "failed", Revision: 5},
		phase:    llmsvc.PhaseFailed, helmRev: 4, helmStat: "deployed",
	})
	rev, exists := diffOf(t, rig, hash)
	if rev != 5 || !exists {
		t.Fatalf("diff revision %d exists %v, want 5 true", rev, exists)
	}
	code, st := get(t, rig.srv, "/api/releases/models/"+llmRelease+"/status")
	if code != 200 || asNum(st["revision"]) != 5 || st["helmStatus"] != "failed" {
		t.Fatalf("status %d %v", code, st)
	}
	ps, _ := st["planStatus"].(map[string]any)
	svc, _ := st["llmservice"].(map[string]any)
	if asNum(ps["revision"]) != 4 || asNum(svc["revision"]) != 4 {
		t.Fatalf("recorded revision changed: plan %v service %v", ps["revision"], svc["revision"])
	}
	code, body := get(t, rig.srv, "/api/releases/models/"+llmRelease+"/revisions")
	if code != 200 {
		t.Fatalf("revisions %d %v", code, body)
	}
	rows, _ := body["revisions"].([]any)
	sawRecorded := false
	for _, raw := range rows {
		row := raw.(map[string]any)
		if asNum(row["revision"]) == 4 {
			sawRecorded = true
			if row["current"] == true {
				t.Fatalf("last success marked current: %v", row)
			}
		}
		if row["current"] == true {
			t.Fatalf("current row %v, want none until the failed revision is recorded", row)
		}
	}
	if !sawRecorded {
		t.Fatalf("history missing the recorded revision: %v", body["revisions"])
	}

	code, out := post(t, rig.srv, "/api/apply", map[string]any{
		"planHash": hash, "expectRevision": rev, "applyFlags": map[string]any{"forceConflicts": true},
	})
	if code != 200 {
		t.Fatalf("apply at the failed revision %d %v", code, out)
	}
}

func TestLLMStaleSuccessRevisionConflicts(t *testing.T) {
	rig, hash := seedLLMBaseline(t, llmBaseline{
		operator: false,
		secret:   &cluster.Release{Name: llmRelease, Namespace: "models", Status: "failed", Revision: 5},
		phase:    llmsvc.PhaseFailed, helmRev: 4, helmStat: "deployed",
	})
	for _, flags := range []any{nil, map[string]any{"forceConflicts": true}} {
		body := map[string]any{"planHash": hash, "expectRevision": 4}
		if flags != nil {
			body["applyFlags"] = flags
		}
		code, out := post(t, rig.srv, "/api/apply", body)
		if code != 409 || !strings.Contains(errText(out), "moved from revision 4 to 5") {
			t.Fatalf("flags %v: %d %v", flags, code, out)
		}
	}
	if runs := rig.runs(t); len(runs) != 0 {
		t.Fatalf("a revision conflict is not an apply: %+v", runs)
	}
}

func TestLLMPendingAndApplyingIgnoreForceConflicts(t *testing.T) {
	rig, hash := seedLLMBaseline(t, llmBaseline{
		operator: false,
		secret:   &cluster.Release{Name: llmRelease, Namespace: "models", Status: "pending-upgrade", Revision: 5},
		phase:    llmsvc.PhaseFailed, helmRev: 4, helmStat: "deployed",
	})
	code, out := post(t, rig.srv, "/api/apply", map[string]any{
		"planHash": hash, "expectRevision": 5, "applyFlags": map[string]any{"forceConflicts": true},
	})
	want := "release models/" + llmRelease + " is pending-upgrade: wait for it, or `helm rollback` first"
	if code != 409 || errText(out) != want {
		t.Fatalf("pending %d %q", code, errText(out))
	}

	rig, hash = seedLLMBaseline(t, llmBaseline{
		operator: false,
		phase:    llmsvc.PhaseApplying, helmRev: 1, helmStat: "pending-upgrade",
	})
	code, out = post(t, rig.srv, "/api/apply", map[string]any{
		"planHash": hash, "expectRevision": 1, "applyFlags": map[string]any{"forceConflicts": true},
	})
	want = "release models/" + llmRelease + " is Applying: wait for it, or `helm rollback` first"
	if code != 409 || errText(out) != want {
		t.Fatalf("applying %d %q", code, errText(out))
	}
}

// No helm secret: the operator's status.helm is the lock, and diff reports it.
func TestLLMStatusHelmIsTheLockWhenHelmHasNoSecret(t *testing.T) {
	rig, hash := seedLLMBaseline(t, llmBaseline{
		operator: true,
		phase:    llmsvc.PhaseFailed, helmRev: 4, helmStat: "failed",
	})
	rev, exists := diffOf(t, rig, hash)
	if rev != 4 || !exists {
		t.Fatalf("diff revision %d exists %v, want 4 true", rev, exists)
	}
	code, st := get(t, rig.srv, "/api/releases/models/"+llmRelease+"/status")
	if code != 200 || asNum(st["revision"]) != rev || st["helmStatus"] != "failed" {
		t.Fatalf("status %d %v", code, st)
	}
	code, out := post(t, rig.srv, "/api/apply", map[string]any{"planHash": hash, "expectRevision": 3})
	if code != 409 || !strings.Contains(errText(out), "moved from revision 3 to 4") {
		t.Fatalf("stale %d %v", code, out)
	}
	code, out = post(t, rig.srv, "/api/apply", map[string]any{"planHash": hash, "expectRevision": rev})
	if code != 200 {
		t.Fatalf("apply %d %v", code, out)
	}
}

func TestHelmFailedRevisionIsTheApplyLock(t *testing.T) {
	probe := liveProbe()
	probe.Maps["swiss/site-profile"]["profile.yaml"] += "chartRepo: oci://ghcr.io/modelsphere/charts\n"
	const ns, release = "models", "glm-53"
	for i := range probe.Rel {
		if probe.Rel[i].Namespace == ns && probe.Rel[i].Name == release {
			probe.Rel[i].Revision = 5
			probe.Rel[i].Status = "failed"
		}
	}
	srv, s := deployServerWith(t, &probe, true)
	bin, _ := recordHelmfile(t)
	s.cfg.Server.HelmBin, s.cfg.Server.HelmfileBin = stubHelm(t, 0), bin

	code, body := post(t, srv, "/api/plans", map[string]any{
		"model": "qwen3.6-35b-a3b", "release": release, "serviceId": release,
	})
	hash, _ := body["hash"].(string)
	if code != 200 || hash == "" {
		t.Fatalf("plan %d %v", code, body)
	}
	code, diff := post(t, srv, "/api/diff", map[string]any{"planHash": hash})
	if code != 200 || asNum(diff["revision"]) != 5 || diff["exists"] != true {
		t.Fatalf("diff %d %v", code, diff)
	}
	code, st := get(t, srv, "/api/releases/"+ns+"/"+release+"/status")
	if code != 200 || asNum(st["revision"]) != 5 || st["helmStatus"] != "failed" {
		t.Fatalf("status %d %v", code, st)
	}
	code, out := post(t, srv, "/api/apply", map[string]any{"planHash": hash, "expectRevision": 5})
	if code != 200 {
		t.Fatalf("apply %d %v", code, out)
	}
	code, out = post(t, srv, "/api/apply", map[string]any{
		"planHash": hash, "expectRevision": 4, "applyFlags": map[string]any{"forceConflicts": true},
	})
	if code != 409 || !strings.Contains(errText(out), "moved from revision 4 to 5") {
		t.Fatalf("stale force %d %v", code, out)
	}
	for i := range probe.Rel {
		if probe.Rel[i].Namespace == ns && probe.Rel[i].Name == release {
			probe.Rel[i].Status = "pending-upgrade"
		}
	}
	code, out = post(t, srv, "/api/apply", map[string]any{
		"planHash": hash, "expectRevision": 5, "applyFlags": map[string]any{"forceConflicts": true},
	})
	if code != 409 || !strings.Contains(errText(out), "pending-upgrade") {
		t.Fatalf("pending force %d %v", code, out)
	}
}

// A failed PD scale leaves status.helm on the last success and the helm secret
// on the failed attempt. The next autoscale apply has to lock the secret.
func TestAutoscaleRetriesAFailedLLMReleaseAtTheLiveRevision(t *testing.T) {
	const ns, release = "models", "pd"
	dec := decisionServer(t, ns, release, 1)
	p, _ := pdPlan(t, ns, release, release, 2, true, false, boolPtr(true), "v2")

	probe := llmProbe()
	setDecisionProfile(probe, dec.URL)
	probe.Maps["swiss/site-profile"]["profile.yaml"] += "pdScale:\n  deferTimeoutSeconds: 600\n"
	probe.Deps = readyRouters(ns, release, 2)
	probe.DSs = map[string]cluster.DisaggregatedSet{ns + "/" + release: readyDS(2)}
	probe.Rel = []cluster.Release{{
		Name: release, Namespace: ns, Status: "failed", Revision: 5,
	}}
	rig := startRig(t, probe, true, true, backendLLMSVC)

	u, err := llmsvc.FromPlan(p, "install", "seed")
	if err != nil {
		t.Fatal(err)
	}
	u.SetGeneration(1)
	if err := unstructured.SetNestedMap(u.Object, map[string]any{
		"observedGeneration": int64(1),
		"phase":              llmsvc.PhaseFailed,
		"message":            "scale failed",
		"helm":               map[string]any{"revision": int64(4), "status": "deployed"},
	}, "status"); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.c.Create(t.Context(), u); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	rig.s.now = func() time.Time { return now }
	logs := captureLog(rig.s)

	rig.s.autoscaleTick(t.Context())
	st, ok := rig.s.autoscaleStateOf(ns, release)
	if !ok || st.Outcome != outcomeHeld || st.Hold != "release failed" {
		t.Fatalf("first tick ok=%v %+v log=%s", ok, st, logs)
	}

	now = now.Add(11 * time.Minute)
	rig.s.autoscaleTick(t.Context())
	st, ok = rig.s.autoscaleStateOf(ns, release)
	if !ok || st.Outcome != outcomeScaledDown || st.Error != "" {
		t.Fatalf("retry ok=%v %+v log=%s", ok, st, logs)
	}
	if strings.Contains(logs.String(), "moved from revision") {
		t.Fatalf("retry conflicted: %s", logs)
	}
	got, err := rig.c.Get(t.Context(), ns, release)
	if err != nil {
		t.Fatal(err)
	}
	if got.GetAnnotations()[llmsvc.AnnAction] != "autoscale" {
		t.Fatalf("action %q log=%s", got.GetAnnotations()[llmsvc.AnnAction], logs)
	}
}

type llmBaseline struct {
	operator bool
	secret   *cluster.Release
	phase    string
	helmRev  int64
	helmStat string
}

func seedLLMBaseline(t *testing.T, b llmBaseline) (*llmRig, string) {
	t.Helper()
	probe := llmProbe()
	if b.secret != nil {
		probe.Rel = []cluster.Release{*b.secret}
	}
	rig := startRig(t, probe, true, b.operator, backendLLMSVC)
	u := llmObject("models", llmRelease, map[string]string{
		llmsvc.AnnProfile: "prod", llmsvc.AnnAction: "apply",
	})
	gen := int64(1)
	if b.phase == llmsvc.PhaseApplying {
		gen = 2
	}
	u.SetGeneration(gen)
	u.Object["status"] = map[string]any{
		"observedGeneration": gen,
		"phase":              b.phase,
		"message":            "scale failed",
		"helm":               map[string]any{"revision": b.helmRev, "status": b.helmStat},
		"history":            []any{map[string]any{"revision": b.helmRev, "hash": "sha256:old"}},
	}
	if _, err := rig.c.Create(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	hash := rig.storePlan(t, map[string]any{
		"release": llmRelease, "overrides": map[string]any{"replicaCount": 2},
	})
	bin, _ := recordHelmfile(t)
	rig.s.cfg.Server.HelmBin = stubHelm(t, 0)
	rig.s.cfg.Server.HelmfileBin = bin
	return rig, hash
}

func diffOf(t *testing.T, rig *llmRig, hash string) (int, bool) {
	t.Helper()
	code, out := post(t, rig.srv, "/api/diff", map[string]any{"planHash": hash})
	if code != 200 {
		t.Fatalf("diff %d %v", code, out)
	}
	return asNum(out["revision"]), out["exists"] == true
}
