package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/aceforeverd/swiss/internal/catalog"
	"github.com/aceforeverd/swiss/internal/cluster"
	"github.com/aceforeverd/swiss/internal/compose"
	"github.com/aceforeverd/swiss/internal/exec"
	"github.com/aceforeverd/swiss/internal/plan"
	"github.com/aceforeverd/swiss/internal/store"
	"github.com/aceforeverd/swiss/internal/values"
)

type planRequest struct {
	Model     string      `json:"model"`
	Variant   string      `json:"variant,omitempty"`
	Release   string      `json:"release,omitempty"`
	Namespace string      `json:"namespace,omitempty"`
	Overrides values.Tree `json:"overrides,omitempty"`
}

type applyRequest struct {
	PlanHash string `json:"planHash"`
	// ExpectRevision is the live helm revision the diff was computed against.
	ExpectRevision int `json:"expectRevision,omitempty"`
	// ExpectVersion is the deployment row the caller read.
	ExpectVersion int `json:"expectVersion,omitempty"`
}

func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()

	var req planRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := s.compose(ctx, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.store != nil {
		if err := s.store.PutPlan(ctx, s.cfg.Cluster.Name, p); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) compose(ctx context.Context, req planRequest) (*plan.Plan, error) {
	if req.Model == "" {
		return nil, fmt.Errorf("model is required")
	}
	cat, err := s.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	entry, err := cat.Entry(ctx, req.Model)
	if err != nil {
		return nil, err
	}
	var v catalog.Variant
	if req.Variant == "" {
		v, err = entry.DefaultVariant()
	} else {
		v, err = entry.Variant(req.Variant)
	}
	if err != nil {
		return nil, err
	}
	prof, err := s.Profile(ctx)
	if err != nil {
		return nil, err
	}
	release := req.Release
	if release == "" {
		release = entry.Name
	}
	return compose.Compose(compose.Input{
		Catalog:   cat.Fetcher.String(),
		Ref:       cat.Ref,
		Entry:     entry,
		Variant:   v,
		Profile:   *prof,
		Release:   release,
		Namespace: req.Namespace,
		Overrides: req.Overrides,
	})
}

func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 5*time.Minute)
	defer cancel()

	p, err := s.planFromRequest(ctx, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	st, err := exec.Lookup(ctx, s.probe, p.Release.Namespace, p.Release.Name)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	started := time.Now()
	res, err := s.runner().Diff(ctx, p)
	s.record(ctx, "diff", p, res, err, started)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"planHash": p.Hash,
		"changed":  res.Changed,
		"output":   res.Output,
		// Carry these back so apply can assert nothing moved in between.
		"revision": st.Revision,
		"exists":   st.Exists,
	})
}

func (s *Server) handleApply(mode exec.Mode) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := contextWithTimeout(r, 15*time.Minute)
		defer cancel()

		var req applyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if s.store == nil {
			writeError(w, http.StatusServiceUnavailable, "no database: apply needs a stored plan")
			return
		}
		p, err := s.store.Plan(ctx, req.PlanHash)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}

		st, err := exec.Lookup(ctx, s.probe, p.Release.Namespace, p.Release.Name)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		if err := exec.Check(st, mode, p); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if err := exec.CheckRevision(st, req.ExpectRevision); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}

		started := time.Now()
		res, err := s.runner().Apply(ctx, p)
		s.record(ctx, actionName(mode), p, res, err, started)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		// The plan goes beside the release, so the cluster describes itself and
		// the database can be rebuilt from it.
		var planErr string
		if err := s.writePlan(ctx, p); err != nil {
			planErr = err.Error()
			s.log.ErrorContext(ctx, "plan configmap not written", "release", p.Release.Name, "err", err)
		}

		after, _ := exec.Lookup(ctx, s.probe, p.Release.Namespace, p.Release.Name)
		if err := s.store.RecordApply(ctx, store.Deployment{
			Cluster: s.cfg.Cluster.Name, Namespace: p.Release.Namespace,
			Release: p.Release.Name, PlanHash: p.Hash, Revision: after.Revision,
		}, req.ExpectVersion); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"planHash":           p.Hash,
			"release":            p.Release.Name,
			"revision":           after.Revision,
			"output":             res.Output,
			"planConfigMapError": planErr,
			// Deliberately not waiting: a cold load is 20-40 minutes.
			"status": "submitted",
		})
	}
}

func (s *Server) writePlan(ctx context.Context, p *plan.Plan) error {
	if s.writer == nil {
		return fmt.Errorf("no cluster writer")
	}
	doc, err := p.YAML()
	if err != nil {
		return err
	}
	ref := p.Release.Namespace + "/" + cluster.PlanConfigMapPrefix + p.Release.Name
	return s.writer.PutConfigMap(ctx, ref, map[string]string{"plan.yaml": string(doc)})
}

func (s *Server) planFromRequest(ctx context.Context, r *http.Request) (*plan.Plan, error) {
	var body struct {
		planRequest
		PlanHash string `json:"planHash,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.PlanHash != "" {
		if s.store == nil {
			return nil, fmt.Errorf("no database: pass a model instead of a planHash")
		}
		return s.store.Plan(ctx, body.PlanHash)
	}
	return s.compose(ctx, body.planRequest)
}

func (s *Server) runner() exec.Runner {
	return exec.Runner{HelmBin: s.cfg.Server.HelmBin, HelmfileBin: s.cfg.Server.HelmfileBin}
}

func (s *Server) record(ctx context.Context, action string, p *plan.Plan, res exec.Result, err error, started time.Time) {
	if s.store == nil {
		return
	}
	run := store.Run{
		Cluster: s.cfg.Cluster.Name, Namespace: p.Release.Namespace, Release: p.Release.Name,
		Action: action, PlanHash: p.Hash, Changed: res.Changed, Output: res.Output,
		StartedAt: started.UTC().Format(time.RFC3339), EndedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err != nil {
		run.Error = err.Error()
	}
	if _, e := s.store.RecordRun(ctx, run); e != nil {
		s.log.ErrorContext(ctx, "run not recorded", "err", e)
	}
}

func actionName(m exec.Mode) string {
	if m == exec.Install {
		return "install"
	}
	return "apply"
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSON(w, http.StatusOK, map[string]any{"runs": []any{}})
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	runs, err := s.store.Runs(ctx, s.cfg.Cluster.Name, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}
