package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/testutil/cases"
)

type autoscaleAPICase struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Decision    int             `json:"decision,omitempty"`
	NoDecision  bool            `json:"noDecision,omitempty"`
	HelmStatus  string          `json:"helmStatus,omitempty"`
	SLO         *bool           `json:"slo,omitempty"`
	PD          *bool           `json:"pd,omitempty"`
	Slices      int             `json:"slices,omitempty"`
	Extra       *autoscaleExtra `json:"extra,omitempty"`
	Unknown     bool            `json:"unknown,omitempty"`
	WantOutcome string          `json:"wantOutcome,omitempty"`
	WantHold    string          `json:"wantHold,omitempty"`
	WantWant    *int            `json:"wantWant,omitempty"`
	WantCurrent *int            `json:"wantCurrent,omitempty"`
	WantEnabled *bool           `json:"wantEnabled,omitempty"`
	WantList    []string        `json:"wantList,omitempty"`
	WantStatus  int             `json:"wantStatus,omitempty"`
}

type autoscaleExtra struct {
	Release string `json:"release"`
	PD      *bool  `json:"pd,omitempty"`
	SLO     *bool  `json:"slo,omitempty"`
}

func TestAutoscaleStateAPI(t *testing.T) {
	cases.Run(t, "testdata/autoscale-state", func(t *testing.T, tc autoscaleAPICase) {
		runAutoscaleStateCase(t, tc)
	})
}

func runAutoscaleStateCase(t *testing.T, tc autoscaleAPICase) {
	t.Helper()
	const ns, release = "models", "pd"
	probe := fakeProbe()
	if !tc.Unknown {
		var decURL string
		if tc.NoDecision {
			dec, _ := countingDecisions(t, ns, release, 2, true)
			decURL = dec.URL
		} else {
			if tc.Decision == 0 {
				t.Fatal("decision is required")
			}
			decURL = decisionServer(t, ns, release, tc.Decision).URL
		}
		setDecisionProfile(probe, decURL)
		n := tc.Slices
		if n == 0 {
			n = 1
		}
		slo := true
		if tc.SLO != nil {
			slo = *tc.SLO
		}
		seedAutoscaleRelease(t, &probe, ns, release, n, boolOr(tc.PD, true), boolPtr(slo), tc.HelmStatus)
		if tc.Extra != nil {
			if tc.Extra.Release == "" {
				t.Fatal("extra.release is empty")
			}
			seedAutoscaleRelease(t, &probe, ns, tc.Extra.Release, 1, boolOr(tc.Extra.PD, false), tc.Extra.SLO, "")
		}
	}

	logPath := filepath.Join(t.TempDir(), "helmfile.log")
	bin := countBin(t, logPath)
	s, _ := newLoopServer(t, probe, bin, bin)
	s.autoscaleTick(t.Context())
	h := s.Handler()

	if tc.WantStatus == 0 {
		tc.WantStatus = http.StatusOK
	}
	code, body := autoscaleGET(t, h, "/api/releases/"+ns+"/"+release+"/autoscaling")
	if code != tc.WantStatus {
		t.Fatalf("status %d, want %d: %v", code, tc.WantStatus, body)
	}
	if tc.WantStatus == http.StatusNotFound {
		if body["error"] != "no such release" {
			t.Fatalf("error %v", body["error"])
		}
	}
	if tc.WantStatus == http.StatusOK {
		if body["namespace"] != ns || body["release"] != release {
			t.Fatalf("identity %v", body)
		}
		if tc.WantOutcome == "" {
			if o, ok := body["outcome"]; ok {
				t.Fatalf("outcome %v", o)
			}
		} else if body["outcome"] != tc.WantOutcome {
			t.Fatalf("outcome %v, want %q", body["outcome"], tc.WantOutcome)
		}
		if tc.WantHold == "" {
			if hold, ok := body["hold"]; ok {
				t.Fatalf("hold %v", hold)
			}
		} else if body["hold"] != tc.WantHold {
			t.Fatalf("hold %v, want %q", body["hold"], tc.WantHold)
		}
		if tc.WantWant == nil {
			if w, ok := body["want"]; ok {
				t.Fatalf("want %v", w)
			}
		} else if jsonInt(body["want"]) != *tc.WantWant {
			t.Fatalf("want %v, want %d", body["want"], *tc.WantWant)
		}
		if tc.WantCurrent != nil && jsonInt(body["current"]) != *tc.WantCurrent {
			t.Fatalf("current %v, want %d", body["current"], *tc.WantCurrent)
		}
		if tc.WantEnabled != nil && body["enabled"] != *tc.WantEnabled {
			t.Fatalf("enabled %v, want %v", body["enabled"], *tc.WantEnabled)
		}
		if tc.WantOutcome != "" {
			if tc.NoDecision {
				if body["noDecision"] != true {
					t.Fatalf("noDecision %v", body["noDecision"])
				}
				if _, ok := body["decision"]; ok {
					t.Fatalf("decision %v", body["decision"])
				}
			} else if jsonInt(body["decision"]) != tc.Decision {
				t.Fatalf("decision %v, want %d", body["decision"], tc.Decision)
			}
			assertTimestamp(t, body["lastEvaluated"])
		} else if _, ok := body["lastEvaluated"]; ok {
			t.Fatalf("lastEvaluated %v", body["lastEvaluated"])
		}
		scaled := tc.WantOutcome == outcomeScaledUp || tc.WantOutcome == outcomeScaledDown
		if scaled {
			assertTimestamp(t, body["lastScaled"])
		} else if _, ok := body["lastScaled"]; ok {
			t.Fatalf("lastScaled %v", body["lastScaled"])
		}
	}

	if tc.Extra != nil {
		extraCode, extra := autoscaleGET(t, h, "/api/releases/"+ns+"/"+tc.Extra.Release+"/autoscaling")
		if extraCode != http.StatusOK || extra["enabled"] != false || extra["release"] != tc.Extra.Release {
			t.Fatalf("extra %d %v", extraCode, extra)
		}
		if _, ok := extra["outcome"]; ok {
			t.Fatalf("extra outcome %v", extra["outcome"])
		}
		if jsonInt(extra["current"]) != 1 {
			t.Fatalf("extra current %v", extra["current"])
		}
	}

	listCode, list := autoscaleGET(t, h, "/api/autoscaling")
	if listCode != http.StatusOK {
		t.Fatalf("list %d %v", listCode, list)
	}
	if names := autoscaleReleaseNames(t, list); !slices.Equal(names, tc.WantList) {
		t.Fatalf("list %v, want %v", names, tc.WantList)
	}
}

