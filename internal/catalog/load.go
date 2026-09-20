package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aceforeverd/swiss/internal/values"
	"gopkg.in/yaml.v3"
)

// Catalog is a loaded set of entries, indexed by model name.
type Catalog struct {
	Root    string
	Ref     string // commit sha the tree was read at; empty when unknown
	Entries []Entry
}

// Load reads models/*/entry.yaml under root and validates every entry.
//
// Validation is not optional and there is no flag to skip it. A catalog is a
// public repo fetched over the network; the moment a consumer will render an
// unchecked entry, "the CI would have caught it" becomes the only thing standing
// between a bad push and a cluster.
func Load(root string) (*Catalog, error) {
	paths, err := filepath.Glob(filepath.Join(root, "models", "*", "entry.yaml"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no models/*/entry.yaml under %s -- is this a swiss catalog?", root)
	}
	sort.Strings(paths)

	c := &Catalog{Root: root, Ref: gitRef(root)}
	seen := map[string]string{}
	for _, p := range paths {
		e, err := loadEntry(p)
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[e.Name]; dup {
			return nil, fmt.Errorf("%s: model %q already defined in %s", p, e.Name, prev)
		}
		seen[e.Name] = p
		c.Entries = append(c.Entries, *e)
	}
	return c, nil
}

func loadEntry(path string) (*Entry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var e Entry
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true) // an unknown key is a typo, not a setting that does nothing
	if err := dec.Decode(&e); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := e.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &e, nil
}

// Validate checks the invariants a single entry can be judged on. The JSON
// schema in the catalog repo is the authority on shape; this is the subset a
// consumer must not take on trust, plus the cross-variant rules a per-file
// schema cannot see.
func (e Entry) Validate() error {
	if e.APIVersion != APIVersion {
		return fmt.Errorf("apiVersion is %q, want %q", e.APIVersion, APIVersion)
	}
	if e.Name == "" {
		return fmt.Errorf("name is required")
	}
	if e.Source.HF == "" {
		return fmt.Errorf("source.hf is required -- it is the identity the site profile derives a path from")
	}
	if len(e.Variants) == 0 {
		return fmt.Errorf("no variants")
	}

	ids := map[string]bool{}
	defaults := 0
	for i, v := range e.Variants {
		where := fmt.Sprintf("variant[%d]", i)
		if v.ID == "" {
			return fmt.Errorf("%s: id is required", where)
		}
		where = "variant " + v.ID
		if ids[v.ID] {
			return fmt.Errorf("%s: duplicate id", where)
		}
		ids[v.ID] = true
		if v.Default {
			defaults++
		}
		switch v.Engine {
		case "sglang", "vllm":
		default:
			return fmt.Errorf("%s: engine %q is not sglang or vllm", where, v.Engine)
		}
		if v.Chart.Name != v.Engine {
			return fmt.Errorf("%s: chart %q does not match engine %q", where, v.Chart.Name, v.Engine)
		}
		if v.Chart.Version == "" {
			return fmt.Errorf("%s: chart.version is required -- an unpinned chart is not a reproducible deploy", where)
		}
		if v.Requires.GPUs < 1 {
			return fmt.Errorf("%s: requires.gpus must be at least 1", where)
		}

		// The catalog layer may only write keys it owns. This is the check that
		// makes a public catalog safe to merge: a namespace, a host path or a
		// registry in an entry is refused here rather than rendered.
		if err := values.CheckOwnership(v.Values, values.LayerCatalog); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}

		// lws.size and requires.nodes describe one group. A disagreement does
		// not fail -- the group hangs at rendezvous waiting for a peer that was
		// never scheduled -- so it has to be caught before it is rendered.
		if v.Requires.TopologyOrDefault() == TopologyLWS {
			size, ok := values.Get(v.Values, "lws.size")
			if !ok {
				return fmt.Errorf("%s: topology is lws but values.lws.size is unset", where)
			}
			if n, _ := size.(int); n != v.Requires.NodesOrDefault() {
				return fmt.Errorf("%s: values.lws.size (%v) != requires.nodes (%d)", where, size, v.Requires.NodesOrDefault())
			}
		}
	}
	if defaults > 1 {
		return fmt.Errorf("%d variants marked default, at most one allowed", defaults)
	}
	return nil
}

// Entry looks up a model by name.
func (c *Catalog) Entry(name string) (Entry, error) {
	for _, e := range c.Entries {
		if e.Name == name {
			return e, nil
		}
	}
	return Entry{}, fmt.Errorf("no model %q in catalog %s", name, c.Root)
}

// gitRef reports the commit the catalog tree is at, best effort. A plan records
// it so a deploy can be traced back to an exact catalog state; an empty value
// means the tree is not a git checkout, which a caller may want to refuse.
func gitRef(root string) string {
	head, err := os.ReadFile(filepath.Join(root, ".git", "HEAD"))
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(head))
	if ref, ok := strings.CutPrefix(s, "ref: "); ok {
		b, err := os.ReadFile(filepath.Join(root, ".git", ref))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	return s
}
