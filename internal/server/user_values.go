package server

import (
	"fmt"
	"strings"

	"github.com/modelsphere/swiss/internal/values"
	"gopkg.in/yaml.v3"
)

func normalizeUserValues(req planRequest) (planRequest, error) {
	if req.UserValues == nil {
		return req, nil
	}
	if len(req.Overrides) > 0 || strings.TrimSpace(req.OverridesYAML) != "" || strings.TrimSpace(req.EditsYAML) != "" || req.ReuseSource {
		return req, fmt.Errorf("userValues cannot be combined with overrides, overridesYAML, editsYAML or reuseSource")
	}
	if enabled, _ := values.Get(*req.UserValues, "scaler.enabled"); enabled == true {
		if value, fixed := (*req.UserValues)["replicaCount"]; fixed && value != nil {
			return req, fmt.Errorf("userValues.replicaCount cannot be set while scaler.enabled is true")
		}
	}
	document, err := yaml.Marshal(*req.UserValues)
	if err != nil {
		return req, fmt.Errorf("userValues: %w", err)
	}
	req.EditsYAML = string(document)
	return req, nil
}

func validateUserIdentity(req planRequest, release string) error {
	if req.UserValues == nil {
		return nil
	}
	identity := req.ServiceID
	if identity == "" {
		identity = release
	}
	if actual, present := values.Get(*req.UserValues, "serviceId"); present && actual != identity {
		return fmt.Errorf("userValues.serviceId must match the service identity %q", identity)
	}
	return nil
}
