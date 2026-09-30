package server

import (
	"testing"
)

// nodeSelector ANDs its labels, so it cannot express "any of these products".
// One term with one In expression is the OR, and the label key comes from the
// variant's vendor -- which is why the form sends product names, not affinity
// it had to build itself.
func TestGPUProductsBecomeNodeAffinity(t *testing.T) {
	srv, _ := deployServer(t, true)
	code, p := post(t, srv, "/api/plans", map[string]any{
		"model": "qwen3.6-35b-a3b", "serviceId": "r",
		"gpuProducts": []string{"NVIDIA-B300-SXM6-AC", "NVIDIA-H100-SXM5"},
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, p)
	}
	if _, present := planValues(p)["nodeSelector"]; present {
		t.Error("nodeSelector must not be written any more")
	}

	expr := matchExpr(t, p)
	if expr["key"] != "nvidia.com/gpu.product" || expr["operator"] != "In" {
		t.Fatalf("unexpected expression: %v", expr)
	}
	got := expr["values"].([]any)
	if len(got) != 2 || got[0] != "NVIDIA-B300-SXM6-AC" || got[1] != "NVIDIA-H100-SXM5" {
		t.Fatalf("both products must be offered as alternatives: %v", got)
	}
}

// Set at the nodeSelectorTerms path, so a preferred rule beside it survives.
func TestGPUProductsKeepAPreferredAffinityRule(t *testing.T) {
	srv, _ := deployServer(t, true)
	_, p := post(t, srv, "/api/plans", map[string]any{
		"model": "qwen3.6-35b-a3b", "serviceId": "r",
		"gpuProducts": []string{"NVIDIA-B300-SXM6-AC"},
		"overrides": map[string]any{
			"affinity": map[string]any{
				"nodeAffinity": map[string]any{
					"preferredDuringSchedulingIgnoredDuringExecution": []any{
						map[string]any{"weight": 1},
					},
				},
			},
		},
	})
	na := planValues(p)["affinity"].(map[string]any)["nodeAffinity"].(map[string]any)
	if na["preferredDuringSchedulingIgnoredDuringExecution"] == nil {
		t.Fatalf("a preferred rule beside the required one was dropped: %v", na)
	}
	if na["requiredDuringSchedulingIgnoredDuringExecution"] == nil {
		t.Fatal("the required rule is missing")
	}
}

func matchExpr(t *testing.T, p map[string]any) map[string]any {
	t.Helper()
	na := planValues(p)["affinity"].(map[string]any)["nodeAffinity"].(map[string]any)
	req := na["requiredDuringSchedulingIgnoredDuringExecution"].(map[string]any)
	terms := req["nodeSelectorTerms"].([]any)
	if len(terms) != 1 {
		t.Fatalf("want one term, got %d", len(terms))
	}
	exprs := terms[0].(map[string]any)["matchExpressions"].([]any)
	if len(exprs) != 1 {
		t.Fatalf("want one expression, got %d", len(exprs))
	}
	return exprs[0].(map[string]any)
}

// The profile is where "what this cluster schedules GPU work on" belongs, and
// an explicit form value still wins.
func TestSchedulingDefaultsComeFromTheProfile(t *testing.T) {
	srv, _ := deployServer(t, true)
	_, p := post(t, srv, "/api/plans", map[string]any{
		"model": "qwen3.6-35b-a3b", "serviceId": "r",
	})
	vals := planValues(p)

	// testProfile carries no schedule block, so nothing is forced.
	if _, set := vals["schedulerName"]; set && layerOf(p, "schedulerName") != "site" {
		t.Errorf("an unset profile must not invent a scheduler: %v", vals["schedulerName"])
	}

	_, withForm := post(t, srv, "/api/plans", map[string]any{
		"model": "qwen3.6-35b-a3b", "serviceId": "r",
		"overrides": map[string]any{"schedulerName": "volcano"},
	})
	fv := planValues(withForm)
	if fv["schedulerName"] != "volcano" {
		t.Fatalf("the form must win: %v", fv["schedulerName"])
	}
	if got := layerOf(withForm, "schedulerName"); got != "form" {
		t.Errorf("an explicit value is the form's, whatever the profile said: %v", got)
	}
}

// nodeSelector, affinity and tolerations arrive as a YAML fragment, parsed on
// the server so there is one parser for it.
func TestSchedulingYAMLIsAccepted(t *testing.T) {
	srv, _ := deployServer(t, true)
	for _, frag := range []string{
		"tolerations:\n  - key: gpu\n    operator: Exists\n",
		"extraArgs: [--tp-size=8]\n", // the model entry set this too; the form wins
	} {
		code, body := post(t, srv, "/api/plans", map[string]any{
			"model": "qwen3.6-35b-a3b", "serviceId": "r", "overridesYAML": frag,
		})
		if code != 200 {
			t.Fatalf("%q: status %d: %v", frag, code, body)
		}
	}
}

// tolerations is a plain form-owned key, so the form sends the array straight
// through -- no vendor mapping, nothing for the server to translate.
func TestTolerationsFromTheFormLandInThePlan(t *testing.T) {
	srv, _ := deployServer(t, true)
	code, p := post(t, srv, "/api/plans", map[string]any{
		"model": "qwen3.6-35b-a3b", "serviceId": "r",
		"overrides": map[string]any{
			"tolerations": []any{
				map[string]any{"key": "gpu", "operator": "Exists", "effect": "NoSchedule"},
				map[string]any{"key": "tier", "operator": "Equal", "value": "spot"},
			},
		},
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, p)
	}
	tol, ok := planValues(p)["tolerations"].([]any)
	if !ok || len(tol) != 2 {
		t.Fatalf("both tolerations must survive: %v", planValues(p)["tolerations"])
	}
	first := tol[0].(map[string]any)
	if first["key"] != "gpu" || first["effect"] != "NoSchedule" {
		t.Errorf("unexpected toleration: %v", first)
	}
	if got := layerOf(p, "tolerations"); got != "form" {
		t.Errorf("tolerations are the form's: %v", got)
	}
}
