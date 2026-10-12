package catalog

import (
	"bytes"
	"fmt"

	"github.com/modelsphere/swiss/internal/values"
	"gopkg.in/yaml.v3"
)

// variantDoc is one v2 variant file (apiVersion catalog.swiss/v2, kind Variant).
// It is normalized into catalog.Variant (+ Entry cosmetics) on load.
type variantDoc struct {
	APIVersion  string      `yaml:"apiVersion"`
	Kind        string      `yaml:"kind"`
	Model       string      `yaml:"model"`
	ID          string      `yaml:"id"`
	Version     string      `yaml:"version,omitempty"`
	ServedName  string      `yaml:"servedName,omitempty"`
	Description string      `yaml:"description,omitempty"`
	Link        string      `yaml:"link,omitempty"`
	Engine      string      `yaml:"engine"`
	Chart       Chart       `yaml:"chart"`
	Image       *Image      `yaml:"image,omitempty"`
	Requires    Requires    `yaml:"requires"`
	Values      values.Tree `yaml:"values,omitempty"`
}

// ParseVariantDoc decodes a v2 variant file. KnownFields stays strict: a typo
// in values or requires must not silently drop.
func ParseVariantDoc(raw []byte) (*variantDoc, error) {
	var d variantDoc
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		return nil, err
	}
	if d.APIVersion != APIVersionV2 {
		return nil, fmt.Errorf("apiVersion is %q, want %q", d.APIVersion, APIVersionV2)
	}
	if d.Kind != "Variant" {
		return nil, fmt.Errorf("kind is %q, want Variant", d.Kind)
	}
	if d.Model == "" || d.ID == "" {
		return nil, fmt.Errorf("model and id are required")
	}
	if d.Engine == "" {
		return nil, fmt.Errorf("engine is required")
	}
	return &d, nil
}

func (d *variantDoc) toVariant(pin IndexVariant) Variant {
	return Variant{
		ID:          d.ID,
		Description: d.Description,
		Link:        d.Link,
		Default:     pin.Default,
		Engine:      d.Engine,
		Chart:       d.Chart,
		Image:       d.Image,
		Requires:    d.Requires,
		Values:      d.Values,
		Digest:      pin.Digest,
		Version:     pin.Version,
		Path:        pin.Path,
	}
}
