package catalog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/modelsphere/swiss/internal/chart"
	"github.com/modelsphere/swiss/internal/values"
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
	// SchemaVersion is SchemaV1 or SchemaV2: which published index this catalog
	// was opened from. Entry loading branches on it; compose does not.
	SchemaVersion string
	// Ref identifies the catalog state a plan was composed against: a digest of
	// the index bytes. Recorded in every plan, so a deploy can be traced back.
	Ref string

	// raw is the index as published, kept so a Cache writes back the exact
	// bytes Ref was computed over rather than a re-marshalling of them.
	raw []byte

	mu      sync.Mutex
	entries map[string]Entry
}

// Open fetches and validates the index at loc with schemaVersion unset: a base
// location reads index.json as before, and a location naming its .json is read
// by that document's apiVersion.
func Open(ctx context.Context, loc string) (*Catalog, error) {
	return OpenWithSchema(ctx, loc, "")
}

// OpenWithSchema opens a catalog at loc for a site profile's schemaVersion.
func OpenWithSchema(ctx context.Context, loc, schema string) (*Catalog, error) {
	f, err := NewFetcherWithSchema(loc, schema)
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
	return &Catalog{
		Fetcher: f, Index: idx, SchemaVersion: schemaOf(idx),
		Ref: contentRef(raw), raw: raw, entries: map[string]Entry{},
	}, nil
}

// schemaOf is the schema an index was parsed as, read off the index itself so
// any Fetcher gets it right.
func schemaOf(idx *Index) string {
	if idx != nil && idx.APIVersion == APIVersionV2 {
		return SchemaV2
	}
	return SchemaV1
}

// Entry fetches and validates one model at one version, memoised. An empty
// version resolves to the latest published one.
//
// Validation happens here, on every fetch, and cannot be skipped. A catalog is
// fetched over a network from a repo this cluster does not control, so it is
// untrusted input at the point of use; trusting it because some CI somewhere was
// supposed to have run is hoping, not validating.
func (c *Catalog) Entry(ctx context.Context, name, wantVersion string) (Entry, error) {
	if c.SchemaVersion == SchemaV2 {
		return c.entryV2(ctx, name, wantVersion)
	}
	return c.entryV1(ctx, name, wantVersion)
}

func (c *Catalog) entryV1(ctx context.Context, name, wantVersion string) (Entry, error) {
	m, ok := c.Index.Model(name)
	if !ok {
		return Entry{}, fmt.Errorf("no model %q in catalog %s", name, c.Fetcher)
	}
	iv, err := m.Version(wantVersion)
	if err != nil {
		return Entry{}, err
	}

	key := name + "@" + iv.Version
	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
		c.mu.Unlock()
		return e, nil
	}
	c.mu.Unlock()

	raw, err := c.Fetcher.Fetch(ctx, iv.Path)
	if err != nil {
		return Entry{}, fmt.Errorf("%s %s (%s): %w", name, iv.Version, iv.Path, err)
	}
	// The digest is the lock. A published version is immutable; bytes that no
	// longer match the index are a rewritten release, not a new one.
	if got := contentRef(raw); got != iv.Digest {
		return Entry{}, fmt.Errorf("%s %s: digest %s does not match the index (%s) -- the published version was rewritten", name, iv.Version, got, iv.Digest)
	}

	e, err := ParseEntry(raw)
	if err != nil {
		return Entry{}, fmt.Errorf("%s %s (%s): %w", name, iv.Version, iv.Path, err)
	}
	if e.Name != name || e.Version != iv.Version {
		return Entry{}, fmt.Errorf("%s %s (%s): entry says %s %s -- index is stale", name, iv.Version, iv.Path, e.Name, e.Version)
	}
	e.Digest = iv.Digest
	e.applyMetadata(m)
	if err := e.Validate(); err != nil {
		return Entry{}, fmt.Errorf("%s %s (%s): %w", name, iv.Version, iv.Path, err)
	}

	c.mu.Lock()
	c.entries[key] = *e
	c.mu.Unlock()
	return *e, nil
}

