package server

import (
	"strings"
	"testing"

	"github.com/aceforeverd/swiss/internal/cluster"
	"github.com/aceforeverd/swiss/internal/values"
)

func statusOf(t *testing.T, gateway string, overrides values.Tree) map[string]any {
	t.Helper()
	_, doc := livePlan(t, planRequest{
		Model: "modelforge", Release: "r", ServiceID: "r", Overrides: overrides,
	})
	probe := liveProbe()
	probe.Rel = append(probe.Rel, cluster.Release{
		Name: "r", Namespace: "modelforge", Status: "deployed", Revision: 1, SwissFiles: doc,
	})
	if gateway != "" {
		probe.Maps["swiss/site-profile"] = map[string]string{
			"profile.yaml": profileYAML + "route:\n  gateway: " + gateway + "\n",
		}
	}
	srv := testServer(t, probe)
	code, body := get(t, srv, "/api/releases/modelforge/r/status")
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	return body
}

func TestStatusURLWhenRoutingIsOn(t *testing.T) {
	body := statusOf(t, "https://llm.example.com", values.Tree{
		"modelRoute": values.Tree{"enabled": true, "nginx": values.Tree{"route": "glm-53"}},
	})
	if body["route"] != "glm-53" {
		t.Fatalf("route = %v", body["route"])
	}
	if body["url"] != "https://llm.example.com/glm-53" {
		t.Fatalf("url = %v", body["url"])
	}
}

// A release with routing off publishes nothing, so there is no outside URL to
// show. Reporting one would send somebody to a path openresty never had.
func TestNoURLWhenRoutingIsOff(t *testing.T) {
	body := statusOf(t, "https://llm.example.com", values.Tree{
		"modelRoute": values.Tree{"enabled": false},
	})
	if body["route"] != nil {
		t.Errorf("a release that publishes no route has none: %v", body["route"])
	}
	if body["url"] != nil {
		t.Fatalf("no route means no URL: %v", body["url"])
	}
}

// Routing on but no gateway configured: the route is real, the outside URL is
// simply unknown to this swissd.
func TestNoURLWithoutAGateway(t *testing.T) {
	body := statusOf(t, "", values.Tree{
		"modelRoute": values.Tree{"enabled": true, "nginx": values.Tree{"route": "glm-53"}},
	})
	if body["route"] != "glm-53" {
		t.Errorf("route = %v", body["route"])
	}
	if body["url"] != nil {
		t.Fatalf("no gateway, no URL: %v", body["url"])
	}
}

// The status view offers a worked curl, so it needs the served model name and
// the header shape -- never the key, which stays in its Secret.
func TestStatusCarriesWhatACurlNeeds(t *testing.T) {
	body := statusOf(t, "https://llm.example.com", values.Tree{
		"modelRoute": values.Tree{"enabled": true, "nginx": values.Tree{"route": "glm-53"}},
	})
	// modelforge is served as "kimi" so callers do not change when they land on
	// the fallback tier. A curl naming the catalog id would 404 at the engine.
	if body["model"] != "kimi" {
		t.Errorf("model = %v, want the served name", body["model"])
	}
	if body["authHeader"] != "Authorization" {
		t.Errorf("authHeader = %v", body["authHeader"])
	}
	if body["authPrefix"] != "Bearer " {
		t.Errorf("authPrefix = %v", body["authPrefix"])
	}
}

// A custom header carries the key bare, and an empty prefix has to survive the
// wire: omitting it would have the page print "Bearer" where none belongs.
func TestEmptyAuthPrefixIsStillReported(t *testing.T) {
	_, doc := livePlan(t, planRequest{
		Model: "modelforge", Release: "r", ServiceID: "r",
		Overrides: values.Tree{"modelRoute": values.Tree{"enabled": true}},
	})
	probe := liveProbe()
	probe.Rel = append(probe.Rel, cluster.Release{
		Name: "r", Namespace: "modelforge", Status: "deployed", Revision: 1, SwissFiles: doc,
	})
	probe.Maps["swiss/site-profile"] = map[string]string{
		"profile.yaml": profileYAML +
			"route:\n  gateway: https://llm.example.com\n  auth:\n    header: X-Api-Key\n",
	}
	srv := testServer(t, probe)
	_, body := get(t, srv, "/api/releases/modelforge/r/status")

	if body["authHeader"] != "X-Api-Key" {
		t.Errorf("authHeader = %v", body["authHeader"])
	}
	prefix, present := body["authPrefix"]
	if !present || prefix != "" {
		t.Fatalf("an empty prefix must be reported, got %v (present=%v)", prefix, present)
	}
}

// The deploy form shows the path a model's weights actually default to, which
// means resolving the site's template here rather than describing it there.
func TestModelEndpointResolvesTheSitePath(t *testing.T) {
	srv := testServer(t, liveProbe())
	code, body := get(t, srv, "/api/catalog/modelforge")
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	path, _ := body["localPath"].(string)
	if path == "" {
		t.Fatal("no localPath: the form has nothing to show as the default")
	}
	if !strings.Contains(path, "/") {
		t.Errorf("localPath = %q, want a resolved host path", path)
	}
	// Built from the template, so no placeholder may survive into it.
	if strings.Contains(path, "{{") {
		t.Errorf("localPath = %q, template was not substituted", path)
	}

	// The template comes too: it is what the form shows when this entry's hf
	// cannot be substituted into it, instead of a sentence about templates.
	tmpl, _ := body["pathTemplate"].(string)
	if !strings.Contains(tmpl, "{{") {
		t.Errorf("pathTemplate = %q, want the unsubstituted template", tmpl)
	}
}

// The overview is where an operator looks for the address, so the route is on
// the row rather than one page deeper. It is derived the same way the detail
// view derives it, off the composed values.
func TestDeploymentsCarryTheRoute(t *testing.T) {
	_, doc := livePlan(t, planRequest{
		Model: "modelforge", Release: "r", ServiceID: "r",
		Overrides: values.Tree{
			"modelRoute": values.Tree{"enabled": true, "nginx": values.Tree{"route": "glm-53"}},
		},
	})
	probe := liveProbe()
	probe.Rel = append(probe.Rel, cluster.Release{
		Name: "r", Namespace: "modelforge", Status: "deployed", Revision: 1, SwissFiles: doc,
	})
	srv := testServer(t, probe)

	_, body := get(t, srv, "/api/deployments")
	var routed, untracked map[string]any
	for _, row := range body["deployments"].([]any) {
		d := row.(map[string]any)
		switch d["release"] {
		case "r":
			routed = d
		case "by-hand":
			untracked = d
		}
	}
	if routed == nil || routed["route"] != "glm-53" {
		t.Fatalf("the row must carry the route: %v", routed)
	}
	// A release with no plan beside it has no route to report, and an invented
	// one would send somebody to a path openresty never had.
	if untracked == nil || untracked["route"] != nil {
		t.Errorf("an untracked release has no route: %v", untracked)
	}
}
