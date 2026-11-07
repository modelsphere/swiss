package server

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/values"
	"gopkg.in/yaml.v3"
)

func TestUserValuesReplacesUpgradeDocument(t *testing.T) {
	probe := deployed(t, planRequest{Model: "qwen3.6-35b-a3b", Release: "r", ServiceID: "r", Overrides: values.Tree{"replicaCount": 3, "priorityClassName": "old"}, EditsYAML: "oldCustom: true\n"})
	srv, _ := deployServerWith(t, probe, true)
	for _, document := range []map[string]any{{"replicaCount": 2, "custom": map[string]any{"keep": true}}, {}} {
		code, result := post(t, srv, "/api/plans", map[string]any{"fromRelease": "r", "userValues": document})
		if code != http.StatusOK {
			t.Fatalf("status %d: %v", code, result)
		}
		edits := layerDoc(result, values.LayerEdit)
		if !reflect.DeepEqual(jsonNumbers(edits), jsonNumbers(document)) {
			t.Fatalf("user document %v, want %v", edits, document)
		}
		form := layerDoc(result, values.LayerForm)
		if form["serviceId"] != "r" || form["replicaCount"] != nil || form["priorityClassName"] != nil {
			t.Fatalf("retained form values or missing identity: %v", form)
		}
		if planValues(result)["oldCustom"] != nil {
			t.Fatalf("retained old edit: %v", planValues(result))
		}
	}
}

func jsonNumbers(document map[string]any) map[string]any {
	result := map[string]any{}
	for key, value := range document {
		if integer, ok := value.(int); ok {
			result[key] = float64(integer)
		} else {
			result[key] = value
		}
	}
	return result
}

func TestUserValuesRejectsAmbiguousInputs(t *testing.T) {
	srv, _ := deployServerWith(t, liveProbe(), true)
	for _, legacy := range []map[string]any{{"overrides": map[string]any{"replicaCount": 2}}, {"overridesYAML": "x: 1"}, {"editsYAML": "x: 1"}, {"reuseSource": true}} {
		body := map[string]any{"model": "qwen3.6-35b-a3b", "userValues": map[string]any{}}
		for key, value := range legacy {
			body[key] = value
		}
		code, result := post(t, srv, "/api/plans", body)
		if code != http.StatusBadRequest {
			t.Fatalf("ambiguous input accepted: %v, status %d: %v", body, code, result)
		}
	}
}

func TestUserValuesCannotRenameService(t *testing.T) {
	probe := deployed(t, planRequest{Model: "qwen3.6-35b-a3b", Release: "r", ServiceID: "r"})
	srv, _ := deployServerWith(t, probe, true)
	for _, body := range []map[string]any{
		{"fromRelease": "r", "serviceId": "other", "userValues": map[string]any{}},
		{"fromRelease": "r", "userValues": map[string]any{"serviceId": "other"}},
		{"model": "qwen3.6-35b-a3b", "release": "r", "serviceId": "r", "userValues": map[string]any{"serviceId": nil}},
	} {
		code, result := post(t, srv, "/api/plans", body)
		if code != http.StatusBadRequest {
			t.Fatalf("rename accepted, status %d: %v", code, result)
		}
	}
}