// entryV2 assembles one Entry from a model's current variant files. A v2 index
// lists each variant at its own version, under one label (m.Latest, the default
// variant's): wantVersion "" or the label is every variant, and any other
// version keeps the variants currently at it. Older revisions are not served.
//
// Each file is fetched, verified and validated on its own, in parallel. One
// that fails is recorded in Unavailable instead of failing its siblings, and an
// Entry with any is not memoised, so a transient failure is retried.
func (c *Catalog) entryV2(ctx context.Context, name, wantVersion string) (Entry, error) {
	m, ok := c.Index.Model(name)
	if !ok {
		return Entry{}, fmt.Errorf("no model %q in catalog %s", name, c.Fetcher)
	}
	if len(m.Versions) == 0 || len(m.Versions[0].Variants) == 0 {
		return Entry{}, fmt.Errorf("model %q has no variants in catalog %s", name, c.Fetcher)
	}
	pins := m.Versions[0].Variants
	version := m.Latest
	if wantVersion != "" && wantVersion != m.Latest {
		var at, have []IndexVariant
		for _, p := range pins {
			if p.Version == wantVersion {
				at = append(at, p)
			}
			have = append(have, p)
		}
		if len(at) == 0 {
			return Entry{}, fmt.Errorf("model %q has no variant at version %q -- a v2 catalog serves each variant's current version (have: %s)", name, wantVersion, pinList(have))
		}
		pins, version = at, wantVersion
	}

	key := name + "@" + version
	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
		c.mu.Unlock()
		return e, nil
	}
	c.mu.Unlock()

	loaded := make([]Variant, len(pins))
	errs := make([]error, len(pins))
	var wg sync.WaitGroup
	for i, pin := range pins {
		wg.Add(1)
		go func() {
			defer wg.Done()
			loaded[i], errs[i] = c.variantV2(ctx, m, pin)
		}()
	}
	wg.Wait()

	e := Entry{APIVersion: APIVersion, Name: name, Version: version}
	var failed []error
	for i, pin := range pins {
		if pin.Default {
			e.defaultID = pin.ID
		}
		if errs[i] != nil {
			if e.Unavailable == nil {
				e.Unavailable = map[string]string{}
			}
			e.Unavailable[pin.ID] = errs[i].Error()
			failed = append(failed, fmt.Errorf("%s %s: %w", name, pin.ID, errs[i]))
			continue
		}
		e.Variants = append(e.Variants, loaded[i])
	}
	if len(e.Variants) == 0 {
		return Entry{}, errors.Join(failed...)
	}
	// v1 has one servedName per entry; here it is that only when every
	// variant agrees, and compose reads each variant's own (ServedNameOf).
	for i, v := range e.Variants {
		if i == 0 {
			e.ServedName = v.ServedName
		} else if v.ServedName != e.ServedName {
			e.ServedName = ""
			break
		}
	}
	e.applyMetadata(m)
	if err := e.Validate(); err != nil {
		return Entry{}, fmt.Errorf("%s (%s): %w", name, c.Fetcher, err)
	}

	if len(e.Unavailable) == 0 {
		c.mu.Lock()
		c.entries[key] = e
		c.mu.Unlock()
	}
	return e, nil
}

// variantV2 fetches one variant file and checks it against its pin: the digest
// is the lock, and the document must be the model, id and version the index
// says it is.
func (c *Catalog) variantV2(ctx context.Context, m IndexModel, pin IndexVariant) (Variant, error) {
	raw, err := c.Fetcher.Fetch(ctx, pin.Path)
	if err != nil {
		return Variant{}, fmt.Errorf("%s: %w", pin.Path, err)
	}
	if got := contentRef(raw); got != pin.Digest {
		return Variant{}, fmt.Errorf("%s: digest %s does not match the index (%s) -- the published variant was rewritten", pin.Path, got, pin.Digest)
	}
	doc, err := ParseVariantDoc(raw)
	if err != nil {
		return Variant{}, fmt.Errorf("%s: %w", pin.Path, err)
	}
	if doc.Model != m.Name || doc.ID != pin.ID || doc.Version != pin.Version {
		return Variant{}, fmt.Errorf("%s: document says %s/%s %s, the index %s/%s %s -- index is stale", pin.Path, doc.Model, doc.ID, doc.Version, m.Name, pin.ID, pin.Version)
	}
	v := doc.toVariant(pin)
	// The per-variant rules, judged on this variant alone so a bad one does not
	// take its siblings down; the cross-variant rules run on the whole Entry.
	one := Entry{APIVersion: APIVersion, Name: m.Name, Version: pin.Version, Variants: []Variant{v}}
	one.applyMetadata(m)
	if err := one.Validate(); err != nil {
		return Variant{}, fmt.Errorf("%s: %w", pin.Path, err)
	}
	return v, nil
}

func pinList(pins []IndexVariant) string {
	out := make([]string, 0, len(pins))
	for _, p := range pins {
		out = append(out, p.ID+"@"+p.Version)
	}
	return strings.Join(out, ", ")
}

// applyMetadata fills the cosmetic half from the index, which build-index.sh
// inlined from metadata.yaml. A version file carries none of it, so that a
// description can be fixed without republishing a version somebody has pinned.
func (e *Entry) applyMetadata(m IndexModel) {
	e.Source = Source{HF: m.Source.HF, Revision: m.Source.Revision, SizeGiB: m.Source.SizeGiB}
	e.DisplayName = m.DisplayName
	e.Description = m.Description
	e.Family = m.Family
	e.Tags = m.Tags
	e.Deprecated = m.Deprecated
}

// All fetches every entry. Used by validation and by anything that genuinely
// needs the full set; ordinary listing should use the index.
func (c *Catalog) All(ctx context.Context) ([]Entry, error) {
	out := make([]Entry, 0, len(c.Index.Models))
	for _, m := range c.Index.Models {
		e, err := c.Entry(ctx, m.Name, "")
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// ParseEntry decodes and validates one version document. Source and the
// cosmetic fields arrive separately, from metadata.yaml by way of the index, so
// Validate is called by the caller once they are in place.
func ParseEntry(raw []byte) (*Entry, error) {
	var e Entry
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true) // an unknown key is a typo, not a setting that does nothing
	if err := dec.Decode(&e); err != nil {
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
	if e.Version == "" {
		return fmt.Errorf("version is required -- a deploy pins one")
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
		// A range is fine: the plan records the one version it resolved to.
		if _, err := chart.ParseSpec(v.Chart.Version); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		if v.Requires.GPUs < 1 {
			return fmt.Errorf("%s: requires.gpus must be at least 1", where)
		}
		// An unknown vendor would render a pod requesting an empty resource
		// name, which schedules and then runs on no accelerator at all.
		if _, ok := accelerators[v.Requires.VendorOrDefault()]; !ok {
			return fmt.Errorf("%s: requires.vendor %q is not a known accelerator", where, v.Requires.Vendor)
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
