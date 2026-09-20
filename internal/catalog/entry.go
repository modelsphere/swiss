// Package catalog reads swiss-catalog entries: one model, and the variants it
// can be served as.
//
// Everything here re-checks what the catalog's own CI already checks. That is
// not redundancy -- the catalog is a public repo fetched over the network, so it
// is untrusted input at the point of use, and a consumer that trusts a remote
// file because some CI somewhere was supposed to have run is not validating, it
// is hoping.
package catalog

import (
	"fmt"

	"github.com/aceforeverd/swiss/internal/values"
)

const APIVersion = "catalog.swiss/v1"

type Entry struct {
	APIVersion  string    `yaml:"apiVersion" json:"apiVersion"`
	Name        string    `yaml:"name" json:"name"`
	ServedName  string    `yaml:"servedName,omitempty" json:"servedName,omitempty"`
	DisplayName string    `yaml:"displayName,omitempty" json:"displayName,omitempty"`
	Description string    `yaml:"description,omitempty" json:"description,omitempty"`
	Family      string    `yaml:"family,omitempty" json:"family,omitempty"`
	License     string    `yaml:"license,omitempty" json:"license,omitempty"`
	Homepage    string    `yaml:"homepage,omitempty" json:"homepage,omitempty"`
	Tags        []string  `yaml:"tags,omitempty" json:"tags,omitempty"`
	Deprecated  any       `yaml:"deprecated,omitempty" json:"deprecated,omitempty"`
	Source      Source    `yaml:"source" json:"source"`
	Variants    []Variant `yaml:"variants" json:"variants"`
}

// Source is model identity, never a location. The charts mount weights from
// model.localPath on the host, which is a fact about a cluster -- so the path is
// the site profile's to supply, derived from HF.
type Source struct {
	HF            string   `yaml:"hf" json:"hf"`
	Revision      string   `yaml:"revision,omitempty" json:"revision,omitempty"`
	SizeGiB       float64  `yaml:"sizeGiB,omitempty" json:"sizeGiB,omitempty"`
	RequiredGlobs []string `yaml:"requiredGlobs,omitempty" json:"requiredGlobs,omitempty"`
}

// Variant is a hardware and parallelism decision. Its fields only ever move
// together, which is why they are one object rather than independent form
// fields: nothing should be able to select --tp-size=8 and a 2-GPU node.
type Variant struct {
	ID          string      `yaml:"id" json:"id"`
	Description string      `yaml:"description,omitempty" json:"description,omitempty"`
	Default     bool        `yaml:"default,omitempty" json:"default,omitempty"`
	Engine      string      `yaml:"engine" json:"engine"`
	Chart       Chart       `yaml:"chart" json:"chart"`
	Image       *Image      `yaml:"image,omitempty" json:"image,omitempty"`
	Requires    Requires    `yaml:"requires" json:"requires"`
	Values      values.Tree `yaml:"values,omitempty" json:"values,omitempty"`
}

// Chart carries a name and a version, never a repository: a public catalog
// cannot know which registry a reader mirrors into.
type Chart struct {
	Name    string `yaml:"name" json:"name"`
	Version string `yaml:"version" json:"version"`
}

type Image struct {
	Repository string `yaml:"repository" json:"repository"`
	Tag        string `yaml:"tag" json:"tag"`
	Digest     string `yaml:"digest,omitempty" json:"digest,omitempty"`
}

type Requires struct {
	// GPUs is per pod, not per group. A two-pod lws group at 8 needs 16.
	GPUs            int      `yaml:"gpus" json:"gpus"`
	Nodes           int      `yaml:"nodes,omitempty" json:"nodes,omitempty"`
	Topology        string   `yaml:"topology,omitempty" json:"topology,omitempty"`
	GPUProduct      []string `yaml:"gpuProduct,omitempty" json:"gpuProduct,omitempty"`
	MinGPUMemoryGiB float64  `yaml:"minGpuMemoryGiB,omitempty" json:"minGpuMemoryGiB,omitempty"`
	RDMA            bool     `yaml:"rdma,omitempty" json:"rdma,omitempty"`
}

const (
	TopologySingleNode = "single-node"
	TopologyLWS        = "lws"
)

func (r Requires) TopologyOrDefault() string {
	if r.Topology == "" {
		return TopologySingleNode
	}
	return r.Topology
}

func (r Requires) NodesOrDefault() int {
	if r.Nodes < 1 {
		return 1
	}
	return r.Nodes
}

// TotalGPUs across a whole group.
func (r Requires) TotalGPUs() int { return r.GPUs * r.NodesOrDefault() }

// ServedModelName is what the engine advertises (--served-model-name).
func (e Entry) ServedModelName() string {
	if e.ServedName != "" {
		return e.ServedName
	}
	return e.Name
}

// RequiredGlobsOrDefault mirrors the charts' own default.
func (s Source) RequiredGlobsOrDefault() []string {
	if len(s.RequiredGlobs) > 0 {
		return s.RequiredGlobs
	}
	return []string{"config.json"}
}

// Variant looks up a variant by id.
func (e Entry) Variant(id string) (Variant, error) {
	for _, v := range e.Variants {
		if v.ID == id {
			return v, nil
		}
	}
	var ids []string
	for _, v := range e.Variants {
		ids = append(ids, v.ID)
	}
	return Variant{}, fmt.Errorf("model %q has no variant %q (have: %v)", e.Name, id, ids)
}

// DefaultVariant is the one marked default, else the first. Entries are
// validated to carry at most one default.
func (e Entry) DefaultVariant() (Variant, error) {
	if len(e.Variants) == 0 {
		return Variant{}, fmt.Errorf("model %q has no variants", e.Name)
	}
	for _, v := range e.Variants {
		if v.Default {
			return v, nil
		}
	}
	return e.Variants[0], nil
}