func TestUserValuesCarriesAutoscaleThroughBothCompositionEndpoints(t *testing.T) {
	previous, _ := livePlan(t, planRequest{Model: "qwen3.6-35b-a3b", Release: "r", ServiceID: "r"})
	setAutoscaleRule(previous)
	previous.Layers[values.LayerAutoscale] = values.Tree{"replicaCount": 4}
	files, err := previous.Files("")
	if err != nil {
		t.Fatal(err)
	}
	probe := liveProbe()
	probe.Maps["swiss/site-profile"]["profile.yaml"] = profileYAML + "chartRepo: oci://ghcr.io/modelsphere/charts\n"
	probe.Rel = append(probe.Rel, cluster.Release{Name: "r", Namespace: "models", Status: "deployed", Revision: 3, SwissFiles: files})
	srv, server := deployServerWith(t, probe, true)
	server.cfg.Server.HelmBin, _ = stubShowValues(t, 0)
	document := values.Tree{}
	values.Merge(document, previous.Layers[values.LayerForm], values.LayerEdit, nil)
	document["replicaCount"] = 2
	body := map[string]any{"fromRelease": "r", "userValues": document}
	code, result := post(t, srv, "/api/plans", body)
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, result)
	}
	if jsonInt(planValues(result)["replicaCount"]) != 4 {
		t.Fatalf("autoscale lost: %v", result)
	}
	code, preview := post(t, srv, "/api/v2/compose?dryRun=true&dumpHelmValues=true", body)
	if code != http.StatusOK {
		t.Fatalf("preview status %d: %v", code, preview)
	}
	helmValues := preview["helmValues"].(map[string]any)
	var effective values.Tree
	if err := yaml.Unmarshal([]byte(helmValues["overrides"].(string)), &effective); err != nil {
		t.Fatal(err)
	}
	if jsonInt(effective["replicaCount"]) != 4 {
		t.Fatalf("preview lost autoscale: %v", effective)
	}
	document["sloRequirement"] = values.Tree{"enabled": false}
	code, result = post(t, srv, "/api/plans", body)
	if code != http.StatusOK || len(layerDoc(result, values.LayerAutoscale)) != 0 || jsonInt(planValues(result)["replicaCount"]) != 2 {
		t.Fatalf("leaving autoscale did not restore the user count: %d %v", code, result)
	}
}

func TestUserValuesCapability(t *testing.T) {
	srv := testServer(t, fakeProbe())
	code, result := get(t, srv, "/api/cluster")
	capabilities, _ := result["capabilities"].(map[string]any)
	if code != http.StatusOK || capabilities["userValues"] != true {
		t.Fatalf("missing capability: %d %v", code, result)
	}
}

func TestUserValuesRejectsTwoReplicaControllers(t *testing.T) {
	srv, _ := deployServerWith(t, liveProbe(), true)
	code, result := post(t, srv, "/api/plans", map[string]any{"model": "qwen3.6-35b-a3b", "userValues": map[string]any{"replicaCount": 2, "scaler": map[string]any{"enabled": true}}})
	if code != http.StatusBadRequest {
		t.Fatalf("competing replica settings accepted: %d %v", code, result)
	}
}

func TestUserValuesCarriesCanarySystem(t *testing.T) {
	previous, _ := livePlan(t, planRequest{Model: "qwen3.6-35b-a3b", Release: "r", ServiceID: "r"})
	previous.Layers[values.LayerCanarySystem] = values.Tree{"scaler": values.Tree{"enabled": false}}
	files, err := previous.Files("")
	if err != nil {
		t.Fatal(err)
	}
	probe := liveProbe()
	probe.Rel = append(probe.Rel, cluster.Release{Name: "r", Namespace: "models", Status: "deployed", Revision: 3, SwissFiles: files})
	srv, _ := deployServerWith(t, probe, true)
	code, result := post(t, srv, "/api/plans", map[string]any{"fromRelease": "r", "userValues": map[string]any{"scaler": map[string]any{"enabled": true}}})
	if code != http.StatusOK || layerDoc(result, values.LayerCanarySystem)["scaler"] == nil {
		t.Fatalf("lost canary policy: %d %v", code, result)
	}
	if enabled, _ := values.Get(planValues(result), "scaler.enabled"); enabled != false {
		t.Fatalf("user values bypassed canary policy: %v", result)
	}
}

func TestUserSLOOptIn(t *testing.T) {
	configuration := &plan.Plan{Layers: map[string]values.Tree{values.LayerSite: {"sloRequirement": values.Tree{"enabled": true}}}}
	if userSLO(configuration) {
		t.Fatal("site default opted a release in")
	}
	configuration.Layers[values.LayerForm] = values.Tree{"sloRequirement": values.Tree{"enabled": false}}
	configuration.Layers[values.LayerEdit] = values.Tree{"sloRequirement": values.Tree{"enabled": true}}
	if !userSLO(configuration) {
		t.Fatal("YAML opt-in was ignored")
	}
	configuration.Layers[values.LayerEdit] = values.Tree{"sloRequirement": values.Tree{"enabled": false}}
	if userSLO(configuration) {
		t.Fatal("YAML opt-out was ignored")
	}
}
