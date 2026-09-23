package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aceforeverd/swiss/internal/cluster"
	"github.com/aceforeverd/swiss/internal/values"
)

// deployed seeds a release with a plan beside it, the way an apply leaves it.
func deployed(t *testing.T, req planRequest) cluster.Fake {
	t.Helper()
	_, doc := livePlan(t, req)
	probe := liveProbe()
	probe.Rel = append(probe.Rel, cluster.Release{
		Name: req.Release, Namespace: "modelforge", Status: "deployed", Revision: 3, SwissFiles: doc,
	})
	return probe
}

// The upgrade form sends the whole form, and it wins. This is the change that
// makes the upgrade page editable rather than a carried-forward summary.
func TestUpgradeFormOverridesWin(t *testing.T) {
	probe := deployed(t, planRequest{
		Model: "modelforge", Release: "r", ServiceID: "r",
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

// A value no form field covers -- set once from a flag or the advanced box --
// must survive an upgrade rather than being dropped by a form that never knew
// about it. This is why the overrides merge instead of replacing.
func TestUpgradeKeepsValuesTheFormDoesNotCover(t *testing.T) {
	probe := deployed(t, planRequest{
		Model: "modelforge", Release: "r", ServiceID: "r",
		Overrides: values.Tree{
			"replicaCount":      2,
			"priorityClassName": "high",
			"nodeSelector":      values.Tree{"disktype": "nvme"},
		},
	})
	srv, _ := deployServerWith(t, probe, true)

	// The web form covers replicaCount but has no nodeSelector field.
	code, up := post(t, srv, "/api/plans", map[string]any{
		"fromRelease": "r",
		"overrides":   map[string]any{"replicaCount": 4},
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, up)
	}
	vals := planValues(up)
	sel, ok := vals["nodeSelector"].(map[string]any)
	if !ok || sel["disktype"] != "nvme" {
		t.Fatalf("a value the form does not cover must survive: %v", vals["nodeSelector"])
	}
	if vals["priorityClassName"] != "high" {
		t.Errorf("priorityClassName was dropped: %v", vals["priorityClassName"])
	}
}

// Release identity is not a setting. The upgrade form renders it read-only, and
// the server ignores it regardless: renaming here would not rename the release,
// it would install a second one beside the first -- two engines on one set of
// GPUs, the hazard `helmfile.yaml` spends thirty lines on.
func TestUpgradeIgnoresARenamedRelease(t *testing.T) {
	probe := deployed(t, planRequest{Model: "modelforge", Release: "r", ServiceID: "r"})
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
	if rel["name"] != "r" || rel["namespace"] != "modelforge" {
		t.Fatalf("an upgrade must stay on its own release, got %v", rel)
	}
}

// The namespace is what scopes the lookup, so a mismatched one cannot silently
// retarget: it finds no release and the upgrade is refused outright.
func TestUpgradeRefusesAMismatchedNamespace(t *testing.T) {
	probe := deployed(t, planRequest{Model: "modelforge", Release: "r", ServiceID: "r"})
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
		Model: "modelforge", Release: "r", ServiceID: "r",
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
		Model: "modelforge", Release: "r", ServiceID: "r",
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

// Ownership still decides what the upgrade form may set. The page is editable
// now; "select, don't edit" is enforced by the same check either way.
func TestUpgradeFormStillObeysOwnership(t *testing.T) {
	probe := deployed(t, planRequest{Model: "modelforge", Release: "r", ServiceID: "r"})
	srv, _ := deployServerWith(t, probe, true)

	code, _ := post(t, srv, "/api/plans", map[string]any{
		"fromRelease": "r",
		"overrides":   map[string]any{"extraArgs": []string{"--tp-size=8"}},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("a catalog-owned key must be refused from the upgrade form too, got %d", code)
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
