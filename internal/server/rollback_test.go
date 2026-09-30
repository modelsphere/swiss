package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
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
	p.Chart.Repo = "https://modelsphere.github.io/helm-charts"
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
				Name: "glm-53", Namespace: "models", Chart: "sglang-0.7.0",
				Status: "deployed", Revision: revision, SwissFiles: files,
			}
		}
	}
	probe.Maps["models/"+cluster.PlanConfigMapPrefix+"glm-53"] = files
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
	ref := archiveRef("models", "glm-53", 4)
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
		archiveRef("models", "glm-53", 4): files,
	}
	probe.SecretLabels = map[string]map[string]string{
		archiveRef("models", "glm-53", 4): archiveLabels("glm-53", 4),
	}
	srv, s := deployServerWith(t, probe, true)
	s.cfg.Server.HelmBin, s.cfg.Server.HelmfileBin = stubHelm(t, 0), stubHelm(t, 0)

	code, out := post(t, srv, "/api/releases/models/glm-53/rollback",
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
		archiveRef("models", "glm-53", 4): files,
	}
	probe.SecretLabels = map[string]map[string]string{
		archiveRef("models", "glm-53", 4): archiveLabels("glm-53", 4),
	}
	srv, s := deployServerWith(t, probe, true)
	s.cfg.Server.HelmBin = stubHelm(t, 0)

	// The operator was looking at revision 6; someone else applied since.
	code, out := post(t, srv, "/api/releases/models/glm-53/rollback",
		map[string]any{"toRevision": 4, "expectRevision": 6})
	if code != http.StatusConflict {
		t.Fatalf("want 409, got %d %v", code, out)
	}
}

func TestRollbackNeedsTheRevisionYouSaw(t *testing.T) {
	probe, files := seedRelease(t, 6, nil)
	probe.Secrets = map[string]map[string]string{
		archiveRef("models", "glm-53", 4): files,
	}
	srv, _ := deployServerWith(t, probe, true)

	code, out := post(t, srv, "/api/releases/models/glm-53/rollback",
		map[string]any{"toRevision": 4})
	if code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d %v", code, out)
	}
}

func TestRollbackToAnUnknownRevisionIsNotFound(t *testing.T) {
	probe, _ := seedRelease(t, 6, nil)
	srv, _ := deployServerWith(t, probe, true)
	code, _ := post(t, srv, "/api/releases/models/glm-53/rollback",
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
		archiveRef("models", "glm-53", 4): tampered,
	}
	srv, _ := deployServerWith(t, probe, true)

	code, out := post(t, srv, "/api/releases/models/glm-53/rollback",
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
		archiveRef("models", "glm-53", 4): files,
	}
	srv, s := deployServerWith(t, probe, true)
	s.cfg.Server.HelmBin, s.cfg.Server.HelmfileBin = stubHelm(t, 0), stubHelm(t, 0)

	code, out := post(t, srv, "/api/releases/models/glm-53/revisions/4/diff", map[string]any{})
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
	code, _ := post(t, srv, "/api/releases/models/glm-53/revisions/99/diff", map[string]any{})
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
		archiveRef("models", "glm-53", 4): files,
	}
	probe.SecretLabels = map[string]map[string]string{
		archiveRef("models", "glm-53", 4): archiveLabels("glm-53", 4),
	}
	srv, _ := deployServerWith(t, probe, true)

	code, out := get(t, srv, "/api/releases/models/glm-53/revisions")
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

// stubHelmValues records every invocation and answers `get values` with a
// different document depending on --all, which is the whole point: helm reports
// only what was supplied without it, and the chart's defaults merged in with it.
func stubHelmValues(t *testing.T) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	bin, log = filepath.Join(dir, "helm"), filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + log + "\n" +
		"case \"$*\" in\n" +
		"  *--all*) printf 'replicaCount: 1\\nextraArgs:\\n  - \"--tp-size=2\"\\nimage:\\n  tag: v0.5.19\\n' ;;\n" +
		"  *) printf 'extraArgs:\\n  - \"--tp-size=2\"\\n' ;;\n" +
		"esac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

// What helm was given, rather than what swiss composed. The archived plan
// answers the second question already; only helm answers the first.
//
// Two runs, because helm will not answer both questions at once: one asks what
// was supplied, the other what that merged to.
func TestRevisionValuesAsksHelmTwice(t *testing.T) {
	probe, _ := seedRelease(t, 6, nil)
	srv, s := deployServerWith(t, probe, true)
	bin, log := stubHelmValues(t)
	s.cfg.Server.HelmBin = bin

	code, out := get(t, srv, "/api/releases/models/glm-53/revisions/4/values")
	if code != 200 {
		t.Fatalf("status %d: %v", code, out)
	}

	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(calls) != 2 {
		t.Fatalf("want one run per question, got %d: %q", len(calls), calls)
	}
	var withAll, without int
	for _, c := range calls {
		for _, want := range []string{"get values", "glm-53", "--namespace models", "--revision 4"} {
			if !strings.Contains(c, want) {
				t.Errorf("helm was not asked for %q: %s", want, c)
			}
		}
		if strings.Contains(c, "--all") {
			withAll++
		} else {
			without++
		}
	}
	if withAll != 1 || without != 1 {
		t.Errorf("want exactly one run each way, got %d with --all and %d without", withAll, without)
	}

	// Both documents come back as helm printed them, one per tab.
	supplied, _ := out["supplied"].(string)
	all, _ := out["all"].(string)
	if !strings.Contains(supplied, "--tp-size=2") {
		t.Errorf("supplied should hold what was set: %q", supplied)
	}
	if !strings.Contains(all, "replicaCount") || !strings.Contains(all, "v0.5.19") {
		t.Errorf("the merged document should carry the chart defaults: %q", all)
	}
	// The key in one and not the other is the whole reason for two tabs:
	// replicaCount came from the chart, not from anybody.
	if strings.Contains(supplied, "replicaCount") {
		t.Errorf("a chart default must not appear as supplied: %q", supplied)
	}
}

// The merged read is the one that has to work. A release where nothing was
// supplied is ordinary, so that half failing costs its tab, not the response.
func TestRevisionValuesSurvivesAFailedSuppliedRead(t *testing.T) {
	probe, _ := seedRelease(t, 6, nil)
	srv, s := deployServerWith(t, probe, true)
	dir := t.TempDir()
	bin := filepath.Join(dir, "helm")
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *--all*) printf 'replicaCount: 1\\n' ;;\n" +
		"  *) echo 'boom' >&2; exit 1 ;;\n" +
		"esac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s.cfg.Server.HelmBin = bin

	code, out := get(t, srv, "/api/releases/models/glm-53/revisions/4/values")
	if code != 200 {
		t.Fatalf("the merged read worked, so this must answer: %d %v", code, out)
	}
	if all, _ := out["all"].(string); !strings.Contains(all, "replicaCount") {
		t.Errorf("the merged document must still be there: %q", all)
	}
	if out["suppliedError"] == nil {
		t.Error("the failure must be reported rather than shown as an empty tab")
	}
}

// Reading values changes nothing, so it is not behind allowDeploy -- a
// read-only swissd must still be able to answer what is running.
func TestRevisionValuesIsNotBehindAllowDeploy(t *testing.T) {
	probe, _ := seedRelease(t, 6, nil)
	srv, s := deployServerWith(t, probe, false)
	s.cfg.Server.HelmBin = stubHelm(t, 0)

	code, out := get(t, srv, "/api/releases/models/glm-53/revisions/6/values")
	if code != 200 {
		t.Fatalf("a read-only swissd must still read values: %d %v", code, out)
	}
}
