package llmsvc

import (
	"k8s.io/apimachinery/pkg/runtime"
	"strings"
)

const AdaptiveBaselineKey = "canary.modelsphere.dev/adaptive-baseline"

func AdaptiveKey(key string) bool {
	return key == "adaptive_cc" || strings.HasPrefix(key, "adaptive_cc_")
}

func WithoutAdaptiveValues(values map[string]any) map[string]any {
	if values == nil {
		return map[string]any{}
	}
	copy := runtime.DeepCopyJSONValue(values).(map[string]any)
	stripAdaptive(copy, "modelRoute", "nginx", "values")
	return copy
}

func stripAdaptive(tree map[string]any, path ...string) {
	if len(path) == 0 {
		for key := range tree {
			if AdaptiveKey(key) {
				delete(tree, key)
			}
		}
		return
	}
	child, ok := tree[path[0]].(map[string]any)
	if !ok {
		return
	}
	stripAdaptive(child, path[1:]...)
	if len(child) == 0 {
		delete(tree, path[0])
	}
}

func WithoutAdaptiveSpec(spec map[string]any) map[string]any {
	copy := runtime.DeepCopyJSONValue(spec).(map[string]any)
	layers, ok := copy["layers"].([]any)
	if !ok {
		return copy
	}
	result := make([]any, 0, len(layers))
	for _, raw := range layers {
		layer, ok := raw.(map[string]any)
		if !ok {
			result = append(result, raw)
			continue
		}
		if layer["name"] == "form" {
			if values, ok := layer["values"].(map[string]any); ok {
				layer["values"] = WithoutAdaptiveValues(values)
				if len(layer["values"].(map[string]any)) == 0 {
					continue
				}
			}
		}
		result = append(result, layer)
	}
	copy["layers"] = result
	return copy
}
