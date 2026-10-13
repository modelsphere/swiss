package catalog

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// APIVersionV2 is the apiVersion of index-v2.json and of a v2 variant file.
const APIVersionV2 = "catalog.swiss/v2"

// IndexV2 is index-v2.json: the v2 models and their variant files, each file
// pinned on its own version. v1 models stay in index.json.
type IndexV2 struct {
	APIVersion string `json:"apiVersion"`
	Site       string `json:"site,omitempty"`
	Repo       string `json:"repo,omitempty"`
	// Raw is where an old revision is fetched, at <raw><commit>/<path>.
	Raw string `json:"raw,omitempty"`
	// Commit is the last commit that changed models/ or catalog.yaml.
	Commit string    `json:"commit,omitempty"`
	Count  int       `json:"count"`
	Models []ModelV2 `json:"models"`
}

// ModelV2 is one model of index-v2.json: metadata.yaml inlined, and its
// variants.
type ModelV2 struct {
	Name         string      `json:"name"`
	DisplayName  string      `json:"displayName,omitempty"`
	Description  string      `json:"description,omitempty"`
	Family       string      `json:"family,omitempty"`
	Series       string      `json:"series,omitempty"`
	Tags         []string    `json:"tags,omitempty"`
	Deprecated   any         `json:"deprecated,omitempty"`
	Source       IndexSource `json:"source"`
	Quantization string      `json:"quantization,omitempty"`
	// DefaultVariant is an id; the first variant when absent.
	DefaultVariant string         `json:"defaultVariant,omitempty"`
	Reports        []string       `json:"reports"`
	Tuning         []TuningV2     `json:"tuning"`
	Variants       []VariantPinV2 `json:"variants"`
}

// TuningV2 names its pair as "<variant id>@<variant version>".
type TuningV2 struct {
	Baseline  string          `json:"baseline"`
	Optimized string          `json:"optimized"`
	Uplift    *float64        `json:"uplift,omitempty"`
	Workloads []IndexWorkload `json:"workloads,omitempty"`
	Report    string          `json:"report,omitempty"`
}

// VariantPinV2 is one entry of a model's variants: the variant without its
// values, and the pin -- path, digest and version -- of its file.
type VariantPinV2 struct {
	ID          string   `json:"id"`
	Version     string   `json:"version"`
	ReleaseDate string   `json:"releaseDate,omitempty"`
	Commit      string   `json:"commit,omitempty"`
	Path        string   `json:"path"`
	Digest      string   `json:"digest"`
	History     string   `json:"history,omitempty"`
	ServedName  string   `json:"servedName,omitempty"`
	Engine      string   `json:"engine"`
	Description string   `json:"description,omitempty"`
	Link        string   `json:"link,omitempty"`
	Chart       Chart    `json:"chart"`
	Image       *Image   `json:"image,omitempty"`
	Requires    Requires `json:"requires"`
	Deprecated  any      `json:"deprecated,omitempty"`
	Replaces    string   `json:"replaces,omitempty"`
}

// parseIndexV2 decodes and checks index-v2.json. Unknown keys are ignored on
// purpose (issue #30): the index is shared with the catalog's site, and a
// display-only key must not force a swissd release. What a deploy relies on --
// names, ids and each file's pin -- is checked.
func parseIndexV2(b []byte) (*IndexV2, error) {
	var idx IndexV2
	if err := json.NewDecoder(bytes.NewReader(b)).Decode(&idx); err != nil {
		return nil, fmt.Errorf("index-v2.json: %w", err)
	}
	if idx.APIVersion != APIVersionV2 {
		return nil, fmt.Errorf("index-v2.json: apiVersion %q, want %q", idx.APIVersion, APIVersionV2)
	}
	seen := map[string]bool{}
	for i, m := range idx.Models {
		switch {
		case m.Name == "":
			return nil, fmt.Errorf("index-v2.json: models[%d] has no name", i)
		case seen[m.Name]:
			return nil, fmt.Errorf("index-v2.json: model %q listed twice", m.Name)
		case len(m.Variants) == 0:
			return nil, fmt.Errorf("index-v2.json: model %q has no variants", m.Name)
		}
		seen[m.Name] = true
		ids := map[string]bool{}
		for _, v := range m.Variants {
			switch {
			case v.ID == "":
				return nil, fmt.Errorf("index-v2.json: model %q has a variant with no id", m.Name)
			case ids[v.ID]:
				return nil, fmt.Errorf("index-v2.json: %s variant %q listed twice", m.Name, v.ID)
			case v.Path == "":
				return nil, fmt.Errorf("index-v2.json: %s/%s has no path", m.Name, v.ID)
			case v.Digest == "":
				return nil, fmt.Errorf("index-v2.json: %s/%s has no digest -- a pin cannot be verified without one", m.Name, v.ID)
			case v.Version == "":
				return nil, fmt.Errorf("index-v2.json: %s/%s has no version", m.Name, v.ID)
			}
			if err := safeRel(v.Path); err != nil {
				return nil, fmt.Errorf("index-v2.json: %s/%s: %w", m.Name, v.ID, err)
			}
			if v.History != "" {
				if err := safeRel(v.History); err != nil {
					return nil, fmt.Errorf("index-v2.json: %s/%s history: %w", m.Name, v.ID, err)
				}
			}
			ids[v.ID] = true
		}
	}
	if idx.Count != 0 && idx.Count != len(idx.Models) {
		return nil, fmt.Errorf("index-v2.json: count is %d but %d models are listed", idx.Count, len(idx.Models))
	}
	return &idx, nil
}

// Model looks up a model by name.
func (i *IndexV2) Model(name string) (ModelV2, bool) {
	for _, m := range i.Models {
		if m.Name == name {
			return m, true
		}
	}
	return ModelV2{}, false
}

// DefaultID is the variant resolved when none is named: defaultVariant when it
// names one, else the first.
func (m ModelV2) DefaultID() string {
	for _, v := range m.Variants {
		if v.ID == m.DefaultVariant {
			return v.ID
		}
	}
	return m.Variants[0].ID
}

// Variant looks up a variant's pin by id; "" is the default.
func (m ModelV2) Variant(id string) (VariantPinV2, error) {
	if id == "" {
		id = m.DefaultID()
	}
	ids := make([]string, 0, len(m.Variants))
	for _, v := range m.Variants {
		if v.ID == id {
			return v, nil
		}
		ids = append(ids, v.ID)
	}
	return VariantPinV2{}, fmt.Errorf("model %q has no variant %q (have: %v)", m.Name, id, ids)
}
