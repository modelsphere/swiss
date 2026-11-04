package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/site"
)

// A stored profile from before nodes.gpus gets the well-known resources, once,
// at start, and reads as a valid profile after.
func TestStartMigratesAProfileWithoutGPUs(t *testing.T) {
	probe := liveProbe()
	probe.Maps["swiss/site-profile"] = map[string]string{"profile.yaml": "# ours\n" + profileHead}
	_, s, w := profileServer(t, probe)
	s.migrateProfile(context.Background())

	stored, ok := w.written["swiss/site-profile"]["profile.yaml"]
	if !ok {
		t.Fatalf("nothing was written; wrote %v", keys(w.written))
	}
	if !strings.Contains(stored, "# ours") {
		t.Errorf("the migration must keep the operator's comments:\n%s", stored)
	}
	p, err := site.Parse([]byte(stored), "migrated")
	if err != nil {
		t.Fatal(err)
	}
	if gpus := p.Nodes.GPUTable(); len(gpus) != 5 || gpus.Default("nvidia") != "nvidia.com/gpu" {
		t.Errorf("want the five well-known resources, got %+v", gpus)
	}
}

// Anything already there is the operator's, even when it does not parse; so is
// a profile that is not stored yet.
func TestStartLeavesAPresentOrMissingProfile(t *testing.T) {
	for name, stored := range map[string]map[string]string{
		"a table":    {"profile.yaml": profileYAML},
		"empty list": {"profile.yaml": profileHead + "nodes:\n  gpus: []\n"},
		"empty map":  {"profile.yaml": profileHead + "nodes:\n  gpus: {}\n"},
		"no profile": nil,
	} {
		t.Run(name, func(t *testing.T) {
			probe := liveProbe()
			probe.Maps["swiss/site-profile"] = stored
			_, s, w := profileServer(t, probe)
			s.migrateProfile(context.Background())
			if len(w.written) != 0 {
				t.Errorf("nothing should have been written: %v", w.written)
			}
		})
	}
}

// swissd does not write a profile it reads from a file: the operator adds
// nodes.gpus, and until then the profile reads as invalid.
func TestStartDoesNotWriteAProfileFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.yaml")
	if err := os.WriteFile(path, []byte(profileHead), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig("prod-b300")
	cfg.Cluster.Profile.ConfigMap, cfg.Cluster.Profile.File = "", path
	s := New(cfg, liveProbe(), discardLogger(), "test")
	w := &fakeWriter{}
	s.SetWriter(w)
	s.migrateProfile(context.Background())
	if len(w.written) != 0 {
		t.Errorf("nothing should have been written: %v", w.written)
	}
	if got, _ := os.ReadFile(path); string(got) != profileHead {
		t.Errorf("the file must be left as it was:\n%s", got)
	}
	if _, err := s.Profile(context.Background()); err == nil || !strings.Contains(err.Error(), "nodes.gpus is required") {
		t.Errorf("the profile must read as invalid until nodes.gpus is added: %v", err)
	}
}
