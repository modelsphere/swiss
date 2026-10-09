package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/modelsphere/swiss/internal/exec"
	"github.com/modelsphere/swiss/internal/plan"
)

type rollbackRequest struct {
	// ToRevision is the revision whose plan to re-apply.
	ToRevision int `json:"toRevision"`
	// ExpectRevision is what the caller last saw live. A rollback runs when
	// something is already wrong, which is exactly when a second operator is
	// likely to be acting on the same release -- so it asserts that nothing
	// moved rather than silently overwriting whatever landed in between.
	ExpectRevision int `json:"expectRevision"`
	// Note is why the release is going back, in the operator's own words. A
	// rollback is the operation whose reason is least recoverable from the
	// diff: the plan says what came back, never what was wrong with what it
	// replaced.
	Note string `json:"note,omitempty"`
}

type revision struct {
	Revision int    `json:"revision"`
	Model    string `json:"model,omitempty"`
	Version  string `json:"version,omitempty"`
	Variant  string `json:"variant,omitempty"`
	Chart    string `json:"chart,omitempty"`
	PlanHash string `json:"planHash,omitempty"`
	Current  bool   `json:"current,omitempty"`
}

// handleRevisions lists what a release can be rolled back to: one archived
// workspace per applied revision, read from the cluster rather than the audit
// log, so losing the database does not cost the ability to roll back.
func (s *Server) handleRevisions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()

	ns, release := r.PathValue("namespace"), r.PathValue("release")
	revs, err := s.revisions(ctx, ns, release)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"release": release, "namespace": ns, "revisions": revs})
}

func (s *Server) revisions(ctx context.Context, ns, release string) ([]revision, error) {
	b, err := s.readBackend(ctx, ns, release)
	if err != nil {
		return nil, err
	}
	return b.Revisions(ctx, ns, release)
}

// handleRevisionValues answers with the values helm holds for a revision,
// chart defaults included.
//
// Read-only, so it is not behind allowDeploy: it runs `helm get values` and
// nothing else. The archived plan beside the release says what swiss composed;
// this says what helm was given, and the two are only the same until something
// is wrong.
func (s *Server) handleRevisionValues(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, time.Minute)
	defer cancel()

	ns, release := r.PathValue("namespace"), r.PathValue("release")
	rev, err := strconv.Atoi(r.PathValue("revision"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "revision must be a number")
		return
	}
	// Two runs, because helm answers two different questions and will not answer
	// both at once. In parallel: they are independent, and one helm invocation of
	// latency is enough for opening a row.
	var (
		wg              sync.WaitGroup
		allRaw, suppRaw string
		allErr, suppErr error
	)
	wg.Add(2)
	go func() { defer wg.Done(); allRaw, allErr = s.runner().Values(ctx, ns, release, rev, true) }()
	go func() { defer wg.Done(); suppRaw, suppErr = s.runner().Values(ctx, ns, release, rev, false) }()
	wg.Wait()

	// The merged read is the one that has to work: it is what the release was
	// rendered from. A release with no supplied values at all is ordinary, so
	// that half failing costs its tab and not the response.
	if allErr != nil {
		writeError(w, http.StatusBadGateway, allErr.Error())
		return
	}
	out := map[string]any{
		"namespace": ns, "release": release, "revision": rev,
		"all": allRaw, "supplied": suppRaw,
	}
	if suppErr != nil {
		s.log.WarnContext(ctx, "supplied values unreadable",
			"namespace", ns, "release", release, "revision", rev, "err", suppErr)
		out["suppliedError"] = suppErr.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRevisionPlan answers with the plan that produced a revision, read from
// its archive.
//
// It is what the rollback page shows in place of the upgrade form: the settings
// that would come back, rendered read-only, because a rollback re-applies that
// plan verbatim. A form beside a rollback that could be edited would be an
// upgrade wearing a rollback's name.
//
// Read-only, so it is not behind allowDeploy -- it reads a Secret and composes
// nothing.
func (s *Server) handleRevisionPlan(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()

	ns, release := r.PathValue("namespace"), r.PathValue("release")
	rev, err := strconv.Atoi(r.PathValue("revision"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "revision must be a number")
		return
	}
	// The live revision has no archive: its plan is the one beside the release,
	// and it is archived only when the next apply overwrites it.
	if st, err := exec.Lookup(ctx, s.probe, ns, release); err == nil && st.Exists && st.Revision == rev {
		if p, err := s.currentPlan(ctx, ns, release); err == nil {
			writeJSON(w, http.StatusOK, p)
			return
		}
	}
	p, err := s.archivedPlan(ctx, ns, release, rev)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// handleRevisionDiff renders what rolling back to a revision would change,
// against the release as it is now. Optional, like every other diff here -- and
// like every other diff it carries back the live revision it saw, which is what
// a rollback then asserts has not moved.
func (s *Server) handleRevisionDiff(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 5*time.Minute)
	defer cancel()

	ns, release := r.PathValue("namespace"), r.PathValue("release")
	rev, err := strconv.Atoi(r.PathValue("revision"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "revision must be a number")
		return
	}
	p, err := s.archivedPlan(ctx, ns, release, rev)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	st, err := exec.Lookup(ctx, s.probe, ns, release)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	// Not audited, like every other diff: it changes nothing, and the rollback
	// that follows carries the diff in its own output.
	res, err := s.runner().Diff(ctx, p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"planHash":   p.Hash,
		"toRevision": rev,
		"changed":    res.Changed,
		"output":     res.Output,
		// Carry these back so the rollback can assert nothing moved in between.
		"revision": st.Revision,
		"exists":   st.Exists,
	})
}

