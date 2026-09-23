package exec

import (
	"context"
	"fmt"
	"strings"

	"github.com/aceforeverd/swiss/internal/cluster"
	"github.com/aceforeverd/swiss/internal/plan"
)

type State struct {
	Exists   bool
	Status   string
	Revision int
}

func Lookup(ctx context.Context, p cluster.Probe, namespace, name string) (State, error) {
	releases, err := p.Releases(ctx)
	if err != nil {
		return State{}, err
	}
	for _, r := range releases {
		if r.Name == name && r.Namespace == namespace {
			return State{Exists: true, Status: r.Status, Revision: r.Revision}, nil
		}
	}
	return State{}, nil
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
