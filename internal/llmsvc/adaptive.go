package llmsvc

import (
	"k8s.io/apimachinery/pkg/runtime"
	"strings"
)

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