func seedAutoscaleRelease(t *testing.T, probe *cluster.Fake, ns, release string, replicas int, pd bool, slo *bool, helmStatus string) {
	t.Helper()
	_, seed := pdPlan(t, ns, release, release, replicas, pd, false, slo, "")
	if probe.Maps == nil {
		probe.Maps = map[string]map[string]string{}
	}
	probe.Maps[planRef(ns, release)] = seed
	if helmStatus == "" {
		helmStatus = "deployed"
	}
	probe.Rel = append(probe.Rel, cluster.Release{
		Name: release, Namespace: ns, Status: helmStatus, Revision: 2,
		Chart: "sglang-0.7.0", SwissFiles: seed,
	})
	if probe.Deps == nil {
		probe.Deps = map[string]cluster.Deployment{}
	}
	for k, v := range readyRouters(ns, release, replicas) {
		probe.Deps[k] = v
	}
	if probe.DSs == nil {
		probe.DSs = map[string]cluster.DisaggregatedSet{}
	}
	probe.DSs[ns+"/"+release] = readyDS(replicas)
}

func autoscaleGET(t *testing.T, h http.Handler, path string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: %d not JSON: %s", path, rec.Code, rec.Body.Bytes())
	}
	return rec.Code, out
}

func autoscaleReleaseNames(t *testing.T, body map[string]any) []string {
	t.Helper()
	raw, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("items %T %v", body["items"], body)
	}
	names := make([]string, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("item %T", item)
		}
		name, _ := m["release"].(string)
		if m["enabled"] != true {
			t.Fatalf("list entry not enabled: %v", m)
		}
		names = append(names, name)
	}
	return names
}

func assertTimestamp(t *testing.T, v any) {
	t.Helper()
	s, ok := v.(string)
	if !ok {
		t.Fatalf("timestamp %v", v)
	}
	if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
		t.Fatalf("timestamp %q: %v", s, err)
	}
}

func TestAutoscaleStateOrderAndLastScaled(t *testing.T) {
	when := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	scaled := when.Add(-time.Minute)
	s := &Server{now: func() time.Time { return when }}
	s.recordAutoscale(autoscaleState{Namespace: "b", Release: "z", Enabled: true, Outcome: outcomeHeld})
	s.recordAutoscale(autoscaleState{
		Namespace: "a", Release: "m", Enabled: true, Outcome: outcomeScaledUp, Current: 1, LastScaled: &scaled,
	})
	s.recordAutoscale(autoscaleState{Namespace: "a", Release: "a", Enabled: false, Outcome: outcomeUnchanged})
	s.recordAutoscale(autoscaleState{Namespace: "a", Release: "m", Enabled: true, Outcome: outcomeUnchanged, Current: 2})

	got := s.autoscaleStates()
	if len(got) != 2 || got[0].Namespace != "a" || got[0].Release != "m" || got[1].Namespace != "b" || got[1].Release != "z" {
		t.Fatalf("order %+v", got)
	}
	if got[0].Outcome != outcomeUnchanged || got[0].Current != 2 {
		t.Fatalf("carried pass %+v", got[0])
	}
	if got[0].LastScaled == nil || !got[0].LastScaled.Equal(scaled) {
		t.Fatalf("lastScaled %+v", got[0].LastScaled)
	}
	if got[0].LastEvaluated == nil || !got[0].LastEvaluated.Equal(when) {
		t.Fatalf("lastEvaluated %+v", got[0].LastEvaluated)
	}
	s.pruneAutoscale(map[string]bool{autoscaleKey("a", "m"): true})
	got = s.autoscaleStates()
	if len(got) != 1 || got[0].Release != "m" {
		t.Fatalf("after prune %+v", got)
	}
	s.dropAutoscale(autoscaleKey("a", "m"))
	if _, ok := s.autoscaleStateOf("a", "m"); ok || len(s.autoscaleStates()) != 0 {
		t.Fatal("drop left an entry")
	}
}

