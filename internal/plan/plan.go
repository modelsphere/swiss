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
	"fmt"
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

	// Edits are the escape hatch, applied after every layer and exempt from
	// ownership. Kept apart from Overrides so an upgrade can carry them forward
	// or drop them deliberately, and so provenance can show which values came
	// from a human editing the plan rather than from the form.
	Edits values.Tree `json:"edits,omitempty"`

	// Layers is the deploy: one override document per layer, each exactly as
	// that layer wrote it. Applied in Layers order, last writer wins -- which is
	// helm's own values-file semantics, so helm merging these files lands on the
	// same result swiss did.
	//
	// The order determines the outcome, so nothing here records which layer won
	// a path: that is read off the order. There is no separate provenance map,
	// and no separate copy of the form's or the editor's input, because those
	// are two of these documents.
	Layers map[string]values.Tree `json:"layers,omitempty"`

	// CreateNamespace passes --create-namespace to helm.
	CreateNamespace bool `json:"createNamespace,omitempty"`

	// Helmfile is the release declaration these values are applied through, so
	// a plan is a complete deploy on its own. Derived, and excluded from Hash:
	// it explains the values rather than changing them.
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
	}{p.Release, p.Source, p.Chart, p.Values()}

	b, err := json.Marshal(payload) // encoding/json sorts map keys, so this is stable
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	p.Hash = "sha256:" + hex.EncodeToString(sum[:])
	return nil
}

// VerifyHash recomputes the hash over the layers and compares. A plan read back
// from the cluster has been bytes in etcd since it was written, and a rollback
// applies it without composing anything.
func (p *Plan) VerifyHash() error {
	want := p.Hash
	if want == "" {
		return fmt.Errorf("plan carries no hash")
	}
	c := *p
	if err := c.ComputeHash(); err != nil {
		return err
	}
	if c.Hash != want {
		return fmt.Errorf("hash %s does not match the plan it is stored under (%s) -- it was edited in place", c.Hash, want)
	}
	return nil
}

// Values is the document helm receives: the union of the layers. They are
// disjoint, so this does not depend on merge order and cannot drift from what
// compose produced the way a separately stored copy could.
func (p *Plan) Values() values.Tree {
	out := values.Tree{}
	for _, layer := range Layers {
		if tree, ok := p.Layers[layer]; ok {
			values.Merge(out, tree, layer, nil)
		}
	}
	return out
}

// LayerOf is the layer whose value for a path survived. Several documents may
// set one path -- that is what overriding is -- so this reads them back to
// front: the last writer in Layers order is the one that won.
func (p *Plan) LayerOf(path string) string {
	for i := len(Layers) - 1; i >= 0; i-- {
		if tree, ok := p.Layers[Layers[i]]; ok {
			if _, found := values.Get(tree, path); found {
				return Layers[i]
			}
		}
	}
	return ""
}

// Shadow is one value that a layer set and a later layer overrode. It is not an
// error: the merge order is the rule, and a later layer overriding an earlier
// one is the mechanism working. It is worth showing because the layer that lost
// is usually the one the reader thought they were configuring -- an edit that
// silently replaces extraArgs from the model entry is the case this exists for.
type Shadow struct {
	Path string `json:"path"`
	// By is the layer whose value is in the rendered document.
	By string `json:"by"`
	// Under lists the layers it overrode, in merge order.
	Under []string `json:"under"`
}

