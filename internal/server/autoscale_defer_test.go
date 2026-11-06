package server

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/testutil/cases"
)

// deferCase runs its ticks in order, each with its own expected.
type deferCase struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Input       deferInput  `json:"input"`
	Ticks       []deferTick `json:"ticks"`
}

type deferInput struct {
	// DeferTimeoutSeconds is pdScale.deferTimeoutSeconds. Unset leaves the key out.
	DeferTimeoutSeconds *int   `json:"deferTimeoutSeconds,omitempty"`
	Decision            int    `json:"decision"`
	Slices              int    `json:"slices"`
	HelmStatus          string `json:"helmStatus,omitempty"`
	RoleNotReady        bool   `json:"roleNotReady,omitempty"`
	// DSBehind leaves the DisaggregatedSet observedGeneration a generation back.
	DSBehind bool `json:"dsBehind,omitempty"`
}

type deferTick struct {
	Advance  string            `json:"advance,omitempty"`
	Expected deferTickExpected `json:"expected"`
}

type deferTickExpected struct {
	Outcome  string `json:"outcome"`
	Hold     string `json:"hold,omitempty"`
	TimedOut string `json:"timedOut,omitempty"`
	// DeferredFor is the tick's time minus deferredSince. Empty is none.
	DeferredFor string `json:"deferredFor,omitempty"`
}

func TestAutoscaleDeferTimeout(t *testing.T) {
	cases.Run(t, "testdata/autoscale-defer", func(t *testing.T, tc deferCase) {
		const ns, release = "models", "pd"
		dec := decisionServer(t, ns, release, tc.Input.Decision)
		_, seed := pdPlan(t, ns, release, release, tc.Input.Slices, true, false, boolPtr(true), "")

		probe := fakeProbe()
		prof := profileYAML + "scaler:\n  serverAddress: " + dec.URL + "\n"
		if tc.Input.DeferTimeoutSeconds != nil {
			prof += fmt.Sprintf("pdScale:\n  deferTimeoutSeconds: %d\n", *tc.Input.DeferTimeoutSeconds)
		}
		probe.Maps["swiss/site-profile"]["profile.yaml"] = prof
		probe.Deps = readyRouters(ns, release, tc.Input.Slices)
		ds := readyDS(tc.Input.Slices)
		if tc.Input.RoleNotReady {
			ds.Roles[0].ReadyReplicas = 0
		}
		if tc.Input.DSBehind {
			ds.Generation++
		}
		probe.DSs = map[string]cluster.DisaggregatedSet{ns + "/" + release: ds}
		probe.Maps[planRef(ns, release)] = seed
		helmStatus := "deployed"
		if tc.Input.HelmStatus != "" {
			helmStatus = tc.Input.HelmStatus
		}
		probe.Rel = []cluster.Release{{
			Name: release, Namespace: ns, Status: helmStatus, Revision: 2,
			Chart: "sglang-0.7.0", SwissFiles: seed,
		}}

		logPath := filepath.Join(t.TempDir(), "helmfile.log")
		bin := countBin(t, logPath)
		s, w := newLoopServer(t, &revProbe{Fake: probe}, bin, bin)
		logs := captureLog(s)
		now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
		s.now = func() time.Time { return now }

		applies := 0
		for i, tick := range tc.Ticks {
			if tick.Advance != "" {
				now = now.Add(mustDuration(t, tick.Advance))
			}
			s.autoscaleTick(t.Context())
			st, ok := s.autoscaleStateOf(ns, release)
			if !ok {
				t.Fatalf("tick %d: no state", i)
			}
			if st.Outcome != tick.Expected.Outcome || st.Hold != tick.Expected.Hold || st.TimedOut != tick.Expected.TimedOut {
				t.Fatalf("tick %d: outcome %q hold %q timedOut %q, want %q %q %q (err %q) log=%s",
					i, st.Outcome, st.Hold, st.TimedOut, tick.Expected.Outcome, tick.Expected.Hold, tick.Expected.TimedOut, st.Error, logs.String())
			}
			switch {
			case tick.Expected.DeferredFor == "" && st.DeferredSince != nil:
				t.Fatalf("tick %d: deferredSince %v, want none", i, st.DeferredSince)
			case tick.Expected.DeferredFor != "" && st.DeferredSince == nil:
				t.Fatalf("tick %d: no deferredSince, want %s", i, tick.Expected.DeferredFor)
			case tick.Expected.DeferredFor != "" && now.Sub(*st.DeferredSince) != mustDuration(t, tick.Expected.DeferredFor):
				t.Fatalf("tick %d: deferred for %s, want %s", i, now.Sub(*st.DeferredSince), tick.Expected.DeferredFor)
			}
			if st.Outcome == outcomeScaledUp || st.Outcome == outcomeScaledDown {
				applies++
				note := fmt.Sprintf("autoscale %d -> %d (decision-gen)", st.Current, *st.Want)
				if st.TimedOut != "" {
					note = fmt.Sprintf("autoscale %d -> %d (decision-gen, deferral timed out: %s)", st.Current, *st.Want, st.TimedOut)
				}
				if raw := w.written[planRef(ns, release)]["status.yaml"]; !strings.Contains(raw, note) {
					t.Fatalf("tick %d: status note missing %q:\n%s", i, note, raw)
				}
			}
			if got := countLines(t, logPath); got != applies {
				t.Fatalf("tick %d: helmfile calls %d, want %d log=%s", i, got, applies, logs.String())
			}
		}
	})
}

func mustDuration(t *testing.T, s string) time.Duration {
	t.Helper()
	d, err := time.ParseDuration(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
