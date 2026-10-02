package chart

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func spec(t *testing.T, s string) Spec {
	t.Helper()
	sp, err := ParseSpec(s)
	if err != nil {
		t.Fatal(err)
	}
	return sp
}

// A bare version is that version and nothing else, as before ranges.
func TestABareVersionIsExact(t *testing.T) {
	sp := spec(t, "0.7.1")
	if sp.Exact() != "0.7.1" {
		t.Errorf("Exact = %q, want 0.7.1", sp.Exact())
	}
	if sp.Allows("0.7.2") || !sp.Allows("0.7.1") {
		t.Error("0.7.1 must allow 0.7.1 and only it")
	}
	if spec(t, ">=0.7.1").Exact() != "" {
		t.Error("a range names no one version")
	}
}

func TestParseSpecRefusesWhatIsNotAVersion(t *testing.T) {
	for _, s := range []string{"", "latest", ">=banana"} {
		if _, err := ParseSpec(s); err == nil {
			t.Errorf("%q: want an error", s)
		}
	}
}

func TestMatchIsNewestFirstAndSkipsNonVersions(t *testing.T) {
	got := spec(t, ">=0.7.1").Match([]string{"0.7.0", "0.7.1", "0.10.0", "latest", "0.8.0", "0.9.0-rc1", "0.8.0"})
	want := []string{"0.10.0", "0.8.0", "0.7.1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v -- newest first, no prerelease, no duplicates", got, want)
	}
	if got := spec(t, "^0.7.1").Match([]string{"0.7.1", "0.7.9", "0.8.0"}); !reflect.DeepEqual(got, []string{"0.7.9", "0.7.1"}) {
		t.Errorf("^0.7.1 matched %v, want 0.7.x only", got)
	}
}

func list(vs ...string) Lister {
	return func(context.Context) ([]string, error) { return vs, nil }
}

func noList(t *testing.T) Lister {
	return func(context.Context) ([]string, error) {
		t.Error("the repository must not be read")
		return nil, errors.New("read")
	}
}

func TestResolve(t *testing.T) {
	ctx := t.Context()
	for _, tc := range []struct {
		name, spec, want, keep, got string
		list                        Lister
	}{
		{name: "named, in range", spec: ">=0.7.1", want: "0.7.3", got: "0.7.3", list: noList(t)},
		{name: "exact needs no registry", spec: "0.7.1", got: "0.7.1", list: noList(t)},
		{name: "exact moves a running one off", spec: "0.7.2", keep: "0.7.1", got: "0.7.2", list: noList(t)},
		{name: "running one kept while in range", spec: ">=0.7.1", keep: "0.7.2", got: "0.7.2", list: noList(t)},
		{name: "running one out of range", spec: ">=0.8.0", keep: "0.7.2", got: "0.9.0", list: list("0.7.2", "0.8.0", "0.9.0")},
		{name: "newest in range", spec: ">=0.7.1 <0.9.0", got: "0.8.1", list: list("0.7.1", "0.8.1", "0.9.0")},
	} {
		got, err := Resolve(ctx, spec(t, tc.spec), tc.want, tc.keep, tc.list)
		if err != nil || got != tc.got {
			t.Errorf("%s: got %q, %v; want %q", tc.name, got, err, tc.got)
		}
	}
}

func TestResolveRefuses(t *testing.T) {
	ctx := t.Context()
	if _, err := Resolve(ctx, spec(t, ">=0.7.1"), "0.6.0", "", noList(t)); err == nil || !strings.Contains(err.Error(), "not in the catalog's") {
		t.Errorf("a named version out of range: %v", err)
	}
	if _, err := Resolve(ctx, spec(t, ">=0.7.1"), "", "", list("0.6.0")); err == nil || !strings.Contains(err.Error(), "no chart version satisfies") {
		t.Errorf("nothing in range: %v", err)
	}
	if _, err := Resolve(ctx, spec(t, ">=0.7.1"), "", "", nil); err == nil || !strings.Contains(err.Error(), "Name a chart version") {
		t.Errorf("a range with nowhere to list: %v", err)
	}
}