// Shadowed lists every path more than one layer set, in path order. Derived
// rather than stored: it is a view of Layers, so it cannot disagree with them,
// and it stays out of the plan hash.
//
// Leaf paths, which means a list counts as one value -- helm replaces a list
// wholesale rather than appending, so an extraArgs in a later layer really does
// drop every flag the earlier one set. That is the most important row this
// report ever prints.
func (p *Plan) Shadowed() []Shadow {
	setters := map[string][]string{}
	for _, layer := range Layers {
		tree, ok := p.Layers[layer]
		if !ok {
			continue
		}
		for _, path := range values.Paths(tree) {
			setters[path] = append(setters[path], layer)
		}
	}
	var out []Shadow
	for path, layers := range setters {
		if len(layers) < 2 {
			continue
		}
		out = append(out, Shadow{Path: path, By: layers[len(layers)-1], Under: layers[:len(layers)-1]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// ByLayer lists the paths each layer set, for display.
func (p *Plan) ByLayer() map[string][]string {
	out := map[string][]string{}
	for _, layer := range Layers {
		tree, ok := p.Layers[layer]
		if !ok {
			continue
		}
		paths := values.Paths(tree)
		sort.Strings(paths)
		out[layer] = paths
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

// Layers are the values documents a release is applied through, in merge order.
var Layers = []string{values.LayerCatalog, values.LayerSite, values.LayerDerived, values.LayerForm, values.LayerEdit}

// LayerValues splits the composed values by the layer that set each path, so a
// release is applied through one document per layer rather than one merged file.
// Layers that contributed nothing are omitted.
func (p *Plan) LayerValues() map[string]values.Tree { return p.Layers }

// ValuesFiles names the documents LayerValues produces, in merge order.
func (p *Plan) ValuesFiles() []string {
	present := p.LayerValues()
	var out []string
	for _, layer := range Layers {
		if _, ok := present[layer]; ok {
			out = append(out, layer+".yaml")
		}
	}
	if len(out) == 0 {
		return []string{"values.yaml"}
	}
	return out
}

// MetaFile is the key holding everything but the values documents: identity,
// the catalog and chart locks, and the hash. Named so a mounted directory is
// self-describing rather than just runnable.
const MetaFile = "plan.yaml"

// HelmfileFile is the release declaration helmfile is pointed at.
const HelmfileFile = "helmfile.yaml"

// Files is the deploy as a directory: helmfile.yaml, one values file per layer,
// and the metadata. This is both what the plan ConfigMap stores and what a
// workspace is materialised from -- one definition, so a mounted ConfigMap is
// exactly the tree helmfile runs against.
func (p *Plan) Files(chartRoot string) (map[string]string, error) {
	out := map[string]string{}
	for layer, tree := range p.Layers {
		doc, err := yaml.Marshal(tree)
		if err != nil {
			return nil, err
		}
		out[layer+".yaml"] = string(doc)
	}

	// Best effort: a profile naming neither a chart repo nor a path leaves this
	// unrenderable, and the runner resolves it against --chart-root instead.
	if doc, err := p.RenderHelmfile(chartRoot); err == nil {
		out[HelmfileFile] = doc
	}

	// The metadata carries no values: those are the files beside it.
	meta := *p
	meta.Layers, meta.Helmfile = nil, ""
	doc, err := meta.YAML()
	if err != nil {
		return nil, err
	}
	out[MetaFile] = string(doc)
	return out, nil
}

// FromFiles rebuilds a plan from that directory.
func FromFiles(files map[string]string) (*Plan, error) {
	raw, ok := files[MetaFile]
	if !ok {
		return nil, fmt.Errorf("no %s", MetaFile)
	}
	p, err := ParseYAML([]byte(raw))
	if err != nil {
		return nil, err
	}
	p.Helmfile = files[HelmfileFile]
	p.Layers = map[string]values.Tree{}
	for _, layer := range Layers {
		doc, ok := files[layer+".yaml"]
		if !ok {
			continue
		}
		var tree values.Tree
		if err := yaml.Unmarshal([]byte(doc), &tree); err != nil {
			return nil, fmt.Errorf("%s.yaml: %w", layer, err)
		}
		if len(tree) > 0 {
			p.Layers[layer] = tree
		}
	}
	return p, nil
}

// ParseYAML reads a plan stored beside a release. It goes through JSON because
// the document was written that way: the field names are the json tags.
func ParseYAML(raw []byte) (*Plan, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var p Plan
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	return &p, nil
}
