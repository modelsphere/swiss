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
	APIVersion string `json:"apiVersion"`
	// Site is where the catalog's own site is published, from its catalog.yaml.
	// A tuned variant's perf report is served there at
	// <site>models/<name>/<report>, so the link is built from this and the
	// model's tuning rather than carried by the variant: a version file is
	// immutable, and could not name a page published after it.
	Site   string       `json:"site,omitempty"`
	Count  int          `json:"count"`
	Models []IndexModel `json:"models"`
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
	Tuning      []IndexTuning  `json:"tuning,omitempty"`
	Source      IndexSource    `json:"source"`
	Latest      string         `json:"latest"`
	Versions    []IndexVersion `json:"versions"`
}

// IndexTuning is a measured result: a tuned variant against a baseline, in one
// published version. It comes from metadata.yaml, so a re-measurement needs no
// new version, and nothing composes from it -- it is for display.
//
// parseIndex does not check that the ids or the version exist. A mistake in a
// display field must not stop a catalog loading for deploys; the catalog's own
// CI checks it, and the UI skips a pair whose variants it cannot find.
type IndexTuning struct {
	Version   string `json:"version"`
	Baseline  string `json:"baseline"`
	Optimized string `json:"optimized"`
	// The headline: percent gained on the tuning benchmark, 44 meaning +44%.
	// Absent when the pair is recorded without a number.
	Uplift *float64 `json:"uplift,omitempty"`
	// Every workload the benchmark ran, when it ran more than one. The
	// headline is one of these.
	Workloads []IndexWorkload `json:"workloads,omitempty"`
	// A file beside metadata.yaml, such as an AutoTune HTML report.
	Report string `json:"report,omitempty"`
}

// IndexWorkload is one workload's result, e.g. "50k + 1.5k" (input + output
// tokens) at +58%.
type IndexWorkload struct {
	Name   string  `json:"name"`
	Uplift float64 `json:"uplift"`
}

// IndexVersion is one published version of a model. Digest is a sha256 of the
// entry file: a deploy records it, and a later fetch that does not match is
// refused rather than used.
type IndexVersion struct {
	Version  string         `json:"version"`
	Path     string         `json:"path"`
	Digest   string         `json:"digest"`
	Variants []IndexVariant `json:"variants"`
}

type IndexSource struct {
	HF       string  `json:"hf"`
	Revision string  `json:"revision,omitempty"`
	SizeGiB  float64 `json:"sizeGiB,omitempty"`
}

type IndexVariant struct {
	ID          string   `json:"id"`
	Engine      string   `json:"engine"`
	Default     bool     `json:"default,omitempty"`
	Description string   `json:"description,omitempty"`
	Link        string   `json:"link,omitempty"`
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
		case len(m.Versions) == 0:
			return nil, fmt.Errorf("index.json: model %q has no versions", m.Name)
		case seen[m.Name]:
			return nil, fmt.Errorf("index.json: model %q listed twice", m.Name)
		}
		seen[m.Name] = true

		versions := map[string]bool{}
		for _, v := range m.Versions {
			switch {
			case v.Version == "":
				return nil, fmt.Errorf("index.json: model %q has an unnamed version", m.Name)
			case v.Path == "":
				return nil, fmt.Errorf("index.json: %s %s has no path", m.Name, v.Version)
			case v.Digest == "":
				return nil, fmt.Errorf("index.json: %s %s has no digest -- a pin cannot be verified without one", m.Name, v.Version)
			case versions[v.Version]:
				return nil, fmt.Errorf("index.json: %s %s listed twice", m.Name, v.Version)
			}
			if err := safeRel(v.Path); err != nil {
				return nil, fmt.Errorf("index.json: %s %s: %w", m.Name, v.Version, err)
			}
			versions[v.Version] = true
		}
		if m.Latest == "" || !versions[m.Latest] {
			return nil, fmt.Errorf("index.json: model %q names latest %q, which is not published", m.Name, m.Latest)
		}
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

// Version resolves a version, or the latest when asked for "".
func (m IndexModel) Version(want string) (IndexVersion, error) {
	if want == "" {
		want = m.Latest
	}
	for _, v := range m.Versions {
		if v.Version == want {
			return v, nil
		}
	}
	have := make([]string, 0, len(m.Versions))
	for _, v := range m.Versions {
		have = append(have, v.Version)
	}
	return IndexVersion{}, fmt.Errorf("model %q has no version %q (have: %v)", m.Name, want, have)
}
