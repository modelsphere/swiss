package catalog

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/Masterminds/semver/v3"
	"github.com/modelsphere/swiss/internal/chart"
	"github.com/modelsphere/swiss/internal/values"
	"gopkg.in/yaml.v3"
)

// V1 is a schema v1 catalog: index.json, and entries fetched on demand. A model
// version pins one entry file holding every variant at that version.
//
// Opening reads index.json only. An entry is fetched when a model is actually
// opened, which is what keeps a marketplace listing one request rather than one
// per model.
type V1 struct {
	Index   *Index
	fetcher Fetcher
	// ref identifies the catalog state a plan was composed against: a digest of
	// the index bytes. Recorded in every plan, so a deploy can be traced back.
	ref string

	// raw is the index as published, kept so a Cache writes back the exact
	// bytes ref was computed over rather than a re-marshalling of them.
	raw []byte

	mu      sync.Mutex
	entries map[string]Entry
}

func (c *V1) Schema() string     { return SchemaV1 }
func (c *V1) Ref() string        { return c.ref }
func (c *V1) Source() string     { return c.fetcher.String() }
func (c *V1) indexBytes() []byte { return c.raw }

// Models lists each model's latest version, every variant at it.
func (c *V1) Models() []Model {
	out := make([]Model, 0, len(c.Index.Models))
	for _, m := range c.Index.Models {
		out = append(out, v1Model(m))
	}
	return out
}

func (c *V1) Model(name string) (Model, bool) {
	m, ok := c.Index.Model(name)
	if !ok {
		return Model{}, false
	}
	return v1Model(m), true
}

func v1Model(m IndexModel) Model {
	out := Model{
		Name: m.Name, DisplayName: m.DisplayName, Description: m.Description,
		Family: m.Family, Tags: m.Tags, Deprecated: m.Deprecated, Source: m.Source,
	}
	latest, _ := m.Version("") // parseIndex refuses an unpublished latest
	for _, v := range latest.Variants {
		out.Variants = append(out.Variants, ModelVariant{
			ID: v.ID, Version: latest.Version, Digest: latest.Digest,
			Engine: v.Engine, Default: v.Default, Description: v.Description, Link: v.Link,
			Chart: v.Chart, Requires: v.Requires,
		})
		if v.Default {
			out.DefaultVariant = v.ID
		}
	}
	if out.DefaultVariant == "" && len(out.Variants) > 0 {
		out.DefaultVariant = out.Variants[0].ID
	}
	for _, t := range m.Tuning {
		out.Tuning = append(out.Tuning, Tuning{
			Baseline:  VariantRef{ID: t.Baseline, Version: t.Version},
			Optimized: VariantRef{ID: t.Optimized, Version: t.Version},
			Uplift:    t.Uplift, Workloads: t.Workloads, Report: t.Report,
		})
	}
	return out
}

// History is every published version that carries the variant, newest first,
// each pinned by its entry file. The index lists them all, so nothing is
// fetched.
func (c *V1) History(_ context.Context, model, variant string) ([]Revision, error) {
	m, ok := c.Index.Model(model)
	if !ok {
		return nil, notFound("no model %q in catalog %s", model, c.fetcher)
	}
	if variant == "" {
		variant = v1Model(m).DefaultVariant
	}
	var out []Revision
	for _, iv := range m.Versions {
		for _, v := range iv.Variants {
			if v.ID == variant {
				out = append(out, Revision{Version: iv.Version, Digest: iv.Digest, Current: iv.Version == m.Latest})
				break
			}
		}
	}
	if len(out) == 0 {
		return nil, notFound("model %q has no variant %q in any version", model, variant)
	}
	sort.SliceStable(out, func(i, j int) bool { return newer(out[i].Version, out[j].Version) })
	return out, nil
}

// newer orders semver versions, falling back to the strings for one that is
// not.
func newer(a, b string) bool {
	va, errA := semver.NewVersion(a)
	vb, errB := semver.NewVersion(b)
	if errA != nil || errB != nil {
		return a > b
	}
	return va.GreaterThan(vb)
}

// Resolve is Entry at version, and the variant in it.
func (c *V1) Resolve(ctx context.Context, model, variant, version string) (Resolved, error) {
	e, err := c.Entry(ctx, model, version)
	if err != nil {
		return Resolved{}, err
	}
	var v Variant
	if variant == "" {
		v, err = e.DefaultVariant()
	} else {
		v, err = e.Variant(variant)
	}
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{
		Model: e.Name, Source: e.Source, ServedName: e.ServedModelName(),
		Version: e.Version, Digest: e.Digest, Variant: v,
	}, nil
}

// Published is the entry digest of that version, when the version still lists
// the variant.
func (c *V1) Published(model, variant, version string) (string, bool) {
	m, ok := c.Index.Model(model)
	if !ok {
		return "", false
	}
	iv, err := m.Version(version)
	if err != nil {
		return "", false
	}
	for _, v := range iv.Variants {
		if v.ID == variant {
			return iv.Digest, true
		}
	}
	return "", false
}

// Entry fetches and validates one model at one version, memoised. An empty
// version resolves to the latest published one.
//
// Validation happens here, on every fetch, and cannot be skipped. A catalog is
// fetched over a network from a repo this cluster does not control, so it is
// untrusted input at the point of use; trusting it because some CI somewhere was
// supposed to have run is hoping, not validating.
func (c *V1) Entry(ctx context.Context, name, wantVersion string) (Entry, error) {
	m, ok := c.Index.Model(name)
	if !ok {
		return Entry{}, fmt.Errorf("no model %q in catalog %s", name, c.fetcher)
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

	raw, err := c.fetcher.Fetch(ctx, iv.Path)
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
func (c *V1) All(ctx context.Context) ([]Entry, error) {
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
		if ids[v.ID] {
			return fmt.Errorf("variant %s: duplicate id", v.ID)
		}
		ids[v.ID] = true
		if v.Default {
			defaults++
		}
		if err := v.validate(); err != nil {
			return err
		}
	}
	if defaults > 1 {
		return fmt.Errorf("%d variants marked default, at most one allowed", defaults)
	}
	return nil
}

// validate is the rules one variant is judged on alone, the same in a v1 entry
// and a v2 variant file.
func (v Variant) validate() error {
	where := "variant " + v.ID
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
	return nil
}
