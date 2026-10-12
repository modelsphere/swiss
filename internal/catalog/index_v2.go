package catalog

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// parseIndexForSchema decodes an index document for the requested schema.
// v1 stays DisallowUnknownFields-strict; v2 ignores unknown display fields
// (issue #30) and projects into the same Index shape compose already uses.
func parseIndexForSchema(b []byte, schema string) (*Index, error) {
	schema, err := NormalizeSchema(schema)
	if err != nil {
		return nil, err
	}
	if schema == SchemaV2 {
		return parseIndexV2(b)
	}
	return parseIndex(b)
}

// indexV2Wire is the published index-v2.json surface. Only fields swiss needs
// to list and fetch are typed; the rest is ignored so site display fields can
// grow without a swissd bump.
type indexV2Wire struct {
	APIVersion string         `json:"apiVersion"`
	Site       string         `json:"site"`
	Repo       string         `json:"repo"`
	Raw        string         `json:"raw"`
	Commit     string         `json:"commit"`
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
	Variants       []indexV2Variant `json:"variants"`
}

type indexV2Variant struct {
	ID          string   `json:"id"`
	Version     string   `json:"version"`
	Path        string   `json:"path"`
	Digest      string   `json:"digest"`
	ServedName  string   `json:"servedName"`
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
	idx := &Index{
		APIVersion: APIVersionV2,
		Site:       wire.Site,
		Count:      wire.Count,
		rawBase:    wire.Raw,
		rawCommit:  wire.Commit,
	}
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

// projectV2Model folds a v2 model's latest variants into one IndexVersion so
// the rest of swiss (marketplace listing, Entry) keeps one Index shape.
func projectV2Model(m indexV2Model) (IndexModel, error) {
	vars := make([]IndexVariant, 0, len(m.Variants))
	ids := map[string]bool{}
	var latest string
	var pinPath, pinDigest string
	for _, v := range m.Variants {
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
		def := m.DefaultVariant != "" && v.ID == m.DefaultVariant
		vars = append(vars, IndexVariant{
			ID:          v.ID,
			Engine:      v.Engine,
			Default:     def,
			Description: v.Description,
			Link:        v.Link,
			Chart:       v.Chart,
			Requires:    v.Requires,
			Path:        v.Path,
			Digest:      v.Digest,
			Version:     v.Version,
		})
		if def || (pinPath == "" && m.DefaultVariant == "") {
			latest = v.Version
			pinPath, pinDigest = v.Path, v.Digest
		}
	}
	if latest == "" {
		// No defaultVariant and the first-assign above should have set it; belt.
		latest = vars[0].Version
		pinPath, pinDigest = vars[0].Path, vars[0].Digest
	}
	// When defaultVariant was set but not found, fall back to the first variant.
	if pinPath == "" {
		latest = vars[0].Version
		pinPath, pinDigest = vars[0].Path, vars[0].Digest
	}
	return IndexModel{
		Name:        m.Name,
		DisplayName: m.DisplayName,
		Description: m.Description,
		Family:      m.Family,
		Tags:        m.Tags,
		Deprecated:  m.Deprecated,
		Source:      m.Source,
		Latest:      latest,
		Versions: []IndexVersion{{
			Version:  latest,
			Path:     pinPath,
			Digest:   pinDigest,
			Variants: vars,
		}},
	}, nil
}
