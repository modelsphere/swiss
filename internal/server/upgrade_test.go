package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/values"
)

// deployed seeds a release with a plan beside it, the way an apply leaves it.
func deployed(t *testing.T, req planRequest) cluster.Fake {
	t.Helper()
	_, doc := livePlan(t, req)
	probe := liveProbe()
	probe.Rel = append(probe.Rel, cluster.Release{
		Name: req.Release, Namespace: "models", Status: "deployed", Revision: 3, SwissFiles: doc,
	})
	return probe
}

// The upgrade form sends the whole form, and it wins. This is the change that
// makes the upgrade page editable rather than a carried-forward summary.
func TestUpgradeFormOverridesWin(t *testing.T) {
	probe := deployed(t, planRequest{
		Model: "qwen3.6-35b-a3b", Release: "r", ServiceID: "r",
		Overrides: values.Tree{"replicaCount": 3},
	})
	srv, _ := deployServerWith(t, probe, true)

	code, up := post(t, srv, "/api/plans", map[string]any{
		"fromRelease": "r",
		"overrides":   map[string]any{"replicaCount": 5},
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, up)
	}
	if got := planValues(up)["replicaCount"]; got != float64(5) {
		t.Fatalf("the form must win over what was carried forward, got %v", got)
	}
}

// The form request replaces the previous form layer exactly. A key the
// request omits is removed; an upgrade that sends no overrides keeps it.
func TestUpgradeFormReplacesAbsentKeys(t *testing.T) {
	probe := deployed(t, planRequest{
		Model: "qwen3.6-35b-a3b", Release: "r", ServiceID: "r",
		Overrides: values.Tree{
			"replicaCount":      2,
			"priorityClassName": "high",
		},
	})
	srv, _ := deployServerWith(t, probe, true)

	code, up := post(t, srv, "/api/plans", map[string]any{
		"fromRelease": "r",
		"overrides":   map[string]any{"replicaCount": 4},
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, up)
	}
	form := layerDoc(up, "form")
	if _, ok := form["priorityClassName"]; ok {
		t.Fatalf("a key absent from the request overrides must leave the form layer, got %v", form["priorityClassName"])
	}
	if form["replicaCount"] != float64(4) {
		t.Fatalf("replicaCount = %v, want the request's 4", form["replicaCount"])
	}

	code, bare := post(t, srv, "/api/plans", map[string]any{"fromRelease": "r"})
	if code != 200 {
		t.Fatalf("bare upgrade status %d: %v", code, bare)
	}
	kept := layerDoc(bare, "form")
	if kept["priorityClassName"] != "high" {
		t.Fatalf("an upgrade with no overrides must keep the key, got %v", kept["priorityClassName"])
	}
}

// Release identity is not a setting. The upgrade form renders it read-only, and
// the server ignores it regardless: renaming here would not rename the release,
// it would install a second one beside the first -- two engines on one set of
// GPUs, the hazard `helmfile.yaml` spends thirty lines on.
func TestUpgradeIgnoresARenamedRelease(t *testing.T) {
	probe := deployed(t, planRequest{Model: "qwen3.6-35b-a3b", Release: "r", ServiceID: "r"})
	srv, _ := deployServerWith(t, probe, true)

	code, up := post(t, srv, "/api/plans", map[string]any{
		"fromRelease": "r",
		"release":     "somewhere-else",
		"overrides":   map[string]any{"replicaCount": 1},
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, up)
	}
	rel := up["release"].(map[string]any)
	if rel["name"] != "r" || rel["namespace"] != "models" {
		t.Fatalf("an upgrade must stay on its own release, got %v", rel)
	}
}

