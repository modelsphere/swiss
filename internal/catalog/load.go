package catalog

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	"github.com/aceforeverd/swiss/internal/values"
	"gopkg.in/yaml.v3"
)

// Catalog is an opened catalog: its index, and entries fetched on demand.
//
// Opening reads index.json only. An entry is fetched when a model is actually
// opened, which is what keeps a marketplace listing one request rather than one
// per model.
type Catalog struct {
	Fetcher Fetcher
	Index   *Index
	// Ref identifies the catalog state a plan was composed against: a digest of
	// the index bytes. Recorded in every plan, so a deploy can be traced back.
	Ref string

	mu      sync.Mutex
	entries map[string]Entry
}

// Open fetches and validates the index.
func Open(ctx context.Context, loc string) (*Catalog, error) {
	f, err := NewFetcher(loc)
	if err != nil {
		return nil, err
	}
	return OpenFetcher(ctx, f)
}

// OpenFetcher is Open against an already-built Fetcher, for tests and for
// callers holding a configured HTTP client.
func OpenFetcher(ctx context.Context, f Fetcher) (*Catalog, error) {
	idx, raw, err := f.Index(ctx)
	if err != nil {
		return nil, err
	}
	return &Catalog{Fetcher: f, Index: idx, Ref: contentRef(raw), entries: map[string]Entry{}}, nil
}

// Entry fetches and validates one model's entry, memoised.
//
// Validation happens here, on every fetch, and cannot be skipped. A catalog is
// fetched over a network from a repo this cluster does not control, so it is
// untrusted input at the point of use; trusting it because some CI somewhere was
// supposed to have run is hoping, not validating.
func (c *Catalog) Entry(ctx context.Context, name string) (Entry, error) {
	c.mu.Lock()
	if e, ok := c.entries[name]; ok {
		c.mu.Unlock()
		return e, nil
	}
	c.mu.Unlock()

	m, ok := c.Index.Model(name)
	if !ok {
		return Entry{}, fmt.Errorf("no model %q in catalog %s", name, c.Fetcher)
	}
	raw, err := c.Fetcher.Fetch(ctx, m.Path)
	if err != nil {
		return Entry{}, fmt.Errorf("model %q (%s): %w", name, m.Path, err)
	}
	e, err := ParseEntry(raw)
	if err != nil {
		return Entry{}, fmt.Errorf("model %q (%s): %w", name, m.Path, err)
	}
	// The index is a summary of the entry. If they disagree, one of them is
	// stale, and composing from the wrong one is how a deploy ends up with a
	// model nobody chose.
	if e.Name != name {
		return Entry{}, fmt.Errorf("model %q (%s): entry says name %q -- index is stale", name, m.Path, e.Name)
	}

	c.mu.Lock()
	c.entries[name] = *e
	c.mu.Unlock()
	return *e, nil
}

// All fetches every entry. Used by validation and by anything that genuinely
// needs the full set; ordinary listing should use the index.
func (c *Catalog) All(ctx context.Context) ([]Entry, error) {
	out := make([]Entry, 0, len(c.Index.Models))
	for _, m := range c.Index.Models {
		e, err := c.Entry(ctx, m.Name)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// ParseEntry decodes and validates one entry document.
func ParseEntry(raw []byte) (*Entry, error) {
	var e Entry
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true) // an unknown key is a typo, not a setting that does nothing
	if err := dec.Decode(&e); err != nil {
		return nil, err
	}
	if err := e.Validate(); err != nil {
		return nil, err
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
