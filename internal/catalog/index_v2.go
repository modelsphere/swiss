package catalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// parseIndexForSchema decodes an index document for the configured schema. v1
// stays DisallowUnknownFields-strict; v2 ignores unknown display fields
// (issue #30) and projects into the same Index shape compose already uses.
// Unset follows the document's apiVersion, which is what lets an explicit
// .../index-v2.json load without a schemaVersion.
func parseIndexForSchema(b []byte, schema string) (*Index, error) {
	var head struct {
		APIVersion string `json:"apiVersion"`
	}
	_ = json.Unmarshal(b, &head) // a document that does not decode gets the v1 parser's error
	v2 := head.APIVersion == APIVersionV2
	switch {
	case schema == SchemaV2 || (schema == "" && v2):
		return parseIndexV2(b)
	case v2:
		return nil, fmt.Errorf("index is %s but the catalog is set to schemaVersion %s -- set schemaVersion: v2", APIVersionV2, schema)
	}
	return parseIndex(b)
}

// indexV2Wire is the published index-v2.json surface. Only fields swiss needs
// to list and fetch are typed; the rest is ignored so site display fields can
// grow without a swissd bump.
type indexV2Wire struct {
	APIVersion string         `json:"apiVersion"`
	Site       string         `json:"site"`
	Count      int            `json:"count"`
	Models     []indexV2Model `json:"models"`
}

type indexV2Model struct {
	Name           string           `json:"name"`
	DisplayName    string           `json:"displayName"`
	Description    string           `json:"description"`
	Family         string           `json:"family"`
	Tags           []string         `json:"tags"`
	Deprecated     any              `json:"deprecated"`
	Source         IndexSource      `json:"source"`
	DefaultVariant string           `json:"defaultVariant"`
	Reports        []string         `json:"reports"`
	Tuning         []indexV2Tuning  `json:"tuning"`
	Variants       []indexV2Variant `json:"variants"`
}

// indexV2Tuning names its pair as "<variant id>@<variant version>": in v2 the
// version belongs to each variant file, not to the model.
type indexV2Tuning struct {
	Baseline  string          `json:"baseline"`
	Optimized string          `json:"optimized"`
	Uplift    *float64        `json:"uplift"`
	Workloads []IndexWorkload `json:"workloads"`
	Report    string          `json:"report"`
}

type indexV2Variant struct {
	ID          string   `json:"id"`
	Version     string   `json:"version"`
	Path        string   `json:"path"`
	Digest      string   `json:"digest"`
	Engine      string   `json:"engine"`
	Description string   `json:"description"`
	Link        string   `json:"link"`
	Chart       Chart    `json:"chart"`
	Requires    Requires `json:"requires"`
}

func parseIndexV2(b []byte) (*Index, error) {
	var wire indexV2Wire
	dec := json.NewDecoder(bytes.NewReader(b))
	// Unknown fields ignored on purpose: the index is a listing shared with the
	// site, and display-only keys must not force a swissd release.
	if err := dec.Decode(&wire); err != nil {
		return nil, fmt.Errorf("index-v2.json: %w", err)
	}
	if wire.APIVersion != APIVersionV2 {
		return nil, fmt.Errorf("index-v2.json: apiVersion %q, want %q", wire.APIVersion, APIVersionV2)
	}
	idx := &Index{APIVersion: APIVersionV2, Site: wire.Site, Count: wire.Count}
	seen := map[string]bool{}
	for i, m := range wire.Models {
		if m.Name == "" {
			return nil, fmt.Errorf("index-v2.json: models[%d] has no name", i)
		}
		if seen[m.Name] {
			return nil, fmt.Errorf("index-v2.json: model %q listed twice", m.Name)
		}
		seen[m.Name] = true
		if len(m.Variants) == 0 {
			return nil, fmt.Errorf("index-v2.json: model %q has no variants", m.Name)
		}
		im, err := projectV2Model(m)
		if err != nil {
			return nil, err
		}
		idx.Models = append(idx.Models, im)
	}
	if idx.Count != 0 && idx.Count != len(idx.Models) {
		return nil, fmt.Errorf("index-v2.json: count is %d but %d models are listed", idx.Count, len(idx.Models))
	}
	return idx, nil
}

// projectV2Model folds a v2 model's current variants into one IndexVersion so
// the marketplace and the version picker keep one Index shape. That version is
// a label, the default variant's version; each variant's own pin is on
// IndexVariant, and Entry, compose and drift read that.
func projectV2Model(m indexV2Model) (IndexModel, error) {
	vars := make([]IndexVariant, 0, len(m.Variants))
	ids := map[string]bool{}
	def := 0 // "The first variant when absent."
	for i, v := range m.Variants {
		switch {
		case v.ID == "":
			return IndexModel{}, fmt.Errorf("index-v2.json: model %q has a variant with no id", m.Name)
		case v.Path == "":
			return IndexModel{}, fmt.Errorf("index-v2.json: %s/%s has no path", m.Name, v.ID)
		case v.Digest == "":
			return IndexModel{}, fmt.Errorf("index-v2.json: %s/%s has no digest -- a pin cannot be verified without one", m.Name, v.ID)
		case v.Version == "":
			return IndexModel{}, fmt.Errorf("index-v2.json: %s/%s has no version", m.Name, v.ID)
		case ids[v.ID]:
			return IndexModel{}, fmt.Errorf("index-v2.json: %s variant %q listed twice", m.Name, v.ID)
		}
		if err := safeRel(v.Path); err != nil {
			return IndexModel{}, fmt.Errorf("index-v2.json: %s/%s: %w", m.Name, v.ID, err)
		}
		ids[v.ID] = true
		if v.ID == m.DefaultVariant {
			def = i
		}
		vars = append(vars, IndexVariant{
			ID:          v.ID,
			Engine:      v.Engine,
			Description: v.Description,
			Link:        v.Link,
			Chart:       v.Chart,
			Requires:    v.Requires,
			Path:        v.Path,
			Digest:      v.Digest,
			Version:     v.Version,
		})
	}
	vars[def].Default = true
	return IndexModel{
		Name:        m.Name,
		DisplayName: m.DisplayName,
		Description: m.Description,
		Family:      m.Family,
		Tags:        m.Tags,
		Deprecated:  m.Deprecated,
		Reports:     m.Reports,
		Tuning:      projectV2Tuning(m.Tuning, vars),
		Source:      m.Source,
		Latest:      vars[def].Version,
		Versions: []IndexVersion{{
			Version: vars[def].Version,
			// The default variant's pin, for a client that reads one per version.
			Path:     vars[def].Path,
			Digest:   vars[def].Digest,
			Variants: vars,
		}},
	}, nil
}

// projectV2Tuning rewrites "<id>@<version>" pairs into the v1 shape the pages
// read: variant ids, and the version the optimized variant was measured at.
// Like v1, a pair naming a variant that does not exist is kept for the UI to
// skip, not refused.
func projectV2Tuning(in []indexV2Tuning, vars []IndexVariant) []IndexTuning {
	if len(in) == 0 {
		return nil
	}
	current := map[string]string{}
	for _, v := range vars {
		current[v.ID] = v.Version
	}
	out := make([]IndexTuning, 0, len(in))
	for _, t := range in {
		base, _, _ := strings.Cut(t.Baseline, "@")
		opt, at, ok := strings.Cut(t.Optimized, "@")
		if !ok {
			at = current[opt]
		}
		out = append(out, IndexTuning{
			Version:   at,
			Baseline:  base,
			Optimized: opt,
			Uplift:    t.Uplift,
			Workloads: t.Workloads,
			Report:    t.Report,
		})
	}
	return out
}
