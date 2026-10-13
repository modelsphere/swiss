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
	Name        string        `json:"name"`
	DisplayName string        `json:"displayName,omitempty"`
	Description string        `json:"description,omitempty"`
	Family      string        `json:"family,omitempty"`
	Tags        []string      `json:"tags,omitempty"`
	Deprecated  any           `json:"deprecated,omitempty"`
	Tuning      []IndexTuning `json:"tuning,omitempty"`
	// Reports are linkable files at <site>models/<name>/<report>. Schema v2
	// only; a v1 index naming them is refused like any unknown key.
	Reports  []string       `json:"reports,omitempty"`
	Source   IndexSource    `json:"source"`
	Latest   string         `json:"latest"`
	Versions []IndexVersion `json:"versions"`
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
//
// A v2 model is projected as a single version holding every current variant,
// labelled with the default variant's version. Path and Digest on it are the
// default variant's; each variant's own pin is IndexVariant.Path, Digest and
// Version, which is what PublishedDigest reads.
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
	// Path, Digest and Version pin one v2 variant file. Empty on v1, which
	// pins the whole entry on IndexVersion; omitempty keeps a v1 listing's
	// JSON the same, and parseIndex refuses them on v1.
	Path    string `json:"path,omitempty"`
	Digest  string `json:"digest,omitempty"`
	Version string `json:"version,omitempty"`
}

// v2Field names the first schema-v2-only field a v1 variant carries. They
// share IndexVariant, so DisallowUnknownFields alone no longer catches them.
func (v IndexVariant) v2Field() string {
	switch {
	case v.Path != "":
		return "path"
	case v.Digest != "":
		return "digest"
	case v.Version != "":
		return "version"
	}
	return ""
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
		if len(m.Reports) > 0 {
			return nil, fmt.Errorf("index.json: model %q: unknown field \"reports\" (schema v2 only)", m.Name)
		}

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
			for _, vv := range v.Variants {
				if f := vv.v2Field(); f != "" {
					return nil, fmt.Errorf("index.json: %s %s variant %s: unknown field %q (schema v2 only)", m.Name, v.Version, vv.ID, f)
				}
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

// PublishedDigest is the digest this index publishes now for one model's
// variant at version, and whether it publishes that pair at all. v1 pins the
// version's entry file. v2 pins each variant file on its own version and lists
// only the current one, so a variant that has moved on is not published at the
// old version any more -- that is a new version, not a rewrite.
func (i *Index) PublishedDigest(model, version, variant string) (string, bool) {
	m, ok := i.Model(model)
	if !ok {
		return "", false
	}
	if i.APIVersion == APIVersionV2 {
		for _, iv := range m.Versions {
			for _, v := range iv.Variants {
				if v.ID == variant && v.Version == version {
					return v.Digest, true
				}
			}
		}
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
