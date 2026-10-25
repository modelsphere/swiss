package server

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/values"
)

func pdPlan(t *testing.T, ns, release, serviceID string, replicas int, pdOn, colocate bool, slo *bool, schema string) (*plan.Plan, map[string]string) {
	t.Helper()
	form := values.Tree{
		"replicaCount":     replicas,
		"serviceId":        serviceID,
		"fullnameOverride": release,
		"pd":               values.Tree{"enabled": pdOn, "colocate": colocate},
	}
	if slo != nil {
		form["sloRequirement"] = values.Tree{"enabled": *slo}
	}
	p := &plan.Plan{
		APIVersion: plan.APIVersion,
		Release:    plan.Release{Name: release, Namespace: ns},
		Source:     plan.SourceRef{Model: "pd", Variant: "pd", Schema: schema},
		Chart:      plan.ChartRef{Name: "sglang", Version: "0.7.0", Repo: "https://modelsphere.github.io/helm-charts"},
		Engine:     "sglang",
		Profile:    "prod",
		Layers:     map[string]values.Tree{values.LayerForm: form},
	}
	if err := p.ComputeHash(); err != nil {
		t.Fatal(err)
	}
	files, err := p.Files("")
	if err != nil {
		t.Fatal(err)
	}
	files["status.yaml"] = "phase: applied\n"
	return p, files
}

func boolPtr(v bool) *bool { return &v }

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func jsonInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

func setDecisionProfile(probe cluster.Fake, rawURL string) {
	m := probe.Maps["swiss/site-profile"]
	m["profile.yaml"] = profileYAML + "scaler:\n  serverAddress: " + rawURL + "\n"
}

func readyDS(slices int) cluster.DisaggregatedSet {
	n := slices
	return cluster.DisaggregatedSet{
		Generation: 1, ObservedGeneration: 1, Slices: &n,
		Roles: []cluster.RoleStatus{{Name: "prefill", Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1}},
	}
}

func readyRouters(ns, fullname string, n int) map[string]cluster.Deployment {
	out := map[string]cluster.Deployment{}
	for i := 0; i < n; i++ {
		out[fmt.Sprintf("%s/%s-router-s%d", ns, fullname, i)] = cluster.Deployment{
			Generation: 1, ObservedGeneration: 1, SpecReplicas: 1, ReadyReplicas: 1,
		}
	}
	return out
}

func asNumber(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return -1
	}
}
