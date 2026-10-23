package server

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
)

// errApplyBusy is the per-release lock being held. errors.Is matches it
// through applyBusyError. The HTTP status is 409.
var errApplyBusy = errors.New("apply in progress")

type applyBusyError struct {
	ns, release string
}

func (e *applyBusyError) Error() string {
	return fmt.Sprintf("release %s/%s has an apply in progress", e.ns, e.release)
}

func (e *applyBusyError) Unwrap() error { return errApplyBusy }

func (e *applyBusyError) As(target any) bool {
	hf, ok := target.(**httpFailure)
	if !ok {
		return false
	}
	*hf = &httpFailure{code: http.StatusConflict, msg: e.Error()}
	return true
}

func (s *Server) applyMu(ns, release string) *sync.Mutex {
	s.locksMu.Lock()
	defer s.locksMu.Unlock()
	if s.locks == nil {
		s.locks = map[string]*sync.Mutex{}
	}
	key := ns + "/" + release
	m := s.locks[key]
	if m == nil {
		m = &sync.Mutex{}
		s.locks[key] = m
	}
	return m
}

func (s *Server) tryApplyLock(ns, release string) bool {
	return s.applyMu(ns, release).TryLock()
}

func (s *Server) unlockApply(ns, release string) {
	s.applyMu(ns, release).Unlock()
}
