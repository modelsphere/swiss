package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/values"
)

// autoscaleOn is the whole gate. A release scales when its plan is
// disaggregated and the form layer turned the SLO requirement on.
func autoscaleOn(p *plan.Plan) bool {
	return disaggregated(p) && formSLO(p)
}

func formSLO(p *plan.Plan) bool {
	if p == nil {
		return false
	}
	on, ok := formBool(p.Layers[values.LayerForm], "sloRequirement.enabled")
	return ok && on
}

// formKeepsAutoscale reports whether an upgrade's form still has the rule on.
// A key the request does not set stays as the previous form left it.
func formKeepsAutoscale(form values.Tree) bool {
	if on, ok := formBool(form, "sloRequirement.enabled"); ok && !on {
		return false
	}
	if on, ok := formBool(form, "pd.enabled"); ok && !on {
		return false
	}
	if col, ok := formBool(form, "pd.colocate"); ok && col {
		return false
	}
	return true
}

func formBool(form values.Tree, path string) (bool, bool) {
	v, ok := values.Get(form, path)
	if !ok {
		return false, false
	}
	b, isBool := v.(bool)
	if !isBool {
		return false, false
	}
	return b, true
}

// overlayAutoscale puts the live autoscale layer onto an archived plan while
// the live plan is autoscaling, then recomputes the hash. No live layer leaves
// the archive as stored.
func (s *Server) overlayAutoscale(ctx context.Context, ns, release string, p *plan.Plan) error {
	if p == nil {
		return nil
	}
	cur, err := s.currentPlan(ctx, ns, release)
	if err != nil {
		return failErr(http.StatusBadGateway, err)
	}
	if !autoscaleOn(cur) {
		return nil
	}
	layer := cur.Layers[values.LayerAutoscale]
	if len(layer) == 0 {
		return nil
	}
	cp := make(map[string]values.Tree, len(p.Layers)+1)
	for k, v := range p.Layers {
		cp[k] = v
	}
	cp[values.LayerAutoscale] = cloneTree(layer)
	p.Layers = cp
	if err := p.ComputeHash(); err != nil {
		return failErr(http.StatusInternalServerError, err)
	}
	if s.store != nil {
		if err := s.store.PutPlan(ctx, p); err != nil {
			return failErr(http.StatusInternalServerError, err)
		}
	}
	return nil
}

func disaggregated(p *plan.Plan) bool {
	if p == nil {
		return false
	}
	v, ok := values.Get(p.Values(), "pd.enabled")
	on, _ := v.(bool)
	if !ok || !on {
		return false
	}
	if c, ok := values.Get(p.Values(), "pd.colocate"); ok {
		if b, ok := c.(bool); ok && b {
			return false
		}
	}
	return true
}

func planReplicaCount(p *plan.Plan) int {
	if p == nil {
		return 0
	}
	v, ok := values.Get(p.Values(), "replicaCount")
	if !ok {
		return 0
	}
	n, ok := coerceInt(v)
	if !ok || n < 0 {
		return 0
	}
	return n
}

func coerceInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float64:
		if n != float64(int64(n)) {
			return 0, false
		}
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	case string:
		i, err := strconv.Atoi(n)
		return i, err == nil
	default:
		return 0, false
	}
}

// autoscaleServiceID is the id the decision server is queried with. An explicit
// serviceId wins; otherwise the chart fullname, which is what the chart renders
// when serviceId is unset. The upgrade check keeps serviceIDOf, the release name.
func autoscaleServiceID(p *plan.Plan) string {
	if p != nil {
		if v, ok := values.Get(p.Values(), "serviceId"); ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	release := ""
	if p != nil {
		release = p.Release.Name
	}
	return fullnameOf(p, release)
}

// fullnameOf is the chart fullname: fullnameOverride, else the release name,
// truncated to 63 characters, with one trailing "-" removed.
func fullnameOf(p *plan.Plan, release string) string {
	name := release
	if p != nil {
		if v, ok := values.Get(p.Values(), "fullnameOverride"); ok {
			if s, ok := v.(string); ok && s != "" {
				name = s
			}
		}
	}
	if len(name) > 63 {
		name = name[:63]
	}
	return strings.TrimSuffix(name, "-")
}

func cloneTree(t values.Tree) values.Tree {
	if t == nil {
		return nil
	}
	out := make(values.Tree, len(t))
	for k, v := range t {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	switch x := v.(type) {
	case values.Tree:
		return cloneTree(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, child := range x {
			out[k] = cloneValue(child)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = cloneValue(x[i])
		}
		return out
	default:
		return v
	}
}

func clonePlan(p *plan.Plan) *plan.Plan {
	if p == nil {
		return nil
	}
	cp := *p
	cp.Layers = make(map[string]values.Tree, len(p.Layers))
	for k, v := range p.Layers {
		cp.Layers[k] = cloneTree(v)
	}
	if p.Warnings != nil {
		cp.Warnings = append([]string(nil), p.Warnings...)
	}
	return &cp
}
