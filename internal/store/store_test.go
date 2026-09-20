package store

import (
	"context"
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
		Values:     values.Tree{"replicaCount": 2},
		Hash:       hash,
	}
}

func TestPlansAreImmutableAndRoundTrip(t *testing.T) {
	s, ctx := open(t), context.Background()
	if err := s.PutPlan(ctx, "prod", testPlan("sha256:a")); err != nil {
		t.Fatal(err)
	}
	// Re-storing the same hash must not clobber: history is what rollback uses.
	if err := s.PutPlan(ctx, "prod", testPlan("sha256:a")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Plan(ctx, "sha256:a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Source.Model != "glm-5.3" || got.Release.Name != "glm-53" {
		t.Fatalf("round trip lost fields: %+v", got)
	}
	if v, _ := values.Get(got.Values, "replicaCount"); v != float64(2) && v != 2 {
		t.Errorf("values lost: %v", got.Values)
	}
	if _, err := s.Plan(ctx, "sha256:missing"); err == nil {
		t.Error("expected an error for an unknown plan")
	}
}

func TestRecordApplyBumpsVersionAndDetectsAConcurrentWrite(t *testing.T) {
	s, ctx := open(t), context.Background()
	d := Deployment{Cluster: "prod", Namespace: "modelforge", Release: "glm-53", PlanHash: "sha256:a", Revision: 4}
	if err := s.RecordApply(ctx, d, 0); err != nil {
		t.Fatal(err)
	}
	list, err := s.Deployments(ctx, "prod")
	if err != nil || len(list) != 1 || list[0].Version != 1 {
		t.Fatalf("got %+v err=%v", list, err)
	}

	d.PlanHash, d.Revision = "sha256:b", 5
	if err := s.RecordApply(ctx, d, 1); err != nil {
		t.Fatal(err)
	}
	list, _ = s.Deployments(ctx, "prod")
	if list[0].Version != 2 || list[0].PlanHash != "sha256:b" {
		t.Fatalf("version/plan not advanced: %+v", list[0])
	}

	// Someone else applied in between.
	if err := s.RecordApply(ctx, d, 1); err == nil {
		t.Fatal("a stale version must be refused")
	}
}

func TestRunsAreAppendOnlyAndNewestFirst(t *testing.T) {
	s, ctx := open(t), context.Background()
	for _, a := range []string{"diff", "apply"} {
		if _, err := s.RecordRun(ctx, Run{
			Cluster: "prod", Namespace: "modelforge", Release: "glm-53",
			Action: a, PlanHash: "sha256:a", Changed: a == "apply",
			StartedAt: "2026-09-20T10:00:00Z", EndedAt: "2026-09-20T10:00:01Z",
		}); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := s.Runs(ctx, "prod", 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("got %d runs err=%v", len(runs), err)
	}
	if runs[0].Action != "apply" || !runs[0].Changed {
		t.Errorf("newest first, with fields: %+v", runs[0])
	}
}

func TestDeploymentsAreScopedByCluster(t *testing.T) {
	s, ctx := open(t), context.Background()
	for _, c := range []string{"prod", "dev"} {
		if err := s.RecordApply(ctx, Deployment{Cluster: c, Namespace: "n", Release: "r", PlanHash: "h"}, 0); err != nil {
			t.Fatal(err)
		}
	}
	list, _ := s.Deployments(ctx, "prod")
	if len(list) != 1 || list[0].Cluster != "prod" {
		t.Fatalf("cluster scoping broken: %+v", list)
	}
}
