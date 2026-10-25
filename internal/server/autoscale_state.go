package server

import (
	"sort"
	"time"
)

const (
	outcomeScaledUp   = "scaled-up"
	outcomeScaledDown = "scaled-down"
	outcomeUnchanged  = "unchanged"
	outcomeNoDecision = "no-decision"
	outcomeHeld       = "held"
	outcomeError      = "error"
)

// autoscaleState is one release's last autoscaler pass. In memory, rebuilt
// by the loop, lost on restart.
type autoscaleState struct {
	Namespace     string     `json:"namespace"`
	Release       string     `json:"release"`
	Enabled       bool       `json:"enabled"`
	Current       int        `json:"current"`
	Want          *int       `json:"want,omitempty"`
	Decision      *int       `json:"decision,omitempty"`
	NoDecision    bool       `json:"noDecision,omitempty"`
	Outcome       string     `json:"outcome,omitempty"`
	Hold          string     `json:"hold,omitempty"`
	Error         string     `json:"error,omitempty"`
	LastEvaluated *time.Time `json:"lastEvaluated,omitempty"`
	// LastScaled is when this process applied a scale. Later passes keep it.
	LastScaled *time.Time `json:"lastScaled,omitempty"`
}

func autoscaleKey(ns, release string) string { return ns + "/" + release }

// recordAutoscale stores one pass. LastEvaluated is this process's clock.
// LastScaled is kept from the previous pass unless st sets it.
func (s *Server) recordAutoscale(st autoscaleState) {
	s.asMu.Lock()
	defer s.asMu.Unlock()
	if s.asState == nil {
		s.asState = map[string]autoscaleState{}
	}
	now := s.clock()
	st.LastEvaluated = &now
	key := autoscaleKey(st.Namespace, st.Release)
	if prev, ok := s.asState[key]; ok && st.LastScaled == nil {
		st.LastScaled = prev.LastScaled
	}
	s.asState[key] = st
}

func (s *Server) dropAutoscale(key string) {
	s.asMu.Lock()
	defer s.asMu.Unlock()
	delete(s.asState, key)
}

// pruneAutoscale drops entries this pass did not see.
func (s *Server) pruneAutoscale(seen map[string]bool) {
	s.asMu.Lock()
	defer s.asMu.Unlock()
	for k := range s.asState {
		if !seen[k] {
			delete(s.asState, k)
		}
	}
}

// autoscaleStates is the cached passes that are still enabled, sorted by
// namespace and release.
func (s *Server) autoscaleStates() []autoscaleState {
	s.asMu.Lock()
	defer s.asMu.Unlock()
	out := make([]autoscaleState, 0, len(s.asState))
	for _, st := range s.asState {
		if st.Enabled {
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Release < out[j].Release
	})
	return out
}

func (s *Server) autoscaleStateOf(ns, release string) (autoscaleState, bool) {
	s.asMu.Lock()
	defer s.asMu.Unlock()
	st, ok := s.asState[autoscaleKey(ns, release)]
	return st, ok
}
