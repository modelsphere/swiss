package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/modelsphere/swiss/internal/chart"
	"gopkg.in/yaml.v3"
)

// helmValues is what an install hands helm, as YAML: the chart's values.yaml
// verbatim, comments and all, and the overrides swiss merges from the plan's
// layers in order, a null kept for helm to delete that default.
type helmValues struct {
	Defaults  string `json:"defaults"`
	Overrides string `json:"overrides"`
}

type composeV2Response struct {
	HelmValues *helmValues `json:"helmValues,omitempty"`
}

// handleComposeV2 is v2 compose, experimental and incomplete: its request,
// response and errors may change. Only its dry run is served: the plan request
// composes as /api/plans does and nothing is stored, so there is no plan to
// diff or apply. dumpHelmValues adds the values helm would be handed.
func (s *Server) handleComposeV2(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, time.Minute)
	defer cancel()

	dryRun, err := queryBool(r, "dryRun")
	if err == nil && !dryRun {
		err = errors.New("only dryRun=true is served")
	}
	dump, dumpErr := queryBool(r, "dumpHelmValues")
	if err != nil || dumpErr != nil {
		writeCodedError(w, http.StatusBadRequest, "INVALID_QUERY", errors.Join(err, dumpErr).Error())
		return
	}
	var req planRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCodedError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	p, err := s.compose(ctx, req)
	if err != nil {
		code, name := http.StatusBadRequest, "INVALID_REQUEST"
		switch {
		case errors.As(err, new(*chart.ListError)):
			code, name = http.StatusBadGateway, "CATALOG_UNAVAILABLE"
		case errors.As(err, new(*gpuInventoryError)):
			code, name = http.StatusBadGateway, "GPU_INVENTORY_UNAVAILABLE"
		}
		writeCodedError(w, code, name, err.Error())
		return
	}
	var out composeV2Response
	if dump {
		defaults, err := s.runner().ChartValues(ctx, p)
		if err != nil {
			writeCodedError(w, http.StatusBadGateway, "CHART_VALUES_UNAVAILABLE", err.Error())
			return
		}
		var overrides string
		if vals := p.Values(); len(vals) > 0 {
			b, err := yaml.Marshal(vals)
			if err != nil {
				writeCodedError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
				return
			}
			overrides = string(b)
		}
		out.HelmValues = &helmValues{Defaults: defaults, Overrides: overrides}
	}
	writeJSON(w, http.StatusOK, out)
}

// queryBool reads true or false; absent is false.
func queryBool(r *http.Request, name string) (bool, error) {
	switch v := r.URL.Query().Get(name); v {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, fmt.Errorf("%s=%q: want true or false", name, v)
	}
}

func writeCodedError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}
