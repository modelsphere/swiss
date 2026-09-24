package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/values"
)

// seedRelease puts a release in the cluster with its live workspace beside it,
// the way an apply leaves things.
func seedRelease(t *testing.T, revision int, overrides values.Tree) (cluster.Fake, map[string]string) {
	t.Helper()
	p, _ := livePlan(t, planRequest{
		Model: "glm5.1", Release: "glm-53", ServiceID: "glm-53", Overrides: overrides,
	})
	// The shared test profile names no chart source, so a workspace rendered
	// from it has no helmfile.yaml and nothing can be applied from it.
	p.Chart.Repo = "https://harbor.4pd.io/chartrepo/hardcore-tech"
	if err := p.ComputeHash(); err != nil {
		t.Fatal(err)
	}
	files, err := p.Files("")
	if err != nil {
		t.Fatal(err)
	}
	probe := liveProbe()
	// liveProbe already carries a glm-53; replace it rather than adding a
	// second, or Lookup finds whichever comes first.
	for i := range probe.Rel {
		if probe.Rel[i].Name == "glm-53" {
			probe.Rel[i] = cluster.Release{
				Name: "glm-53", Namespace: "modelforge", Chart: "sglang-0.7.0",
				Status: "deployed", Revision: revision, SwissFiles: files,
			}
		}
	}
	probe.Maps["modelforge/"+cluster.PlanConfigMapPrefix+"glm-53"] = files
	return probe, files
}

// An apply keeps the plan that produced the outgoing revision, so every applied
// revision stays reproducible from the cluster alone.
func TestApplyArchivesTheOutgoingPlan(t *testing.T) {
	probe, _ := seedRelease(t, 4, values.Tree{"replicaCount": 2})
	srv, s := deployServerWith(t, probe, true)
	s.cfg.Server.HelmBin = stubHelm(t, 0)

	_, p := post(t, srv, "/api/plans", map[string]any{
		"model": "glm5.1", "serviceId": "glm-53",
		"overrides": map[string]any{"replicaCount": 3},
	})
	post(t, srv, "/api/apply", map[string]any{"planHash": p["hash"]})

	w := s.writer.(*fakeWriter)
	ref := archiveRef("modelforge", "glm-53", 4)
	kept, ok := w.secrets[ref]
	if !ok {
		t.Fatalf("revision 4's plan was not archived; secrets: %v", keys(w.secrets))
	}
	// The archive is the whole workspace, not a summary.
	for _, want := range []string{"plan.yaml", "form.yaml", "catalog.yaml"} {
		if _, present := kept[want]; !present {
			t.Errorf("archive is missing %s", want)
		}
	}
	// Labelled the way helm labels its own revisions, so history is a listing.
	if l := w.labels[ref]; l["owner"] != "swiss" || l["name"] != "glm-53" || l["version"] != "4" {
		t.Errorf("archive labels: %v", l)
	}
}

// Rollback re-applies an archived plan forward, as a new revision.
func TestRollbackAppliesAnArchivedPlan(t *testing.T) {
	probe, files := seedRelease(t, 6, values.Tree{"replicaCount": 3})
	probe.Secrets = map[string]map[string]string{
		archiveRef("modelforge", "glm-53", 4): files,
	}
	probe.SecretLabels = map[string]map[string]string{
		archiveRef("modelforge", "glm-53", 4): archiveLabels("glm-53", 4),
	}
	srv, s := deployServerWith(t, probe, true)
	s.cfg.Server.HelmBin, s.cfg.Server.HelmfileBin = stubHelm(t, 0), stubHelm(t, 0)

	code, out := post(t, srv, "/api/releases/modelforge/glm-53/rollback",
		map[string]any{"toRevision": 4, "expectRevision": 6})
	if code != 200 {
		t.Fatalf("status %d: %v", code, out)
	}
	// Forward, as a new revision, applying the archived plan rather than the
	// live one -- so the hash is revision 4's, not revision 6's.
	want, err := plan.FromFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	if out["planHash"] != want.Hash {
		t.Errorf("applied %v, want the archived plan %v", out["planHash"], want.Hash)
	}

	runs, _ := s.store.Runs(context.Background(), 5)
	if len(runs) == 0 || runs[0].Action != "rollback" {
		t.Fatalf("a rollback must be audited as one: %+v", runs)
	}
}

// The lock is the whole point: a rollback runs when something is already wrong,
// which is when a second operator is most likely to be acting on the release.
func TestRollbackRefusesWhenTheReleaseMoved(t *testing.T) {
	probe, files := seedRelease(t, 7, values.Tree{"replicaCount": 3})
	probe.Secrets = map[string]map[string]string{
		archiveRef("modelforge", "glm-53", 4): files,
	}
	probe.SecretLabels = map[string]map[string]string{
		archiveRef("modelforge", "glm-53", 4): archiveLabels("glm-53", 4),
	}
	srv, s := deployServerWith(t, probe, true)
	s.cfg.Server.HelmBin = stubHelm(t, 0)

	// The operator was looking at revision 6; someone else applied since.
	code, out := post(t, srv, "/api/releases/modelforge/glm-53/rollback",
		map[string]any{"toRevision": 4, "expectRevision": 6})
	if code != http.StatusConflict {
		t.Fatalf("want 409, got %d %v", code, out)
	}
}

func TestRollbackNeedsTheRevisionYouSaw(t *testing.T) {
	probe, files := seedRelease(t, 6, nil)
	probe.Secrets = map[string]map[string]string{
		archiveRef("modelforge", "glm-53", 4): files,
	}
	srv, _ := deployServerWith(t, probe, true)

	code, out := post(t, srv, "/api/releases/modelforge/glm-53/rollback",
		map[string]any{"toRevision": 4})
	if code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d %v", code, out)
	}
}

