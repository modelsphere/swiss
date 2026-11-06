package autoscale

import (
	"testing"
	"time"

	"github.com/modelsphere/swiss/internal/testutil/cases"
)

type decideCase struct {
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Live         Live     `json:"live"`
	Decision     Decision `json:"decision"`
	TimedOut     bool     `json:"timedOut,omitempty"`
	WantHold     string   `json:"wantHold"`
	WantScale    bool     `json:"wantScale"`
	WantCurrent  int      `json:"wantCurrent"`
	Want         int      `json:"want"`
	HasDecision  bool     `json:"hasDecision"`
	WantTimedOut string   `json:"wantTimedOut,omitempty"`
	// WantPD and WantCurrentPD are Result.WantPD and Result.CurrentPD; omitted is nil.
	WantPD        *Engines `json:"wantPD,omitempty"`
	WantCurrentPD *Engines `json:"wantCurrentPD,omitempty"`
}

func TestDecide(t *testing.T) {
	cases.Run(t, "testdata/decide", func(t *testing.T, tc decideCase) {
		got := Decide(tc.Live, tc.Decision, tc.TimedOut)
		if got.HoldReason != tc.WantHold {
			t.Errorf("hold %q, want %q", got.HoldReason, tc.WantHold)
		}
		if got.TimedOut != tc.WantTimedOut {
			t.Errorf("timedOut %q, want %q", got.TimedOut, tc.WantTimedOut)
		}
		if got.Scale != tc.WantScale {
			t.Errorf("scale %v, want %v", got.Scale, tc.WantScale)
		}
		if got.Current != tc.WantCurrent {
			t.Errorf("current %d, want %d", got.Current, tc.WantCurrent)
		}
		if got.Want != tc.Want {
			t.Errorf("want %d, want %d", got.Want, tc.Want)
		}
		if tc.HasDecision != (got.Decision != nil) {
			t.Errorf("decision present %v, want %v", got.Decision != nil, tc.HasDecision)
		}
		if got.Decision != nil && tc.Decision.OK && *got.Decision != tc.Decision.Replicas {
			t.Errorf("decision %d, want %d", *got.Decision, tc.Decision.Replicas)
		}
		checkEngines(t, "wantPD", got.WantPD, tc.WantPD)
		checkEngines(t, "currentPD", got.CurrentPD, tc.WantCurrentPD)
		if tc.Decision.OK && tc.Decision.PD != nil {
			checkEngines(t, "decisionPD", got.DecisionPD, tc.Decision.PD)
		} else if got.DecisionPD != nil {
			t.Errorf("decisionPD %+v, want nil", *got.DecisionPD)
		}
	})
}

func checkEngines(t *testing.T, name string, got, want *Engines) {
	t.Helper()
	switch {
	case got == nil && want == nil:
	case got == nil:
		t.Errorf("%s nil, want %+v", name, *want)
	case want == nil:
		t.Errorf("%s %+v, want nil", name, *got)
	case *got != *want:
		t.Errorf("%s %+v, want %+v", name, *got, *want)
	}
}

type timeoutCase struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Live        Live     `json:"live"`
	Decision    Decision `json:"decision"`
	Timeout     string   `json:"timeout,omitempty"`
	// DeferredFor is now minus deferredSince. Empty is a nil deferredSince.
	DeferredFor  string `json:"deferredFor,omitempty"`
	WantHold     string `json:"wantHold"`
	WantTimedOut string `json:"wantTimedOut,omitempty"`
	WantScale    bool   `json:"wantScale"`
	// WantDeferredFor is now minus the returned clock. Empty is nil.
	WantDeferredFor string `json:"wantDeferredFor,omitempty"`
}

func TestDecideWithTimeout(t *testing.T) {
	cases.Run(t, "testdata/decide-timeout", func(t *testing.T, tc timeoutCase) {
		now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
		var since *time.Time
		if tc.DeferredFor != "" {
			s := now.Add(-parseDuration(t, tc.DeferredFor))
			since = &s
		}
		var timeout time.Duration
		if tc.Timeout != "" {
			timeout = parseDuration(t, tc.Timeout)
		}
		got, clock := DecideWithTimeout(tc.Live, tc.Decision, since, now, timeout)
		if got.HoldReason != tc.WantHold || got.TimedOut != tc.WantTimedOut || got.Scale != tc.WantScale {
			t.Errorf("hold %q timedOut %q scale %v, want %q %q %v", got.HoldReason, got.TimedOut, got.Scale, tc.WantHold, tc.WantTimedOut, tc.WantScale)
		}
		switch {
		case tc.WantDeferredFor == "" && clock != nil:
			t.Errorf("deferred for %s, want nil", now.Sub(*clock))
		case tc.WantDeferredFor != "" && clock == nil:
			t.Errorf("deferredSince nil, want deferred for %s", tc.WantDeferredFor)
		case tc.WantDeferredFor != "" && now.Sub(*clock) != parseDuration(t, tc.WantDeferredFor):
			t.Errorf("deferred for %s, want %s", now.Sub(*clock), tc.WantDeferredFor)
		}
	})
}

func parseDuration(t *testing.T, s string) time.Duration {
	t.Helper()
	d, err := time.ParseDuration(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
