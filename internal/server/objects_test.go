package server

import (
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/modelsphere/swiss/internal/cluster"
)

// Each object reports on its own: one the controller deleted, or one swissd
// may not read, must not hide what the others say.
func TestObjectsReportsEachObjectSeparately(t *testing.T) {
	route := cluster.ObjectRef{APIVersion: "routing.modelsphere.dev/v1alpha1", Kind: "ModelRoute", Namespace: "models", Name: "r"}
	scaler := cluster.ObjectRef{APIVersion: "autoscaling.modelsphere.dev/v1alpha1", Kind: "LLMScaler", Namespace: "models", Name: "r"}
	slo := cluster.ObjectRef{APIVersion: "inference.modelsphere.dev/v1alpha1", Kind: "LLMSLORequirement", Namespace: "models", Name: "r"}

	probe := fakeProbe()
	probe.Rel = []cluster.Release{{Name: "r", Namespace: "models", Status: "deployed", Revision: 1,
		Objects: []cluster.ObjectRef{route, scaler, slo}}}
	probe.Objs = map[string]cluster.Object{
		"ModelRoute/models/r": {Generation: 2, Status: map[string]any{"ready": true, "backends": float64(2)}},
	}
	probe.ObjErr = map[string]error{
		"LLMSLORequirement/models/r": apierrors.NewForbidden(schema.GroupResource{Group: "inference.modelsphere.dev", Resource: "llmslorequirements"}, "r", errors.New("no")),
	}
	srv := testServer(t, probe)

	code, body := get(t, srv, "/api/releases/models/r/objects")
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	objs := body["objects"].([]any)
	if len(objs) != 3 {
		t.Fatalf("got %v", objs)
	}
	first := objs[0].(map[string]any)
	if live, _ := first["live"].(map[string]any); live == nil || live["status"].(map[string]any)["ready"] != true {
		t.Errorf("route = %v", first)
	}
	if objs[1].(map[string]any)["missing"] != true {
		t.Errorf("a scaler that is gone is missing: %v", objs[1])
	}
	if e, _ := objs[2].(map[string]any)["error"].(string); e == "" {
		t.Errorf("a forbidden read says so: %v", objs[2])
	}
}

func TestObjectsOfAnAbsentReleaseIsAnEmptyList(t *testing.T) {
	srv := testServer(t, fakeProbe())
	code, body := get(t, srv, "/api/releases/models/nothing/objects")
	if code != 200 || len(body["objects"].([]any)) != 0 {
		t.Fatalf("%d %v", code, body)
	}
}