func TestRollbackToAnUnknownRevisionIsNotFound(t *testing.T) {
	probe, _ := seedRelease(t, 6, nil)
	srv, _ := deployServerWith(t, probe, true)
	code, _ := post(t, srv, "/api/releases/modelforge/glm-53/rollback",
		map[string]any{"toRevision": 99, "expectRevision": 6})
	if code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", code)
	}
}

// An archive has been bytes in etcd since it was written, and a rollback
// applies it without composing anything.
func TestRollbackRefusesAnEditedArchive(t *testing.T) {
	probe, files := seedRelease(t, 6, nil)
	tampered := map[string]string{}
	for k, v := range files {
		tampered[k] = v
	}
	tampered["form.yaml"] = "replicaCount: 99\n"
	probe.Secrets = map[string]map[string]string{
		archiveRef("modelforge", "glm-53", 4): tampered,
	}
	srv, _ := deployServerWith(t, probe, true)

	code, out := post(t, srv, "/api/releases/modelforge/glm-53/rollback",
		map[string]any{"toRevision": 4, "expectRevision": 6})
	if code != http.StatusNotFound {
		t.Fatalf("an edited archive must be refused, got %d %v", code, out)
	}
}

func keys(m map[string]map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A rollback can be previewed, like every other apply here. The diff carries
// back the live revision it saw, which is what the rollback then asserts.
func TestRevisionDiffCarriesTheLiveRevision(t *testing.T) {
	probe, files := seedRelease(t, 6, nil)
	probe.Secrets = map[string]map[string]string{
		archiveRef("modelforge", "glm-53", 4): files,
	}
	srv, s := deployServerWith(t, probe, true)
	s.cfg.Server.HelmBin, s.cfg.Server.HelmfileBin = stubHelm(t, 0), stubHelm(t, 0)

	code, out := post(t, srv, "/api/releases/modelforge/glm-53/revisions/4/diff", map[string]any{})
	if code != 200 {
		t.Fatalf("status %d: %v", code, out)
	}
	if out["toRevision"] != float64(4) {
		t.Errorf("toRevision = %v", out["toRevision"])
	}
	if out["revision"] != float64(6) {
		t.Errorf("the diff must carry the live revision, got %v", out["revision"])
	}

	// It changes nothing in the cluster: no plan written, no archive taken.
	w := s.writer.(*fakeWriter)
	if len(w.written) != 0 || len(w.secrets) != 0 {
		t.Errorf("a diff must not write: cm=%v secrets=%v", keys(w.written), keys(w.secrets))
	}
}

func TestRevisionDiffOnAnUnknownRevisionIsNotFound(t *testing.T) {
	probe, _ := seedRelease(t, 6, nil)
	srv, _ := deployServerWith(t, probe, true)
	code, _ := post(t, srv, "/api/releases/modelforge/glm-53/revisions/99/diff", map[string]any{})
	if code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", code)
	}
}

// The row names what was running, and a chart version is half of that: two
// revisions of one model version can differ only by the chart they were
// deployed through.
func TestRevisionsCarryTheChart(t *testing.T) {
	probe, files := seedRelease(t, 6, nil)
	probe.Secrets = map[string]map[string]string{
		archiveRef("modelforge", "glm-53", 4): files,
	}
	probe.SecretLabels = map[string]map[string]string{
		archiveRef("modelforge", "glm-53", 4): archiveLabels("glm-53", 4),
	}
	srv, _ := deployServerWith(t, probe, true)

	code, out := get(t, srv, "/api/releases/modelforge/glm-53/revisions")
	if code != 200 {
		t.Fatalf("status %d: %v", code, out)
	}
	revs, _ := out["revisions"].([]any)
	if len(revs) != 2 {
		t.Fatalf("want the archive and the live one, got %v", out["revisions"])
	}
	for _, r := range revs {
		row, _ := r.(map[string]any)
		chart, _ := row["chart"].(string)
		if !strings.Contains(chart, "-") {
			t.Errorf("revision %v carries no chart name-version: %q", row["revision"], chart)
		}
	}
}

// What helm was given, rather than what swiss composed. The archived plan
// answers the second question already; only helm answers the first.
func TestRevisionValuesAsksHelmForThatRevision(t *testing.T) {
	probe, _ := seedRelease(t, 6, nil)
	srv, s := deployServerWith(t, probe, true)
	s.cfg.Server.HelmBin = stubHelm(t, 0)

	code, out := get(t, srv, "/api/releases/modelforge/glm-53/revisions/4/values")
	if code != 200 {
		t.Fatalf("status %d: %v", code, out)
	}
	got, _ := out["values"].(string)
	for _, want := range []string{"get values", "glm-53", "--namespace modelforge", "--revision 4", "--all"} {
		if !strings.Contains(got, want) {
			t.Errorf("helm was not asked for %q:\n%s", want, got)
		}
	}
}

// Reading values changes nothing, so it is not behind allowDeploy -- a
// read-only swissd must still be able to answer what is running.
func TestRevisionValuesIsNotBehindAllowDeploy(t *testing.T) {
	probe, _ := seedRelease(t, 6, nil)
	srv, s := deployServerWith(t, probe, false)
	s.cfg.Server.HelmBin = stubHelm(t, 0)

	code, out := get(t, srv, "/api/releases/modelforge/glm-53/revisions/6/values")
	if code != 200 {
		t.Fatalf("a read-only swissd must still read values: %d %v", code, out)
	}
}
