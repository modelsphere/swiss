// Package plan defines the contract between the CLI and the server.
//
// Both frontends produce and consume the same document, which is what makes two
// of them worth more than one rather than the same thing built twice: a plan
// written by the web UI can be diffed in CI, and a plan produced by `swiss plan`
// in a terminal can be handed to the server to apply.
package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/aceforeverd/swiss/internal/values"
	"gopkg.in/yaml.v3"
)

const APIVersion = "plan.swiss/v1"

type Plan struct {
	APIVersion string `json:"apiVersion"`

	Release Release   `json:"release"`
	Source  SourceRef `json:"source"`
	Chart   ChartRef  `json:"chart"`
	Engine  string    `json:"engine"`
	Profile string    `json:"profile"`

	// Overrides is the deploy form's layer, exactly as the user gave it --
	// kept separate from Values so a plan can be re-composed against a newer
	// catalog without the user's intent having been flattened away.
	Overrides values.Tree `json:"overrides,omitempty"`

	// Values is the composed document handed to helm.
	Values values.Tree `json:"values"`

	// Provenance maps each leaf of Values to the layer that set it. This is what
	// makes a derived value legible: a number that appears in a cluster without
	// appearing in a diff is the failure mode the whole design is avoiding.
	Provenance values.Provenance `json:"provenance,omitempty"`

	// Helmfile is the release declaration these values are applied through, so
	// a plan is a complete deploy on its own. Derived, and excluded from Hash
	// for the same reason Provenance is: it explains the values rather than
	// changing them.
	Helmfile string `json:"helmfile,omitempty"`

	// Hash covers release identity, chart, and the composed values. Two plans
	// with one hash render the same thing.
	Hash string `json:"hash"`
}

type Release struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// SourceRef pins the catalog. Ref is a commit sha: a catalog that moves under a
// deploy is a supply-chain surface, and a branch name would not be a record of
// anything.
type SourceRef struct {
	Catalog string `json:"catalog,omitempty"`
	Ref     string `json:"ref,omitempty"`
	Model   string `json:"model"`
	// Version is the model version this was composed from, and Digest the
	// sha256 of that entry: together they are the lock. A recompose that cannot
	// reproduce the digest is refused.
	Version string `json:"version,omitempty"`
	Digest  string `json:"digest,omitempty"`
	Variant string `json:"variant"`
}

type ChartRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Repo    string `json:"repo,omitempty"`
	Path    string `json:"path,omitempty"`
}

// ComputeHash sets p.Hash over the fields that decide what gets rendered.
// Provenance is deliberately excluded: it explains the values, it does not
// change them, and including it would make an explanation look like a diff.
func (p *Plan) ComputeHash() error {
	payload := struct {
		Release Release     `json:"release"`
		Source  SourceRef   `json:"source"`
		Chart   ChartRef    `json:"chart"`
		Values  values.Tree `json:"values"`
	}{p.Release, p.Source, p.Chart, p.Values}

	b, err := json.Marshal(payload) // encoding/json sorts map keys, so this is stable
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	p.Hash = "sha256:" + hex.EncodeToString(sum[:])
	return nil
}

// ByLayer groups provenance for display, so `swiss plan` can show what each
// layer contributed instead of one flat document.
func (p *Plan) ByLayer() map[string][]string {
	out := map[string][]string{}
	for path, layer := range p.Provenance {
		out[layer] = append(out[layer], path)
	}
	for _, paths := range out {
		sort.Strings(paths)
	}
	return out
}

// YAML renders the plan for the ConfigMap stored beside a release. It goes
// through JSON so the field names match the wire form; the hash stays computed
// over canonical JSON, since YAML formatting is the emitter's choice.
func (p *Plan) YAML() ([]byte, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	return yaml.Marshal(doc)
}
