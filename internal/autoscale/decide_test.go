package autoscale

import (
	"testing"
	"time"

	"github.com/modelsphere/swiss/internal/testutil/cases"
)

type decideCase struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Input       decideInput    `json:"input"`
	Expected    decideExpected `json:"expected"`
}

type decideInput struct {
	Live     Live     `json:"live"`
	Decision Decision `json:"decision"`
	TimedOut bool     `json:"timedOut,omitempty"`
}

// decideExpected is the Result. WantPD and CurrentPD omitted are nil.
type decideExpected struct {
	Hold        string   `json:"hold"`
	TimedOut    string   `json:"timedOut,omitempty"`
	Scale       bool     `json:"scale"`
	Current     int      `json:"current"`
	Want        int      `json:"want"`
	HasDecision bool     `json:"hasDecision"`
	WantPD      *Engines `json:"wantPD,omitempty"`
	CurrentPD   *Engines `json:"currentPD,omitempty"`
}

func TestDecide(t *testing.T) {
	cases.Run(t, "testdata/decide", func(t *testing.T, tc decideCase) {
		in, exp := tc.Input, tc.Expected
		got := Decide(in.Live, in.Decision, in.TimedOut)
		if got.HoldReason != exp.Hold {
			t.Errorf("hold %q, want %q", got.HoldReason, exp.Hold)
		}
		if got.TimedOut != exp.TimedOut {
			t.Errorf("timedOut %q, want %q", got.TimedOut, exp.TimedOut)
		}
		if got.Scale != exp.Scale {
			t.Errorf("scale %v, want %v", got.Scale, exp.Scale)
		}
		if got.Current != exp.Current {
			t.Errorf("current %d, want %d", got.Current, exp.Current)
		}
		if got.Want != exp.Want {
			t.Errorf("want %d, want %d", got.Want, exp.Want)
		}
		if exp.HasDecision != (got.Decision != nil) {
			t.Errorf("decision present %v, want %v", got.Decision != nil, exp.HasDecision)
		}
		if got.Decision != nil && in.Decision.OK && *got.Decision != in.Decision.Replicas {
			t.Errorf("decision %d, want %d", *got.Decision, in.Decision.Replicas)
		}
		checkEngines(t, "wantPD", got.WantPD, exp.WantPD)
		checkEngines(t, "currentPD", got.CurrentPD, exp.CurrentPD)
		if in.Decision.OK && in.Decision.PD != nil {
			checkEngines(t, "decisionPD", got.DecisionPD, in.Decision.PD)
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
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Input       timeoutInput    `json:"input"`
	Expected    timeoutExpected `json:"expected"`
}

type timeoutInput struct {
	Live     Live     `json:"live"`
	Decision Decision `json:"decision"`
	Timeout  string   `json:"timeout,omitempty"`
	// DeferredFor is now minus deferredSince. Empty is a nil deferredSince.
	DeferredFor string `json:"deferredFor,omitempty"`
}

type timeoutExpected struct {
	Hold     string `json:"hold"`
	TimedOut string `json:"timedOut,omitempty"`
	Scale    bool   `json:"scale"`
	// DeferredFor is now minus the returned clock. Empty is nil.
	DeferredFor string `json:"deferredFor,omitempty"`
}

func TestDecideWithTimeout(t *testing.T) {
	cases.Run(t, "testdata/decide-timeout", func(t *testing.T, tc timeoutCase) {
		in, exp := tc.Input, tc.Expected
		now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
		var since *time.Time
		if in.DeferredFor != "" {
			s := now.Add(-parseDuration(t, in.DeferredFor))
			since = &s
		}
		var timeout time.Duration
		if in.Timeout != "" {
			timeout = parseDuration(t, in.Timeout)
		}
		got, clock := DecideWithTimeout(in.Live, in.Decision, since, now, timeout)
		if got.HoldReason != exp.Hold || got.TimedOut != exp.TimedOut || got.Scale != exp.Scale {
			t.Errorf("hold %q timedOut %q scale %v, want %q %q %v", got.HoldReason, got.TimedOut, got.Scale, exp.Hold, exp.TimedOut, exp.Scale)
		}
		switch {
		case exp.DeferredFor == "" && clock != nil:
			t.Errorf("deferred for %s, want nil", now.Sub(*clock))
		case exp.DeferredFor != "" && clock == nil:
			t.Errorf("deferredSince nil, want deferred for %s", exp.DeferredFor)
		case exp.DeferredFor != "" && now.Sub(*clock) != parseDuration(t, exp.DeferredFor):
			t.Errorf("deferred for %s, want %s", now.Sub(*clock), exp.DeferredFor)
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
