package server

import (
	"testing"

	"github.com/aceforeverd/swiss/internal/cluster"
	"github.com/aceforeverd/swiss/internal/values"
)

// A fixed replica count beside a live scaler is two answers to one question.
func TestScalerDropsTheFixedReplicaCount(t *testing.T) {
	srv, _ := deployServer(t, true)
	code, p := post(t, srv, "/api/plans", map[string]any{
		"model": "modelforge", "release": "r", "serviceId": "r",
		"overrides": map[string]any{
			"replicaCount": 4,
			"scaler":       map[string]any{"enabled": true, "minReplicas": 1, "maxReplicas": 6},
		},
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, p)
	}
	vals := p["values"].(map[string]any)
	if _, present := vals["replicaCount"]; present {
		t.Fatalf("the scaler owns the count: %v", vals["replicaCount"])
	}
	sc := vals["scaler"].(map[string]any)
	if sc["minReplicas"] != float64(1) || sc["maxReplicas"] != float64(6) {
		t.Errorf("bounds lost: %v", sc)
	}
}

func TestScalerOffKeepsTheFixedReplicaCount(t *testing.T) {
	srv, _ := deployServer(t, true)
	_, p := post(t, srv, "/api/plans", map[string]any{
		"model": "modelforge", "release": "r", "serviceId": "r",
		"overrides": map[string]any{
			"replicaCount": 4,
			"scaler":       map[string]any{"enabled": false},
		},
	})
	if p["values"].(map[string]any)["replicaCount"] != float64(4) {
		t.Fatalf("a fixed count is the point when the scaler is off: %v", p["values"])
	}
}

// The upgrade merge carries forward anything the form does not send, which
// would otherwise resurrect a replicaCount stored before the form made the two
// mutually exclusive.
func TestUpgradeDoesNotResurrectAReplicaCount(t *testing.T) {
	_, doc := livePlan(t, planRequest{
		Model: "modelforge", Release: "r", ServiceID: "r",
		Overrides: values.Tree{"replicaCount": 3},
	})
	probe := liveProbe()
	probe.Rel = append(probe.Rel, cluster.Release{
		Name: "r", Namespace: "modelforge", Status: "deployed", Revision: 1, SwissPlan: doc,
	})
	srv, _ := deployServerWith(t, probe, true)

	_, up := post(t, srv, "/api/plans", map[string]any{
		"fromRelease": "r",
		"overrides":   map[string]any{"scaler": map[string]any{"enabled": true, "minReplicas": 1}},
	})
	if _, present := up["values"].(map[string]any)["replicaCount"]; present {
		t.Fatal("turning the scaler on must clear the count it replaces")
	}
}
