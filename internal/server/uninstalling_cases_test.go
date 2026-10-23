package server

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestUninstallingCases(t *testing.T) {
	paths, err := filepath.Glob("testdata/uninstalling/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no uninstalling cases under testdata/uninstalling")
	}
	sort.Strings(paths)
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		t.Run(name, func(t *testing.T) {
			runUninstallingCase(t, path)
		})
	}
}
