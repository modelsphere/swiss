package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/autoscale"
	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/testutil/cases"
	"gopkg.in/yaml.v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type statusCase struct {
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	Kind          string `json:"kind"`
	Mutate        string `json:"mutate,omitempty"`
	WantEnabled   *bool  `json:"wantEnabled,omitempty"`
	WantPresent   *bool  `json:"wantPresent,omitempty"`
	WantKeyWrites int    `json:"wantKeyWrites"`
	WantConflicts int    `json:"wantConflicts,omitempty"`
	WantVersion   string `json:"wantVersion,omitempty"`
	WantConfig    string `json:"wantConfig,omitempty"`
	WantApplies   *int   `json:"wantApplies,omitempty"`
	WantReads     *int   `json:"wantReads,omitempty"`
}

const (
	statusSeedConfig = "enabled: true\nminSlices: 1\nmaxSlices: 4\n"
	statusNote       = "keep"
	statusNoteKey    = "note"
)

func TestAutoscaleStatus(t *testing.T) {
	cases.Run(t, "testdata/autoscale-status", func(t *testing.T, tc statusCase) {
		switch tc.Kind {
		case "write":
			runStatusWrite(t, tc)
		case "tick":
			runStatusTick(t, tc)
		case "scale":
			runStatusScale(t, tc)
		default:
			t.Fatalf("kind %q", tc.Kind)
		}
	})
}

