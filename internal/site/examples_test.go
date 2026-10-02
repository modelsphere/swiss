package site

import (
	"path/filepath"
	"testing"
)

// The README points at these; a field the parser no longer knows makes swissd
// refuse the whole profile.
func TestExampleProfilesParse(t *testing.T) {
	files, err := filepath.Glob("../../examples/site-*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no example profiles found: %v", err)
	}
	for _, f := range files {
		if _, err := Load(f); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}
