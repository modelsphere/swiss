package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/aceforeverd/swiss/internal/plan"
	"github.com/aceforeverd/swiss/internal/values"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "swiss.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func testPlan(hash string) *plan.Plan {
	return &plan.Plan{
		APIVersion: plan.APIVersion,
		Release:    plan.Release{Name: "glm-53", Namespace: "modelforge"},
		Source:     plan.SourceRef{Model: "glm-5.3", Variant: "sglang-tp8-b300"},
		Layers:     map[string]values.Tree{"form": {"replicaCount": 2}},
		Hash:       hash,
	}
}

func TestPlansAreImmutableAndRoundTrip(t *testing.T) {
	s, ctx := open(t), context.Background()
	if err := s.PutPlan(ctx, testPlan("sha256:a")); err != nil {
		t.Fatal(err)
	}
	// Re-storing the same hash must not clobber: history is what rollback uses.
	if err := s.PutPlan(ctx, testPlan("sha256:a")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Plan(ctx, "sha256:a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Source.Model != "glm-5.3" || got.Release.Name != "glm-53" {
		t.Fatalf("round trip lost fields: %+v", got)
	}
	if v, _ := values.Get(got.Values(), "replicaCount"); v != float64(2) && v != 2 {
		t.Errorf("values lost: %v", got.Values())
	}
	if _, err := s.Plan(ctx, "sha256:missing"); err == nil {
		t.Error("expected an error for an unknown plan")
	}
}

// Nothing in this database claims to know what is deployed. That answer is the
// plan ConfigMap beside each release, and a table holding a second copy of it
// would disagree with the cluster the first time an apply failed between the
// two writes.
func TestNothingHereRecordsWhatIsDeployed(t *testing.T) {
	s, ctx := open(t), context.Background()
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name == "deployments" {
			t.Fatal("a deployments table is a second source of truth for what is running")
		}
	}
}

func TestRunsAreAppendOnlyAndNewestFirst(t *testing.T) {
	s, ctx := open(t), context.Background()
	for _, a := range []string{"diff", "apply"} {
		if _, err := s.RecordRun(ctx, Run{
			Namespace: "modelforge", Release: "glm-53",
			Action: a, PlanHash: "sha256:a", Changed: a == "apply",
			StartedAt: "2026-09-20T10:00:00Z", EndedAt: "2026-09-20T10:00:01Z",
		}); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := s.Runs(ctx, 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("got %d runs err=%v", len(runs), err)
	}
	if runs[0].Action != "apply" || !runs[0].Changed {
		t.Errorf("newest first, with fields: %+v", runs[0])
	}
}

// One swissd, one cluster, one database: a release is identified by namespace
// and name, with no cluster key anywhere.
func TestReleasesAreKeyedByNamespaceAndName(t *testing.T) {
	s, ctx := open(t), context.Background()
	for _, ns := range []string{"modelforge", "kimi"} {
		if _, err := s.RecordRun(ctx, Run{
			Namespace: ns, Release: "r", Action: "apply", PlanHash: "h",
			StartedAt: "2026-09-20T10:00:00Z", EndedAt: "2026-09-20T10:00:01Z",
		}); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := s.Runs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range runs {
		seen[r.Namespace] = true
	}
	if len(seen) != 2 {
		t.Fatalf("the same release name in two namespaces is two releases: %+v", runs)
	}
}

// A swissd upgrade must not need a fresh volume: the columns added after the
// first release are added to the database that is already on the PVC, with the
// rows that are already in it left alone.
func TestOpenAddsColumnsToAnOlderDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	// The schema as it shipped before notes and revisions existed.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
CREATE TABLE runs (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  namespace  TEXT NOT NULL,
  release    TEXT NOT NULL,
  action     TEXT NOT NULL,
  plan_hash  TEXT NOT NULL,
  actor      TEXT NOT NULL DEFAULT '',
  changed    INTEGER NOT NULL DEFAULT 0,
  error      TEXT NOT NULL DEFAULT '',
  output     TEXT NOT NULL DEFAULT '',
  started_at TEXT NOT NULL,
  ended_at   TEXT NOT NULL
);
INSERT INTO runs (namespace, release, action, plan_hash, started_at, ended_at)
VALUES ('modelforge', 'glm-53', 'apply', 'sha256:old', '2026-01-01T00:00:00Z', '2026-01-01T00:01:00Z');
`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("an existing database must open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	if _, err := s.RecordRun(ctx, Run{
		Namespace: "modelforge", Release: "glm-53", Action: "rollback",
		PlanHash: "sha256:new", Note: "b300s were OOMing", Revision: 8,
		StartedAt: "2026-01-02T00:00:00Z", EndedAt: "2026-01-02T00:01:00Z",
	}); err != nil {
		t.Fatalf("record after migrate: %v", err)
	}

	runs, err := s.Runs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("the rows that were already there must survive, got %d", len(runs))
	}
	if runs[0].Note != "b300s were OOMing" || runs[0].Revision != 8 {
		t.Errorf("new columns not stored: %+v", runs[0])
	}
	// The old row has no note, which is what a default is for.
	if runs[1].Note != "" || runs[1].Revision != 0 {
		t.Errorf("an old row must read back empty, got %+v", runs[1])
	}

	// Opening twice must not try to add the columns again.
	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	again.Close()
}
