package llmsvc

import (
	"reflect"
	"testing"
)

func TestAdaptiveSpecScope(t *testing.T) {
	before := map[string]any{"chart": map[string]any{"version": "1"}, "layers": []any{map[string]any{"name": "catalog", "values": map[string]any{"model": map[string]any{"gpus": int64(1)}}}}}
	after := map[string]any{"chart": map[string]any{"version": "1"}, "layers": []any{map[string]any{"name": "catalog", "values": map[string]any{"model": map[string]any{"gpus": int64(1)}}}, map[string]any{"name": "form", "values": map[string]any{"modelRoute": map[string]any{"nginx": map[string]any{"values": map[string]any{"adaptive_cc": "false", "adaptive_cc_ttl": "30"}}}}}}}
	if !reflect.DeepEqual(WithoutAdaptiveSpec(before), WithoutAdaptiveSpec(after)) {
		t.Fatal("adaptive-only change is blocked")
	}
	form := after["layers"].([]any)[1].(map[string]any)["values"].(map[string]any)
	route := form["modelRoute"].(map[string]any)["nginx"].(map[string]any)["values"].(map[string]any)
	for _, key := range []string{"default_max", "expose_routed_peer", "adaptive_cc_other_path"} {
		if key == "adaptive_cc_other_path" {
			form[key] = "bad"
		} else {
			route[key] = "bad"
		}
		if reflect.DeepEqual(WithoutAdaptiveSpec(before), WithoutAdaptiveSpec(after)) {
			t.Fatalf("allowed unrelated change %s", key)
		}
		delete(form, key)
		delete(route, key)
	}
}