// Release fails on the second tick while the release stays in the managed
// set, so the pass must keep the entry instead of pruning it.
func TestAutoscaleReadErrorKeepsLastScale(t *testing.T) {
	const ns, release = "models", "pd"
	dec := decisionServer(t, ns, release, 2)
	probe := &releaseErrProbe{Fake: fakeProbe()}
	setDecisionProfile(probe.Fake, dec.URL)
	seedAutoscaleRelease(t, &probe.Fake, ns, release, 1, true, boolPtr(true), "deployed")
	logPath := filepath.Join(t.TempDir(), "helmfile.log")
	bin := countBin(t, logPath)
	s, _ := newLoopServer(t, probe, bin, bin)
	s.autoscaleTick(t.Context())
	st, ok := s.autoscaleStateOf(ns, release)
	if !ok || st.Outcome != outcomeScaledUp || st.LastScaled == nil {
		t.Fatalf("after scale %+v ok %v", st, ok)
	}
	scaled := *st.LastScaled
	probe.err = errors.New("boom")
	s.autoscaleTick(t.Context())
	st, ok = s.autoscaleStateOf(ns, release)
	if !ok {
		t.Fatal("read error dropped the entry")
	}
	if st.Outcome != outcomeError || st.Error != "boom" || st.LastScaled == nil || !st.LastScaled.Equal(scaled) {
		t.Fatalf("state %+v", st)
	}
	code, body := autoscaleGET(t, s.Handler(), "/api/releases/models/pd/autoscaling")
	if code != http.StatusOK || body["outcome"] != outcomeError || body["error"] != "boom" {
		t.Fatalf("get %d %v", code, body)
	}
}

// The plan stops autoscaling between passes. The cached pass is dropped.
func TestAutoscaleTickDropsWhenSLOTurnsOff(t *testing.T) {
	const ns, release = "models", "pd"
	dec := decisionServer(t, ns, release, 2)
	probe := fakeProbe()
	setDecisionProfile(probe, dec.URL)
	seedAutoscaleRelease(t, &probe, ns, release, 1, true, boolPtr(true), "deployed")
	logPath := filepath.Join(t.TempDir(), "helmfile.log")
	bin := countBin(t, logPath)
	s, _ := newLoopServer(t, &probe, bin, bin)
	s.autoscaleTick(t.Context())
	if st, ok := s.autoscaleStateOf(ns, release); !ok || st.Outcome != outcomeScaledUp {
		t.Fatalf("scaled %+v ok %v", st, ok)
	}
	_, off := pdPlan(t, ns, release, release, 1, true, false, boolPtr(false), "")
	probe.Maps[planRef(ns, release)] = off
	probe.Rel[0].SwissFiles = off
	s.autoscaleTick(t.Context())
	if _, ok := s.autoscaleStateOf(ns, release); ok {
		t.Fatal("slo off left a cached pass")
	}
	code, body := autoscaleGET(t, s.Handler(), "/api/releases/models/pd/autoscaling")
	if code != http.StatusOK || body["enabled"] != false || body["outcome"] != nil || jsonInt(body["current"]) != 1 {
		t.Fatalf("cold read %d %v", code, body)
	}
	code, body = autoscaleGET(t, s.Handler(), "/api/autoscaling")
	if code != http.StatusOK || len(autoscaleReleaseNames(t, body)) != 0 {
		t.Fatalf("list %d %v", code, body)
	}
}

func TestAutoscalingRequiresLogin(t *testing.T) {
	srv, _, c := loginServer(t, fakeProbe())
	if code, _ := do(t, c, http.MethodGet, srv.URL+"/api/autoscaling", nil); code != http.StatusUnauthorized {
		t.Fatalf("list status %d", code)
	}
	if code, _ := do(t, c, http.MethodGet, srv.URL+"/api/releases/models/pd/autoscaling", nil); code != http.StatusUnauthorized {
		t.Fatalf("get status %d", code)
	}
	login(t, srv, c)
	code, body := do(t, c, http.MethodGet, srv.URL+"/api/autoscaling", nil)
	if code != http.StatusOK {
		t.Fatalf("list after login %d %v", code, body)
	}
	if _, ok := body["items"].([]any); !ok {
		t.Fatalf("items %v", body)
	}
	code, body = do(t, c, http.MethodGet, srv.URL+"/api/releases/models/pd/autoscaling", nil)
	if code != http.StatusNotFound || body["error"] != "no such release" {
		t.Fatalf("missing release %d %v", code, body)
	}
}

// releaseErrProbe fails Release once err is set. The managed set still comes
// from the embedded fake, so the tick still visits the release.
type releaseErrProbe struct {
	cluster.Fake
	err error
}

func (p *releaseErrProbe) Release(ctx context.Context, ns, name string) (*cluster.Release, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.Fake.Release(ctx, ns, name)
}
