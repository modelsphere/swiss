package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/exec"
	"github.com/modelsphere/swiss/internal/llmsvc"
	"github.com/modelsphere/swiss/internal/plan"
)

// What each endpoint takes from the request, decoded from the wire so the
// presence of applyFlags.forceConflicts is what is tested.
func TestApplyRequestOpts(t *testing.T) {
	for _, c := range []struct {
		name, body string
		mode       exec.Mode
		want       applyOpts
	}{
		{"deprecated force alone", `{"forceConflicts":true}`, exec.Upgrade, applyOpts{Action: "apply", ForceConflicts: true}},
		{"applyFlags false overrides it", `{"forceConflicts":true,"applyFlags":{"forceConflicts":false}}`, exec.Upgrade, applyOpts{Action: "apply"}},
		{"applyFlags force", `{"applyFlags":{"forceConflicts":true}}`, exec.Upgrade, applyOpts{Action: "apply", ForceConflicts: true}},
		{"skip schema", `{"applyFlags":{"skipSchemaValidation":true}}`, exec.Upgrade, applyOpts{Action: "apply", SkipSchemaValidation: true}},
		{"createNamespace on install", `{"applyFlags":{"createNamespace":true}}`, exec.Install, applyOpts{Action: "install", CreateNamespace: true}},
		{"createNamespace ignored on upgrade", `{"applyFlags":{"createNamespace":true}}`, exec.Upgrade, applyOpts{Action: "apply"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var req applyRequest
			if err := json.Unmarshal([]byte(c.body), &req); err != nil {
				t.Fatal(err)
			}
			if got := req.opts(c.mode); got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

// recordHelmfile is a helmfile that appends "== <verb>" and the helmfile
// document it was pointed at, one entry per run.
func recordHelmfile(t *testing.T) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	bin, log = filepath.Join(dir, "helmfile"), filepath.Join(dir, "runs.log")
	// Runner.run passes --file <doc> --helm-binary <helm> <verb>.
	script := "#!/bin/sh\necho \"== $5\" >> " + log + "\ncat \"$2\" >> " + log + "\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func helmfileRuns(t *testing.T, log string) []string {
	t.Helper()
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimPrefix(string(raw), "== "), "\n== ")
}

// The flag is the request's: /api/plans no longer takes it, and diff and
// install run with it while the plan beside the release does not say so.
func TestSkipSchemaValidationReachesHelmfileNotThePlan(t *testing.T) {
	probe := liveProbe()
	probe.Maps["swiss/site-profile"] = map[string]string{
		"profile.yaml": profileYAML + "chartRepo: oci://ghcr.io/modelsphere/charts\n",
	}
	srv, s := deployServerWith(t, probe, true)
	bin, log := recordHelmfile(t)
	s.cfg.Server.HelmBin, s.cfg.Server.HelmfileBin = stubHelm(t, 0), bin

	code, body := post(t, srv, "/api/plans", map[string]any{
		"model": "kimi-k2.5", "release": "fresh", "serviceId": "fresh", "skipSchemaValidation": true,
	})
	hash, _ := body["hash"].(string)
	if code != 200 || hash == "" {
		t.Fatalf("plan failed: %d %v", code, body)
	}
	if _, ok := body["skipSchemaValidation"]; ok {
		t.Error("a plan no longer carries the flag")
	}

	skip := map[string]any{"skipSchemaValidation": true}
	if code, out := post(t, srv, "/api/diff", map[string]any{"planHash": hash}); code != 200 {
		t.Fatalf("diff %d %v", code, out)
	}
	if code, out := post(t, srv, "/api/diff", map[string]any{"planHash": hash, "applyFlags": skip}); code != 200 {
		t.Fatalf("diff %d %v", code, out)
	}
	if code, out := post(t, srv, "/api/install", map[string]any{"planHash": hash, "applyFlags": skip}); code != 200 {
		t.Fatalf("install %d %v", code, out)
	}

	runs := helmfileRuns(t, log)
	if len(runs) != 3 {
		t.Fatalf("want three helmfile runs, got %d:\n%s", len(runs), strings.Join(runs, "\n== "))
	}
	for i, want := range []bool{false, true, true} {
		if got := strings.Contains(runs[i], "skipSchemaValidation: true"); got != want {
			t.Errorf("run %d skipped the schema: %v, want %v:\n%s", i, got, want, runs[i])
		}
	}
	files := writtenFiles(t, s, "models", "fresh")
	for _, name := range []string{plan.MetaFile, plan.HelmfileFile} {
		if strings.Contains(files[name], "skipSchemaValidation") {
			t.Errorf("%s beside the release must not carry it:\n%s", name, files[name])
		}
	}
}

// createNamespace on the install itself, rather than composed into the plan.
func TestInstallCreatesTheNamespaceFromApplyFlags(t *testing.T) {
	srv, w := strictNSServer(t, "models")

	code, body := post(t, srv, "/api/plans", map[string]any{
		"model": "kimi-k2.5", "release": "fresh", "serviceId": "fresh", "namespace": "test-sw-ns",
	})
	hash, _ := body["hash"].(string)
	if code != 200 || hash == "" {
		t.Fatalf("plan failed: %d %v", code, body)
	}
	code, body = post(t, srv, "/api/install", map[string]any{
		"planHash": hash, "applyFlags": map[string]any{"createNamespace": true},
	})
	if code != 200 {
		t.Fatalf("install into a new namespace must work: %d %v", code, body)
	}
	if len(w.ensured) != 1 || w.ensured[0] != "test-sw-ns" {
		t.Errorf("the namespace must be created before the plan is recorded: %v", w.ensured)
	}
	if meta := w.written["test-sw-ns/swiss-plan-fresh"][plan.MetaFile]; strings.Contains(meta, "createNamespace") {
		t.Errorf("the plan must not carry it:\n%s", meta)
	}
}

// On an LLMService the flag is the annotation the operator reads, written by
// the apply that asks and removed by the next one that does not.
func TestLLMSkipSchemaValidationIsPerApply(t *testing.T) {
	rig := startRig(t, llmProbe(), true, true, backendLLMSVC)
	hash := rig.storePlan(t, map[string]any{"release": llmRelease})
	code, out := post(t, rig.srv, "/api/install", map[string]any{
		"planHash": hash, "applyFlags": map[string]any{"skipSchemaValidation": true},
	})
	if code != 200 {
		t.Fatalf("install %d %v", code, out)
	}
	u, _ := rig.mustGet(t, "models", llmRelease)
	if got := u.GetAnnotations()[llmsvc.AnnSkipSchemaValidation]; got != "true" {
		t.Fatalf("annotation %q after an install that asked", got)
	}

	next := rig.storePlan(t, map[string]any{
		"release": llmRelease, "overrides": map[string]any{"replicaCount": 2},
	})
	if code, out := post(t, rig.srv, "/api/apply", map[string]any{"planHash": next, "expectRevision": 1}); code != 200 {
		t.Fatalf("apply %d %v", code, out)
	}
	u, _ = rig.mustGet(t, "models", llmRelease)
	if _, ok := u.GetAnnotations()[llmsvc.AnnSkipSchemaValidation]; ok {
		t.Fatalf("an apply that did not ask must remove it: %v", u.GetAnnotations())
	}
}
