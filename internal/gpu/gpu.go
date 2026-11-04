// Package gpu is the GPU spec table: the extended resource a vendor's nodes
// advertise, and the node label naming each product under it.
package gpu

import "slices"

type Product struct {
	Product string `yaml:"product" json:"product"`
	// Values are other label values that also name Product. Product itself
	// always does: they add to it, never replace it.
	Values []string `yaml:"values,omitempty" json:"values,omitempty"`
}

type GPU struct {
	Vendor   string `yaml:"vendor" json:"vendor"`
	Resource string `yaml:"resource" json:"resource"`
	// LabelKey tells products apart under Resource. One key is enough: the
	// resource request already keeps a pod to this vendor's nodes.
	LabelKey string    `yaml:"labelKey,omitempty" json:"labelKey,omitempty"`
	Products []Product `yaml:"products,omitempty" json:"products,omitempty"`
}

// Table is ordered: a vendor's first entry is its default resource.
type Table []GPU

// Lookup is the entry for an extended resource.
func (t Table) Lookup(resource string) (GPU, bool) {
	i := slices.IndexFunc(t, func(g GPU) bool { return g.Resource == resource })
	if i < 0 {
		return GPU{}, false
	}
	return t[i], true
}

// Default is the resource a vendor's pods request when inventory names none;
// empty for a vendor the table does not know.
func (t Table) Default(vendor string) string {
	for _, g := range t {
		if g.Vendor == vendor {
			return g.Resource
		}
	}
	return ""
}

// Resources is every resource counted as GPUs.
func (t Table) Resources() []string {
	out := make([]string, 0, len(t))
	for _, g := range t {
		out = append(out, g.Resource)
	}
	return out
}

// Product is what a node whose LabelKey reads raw carries: the product raw
// names, else raw itself.
func (g GPU) Product(raw string) string {
	for _, p := range g.Products {
		if raw == p.Product || slices.Contains(p.Values, raw) {
			return p.Product
		}
	}
	return raw
}

// Values are the label values naming product: product, then its values.
func (g GPU) Values(product string) []string {
	out := []string{product}
	for _, p := range g.Products {
		if p.Product != product {
			continue
		}
		for _, v := range p.Values {
			if !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
	}
	return out
}
