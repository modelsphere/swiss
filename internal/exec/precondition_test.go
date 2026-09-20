package exec

import "testing"

func TestApplyAndInstallHaveOppositePreconditions(t *testing.T) {
	p := testPlan()
	if err := Check(State{}, Upgrade, p); err == nil {
		t.Error("apply on a missing release must be refused")
	}
	if err := Check(State{Exists: true, Revision: 3}, Install, p); err == nil {
		t.Error("install over an existing release must be refused -- that is how a second engine lands on one set of GPUs")
	}
	if err := Check(State{Exists: true, Status: "deployed"}, Upgrade, p); err != nil {
		t.Errorf("apply on a deployed release should pass: %v", err)
	}
	if err := Check(State{}, Install, p); err != nil {
		t.Errorf("install of a new release should pass: %v", err)
	}
}

func TestPendingReleaseIsRefused(t *testing.T) {
	if err := Check(State{Exists: true, Status: "pending-upgrade"}, Upgrade, testPlan()); err == nil {
		t.Fatal("a load in progress must not be stepped on")
	}
}

func TestRevisionLockCatchesAConcurrentApply(t *testing.T) {
	if err := CheckRevision(State{Revision: 5}, 4); err == nil {
		t.Fatal("the live release moved since the diff; apply must refuse")
	}
	if err := CheckRevision(State{Revision: 4}, 4); err != nil {
		t.Errorf("matching revisions should pass: %v", err)
	}
	if err := CheckRevision(State{Revision: 9}, 0); err != nil {
		t.Errorf("no expectation means no check: %v", err)
	}
}
