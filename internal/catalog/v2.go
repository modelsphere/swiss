package catalog

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// V2 is a schema v2 catalog: index-v2.json, and each variant file fetched on
// its own when it is resolved. The index lists every variant at its current
// version; an older one is found in the variant's history.json and fetched
// from the repository at its commit.
type V2 struct {
	Index   *IndexV2
	fetcher Fetcher
	ref     string
	raw     []byte
	// client fetches old revisions from Index.Raw, which is not where the
	// index is served.
	client *http.Client

	mu sync.Mutex
	// files are verified variant files by digest, which names one file
	// whichever revision or location it was read from.
	files map[string]VariantV2
	// histories are history.json by path, for this index's life.
	histories map[string]HistoryV2
}

func (c *V2) Schema() string     { return SchemaV2 }
func (c *V2) Ref() string        { return c.ref }
func (c *V2) Source() string     { return c.fetcher.String() }
func (c *V2) Site() string       { return c.Index.Site }
func (c *V2) indexBytes() []byte { return c.raw }

func (c *V2) Models() []Model {
	out := make([]Model, 0, len(c.Index.Models))
	for _, m := range c.Index.Models {
		out = append(out, v2Model(m))
	}
	return out
}

func (c *V2) Model(name string) (Model, bool) {
	m, ok := c.Index.Model(name)
	if !ok {
		return Model{}, false
	}
	return v2Model(m), true
}

func v2Model(m ModelV2) Model {
	def := m.DefaultID()
	out := Model{
		Name: m.Name, DisplayName: m.DisplayName, Description: m.Description,
		Family: m.Family, Tags: m.Tags, Deprecated: m.Deprecated, Source: m.Source,
		DefaultVariant: def,
	}
	for _, v := range m.Variants {
		out.Variants = append(out.Variants, ModelVariant{
			ID: v.ID, Version: v.Version, Digest: v.Digest,
			Engine: v.Engine, Default: v.ID == def, Description: v.Description, Link: v.Link,
			Chart: v.Chart, Requires: v.Requires,
			ServedName: v.ServedName, Deprecated: v.Deprecated, Replaces: v.Replaces,
		})
	}
	for _, t := range m.Tuning {
		out.Tuning = append(out.Tuning, Tuning{
			Baseline: variantRef(t.Baseline), Optimized: variantRef(t.Optimized),
			Uplift: t.Uplift, Workloads: t.Workloads, Report: t.Report,
		})
	}
	return out
}

func variantRef(s string) VariantRef {
	id, version, _ := strings.Cut(s, "@")
	return VariantRef{ID: id, Version: version}
}

// History is the variant's history.json, newest first.
func (c *V2) History(ctx context.Context, model, variant string) ([]Revision, error) {
	m, pin, err := c.pin(model, variant)
	if err != nil {
		return nil, err
	}
	h, err := c.history(ctx, m.Name, pin)
	if err != nil {
		return nil, err
	}
	out := make([]Revision, 0, len(h.Revisions))
	for i, r := range h.Revisions {
		out = append(out, Revision{
			Version: r.Version, Digest: r.Digest, ReleaseDate: r.ReleaseDate,
			Commit: r.Commit, Subject: r.Subject, Current: i == 0,
		})
	}
	return out, nil
}

func (c *V2) pin(model, variant string) (ModelV2, VariantPinV2, error) {
	m, ok := c.Index.Model(model)
	if !ok {
		return ModelV2{}, VariantPinV2{}, notFound("no model %q in catalog %s", model, c.fetcher)
	}
	pin, err := m.Variant(variant)
	if err != nil {
		return ModelV2{}, VariantPinV2{}, &NotFoundError{err.Error()}
	}
	return m, pin, nil
}

