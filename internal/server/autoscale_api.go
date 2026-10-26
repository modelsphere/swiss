package server

import (
	"net/http"
	"time"
)

// handleAutoscaling is the loop's last pass for one release. A release the
// loop has not recorded is answered from its plan, with no pass data.
func (s *Server) handleAutoscaling(w http.ResponseWriter, r *http.Request) {
	ns, release := r.PathValue("namespace"), r.PathValue("release")
	if st, ok := s.autoscaleStateOf(ns, release); ok {
		writeJSON(w, http.StatusOK, st)
		return
	}

	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	b, _, err := s.backendFor(ctx, ns, release)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if b == nil {
		writeError(w, http.StatusNotFound, "no such release")
		return
	}
	p, err := b.Current(ctx, ns, release)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, autoscaleState{
		Namespace: ns,
		Release:   release,
		Enabled:   autoscaleOn(p),
		Current:   planReplicaCount(p),
	})
}

// handleAutoscalingList is every cached pass. The loop only keeps releases
// it is scaling, so a release with the gate off is absent.
func (s *Server) handleAutoscalingList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": s.autoscaleStates()})
}
