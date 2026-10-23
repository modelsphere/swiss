package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/modelsphere/swiss/internal/autoscale"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/values"
)

const (
	autoscalePrefix    = "swiss-autoscale-"
	autoscaleConfigKey = "config.yaml"
	autoscaleStatusKey = "status.yaml"
)

func autoscaleRef(namespace, release string) string {
	return namespace + "/" + autoscalePrefix + release
}

func autoscaleLabels(release string) map[string]string {
	return map[string]string{"owner": "swiss", "name": release}
}

func (s *Server) handleAutoscaling(w http.ResponseWriter, r *http.Request) {
	if (r.Method == http.MethodPut || r.Method == http.MethodDelete) && !s.cfg.Server.AllowDeploy {
		writeError(w, http.StatusForbidden, "this swissd is read-only: set server.allowDeploy")
		return
	}
	ns, release := r.PathValue("namespace"), r.PathValue("release")
	if ns == "" || release == "" {
		writeError(w, http.StatusBadRequest, "namespace and release are required")
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	switch r.Method {
	case http.MethodGet:
		view, err := s.readAutoscale(ctx, ns, release)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, view)
	case http.MethodPut:
		s.putAutoscale(ctx, w, r, ns, release)
	case http.MethodDelete:
		s.deleteAutoscale(ctx, w, ns, release)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) putAutoscale(ctx context.Context, w http.ResponseWriter, r *http.Request, ns, release string) {
	var cfg autoscale.Config
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if cfg.MinSlices < 1 {
		writeError(w, http.StatusBadRequest, "minSlices must be >= 1")
		return
	}
	if cfg.MinSlices > cfg.MaxSlices {
		writeError(w, http.StatusBadRequest, "minSlices must be <= maxSlices")
		return
	}
	if s.writer == nil {
		writeError(w, http.StatusBadGateway, "no cluster writer")
		return
	}
	b, _, err := s.backendFor(ctx, ns, release)
	if err != nil {
		writeStatus(w, err, http.StatusBadGateway)
		return
	}
	if b == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("release %s/%s is not managed by swiss", ns, release))
		return
	}
	p, err := b.Current(ctx, ns, release)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if !disaggregated(p) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("release %s/%s is not disaggregated: pd.enabled must be true and pd.colocate must not be", ns, release))
		return
	}
	if err := s.writeAutoscaleConfig(ctx, ns, release, cfg); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	view, err := s.readAutoscale(ctx, ns, release)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) deleteAutoscale(ctx context.Context, w http.ResponseWriter, ns, release string) {
	if s.writer == nil {
		writeError(w, http.StatusBadGateway, "no cluster writer")
		return
	}
	b, _, err := s.backendFor(ctx, ns, release)
	if err != nil {
		writeStatus(w, err, http.StatusBadGateway)
		return
	}
	if b == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("release %s/%s is not managed by swiss", ns, release))
		return
	}
	if err := s.writer.DeleteConfigMap(ctx, autoscaleRef(ns, release)); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, autoscaleView{Found: false})
}

type autoscaleView struct {
	Found  bool              `json:"found"`
	Config *autoscale.Config `json:"config,omitempty"`
	Status *autoscale.Status `json:"status,omitempty"`
}

func (s *Server) readAutoscale(ctx context.Context, ns, release string) (autoscaleView, error) {
	data, err := s.probe.ConfigMap(ctx, autoscaleRef(ns, release))
	if err != nil {
		if apierrors.IsNotFound(err) {
			return autoscaleView{}, nil
		}
		return autoscaleView{}, err
	}
	if len(data) == 0 || data[autoscaleConfigKey] == "" {
		return autoscaleView{}, nil
	}
	var cfg autoscale.Config
	if err := yaml.Unmarshal([]byte(data[autoscaleConfigKey]), &cfg); err != nil {
		return autoscaleView{}, fmt.Errorf("config.yaml: %w", err)
	}
	st := &autoscale.Status{}
	if raw := data[autoscaleStatusKey]; raw != "" {
		if err := yaml.Unmarshal([]byte(raw), st); err != nil {
			return autoscaleView{}, fmt.Errorf("status.yaml: %w", err)
		}
	}
	return autoscaleView{Found: true, Config: &cfg, Status: st}, nil
}

func (s *Server) writeAutoscaleConfig(ctx context.Context, ns, release string, cfg autoscale.Config) error {
	ref := autoscaleRef(ns, release)
	data, err := s.probe.ConfigMap(ctx, ref)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	next := map[string]string{}
	for k, v := range data {
		next[k] = v
	}
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	next[autoscaleConfigKey] = string(b)
	return s.writer.PutLabeledConfigMap(ctx, ref, next, autoscaleLabels(release))
}

func (s *Server) writeAutoscaleStatus(ctx context.Context, ns, release string, fn func(*autoscale.Status)) error {
	if s.writer == nil {
		return fmt.Errorf("no cluster writer")
	}
	ref := autoscaleRef(ns, release)
	data, err := s.probe.ConfigMap(ctx, ref)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if data == nil || data[autoscaleConfigKey] == "" {
		return nil
	}
	next := map[string]string{}
	for k, v := range data {
		next[k] = v
	}
	var st autoscale.Status
	if raw := data[autoscaleStatusKey]; raw != "" {
		if err := yaml.Unmarshal([]byte(raw), &st); err != nil {
			return err
		}
	}
	fn(&st)
	b, err := yaml.Marshal(&st)
	if err != nil {
		return err
	}
	next[autoscaleStatusKey] = string(b)
	return s.writer.PutLabeledConfigMap(ctx, ref, next, autoscaleLabels(release))
}

func (s *Server) autoscalingEnabled(ctx context.Context, ns, release string) (bool, error) {
	data, err := s.probe.ConfigMap(ctx, autoscaleRef(ns, release))
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if len(data) == 0 || data[autoscaleConfigKey] == "" {
		return false, nil
	}
	var cfg autoscale.Config
	if err := yaml.Unmarshal([]byte(data[autoscaleConfigKey]), &cfg); err != nil {
		return false, err
	}
	return cfg.Enabled, nil
}

// removeAutoscale deletes the autoscale ConfigMap when one exists. A missing
// map is not an error, so an uninstall that never enabled autoscaling stays
// a single plan-ConfigMap delete.
func (s *Server) removeAutoscale(ctx context.Context, ns, release string) error {
	if s.probe == nil || s.writer == nil {
		return nil
	}
	ref := autoscaleRef(ns, release)
	data, err := s.probe.ConfigMap(ctx, ref)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if data == nil {
		return nil
	}
	return s.writer.DeleteConfigMap(ctx, ref)
}

// overlayAutoscale puts the live autoscale layer onto an archived plan while
// autoscaling is enabled, then recomputes the hash. No live layer leaves the
// archive as stored.
func (s *Server) overlayAutoscale(ctx context.Context, ns, release string, p *plan.Plan) error {
	on, err := s.autoscalingEnabled(ctx, ns, release)
	if err != nil {
		return failErr(http.StatusBadGateway, err)
	}
	if !on || p == nil {
		return nil
	}
	cur, err := s.currentPlan(ctx, ns, release)
	if err != nil {
		return failErr(http.StatusBadGateway, err)
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

func fullnameOf(p *plan.Plan, release string) string {
	if p == nil {
		return release
	}
	v, ok := values.Get(p.Values(), "fullnameOverride")
	if !ok {
		return release
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return release
	}
	return s
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

func intPtr(n int) *int { return &n }
