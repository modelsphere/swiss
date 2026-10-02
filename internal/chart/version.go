// Package chart resolves a catalog's chart.version, one version or a range, to
// the one version a plan records.
package chart

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// Spec is a catalog's chart.version in helm's constraint syntax:
//
//	0.7.1           exactly 0.7.1
//	>=0.7.1         0.7.1 or newer
//	^0.7.1, ~0.7.1  >=0.7.1 <0.8.0
//	>=0.7.1 <0.9.0  both
//
// Prereleases match only a range that names one, as in helm.
type Spec struct {
	raw   string
	c     *semver.Constraints
	exact *semver.Version
}

func ParseSpec(s string) (Spec, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Spec{}, fmt.Errorf("no chart version")
	}
	c, err := semver.NewConstraint(s)
	if err != nil {
		return Spec{}, fmt.Errorf("chart version %q is neither a version nor a range: %w", s, err)
	}
	sp := Spec{raw: s, c: c}
	if v, err := semver.StrictNewVersion(s); err == nil {
		sp.exact = v
	}
	return sp, nil
}

func (s Spec) String() string { return s.raw }

// Exact is the one version a bare spec names, or "" for a range.
func (s Spec) Exact() string {
	if s.exact == nil {
		return ""
	}
	return s.exact.Original()
}

func (s Spec) Allows(v string) bool {
	sv, err := semver.StrictNewVersion(v)
	return err == nil && s.c.Check(sv)
}

// Match is the versions in have that s allows, newest first. Tags that are not
// versions are skipped.
func (s Spec) Match(have []string) []string {
	var vs []*semver.Version
	seen := map[string]bool{}
	for _, h := range have {
		v, err := semver.StrictNewVersion(h)
		if err != nil || !s.c.Check(v) || seen[v.Original()] {
			continue
		}
		seen[v.Original()] = true
		vs = append(vs, v)
	}
	sort.Sort(sort.Reverse(semver.Collection(vs)))
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Original()
	}
	return out
}

type Lister func(ctx context.Context) ([]string, error)

// ListError is a range that could not be resolved because its chart repository
// could not be listed. Not the caller's mistake, and naming a version gets past it.
type ListError struct {
	Spec string
	Err  error
}

func (e *ListError) Error() string {
	return fmt.Sprintf("cannot resolve chart version %q: %v. Name a chart version to deploy without listing the repository", e.Spec, e.Err)
}

func (e *ListError) Unwrap() error { return e.Err }

// Resolve picks the version a plan records; the first that applies wins:
//
//	want   named by the caller, and in s
//	exact  s is a bare version
//	keep   the release's running version, while s allows it
//	list   the newest version in s the repository holds
func Resolve(ctx context.Context, spec Spec, want, keep string, list Lister) (string, error) {
	if want != "" {
		if !spec.Allows(want) {
			return "", fmt.Errorf("chart version %q is not in the catalog's %q", want, spec)
		}
		return want, nil
	}
	if v := spec.Exact(); v != "" {
		return v, nil
	}
	if keep != "" && spec.Allows(keep) {
		return keep, nil
	}
	if list == nil {
		return "", &ListError{Spec: spec.String(), Err: fmt.Errorf("no chart repository to list")}
	}
	have, err := list(ctx)
	if err != nil {
		return "", &ListError{Spec: spec.String(), Err: err}
	}
	m := spec.Match(have)
	if len(m) == 0 {
		return "", fmt.Errorf("no chart version satisfies %q: the repository has %d versions, none in range", spec, len(have))
	}
	return m[0], nil
}