// handleRollback re-applies an archived plan as a new revision.
//
// Forward, not backward: helm's own rollback creates a new revision too, and
// anything else would leave the audit log and the plan beside the release
// describing something other than what is running.
//
// It re-applies rather than recomposes, so the old image, chart version and
// engine flags come back verbatim -- which is the point. Going back to what
// worked must not mean going to whatever the catalog now says that version is.
func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Minute)
	defer cancel()

	var req rollbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ToRevision <= 0 {
		writeError(w, http.StatusBadRequest, "toRevision is required")
		return
	}

	ns, release := r.PathValue("namespace"), r.PathValue("release")
	p, err := s.archivedPlan(ctx, ns, release, req.ToRevision)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if req.ExpectRevision == 0 {
		writeError(w, http.StatusBadRequest,
			"expectRevision is required: send the revision you were looking at, so a rollback cannot overwrite an apply that landed in between")
		return
	}
	// No force here: a rollback restores a plan that was applied cleanly once,
	// and taking fields from another manager during one would be a second
	// surprise on top of the one being undone.
	s.applyPlan(ctx, w, p, exec.Upgrade, req.ExpectRevision, "rollback", req.Note, false)
}

// archivedPlan reads the workspace that produced a revision, and checks it
// still hashes to what it claims. Those bytes have been sitting in etcd.
func (s *Server) archivedPlan(ctx context.Context, ns, release string, rev int) (*plan.Plan, error) {
	b, err := s.readBackend(ctx, ns, release)
	if err != nil {
		return nil, err
	}
	return b.Archived(ctx, ns, release, rev)
}

// planSecret reads the workspace that produced a revision, and checks it
// still hashes to what it claims. Those bytes have been sitting in etcd.
func (s *Server) planSecret(ctx context.Context, ns, release string, revision int) (*plan.Plan, error) {
	data, err := s.probe.Secret(ctx, archiveRef(ns, release, revision))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("no archived plan for %s/%s revision %d", ns, release, revision)
	}
	p, err := plan.FromFiles(data)
	if err != nil {
		return nil, err
	}
	if err := p.VerifyHash(); err != nil {
		return nil, fmt.Errorf("revision %d: %w", revision, err)
	}
	return p, nil
}
