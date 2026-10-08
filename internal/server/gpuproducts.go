package server

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/modelsphere/swiss/internal/cluster"
)

// gpuInventoryError is a forced GPU product that could not be checked because
// the node list was unreadable: the cluster's failure, not the request's.
type gpuInventoryError struct{ err error }

func (e *gpuInventoryError) Error() string {
	return "forceGpuProducts: cannot read this cluster's GPU products to check against: " + e.err.Error()
}

func (e *gpuInventoryError) Unwrap() error { return e.err }

type productCount struct{ nodes, gpus int }

// clusterProducts is every GPU product on the cluster under the variant's
// extended resource. A node with no recorded resource counts: unknown is not a
// mismatch.
func clusterProducts(nodes []cluster.Node, r catalog.Requires) map[string]productCount {
	res := r.ResourceName()
	out := map[string]productCount{}
	for _, n := range nodes {
		if n.GPUProduct == "" || (n.GPUResource != "" && n.GPUResource != res) {
			continue
		}
		c := out[n.GPUProduct]
		c.nodes++
		c.gpus += n.GPUs
		out[n.GPUProduct] = c
	}
	return out
}

func listOrNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}

// checkGPUProducts refuses requested products the variant does not list, unless
// forced; a forced product must be on the cluster. Listed products are never
// checked against the cluster, forced or not, so forcing only ever widens what
// is accepted. Returns a warning per forced product.
func (s *Server) checkGPUProducts(ctx context.Context, v catalog.Variant, products []string, force bool) ([]string, error) {
	listed := v.Requires.GPUProduct
	if len(listed) == 0 {
		return nil, nil
	}
	var off []string
	for _, p := range products {
		if !slices.Contains(listed, p) && !slices.Contains(off, p) {
			off = append(off, p)
		}
	}
	if len(off) == 0 {
		return nil, nil
	}
	if !force {
		return nil, fmt.Errorf("variant %s supports GPU products %s, not %s -- set forceGpuProducts to deploy on a product on this cluster that the catalog does not list",
			v.ID, strings.Join(listed, ", "), strings.Join(off, ", "))
	}
	nodes, err := s.probe.Nodes(ctx)
	if err != nil {
		return nil, &gpuInventoryError{err}
	}
	have := clusterProducts(nodes, v.Requires)
	var missing []string
	for _, p := range off {
		if _, ok := have[p]; !ok {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		present := make([]string, 0, len(have))
		for p := range have {
			present = append(present, p)
		}
		sort.Strings(present)
		return nil, fmt.Errorf("forceGpuProducts: %s not on this cluster as %s (have: %s)",
			strings.Join(missing, ", "), v.Requires.ResourceName(), listOrNone(present))
	}
	warnings := make([]string, 0, len(off))
	for _, p := range off {
		warnings = append(warnings, fmt.Sprintf(
			"GPU product %s is not supported by variant %s in the catalog (supports %s); forced, so the variant's GPU count, image and values are used as published, untested on %s",
			p, v.ID, strings.Join(listed, ", "), p))
	}
	return warnings, nil
}

type gpuProductOption struct {
	Product string `json:"product"`
	// CatalogSupported is true when the variant lists the product, or lists
	// none and so takes any of its vendor's. False means forceGpuProducts.
	CatalogSupported bool `json:"catalogSupported"`
	OnCluster        bool `json:"onCluster"`
	Nodes            int  `json:"nodes"`
	GPUs             int  `json:"gpus"`
}

// handleGPUProducts is what a deploy form may offer for gpuProducts: the
// variant's listed products and every product on the cluster under its vendor.
// An unreadable node list still answers with the listed ones, and says why.
func (s *Server) handleGPUProducts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	v, ok := s.variantFor(ctx, w, r)
	if !ok {
		return
	}
	listed := v.Requires.GPUProduct
	if listed == nil {
		listed = []string{}
	}
	byProduct := map[string]*gpuProductOption{}
	for _, p := range listed {
		byProduct[p] = &gpuProductOption{Product: p, CatalogSupported: true}
	}
	body := map[string]any{
		"variant":  v.ID,
		"vendor":   v.Requires.VendorOrDefault(),
		"resource": v.Requires.ResourceName(),
		"label":    v.Requires.ProductLabel(),
		"catalog":  listed,
	}
	nodes, err := s.probe.Nodes(ctx)
	if err != nil {
		body["inventoryError"] = err.Error()
	}
	for p, c := range clusterProducts(nodes, v.Requires) {
		o, ok := byProduct[p]
		if !ok {
			o = &gpuProductOption{Product: p, CatalogSupported: len(listed) == 0}
			byProduct[p] = o
		}
		o.OnCluster, o.Nodes, o.GPUs = true, c.nodes, c.gpus
	}
	out := make([]gpuProductOption, 0, len(byProduct))
	for _, o := range byProduct {
		out = append(out, *o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Product < out[j].Product })
	body["products"] = out
	writeJSON(w, http.StatusOK, body)
}
