// Package values manipulates chart values as generic maps, with helm's own
// semantics: maps merge key by key, and anything else -- scalars and LISTS --
// replaces wholesale. Lists are the trap. `extraArgs` from the catalog and
// `extraArgs` from a deploy form do not concatenate into a longer command line;
// the later layer wins outright, which is what helm does with `--set` and what
// anyone reading a values file expects.
package values

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Tree is a values document.
type Tree map[string]any

// Provenance records which layer last wrote each leaf path, so a composed
// document can explain itself. Without it a rendered value is just a value, and
// "why is mem-fraction-static 0.85 here" has no answer short of re-deriving the
// whole merge by hand.
type Provenance map[string]string

// Merge writes src over dst, recording every leaf it touches as belonging to
// layer. dst is modified in place.
func Merge(dst, src Tree, layer string, prov Provenance) {
	merge(dst, src, "", layer, prov)
}

func merge(dst, src Tree, prefix, layer string, prov Provenance) {
	for k, sv := range src {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		sm, srcIsMap := sv.(map[string]any)
		if !srcIsMap {
			if t, ok := sv.(Tree); ok {
				sm, srcIsMap = map[string]any(t), true
			}
		}
		dm, dstIsMap := dst[k].(map[string]any)
		if srcIsMap && dstIsMap {
			merge(dm, sm, path, layer, prov)
			continue
		}
		if srcIsMap {
			nested := map[string]any{}
			merge(nested, sm, path, layer, prov)
			dst[k] = nested
			continue
		}
		dst[k] = sv
		if prov != nil {
			prov[path] = layer
		}
	}
}

// Get returns the value at a dotted path.
func Get(t Tree, path string) (any, bool) {
	cur := any(t)
	for _, seg := range strings.Split(path, ".") {
		m, ok := asMap(cur)
		if !ok {
			return nil, false
		}
		cur, ok = m[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// Set writes value at a dotted path, creating intermediate maps. It refuses to
// tunnel through a non-map, which would otherwise silently discard whatever was
// there.
func Set(t Tree, path string, value any) error {
	segs := strings.Split(path, ".")
	cur := map[string]any(t)
	for i, seg := range segs[:len(segs)-1] {
		next, ok := cur[seg]
		if !ok {
			nm := map[string]any{}
			cur[seg] = nm
			cur = nm
			continue
		}
		nm, ok := asMap(next)
		if !ok {
			return fmt.Errorf("cannot set %q: %q is not a map", path, strings.Join(segs[:i+1], "."))
		}
		cur = nm
	}
	cur[segs[len(segs)-1]] = value
	return nil
}

// LeafPaths lists every leaf path, sorted. A map is a leaf only when empty.
func LeafPaths(t Tree) []string {
	var out []string
	var walk func(m map[string]any, prefix string)
	walk = func(m map[string]any, prefix string) {
		if len(m) == 0 && prefix != "" {
			out = append(out, prefix)
			return
		}
		for k, v := range m {
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			if nm, ok := asMap(v); ok {
				walk(nm, path)
				continue
			}
			out = append(out, path)
		}
	}
	walk(t, "")
	sort.Strings(out)
	return out
}

// ParseSet turns "a.b=c" into a single-path tree. Types are inferred the way
// helm's --set does: true/false, integers, floats, everything else a string.
func ParseSet(assignment string) (Tree, error) {
	k, v, ok := strings.Cut(assignment, "=")
	if !ok || k == "" {
		return nil, fmt.Errorf("expected key=value, got %q", assignment)
	}
	t := Tree{}
	if err := Set(t, k, infer(v)); err != nil {
		return nil, err
	}
	return t, nil
}

func infer(s string) any {
	switch s {
	case "true":
		return true
	case "false":
		return false
	case "null", "":
		if s == "null" {
			return nil
		}
		return ""
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}

func asMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case Tree:
		return m, true
	}
	return nil, false
}

// Subtree rebuilds just the given leaf paths into a new tree.
func Subtree(t Tree, paths []string) Tree {
	out := Tree{}
	for _, p := range paths {
		v, ok := Get(t, p)
		if !ok {
			continue
		}
		if err := Set(out, p, v); err != nil {
			continue
		}
	}
	return out
}

// Paths lists every leaf path in a tree, dotted.
func Paths(t Tree) []string {
	var out []string
	var walk func(Tree, string)
	walk = func(n Tree, prefix string) {
		for k, v := range n {
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			if sub, ok := asMap(v); ok && len(sub) > 0 {
				walk(sub, path)
				continue
			}
			out = append(out, path)
		}
	}
	walk(t, "")
	return out
}