// Resolve fetches the variant's file at version: the current one from beside
// the index, an older one from the repository at the commit its history names.
// A version the history does not list is refused rather than swapped for one
// it does: composing another file under that version's name would deploy what
// was not asked for.
func (c *V2) Resolve(ctx context.Context, model, variant, version string) (Resolved, error) {
	m, pin, err := c.pin(model, variant)
	if err != nil {
		return Resolved{}, err
	}
	if m.Source.HF == "" {
		return Resolved{}, fmt.Errorf("%s: source.hf is required -- it is the identity the site profile derives a path from", model)
	}
	rev := RevisionV2{Version: pin.Version, Digest: pin.Digest}
	where := pin.Path
	if version != "" && version != pin.Version {
		h, err := c.history(ctx, m.Name, pin)
		if err != nil {
			return Resolved{}, err
		}
		var ok bool
		if rev, ok = h.revision(version); !ok {
			return Resolved{}, fmt.Errorf("%s variant %s has no version %s (have: %s)", model, pin.ID, version, h.versions())
		}
		if where, err = c.rawURL(rev, pin); err != nil {
			return Resolved{}, fmt.Errorf("%s variant %s %s: %w", model, pin.ID, version, err)
		}
	}
	doc, err := c.file(ctx, m.Name, pin, rev, where)
	if err != nil {
		return Resolved{}, fmt.Errorf("%s %s %s (%s): %w", model, pin.ID, rev.Version, where, err)
	}
	v := doc.variant(pin.ID == m.DefaultID())
	if err := v.validate(); err != nil {
		return Resolved{}, fmt.Errorf("%s %s (%s): %w", model, rev.Version, where, err)
	}
	served := doc.ServedName
	if served == "" {
		served = m.Name
	}
	return Resolved{
		Model:      m.Name,
		Source:     Source{HF: m.Source.HF, Revision: m.Source.Revision, SizeGiB: m.Source.SizeGiB},
		ServedName: served,
		Version:    rev.Version,
		Digest:     rev.Digest,
		Variant:    v,
	}, nil
}

// history fetches and checks a variant's history.json, from beside the index.
func (c *V2) history(ctx context.Context, model string, pin VariantPinV2) (HistoryV2, error) {
	if pin.History == "" {
		return HistoryV2{}, fmt.Errorf("%s variant %s: the catalog publishes no history for it", model, pin.ID)
	}
	c.mu.Lock()
	if h, ok := c.histories[pin.History]; ok {
		c.mu.Unlock()
		return h, nil
	}
	c.mu.Unlock()

	raw, err := c.fetcher.Fetch(ctx, pin.History)
	if err != nil {
		return HistoryV2{}, fmt.Errorf("%s variant %s history: %w", model, pin.ID, err)
	}
	h, err := parseHistoryV2(raw, model, pin)
	if err != nil {
		return HistoryV2{}, fmt.Errorf("%s variant %s history (%s): %w", model, pin.ID, pin.History, err)
	}
	c.mu.Lock()
	c.histories[pin.History] = h
	c.mu.Unlock()
	return h, nil
}

// rawURL is where an old revision is fetched: <raw><commit>/<path>, as the
// index says.
func (c *V2) rawURL(rev RevisionV2, pin VariantPinV2) (string, error) {
	if rev.Commit == "" {
		return "", fmt.Errorf("revision %s was never committed, so it cannot be fetched", rev.Version)
	}
	u, err := url.Parse(c.Index.Raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("index-v2.json names no http(s) raw location (%q) to fetch old revisions from", c.Index.Raw)
	}
	return c.Index.Raw + rev.Commit + "/" + pin.Path, nil
}

// file fetches one variant file -- the current one by its path beside the
// index, an old one by its raw URL -- and checks it against its revision: the
// digest is the lock, and the document must be the model, id and version the
// catalog says it is.
func (c *V2) file(ctx context.Context, model string, pin VariantPinV2, rev RevisionV2, where string) (VariantV2, error) {
	c.mu.Lock()
	if d, ok := c.files[rev.Digest]; ok {
		c.mu.Unlock()
		return d, nil
	}
	c.mu.Unlock()

	var raw []byte
	var err error
	if where == pin.Path {
		raw, err = c.fetcher.Fetch(ctx, pin.Path)
	} else {
		var u *url.URL
		if u, err = url.Parse(where); err == nil {
			raw, err = httpGet(ctx, c.client, u)
		}
	}
	if err != nil {
		return VariantV2{}, err
	}
	if got := contentRef(raw); got != rev.Digest {
		return VariantV2{}, fmt.Errorf("digest %s does not match the catalog's (%s) -- the published variant was rewritten", got, rev.Digest)
	}
	d, err := ParseVariantV2(raw)
	if err != nil {
		return VariantV2{}, err
	}
	if d.Model != model || d.ID != pin.ID || d.Version != rev.Version {
		return VariantV2{}, fmt.Errorf("file says %s/%s %s, the catalog %s/%s %s -- index is stale", d.Model, d.ID, d.Version, model, pin.ID, rev.Version)
	}

	c.mu.Lock()
	c.files[rev.Digest] = *d
	c.mu.Unlock()
	return *d, nil
}

// Published is the variant's own pin, while it is still at that version. A
// variant that has moved on is a new version, not a rewrite.
func (c *V2) Published(model, variant, version string) (string, bool) {
	m, ok := c.Index.Model(model)
	if !ok {
		return "", false
	}
	for _, v := range m.Variants {
		if v.ID == variant && v.Version == version {
			return v.Digest, true
		}
	}
	return "", false
}