func runStatusWrite(t *testing.T, tc statusCase) {
	t.Helper()
	const ns, release = "models", "pd"
	ref := autoscaleRef(ns, release)
	f := &cluster.Fake{
		Maps: map[string]map[string]string{
			ref: {
				autoscaleConfigKey: statusSeedConfig,
				statusNoteKey:      statusNote,
			},
		},
		MapLabels: map[string]map[string]string{
			ref: {"owner": "swiss", "name": release},
		},
	}
	initMapVersions(f)
	s := New(testConfig("prod-b300"), f, discardLogger(), "test")
	w := &fakeWriter{cm: f}
	if tc.Mutate != "" {
		var once bool
		w.beforeKeyUpdate = func(ref string) {
			if once {
				return
			}
			once = true
			switch tc.Mutate {
			case "delete":
				delete(f.Maps, ref)
				delete(f.MapVersions, ref)
			case "disable":
				f.Maps[ref][autoscaleConfigKey] = "enabled: false\nminSlices: 1\nmaxSlices: 2\n"
				f.MapVersions[ref] = "2"
			default:
				t.Fatalf("mutate %q", tc.Mutate)
			}
		}
	}
	s.SetWriter(w)
	err := s.writeAutoscaleStatus(t.Context(), ns, release, func(st *autoscale.Status) {
		st.LastTickAt = "2026-01-01T00:00:00Z"
		st.Current = intPtr(1)
		st.Desired = intPtr(2)
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.keyUpdates != tc.WantKeyWrites {
		t.Fatalf("key updates %d, want %d", w.keyUpdates, tc.WantKeyWrites)
	}
	if w.conflicts != tc.WantConflicts {
		t.Fatalf("conflicts %d, want %d", w.conflicts, tc.WantConflicts)
	}
	assertStatusMap(t, f, ref, tc)
	if data := f.Maps[ref]; data != nil {
		if data[statusNoteKey] != statusNote {
			t.Fatalf("note key = %q", data[statusNoteKey])
		}
		if labels := f.MapLabels[ref]; labels["owner"] != "swiss" || labels["name"] != release {
			t.Fatalf("labels changed: %v", labels)
		}
		if tc.WantKeyWrites > 0 && data[autoscaleStatusKey] == "" {
			t.Fatal("status.yaml was not written")
		}
		if len(data) != 3 {
			t.Fatalf("keys %v, want config.yaml, status.yaml, note", mapKeysOf(data))
		}
	}
}

func runStatusTick(t *testing.T, tc statusCase) {
	t.Helper()
	const ns, release = "models", "pd"
	dec := decisionServer(t, ns, release, 1)
	_, seed := pdPlan(t, ns, release, release, 1, true, false, "")
	probe := fakeProbe()
	setDecisionProfile(probe, dec.URL)
	probe.MapLabels = map[string]map[string]string{}
	probe.Deps = readyRouters(ns, release, 1)
	probe.DSs = map[string]cluster.DisaggregatedSet{ns + "/" + release: readyDS(1)}
	seedAutoscaleCM(&probe, ns, release, true, 1, 4)
	probe.Maps[planRef(ns, release)] = seed
	probe.Rel = []cluster.Release{{
		Name: release, Namespace: ns, Status: "deployed", Revision: 2,
		Chart: "sglang-0.7.0", SwissFiles: seed,
	}}
	logPath := filepath.Join(t.TempDir(), "helmfile.log")
	bin := countBin(t, logPath)
	s, w := newLoopServer(t, &probe, bin, bin)
	s.autoscaleTick(t.Context())
	st := readAutoscaleStatus(t, w, autoscaleRef(ns, release))
	if st.LastTickAt == "" || st.LastDecision == nil || *st.LastDecision != 1 {
		t.Fatalf("first tick status %+v", st)
	}
	first := st.LastTickAt
	s.autoscaleTick(t.Context())
	st = readAutoscaleStatus(t, w, autoscaleRef(ns, release))
	if st.LastTickAt != first {
		t.Fatalf("lastTickAt changed from %s to %s", first, st.LastTickAt)
	}
	if w.keyUpdates != tc.WantKeyWrites {
		t.Fatalf("key updates %d, want %d", w.keyUpdates, tc.WantKeyWrites)
	}
	if tc.WantApplies != nil && countLines(t, logPath) != *tc.WantApplies {
		t.Fatalf("helmfile calls %d, want %d", countLines(t, logPath), *tc.WantApplies)
	}
}

func runStatusScale(t *testing.T, tc statusCase) {
	t.Helper()
	const ns, release = "models", "pd"
	dec := decisionServer(t, ns, release, 2)
	_, seed := pdPlan(t, ns, release, release, 1, true, false, "")
	base := fakeProbe()
	setDecisionProfile(base, dec.URL)
	base.MapLabels = map[string]map[string]string{}
	base.Deps = readyRouters(ns, release, 1)
	base.DSs = map[string]cluster.DisaggregatedSet{ns + "/" + release: readyDS(1)}
	seedAutoscaleCM(&base, ns, release, true, 1, 4)
	base.Maps[planRef(ns, release)] = seed
	base.Rel = []cluster.Release{{
		Name: release, Namespace: ns, Status: "deployed", Revision: 2,
		Chart: "sglang-0.7.0", SwissFiles: seed,
	}}
	gp := &gateProbe{Fake: base, mutate: tc.Mutate}
	logPath := filepath.Join(t.TempDir(), "helmfile.log")
	bin := countBin(t, logPath)
	s, w := newLoopServer(t, gp, bin, bin)
	gp.onMutate = func() {
		if s.tryApplyLock(ns, release) {
			s.unlockApply(ns, release)
			t.Errorf("re-check ran without the apply lock")
		}
	}
	s.autoscaleTick(t.Context())
	applies := countLines(t, logPath)
	if tc.WantApplies != nil && applies != *tc.WantApplies {
		t.Fatalf("helmfile calls %d, want %d", applies, *tc.WantApplies)
	}
	if tc.WantReads != nil && gp.reads != *tc.WantReads {
		t.Fatalf("autoscale config reads %d, want %d", gp.reads, *tc.WantReads)
	}
	if w.written[planRef(ns, release)] != nil {
		t.Fatal("scale wrote the plan ConfigMap")
	}
	assertStatusMap(t, &gp.Fake, autoscaleRef(ns, release), tc)
}

func assertStatusMap(t *testing.T, f *cluster.Fake, ref string, tc statusCase) {
	t.Helper()
	data := f.Maps[ref]
	present := data != nil
	if tc.WantPresent != nil && present != *tc.WantPresent {
		t.Fatalf("present %v, want %v", present, *tc.WantPresent)
	}
	if !present {
		if _, _, err := f.ConfigMapWithVersion(t.Context(), ref); !apierrors.IsNotFound(err) {
			t.Fatalf("deleted map is readable: %v", err)
		}
		return
	}
	if tc.WantVersion != "" && f.MapVersions[ref] != tc.WantVersion {
		t.Fatalf("resourceVersion %s, want %s", f.MapVersions[ref], tc.WantVersion)
	}
	if tc.WantConfig != "" && data[autoscaleConfigKey] != tc.WantConfig {
		t.Fatalf("config.yaml:\n%s\nwant:\n%s", data[autoscaleConfigKey], tc.WantConfig)
	}
	if tc.WantEnabled != nil {
		var cfg autoscale.Config
		if err := yaml.Unmarshal([]byte(data[autoscaleConfigKey]), &cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.Enabled != *tc.WantEnabled {
			t.Fatalf("enabled %v, want %v", cfg.Enabled, *tc.WantEnabled)
		}
	}
}

func mapKeysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// gateProbe flips the autoscale ConfigMap on the read that scaleTo does
// under the per-release lock. The first read is the tick's own.
type gateProbe struct {
	cluster.Fake
	mutate   string
	reads    int
	onMutate func()
}

func (p *gateProbe) ConfigMap(ctx context.Context, ref string) (map[string]string, error) {
	if _, name, err := cluster.SplitRef(ref); err == nil && strings.HasPrefix(name, autoscalePrefix) {
		p.reads++
		if p.reads >= 2 {
			if p.onMutate != nil {
				p.onMutate()
			}
			switch p.mutate {
			case "delete":
				delete(p.Maps, ref)
				return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, ref)
			case "disable":
				if p.Maps[ref] != nil {
					p.Maps[ref][autoscaleConfigKey] = "enabled: false\nminSlices: 1\nmaxSlices: 2\n"
				}
			}
		}
	}
	return p.Fake.ConfigMap(ctx, ref)
}
