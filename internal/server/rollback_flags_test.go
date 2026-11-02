package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/values"
)

// The dry run and the rollback take skipSchemaValidation; a dry run sent with
// no body at all still runs, checking the schema.
func TestRollbackTakesSkipSchemaValidation(t *testing.T) {
	probe, files := seedRelease(t, 6, values.Tree{"replicaCount": 3})
	ref := archiveRef("models", "glm-53", 4)
	probe.Secrets = map[string]map[string]string{ref: files}
	probe.SecretLabels = map[string]map[string]string{ref: archiveLabels("glm-53", 4)}
	srv, s := deployServerWith(t, probe, true)
	bin, log := recordHelmfile(t)
	s.cfg.Server.HelmBin, s.cfg.Server.HelmfileBin = stubHelm(t, 0), bin

	resp, err := http.Post(srv.URL+"/api/releases/models/glm-53/revisions/4/diff", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a dry run with no body: status %d", resp.StatusCode)
	}
	skip := map[string]any{"skipSchemaValidation": true}
	if code, out := post(t, srv, "/api/releases/models/glm-53/revisions/4/diff", map[string]any{"applyFlags": skip}); code != http.StatusOK {
		t.Fatalf("diff %d %v", code, out)
	}
	if code, out := post(t, srv, "/api/releases/models/glm-53/rollback", map[string]any{
		"toRevision": 4, "expectRevision": 6, "applyFlags": skip,
	}); code != http.StatusOK {
		t.Fatalf("rollback %d %v", code, out)
	}

	runs := helmfileRuns(t, log)
	if len(runs) < 3 {
		t.Fatalf("want a helmfile run per diff and the rollback, got %d:\n%s", len(runs), strings.Join(runs, "\n== "))
	}
	for i, run := range runs {
		if got, want := strings.Contains(run, "skipSchemaValidation: true"), i > 0; got != want {
			t.Errorf("run %d skipped the schema: %v, want %v:\n%s", i, got, want, run)
		}
	}
}

// On a PD-autoscaling release the dry run is of the plan the rollback applies:
// the archive with the live autoscale layer, not the archive as stored.
func TestRevisionDiffIsOfThePlanTheRollbackApplies(t *testing.T) {
	probe, baseFiles := seedRelease(t, 6, values.Tree{"replicaCount": 1})
	base, err := plan.FromFiles(baseFiles)
	if err != nil {
		t.Fatal(err)
	}
	withAutoscale := func(replicas int) map[string]string {
		p := clonePlan(base)
		setAutoscaleRule(p)
		p.Layers[values.LayerAutoscale] = values.Tree{"replicaCount": replicas}
		if err := p.ComputeHash(); err != nil {
			t.Fatal(err)
		}
		files, err := p.Files("")
		if err != nil {
			t.Fatal(err)
		}
		return files
	}
	archFiles, liveFiles := withAutoscale(2), withAutoscale(5)
	for i := range probe.Rel {
		if probe.Rel[i].Name == "glm-53" {
			probe.Rel[i].SwissFiles = liveFiles
		}
	}
	probe.Maps[planRef("models", "glm-53")] = liveFiles
	ref := archiveRef("models", "glm-53", 4)
	probe.Secrets = map[string]map[string]string{ref: archFiles}
	probe.SecretLabels = map[string]map[string]string{ref: archiveLabels("glm-53", 4)}

	srv, s := deployServerWith(t, probe, true)
	s.cfg.Server.HelmBin, s.cfg.Server.HelmfileBin = stubHelm(t, 0), stubHelm(t, 0)

	code, diff := post(t, srv, "/api/releases/models/glm-53/revisions/4/diff", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("diff %d %v", code, diff)
	}
	code, out := post(t, srv, "/api/releases/models/glm-53/rollback", map[string]any{"toRevision": 4, "expectRevision": 6})
	if code != http.StatusOK {
		t.Fatalf("rollback %d %v", code, out)
	}
	archived, err := plan.FromFiles(archFiles)
	if err != nil {
		t.Fatal(err)
	}
	if out["planHash"] == archived.Hash {
		t.Fatal("the rollback should have applied the live autoscale layer")
	}
	if diff["planHash"] != out["planHash"] {
		t.Errorf("the dry run diffed %v, the rollback applied %v", diff["planHash"], out["planHash"])
	}
}
