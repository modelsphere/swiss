package catalog

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Index is the catalog's published surface: one document listing every model and
// enough of each variant to render a marketplace, plus the path to the full
// entry. A client fetches this once and an entry only when a model is opened.
type Index struct {
	APIVersion string       `json:"apiVersion"`
	Count      int          `json:"count"`
	Models     []IndexModel `json:"models"`
}

// IndexModel is a summary, deliberately a different type from Entry. It carries
// no variant `values`, so nothing can accidentally compose from a listing and
// render a model with half its flags missing.
type IndexModel struct {
	Name        string         `json:"name"`
	DisplayName string         `json:"displayName,omitempty"`
	Description string         `json:"description,omitempty"`
	Family      string         `json:"family,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	Deprecated  any            `json:"deprecated,omitempty"`
	Source      IndexSource    `json:"source"`
	Variants    []IndexVariant `json:"variants"`
	Path        string         `json:"path"`
}

type IndexSource struct {
	HF      string  `json:"hf"`
	SizeGiB float64 `json:"sizeGiB,omitempty"`
}

type IndexVariant struct {
	ID          string   `json:"id"`
	Engine      string   `json:"engine"`
	Default     bool     `json:"default,omitempty"`
	Description string   `json:"description,omitempty"`
	Chart       Chart    `json:"chart"`
	Requires    Requires `json:"requires"`
}

func parseIndex(b []byte) (*Index, error) {
	var idx Index
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&idx); err != nil {
		return nil, fmt.Errorf("index.json: %w", err)
	}
	if idx.APIVersion != APIVersion {
		return nil, fmt.Errorf("index.json: apiVersion %q, want %q", idx.APIVersion, APIVersion)
	}
	seen := map[string]bool{}
	for i, m := range idx.Models {
		switch {
		case m.Name == "":
			return nil, fmt.Errorf("index.json: models[%d] has no name", i)
		case m.Path == "":
			return nil, fmt.Errorf("index.json: model %q has no path", m.Name)
		case seen[m.Name]:
			return nil, fmt.Errorf("index.json: model %q listed twice", m.Name)
		}
		if err := safeRel(m.Path); err != nil {
			return nil, fmt.Errorf("index.json: model %q: %w", m.Name, err)
		}
		seen[m.Name] = true
	}
	// count is generated alongside the list; a mismatch means the index was
	// hand-edited or half-written, which is worth refusing rather than guessing.
	if idx.Count != 0 && idx.Count != len(idx.Models) {
		return nil, fmt.Errorf("index.json: count is %d but %d models are listed", idx.Count, len(idx.Models))
	}
	return &idx, nil
}

// Model looks up a summary by name.
func (i *Index) Model(name string) (IndexModel, bool) {
	for _, m := range i.Models {
		if m.Name == name {
			return m, true
		}
	}
	return IndexModel{}, false
}