// The namespace is what scopes the lookup, so a mismatched one cannot silently
// retarget: it finds no release and the upgrade is refused outright.
func TestUpgradeRefusesAMismatchedNamespace(t *testing.T) {
	probe := deployed(t, planRequest{Model: "qwen3.6-35b-a3b", Release: "r", ServiceID: "r"})
	srv, _ := deployServerWith(t, probe, true)

	code, out := post(t, srv, "/api/plans", map[string]any{
		"fromRelease": "r",
		"namespace":   "other-ns",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("want a refusal, got %d %v", code, out)
	}
}

// `swiss upgrade` and the version picker send no overrides at all. That still
// has to mean "move the catalog, keep every setting", edits included.
func TestBareUpgradeStillCarriesEverything(t *testing.T) {
	probe := deployed(t, planRequest{
		Model: "qwen3.6-35b-a3b", Release: "r", ServiceID: "r",
		Overrides: values.Tree{"replicaCount": 3},
		EditsYAML: "nodeSelector:\n  gpu: b300\n",
	})
	srv, _ := deployServerWith(t, probe, true)

	code, up := post(t, srv, "/api/plans", map[string]any{"fromRelease": "r"})
	if code != 200 {
		t.Fatalf("status %d: %v", code, up)
	}
	if planValues(up)["replicaCount"] != float64(3) {
		t.Error("a bare upgrade must carry the form layer forward")
	}
	edits := layerDoc(up, "edit")
	if edits["nodeSelector"] == nil {
		t.Fatalf("a bare upgrade must carry the plan editor forward: %v", edits)
	}
}

// The form shows the edits, so an empty box means the operator emptied it. A
// carried-forward edit that cannot be removed is an escape hatch with no exit.
func TestUpgradeFormCanClearTheEdits(t *testing.T) {
	probe := deployed(t, planRequest{
		Model: "qwen3.6-35b-a3b", Release: "r", ServiceID: "r",
		EditsYAML: "nodeSelector:\n  gpu: b300\n",
	})
	srv, _ := deployServerWith(t, probe, true)

	code, up := post(t, srv, "/api/plans", map[string]any{
		"fromRelease": "r",
		"overrides":   map[string]any{"replicaCount": 1},
		// editsYAML deliberately absent: the form was emptied.
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, up)
	}
	if edits := layerDoc(up, "edit"); len(edits) > 0 {
		t.Fatalf("clearing the plan editor must clear it: %v", edits)
	}
}

// The upgrade form may set a key the model entry set, the same as the deploy
// form. Nothing is fenced off; the merge order decides, and the plan says so.
func TestUpgradeFormMaySetACatalogKey(t *testing.T) {
	probe := deployed(t, planRequest{Model: "qwen3.6-35b-a3b", Release: "r", ServiceID: "r"})
	srv, _ := deployServerWith(t, probe, true)

	code, body := post(t, srv, "/api/plans", map[string]any{
		"fromRelease": "r",
		"overrides":   map[string]any{"extraArgs": []string{"--tp-size=8"}},
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	if layerOf(body, "extraArgs") != "form" {
		t.Errorf("extraArgs should be attributed to the form, got %q", layerOf(body, "extraArgs"))
	}
}

// The upgrade form seeds itself from the form layer of the release's stored
// plan. If those inputs are not there, the form comes up empty and composing an
// upgrade resets every setting the release had -- silently, because an empty
// form still sends its defaults.
func TestFormLayerCarriesWhatTheUpgradeFormSeedsFrom(t *testing.T) {
	srv, _ := deployServer(t, true)
	code, p := post(t, srv, "/api/plans", map[string]any{
		"model": "glm5.1", "serviceId": "glm-53", "localPath": "/mnt/disk9/weights",
		"gpuProducts": []string{"NVIDIA-B300-SXM6-AC"},
		"overrides": map[string]any{
			"replicaCount":   2,
			"cart":           map[string]any{"enabled": false},
			"sloRequirement": map[string]any{"enabled": true},
			"modelRoute":     map[string]any{"enabled": true, "nginx": map[string]any{"route": "glm-53"}},
			"tolerations":    []any{map[string]any{"key": "gpu", "operator": "Exists"}},
		},
		"editsYAML": "nodeSelector:\n  zone: az1\n",
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, p)
	}

	form := layerDoc(p, "form")
	if len(form) == 0 {
		t.Fatal("no form layer: the upgrade form would seed from nothing")
	}
	// Every field formFromPlan reads.
	for _, path := range []string{
		"serviceId", "model.localPath", "replicaCount",
		"cart.enabled", "sloRequirement.enabled", "modelRoute.enabled",
		"modelRoute.nginx.route", "tolerations", "affinity",
	} {
		if !hasPath(form, path) {
			t.Errorf("form layer is missing %s", path)
		}
	}
	if edit := layerDoc(p, "edit"); edit["nodeSelector"] == nil {
		t.Error("the plan editor's input must be its own layer, for the form to show it")
	}
}

func hasPath(doc map[string]any, path string) bool {
	var node any = doc
	for _, seg := range strings.Split(path, ".") {
		m, ok := node.(map[string]any)
		if !ok {
			return false
		}
		node, ok = m[seg]
		if !ok {
			return false
		}
	}
	return true
}

// serviceId names the route, the scaler and the SLO. An upgrade that changed it
// would leave the old objects orphaned under a release that kept its name, so
// the request is refused rather than quietly carried out.
func TestUpgradeRefusesAServiceIDChange(t *testing.T) {
	probe := deployed(t, planRequest{Model: "qwen3.6-35b-a3b", Release: "r", ServiceID: "r"})
	srv, _ := deployServerWith(t, probe, true)

	code, out := post(t, srv, "/api/plans", map[string]any{
		"fromRelease": "r",
		"serviceId":   "r-renamed",
		"overrides":   map[string]any{"replicaCount": 2},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %v", code, out)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "serviceId cannot change") {
		t.Fatalf("the refusal must say what is wrong, got %q", msg)
	}
}

// Overrides replace the form layer, which is where serviceId lived. Omitting
// it must not rename the service to the release: the deployed id is identity.
func TestUpgradeKeepsServiceIDWhenTheFormOmitsIt(t *testing.T) {
	probe := deployed(t, planRequest{
		Model: "qwen3.6-35b-a3b", Release: "r", ServiceID: "svc",
	})
	srv, _ := deployServerWith(t, probe, true)

	code, up := post(t, srv, "/api/plans", map[string]any{
		"fromRelease": "r",
		"overrides":   map[string]any{"replicaCount": 2},
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, up)
	}
	if got := planValues(up)["serviceId"]; got != "svc" {
		t.Fatalf("serviceId = %v, want the deployed svc", got)
	}
	rel := up["release"].(map[string]any)
	if rel["name"] != "r" {
		t.Fatalf("release name = %v, want r", rel["name"])
	}
}

// The upgrade form posts every field it shows, serviceId included. Sending the
// deployed value back is what "unchanged" looks like on the wire, so it must
// not be read as an attempt to rename anything.
func TestUpgradeAcceptsTheDeployedServiceID(t *testing.T) {
	probe := deployed(t, planRequest{Model: "qwen3.6-35b-a3b", Release: "r", ServiceID: "r"})
	srv, _ := deployServerWith(t, probe, true)

	code, up := post(t, srv, "/api/plans", map[string]any{
		"fromRelease": "r",
		"serviceId":   "r",
		"overrides":   map[string]any{"replicaCount": 2},
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, up)
	}
	if got := planValues(up)["serviceId"]; got != "r" {
		t.Fatalf("serviceId = %v, want it carried through unchanged", got)
	}
}
