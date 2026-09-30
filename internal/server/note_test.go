package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/values"
)

// The note is the half of a change nothing else records: a diff says what
// moved, and only a person can say why. It is kept in both places swissd
// writes -- the audit log, and the status beside the release, which is the copy
// that survives losing the database.
func TestRollbackRecordsTheNoteInBothPlaces(t *testing.T) {
	probe, files := seedRelease(t, 6, values.Tree{"replicaCount": 3})
	probe.Secrets = map[string]map[string]string{
		archiveRef("models", "glm-53", 4): files,
	}
	probe.SecretLabels = map[string]map[string]string{
		archiveRef("models", "glm-53", 4): archiveLabels("glm-53", 4),
	}
	srv, s := deployServerWith(t, probe, true)
	s.cfg.Server.HelmBin, s.cfg.Server.HelmfileBin = stubHelm(t, 0), stubHelm(t, 0)
	w := &fakeWriter{}
	s.SetWriter(w)

	const why = "tp-size 4 OOMs on the B300s"
	code, out := post(t, srv, "/api/releases/models/glm-53/rollback",
		map[string]any{"toRevision": 4, "expectRevision": 6, "note": why})
	if code != 200 {
		t.Fatalf("status %d: %v", code, out)
	}

	runs, _ := s.store.Runs(context.Background(), 5)
	if len(runs) == 0 || runs[0].Note != why {
		t.Fatalf("the audit row must carry the note, got %+v", runs)
	}
	// The log is a list of rollback targets, which it can only be if each row
	// names the revision it produced.
	if runs[0].Revision == 0 {
		t.Error("the row must name the revision it left the release at")
	}

	status := w.written[planRef("models", "glm-53")]["status.yaml"]
	if !strings.Contains(status, why) {
		t.Errorf("the status beside the release must carry the note, got %q", status)
	}
}

// A failed apply names no revision: helm may leave one behind, but it is not
// one to offer as a rollback target.
func TestAFailedApplyRecordsNoRevision(t *testing.T) {
	probe, _ := seedRelease(t, 6, nil)
	srv, s := deployServerWith(t, probe, true)
	s.cfg.Server.HelmBin, s.cfg.Server.HelmfileBin = stubHelm(t, 0), stubHelm(t, 1)

	p, err := s.compose(context.Background(), planRequest{
		Model: "qwen3.6-35b-a3b", Release: "glm-53", Namespace: "models", ServiceID: "glm-53",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.PutPlan(context.Background(), p); err != nil {
		t.Fatal(err)
	}

	code, _ := post(t, srv, "/api/apply", map[string]any{"planHash": p.Hash, "note": "why not"})
	if code != http.StatusInternalServerError {
		t.Fatalf("the stub helmfile failed, so the apply must too: %d", code)
	}
	runs, _ := s.store.Runs(context.Background(), 5)
	if len(runs) == 0 {
		t.Fatal("a failed apply is the row the log exists for")
	}
	if runs[0].Revision != 0 {
		t.Errorf("revision = %d, want none for a failed apply", runs[0].Revision)
	}
	if runs[0].Note != "why not" {
		t.Errorf("a failed apply keeps its note too, got %q", runs[0].Note)
	}
}

// The rollback page shows the archived plan's settings, read-only. It reads
// them from here rather than recomposing, which would show what the catalog
// says that version is today instead of what actually ran.
func TestRevisionPlanServesTheArchive(t *testing.T) {
	probe, files := seedRelease(t, 6, values.Tree{"replicaCount": 3})
	probe.Secrets = map[string]map[string]string{
		archiveRef("models", "glm-53", 4): files,
	}
	probe.SecretLabels = map[string]map[string]string{
		archiveRef("models", "glm-53", 4): archiveLabels("glm-53", 4),
	}
	srv, _ := deployServerWith(t, probe, true)

	want, err := plan.FromFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	code, body := get(t, srv, "/api/releases/models/glm-53/revisions/4/plan")
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	if body["hash"] != want.Hash {
		t.Errorf("hash = %v, want the archived plan's %v", body["hash"], want.Hash)
	}
	if code, _ := get(t, srv, "/api/releases/models/glm-53/revisions/99/plan"); code != http.StatusNotFound {
		t.Errorf("a revision with no archive must 404, got %d", code)
	}
}
