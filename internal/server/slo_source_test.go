package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelsphere/swiss/internal/cluster"
)

func TestSLOUsesActualQualifiedReferenceAndServiceID(t *testing.T) {
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/config/actual-service" {
			t.Errorf("wrong target: %s", r.URL.Path)
		}
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{"route": "actual-service", "highPriority": false})
	}))
	t.Cleanup(up.Close)
	probe := fakeProbe()
	probe.Maps["swiss/site-profile"] = map[string]string{"profile.yaml": profileYAML + "\nscaler:\n  sloAddress: " + up.URL + "\n  sloTokenSecret: llm-scaler/slo-api\n"}
	probe.Secrets = map[string]map[string]string{"llm-scaler/slo-api": {"token": "slo-secret"}}
	probe.Rel = []cluster.Release{{Namespace: "models", Name: "arm", Status: "deployed", Revision: 1, Objects: []cluster.ObjectRef{{APIVersion: "routing.modelsphere.dev/v1alpha1", Kind: "ModelRoute", Namespace: "models", Name: "different-route"}}}}
	probe.Objs = map[string]cluster.Object{
		"ModelRoute/models/different-route":   {Spec: map[string]any{"slo": map[string]any{"name": "parent/shared-slo"}}},
		"LLMSLORequirement/parent/shared-slo": {Spec: map[string]any{"serviceId": "actual-service"}},
	}
	srv := testServer(t, probe)
	code, body := get(t, srv, "/api/releases/models/arm/slo")
	if code != 200 || body["found"] != true || calls != 1 {
		t.Fatal(code, body, calls)
	}
	source := body["source"].(map[string]any)
	if source["namespace"] != "parent" || source["name"] != "shared-slo" {
		t.Fatal(source)
	}
}
