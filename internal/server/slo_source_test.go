package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
		"ModelRoute/models/different-route": {Spec: map[string]any{"slo": map[string]any{"name": "parent/shared-slo"}}},
	}
	srv := testServer(t, requirementProbe{Probe: probe, objects: map[cluster.ObjectRef]*cluster.Object{
		{APIVersion: "inference.modelsphere.dev/v1alpha1", Kind: "LLMSLORequirement", Namespace: "parent", Name: "shared-slo"}: {Spec: map[string]any{"serviceId": "actual-service"}},
	}})
	code, body := get(t, srv, "/api/releases/models/arm/slo")
	if code != 200 || body["found"] != true || calls != 1 {
		t.Fatal(code, body, calls)
	}
	source := body["source"].(map[string]any)
	if source["namespace"] != "parent" || source["name"] != "shared-slo" {
		t.Fatal(source)
	}
}

type requirementProbe struct {
	cluster.Probe
	objects map[cluster.ObjectRef]*cluster.Object
}

func (p requirementProbe) Object(ctx context.Context, ref cluster.ObjectRef) (*cluster.Object, error) {
	if ref.Kind == "LLMSLORequirement" {
		return p.objects[ref], nil
	}
	return p.Probe.Object(ctx, ref)
}

func TestSLOUsesDeployedRequirementAPIGroup(t *testing.T) {
	for _, version := range []string{"inference.modelsphere.dev/v1alpha1", "inference.x-k8s.io/v1alpha1"} {
		for _, shared := range []bool{false, true} {
			name := version + "/rendered"
			if shared {
				name = version + "/shared"
			}
			t.Run(name, func(t *testing.T) {
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/config/effective-id" {
						t.Errorf("wrong SLO target: %s", r.URL.Path)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"route": "effective-id"})
				}))
				t.Cleanup(up.Close)
				probe := fakeProbe()
				probe.Maps["swiss/site-profile"] = map[string]string{"profile.yaml": profileYAML + "\nscaler:\n  sloAddress: " + up.URL + "\n  sloTokenSecret: llm-scaler/slo-api\n"}
				probe.Secrets = map[string]map[string]string{"llm-scaler/slo-api": {"token": "slo-secret"}}
				ref := cluster.ObjectRef{APIVersion: version, Kind: "LLMSLORequirement", Namespace: "parent", Name: "slo"}
				refs := []cluster.ObjectRef{{APIVersion: "routing.modelsphere.dev/v1alpha1", Kind: "ModelRoute", Namespace: "models", Name: "route"}}
				objects := map[cluster.ObjectRef]*cluster.Object{ref: {Spec: map[string]any{"serviceId": "effective-id"}}}
				if !shared {
					refs = append(refs, ref)
					other := ref
					other.APIVersion = "inference.modelsphere.dev/v1alpha1"
					if version == other.APIVersion {
						other.APIVersion = "inference.x-k8s.io/v1alpha1"
					}
					objects[other] = &cluster.Object{Spec: map[string]any{"serviceId": "wrong-group"}}
				}
				probe.Rel = []cluster.Release{{Namespace: "models", Name: "arm", Status: "deployed", Revision: 1, Objects: refs}}
				probe.Objs = map[string]cluster.Object{"ModelRoute/models/route": {Spec: map[string]any{"slo": map[string]any{"name": "parent/slo"}}}}
				cfg := testConfig("prod")
				cfg.Server.AllowDeploy = true
				srv := httptest.NewServer(New(cfg, requirementProbe{Probe: probe, objects: objects}, discardLogger(), "test").Handler())
				t.Cleanup(srv.Close)
				code, body := get(t, srv, "/api/releases/models/arm/slo")
				if code != http.StatusOK || body["found"] != true {
					t.Fatal(code, body)
				}
				code, body = put(t, srv, "/api/releases/models/arm/slo", map[string]any{"priority": 10})
				if code != http.StatusOK {
					t.Fatal(code, body)
				}
			})
		}
	}
}

func TestSLOSharedRequirementRejectsAmbiguousAPIGroup(t *testing.T) {
	s := New(testConfig("prod"), requirementProbe{Probe: fakeProbe(), objects: map[cluster.ObjectRef]*cluster.Object{
		{APIVersion: "inference.modelsphere.dev/v1alpha1", Kind: "LLMSLORequirement", Namespace: "parent", Name: "slo"}: {},
		{APIVersion: "inference.x-k8s.io/v1alpha1", Kind: "LLMSLORequirement", Namespace: "parent", Name: "slo"}:        {},
	}}, discardLogger(), "test")
	if _, err := s.sloRequirement(t.Context(), nil, "parent", "slo"); err == nil {
		t.Fatal("ambiguous shared requirement selected")
	}
}

func TestSLOWriteTranslatesReleaseAndRoundTripRoute(t *testing.T) {
	for _, route := range []string{"", "arm", "effective-id", "unrelated"} {
		t.Run(route, func(t *testing.T) {
			calls := 0
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if r.URL.Path != "/config/effective-id" || body["route"] != "effective-id" {
					t.Errorf("wrong target %s or body %v", r.URL.Path, body)
				}
				_ = json.NewEncoder(w).Encode(body)
			}))
			t.Cleanup(up.Close)
			s := &Server{}
			r := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"route":"`+route+`","priority":10}`))
			w := httptest.NewRecorder()
			s.writeSLO(w, r, "arm", up.URL, up.URL+"/config/effective-id", "token", sloSource{ServiceID: "effective-id", CanEdit: true})
			if route == "unrelated" {
				if w.Code != http.StatusBadRequest || calls != 0 {
					t.Fatal(w.Code, calls, w.Body.String())
				}
			} else if w.Code != http.StatusOK || calls != 1 {
				t.Fatal(w.Code, calls, w.Body.String())
			}
		})
	}
}
