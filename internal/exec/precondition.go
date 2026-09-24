package exec

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/plan"
)

type State struct {
	Exists   bool
	Status   string
	Revision int
}

// Lookup is the live state of one release. It reads helm directly and does not
// care whether swiss deployed it: a name already taken by a hand-installed
// release is a conflict, and that is the whole reason this exists.
func Lookup(ctx context.Context, p cluster.Probe, namespace, name string) (State, error) {
	rel, err := p.Release(ctx, namespace, name)
	if err != nil {
		return State{}, err
	}
	// A plan with no live release is not an existing release: an uninstall that
	// left its plan behind must not make install refuse.
	if rel == nil || rel.Revision == 0 && rel.Status == "" {
		return State{}, nil
	}
	return State{Exists: true, Status: rel.Status, Revision: rel.Revision}, nil
}

// Check refuses an apply that would do something other than what was asked.
func Check(st State, mode Mode, p *plan.Plan) error {
	ref := p.Release.Namespace + "/" + p.Release.Name
	switch {
	case mode == Upgrade && !st.Exists:
		return fmt.Errorf("no release %s: use `swiss install` to create it", ref)
	case mode == Install && st.Exists:
		return fmt.Errorf("release %s already exists at revision %d: use `swiss apply` to upgrade it", ref, st.Revision)
	}
	// A pending release on this workload is usually a 20-40 minute model load in
	// progress, and stepping on it is how one gets genuinely stuck.
	if strings.HasPrefix(st.Status, "pending") {
		return fmt.Errorf("release %s is %s: wait for it, or `helm rollback` first", ref, st.Status)
	}
	return nil
}

// CheckUninstall refuses to remove a release that is not there.
//
// Unlike Check it tolerates a pending status. An apply must not step on a
// 20-40 minute model load, but a release wedged in pending-upgrade is one of
// the things uninstall exists to clear, and refusing here would leave `helm
// uninstall` by hand as the only way out.
func CheckUninstall(st State, namespace, release string) error {
	if !st.Exists {
		return fmt.Errorf("no release %s/%s", namespace, release)
	}
	return nil
}

// CheckRevision fails when the live release moved since the diff was computed.
// Zero means no diff was run and nothing is asserted: the diff is optional, and
// an apply without one carries no revision to check.
func CheckRevision(st State, diffedAt int) error {
	if diffedAt > 0 && st.Revision != diffedAt {
		return fmt.Errorf("release moved from revision %d to %d since you looked; re-check it before applying", diffedAt, st.Revision)
	}
	return nil
}
