package catalog

import (
	"bytes"
	"fmt"

	"github.com/modelsphere/swiss/internal/values"
	"gopkg.in/yaml.v3"
)

// VariantV2 is one v2 variant file, models/<model>/<id>.yaml: edited in place,
// its version raised on every change.
type VariantV2 struct {
	APIVersion  string      `yaml:"apiVersion" json:"apiVersion"`
	Kind        string      `yaml:"kind" json:"kind"`
	Model       string      `yaml:"model" json:"model"`
	ID          string      `yaml:"id" json:"id"`
	Version     string      `yaml:"version" json:"version"`
	ServedName  string      `yaml:"servedName,omitempty" json:"servedName,omitempty"`
	Description string      `yaml:"description,omitempty" json:"description,omitempty"`
	Link        string      `yaml:"link,omitempty" json:"link,omitempty"`
	Engine      string      `yaml:"engine" json:"engine"`
	Chart       Chart       `yaml:"chart" json:"chart"`
	Image       *Image      `yaml:"image,omitempty" json:"image,omitempty"`
	Requires    Requires    `yaml:"requires" json:"requires"`
	Values      values.Tree `yaml:"values,omitempty" json:"values,omitempty"`
	// Deprecated comes before deleting a file; a string is the reason.
	Deprecated any `yaml:"deprecated,omitempty" json:"deprecated,omitempty"`
	// Replaces is the id of a variant this one renames or supersedes.
	Replaces string `yaml:"replaces,omitempty" json:"replaces,omitempty"`
}

// ParseVariantV2 decodes a v2 variant file. KnownFields stays strict, as for a
// v1 entry: a typo in values or requires must not silently drop.
func ParseVariantV2(raw []byte) (*VariantV2, error) {
	var d VariantV2
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		return nil, err
	}
	switch {
	case d.APIVersion != APIVersionV2:
		return nil, fmt.Errorf("apiVersion is %q, want %q", d.APIVersion, APIVersionV2)
	case d.Kind != "Variant":
		return nil, fmt.Errorf("kind is %q, want Variant", d.Kind)
	case d.Model == "" || d.ID == "":
		return nil, fmt.Errorf("model and id are required")
	case d.Version == "":
		return nil, fmt.Errorf("version is required -- a deploy pins one")
	}
	return &d, nil
}

// variant is the file as the shared Variant a deploy composes from. Default is
// the index's to say, not the file's.
func (d *VariantV2) variant(isDefault bool) Variant {
	return Variant{
		ID: d.ID, Description: d.Description, Link: d.Link, Default: isDefault,
		Engine: d.Engine, Chart: d.Chart, Image: d.Image, Requires: d.Requires, Values: d.Values,
	}
}
