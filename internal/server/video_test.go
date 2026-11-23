package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/values"
)

func TestProbePathPerEngine(t *testing.T) {
	for engine, want := range map[string]string{
		"sglang":            "/v1/models",
		"vllm":              "/v1/models",
		catalog.EngineMock:  "/v1/models",
		catalog.EngineVideo: "/readyz",
	} {
		if got := probePath(&plan.Plan{Engine: engine}); got != want {
			t.Errorf("%s: probe path = %q, want %q", engine, got, want)
		}
	}
}

// A video release has one SLO requirement per pool. ?serviceId= reaches one of
// them, and only one the release itself owns.
func TestSLOReachesAPoolOfTheRelease(t *testing.T) {
	var paths []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		id := strings.TrimPrefix(r.URL.Path, "/config/")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"route": id, "highPriority": false,
			"minimumDeployment": map[string]any{"type": "replica", "value": 1},
			"queue":             map[string]any{"maxDepth": 5},
		})
	}))
	t.Cleanup(up.Close)

	pool := cluster.ObjectRef{APIVersion: "inference.modelsphere.dev/v1alpha1", Kind: "JobSLORequirement", Namespace: "models", Name: "h3-minimax-h3-server-fl2va"}
	cfg := testConfig("prod")
	cfg.Server.AllowDeploy = true
	s := New(cfg, cluster.Fake{
		Maps: map[string]map[string]string{
			"swiss/site-profile": {"profile.yaml": profileYAML + "\nscaler:\n  sloAddress: " + up.URL + "\n  sloTokenSecret: llm-scaler/slo-api\n"},
		},
		Secrets: map[string]map[string]string{"llm-scaler/slo-api": {"token": "slo-secret"}},
		Rel:     []cluster.Release{{Name: "h3", Namespace: "models", Status: "deployed", Revision: 1, Objects: []cluster.ObjectRef{pool}}},
		Objs: map[string]cluster.Object{
			"JobSLORequirement/models/h3-minimax-h3-server-fl2va": {Spec: map[string]any{"serviceId": "h3-minimax-h3-server-fl2va"}},
		},
	}, discardLogger(), "test")
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	code, body := get(t, srv, "/api/releases/models/h3/slo?serviceId=h3-minimax-h3-server-fl2va")
	if code != 200 || body["route"] != "h3-minimax-h3-server-fl2va" || body["queue"] == nil {
		t.Fatalf("get %d %v", code, body)
	}
	code, body = put(t, srv, "/api/releases/models/h3/slo?serviceId=h3-minimax-h3-server-fl2va", map[string]any{
		"maximumDeployment": map[string]any{"type": "replica", "value": 4},
	})
	if code != 200 || body["route"] != "h3-minimax-h3-server-fl2va" {
		t.Fatalf("put %d %v", code, body)
	}
	before := len(paths)
	code, body = get(t, srv, "/api/releases/models/h3/slo?serviceId=someone-else")
	if code != http.StatusNotFound {
		t.Fatalf("a serviceId the release does not own must be refused, got %d %v", code, body)
	}
	if len(paths) != before {
		t.Errorf("a refused serviceId must not reach the SLO server: %v", paths[before:])
	}
	want := []string{"GET /config/h3-minimax-h3-server-fl2va", "PUT /config/h3-minimax-h3-server-fl2va"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Errorf("upstream calls = %v, want %v", paths, want)
	}
}

// A pool's SLO requirement that cannot be read is a failure to tell, not a
// foreign serviceId: the caller sees the cause.
func TestSLOUnreadableRequirementIsNotForeign(t *testing.T) {
	pool := cluster.ObjectRef{APIVersion: "inference.modelsphere.dev/v1alpha1", Kind: "JobSLORequirement", Namespace: "models", Name: "h3-minimax-h3-server-fl2va"}
	cfg := testConfig("prod")
	cfg.Server.AllowDeploy = true
	s := New(cfg, cluster.Fake{
		Maps: map[string]map[string]string{
			"swiss/site-profile": {"profile.yaml": profileYAML + "\nscaler:\n  sloAddress: http://127.0.0.1:1\n  sloTokenSecret: llm-scaler/slo-api\n"},
		},
		Secrets: map[string]map[string]string{"llm-scaler/slo-api": {"token": "slo-secret"}},
		Rel:     []cluster.Release{{Name: "h3", Namespace: "models", Status: "deployed", Revision: 1, Objects: []cluster.ObjectRef{pool}}},
		ObjErr:  map[string]error{"JobSLORequirement/models/h3-minimax-h3-server-fl2va": errors.New("jobslorequirements is forbidden")},
	}, discardLogger(), "test")
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	code, body := get(t, srv, "/api/releases/models/h3/slo?serviceId=h3-minimax-h3-server-fl2va")
	if code != http.StatusBadGateway {
		t.Fatalf("an unreadable requirement must be a 502, got %d %v", code, body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "forbidden") {
		t.Errorf("the cause must reach the caller: %v", body)
	}
}

// A video release that names no route publishes on its chart's own default,
// which swiss does not know: it reports no route rather than the release name.
// A named one, and an LLM release either way, keep reporting theirs.
func TestVideoRouteIsUnknownUntilNamed(t *testing.T) {
	routed := values.Tree{"modelRoute": values.Tree{"enabled": true}}
	unnamed, _ := livePlan(t, planRequest{Model: "glm5.1", Release: "h3", Overrides: routed})
	named, _ := livePlan(t, planRequest{Model: "glm5.1", Release: "h3", ServiceID: "h3-video", Overrides: routed})
	if got := routeOf(unnamed); got != "h3" {
		t.Fatalf("an LLM release falls back to its name, got %q", got)
	}
	unnamed.Engine, named.Engine = catalog.EngineVideo, catalog.EngineVideo
	if got := routeOf(unnamed); got != "" {
		t.Errorf("an unnamed video route must be unknown, got %q", got)
	}
	if got := routeOf(named); got != "h3-video" {
		t.Errorf("a named video route is the name, got %q", got)
	}
}

// A video release's SLO requirements are keyed by its pools, never by the
// release name: without ?serviceId= the call is refused, and the release name
// as a serviceId is checked against the release's own like any other.
func TestVideoSLONeedsAPoolOfItsOwn(t *testing.T) {
	var paths []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"route": strings.TrimPrefix(r.URL.Path, "/config/")})
	}))
	t.Cleanup(up.Close)

	p, _ := livePlan(t, planRequest{Model: "glm5.1", Release: "h3", ServiceID: "h3"})
	p.Engine = catalog.EngineVideo
	doc, err := p.Files("")
	if err != nil {
		t.Fatal(err)
	}
	pool := cluster.ObjectRef{APIVersion: "inference.modelsphere.dev/v1alpha1", Kind: "JobSLORequirement", Namespace: "models", Name: "h3-minimax-h3-server-fl2va"}
	cfg := testConfig("prod")
	cfg.Server.AllowDeploy = true
	s := New(cfg, cluster.Fake{
		Maps: map[string]map[string]string{
			"swiss/site-profile": {"profile.yaml": profileYAML + "\nscaler:\n  sloAddress: " + up.URL + "\n  sloTokenSecret: llm-scaler/slo-api\n"},
		},
		Secrets: map[string]map[string]string{"llm-scaler/slo-api": {"token": "slo-secret"}},
		Rel: []cluster.Release{{
			Name: "h3", Namespace: "models", Status: "deployed", Revision: 1,
			Objects: []cluster.ObjectRef{pool}, SwissFiles: doc,
		}},
		Objs: map[string]cluster.Object{
			"JobSLORequirement/models/h3-minimax-h3-server-fl2va": {Spec: map[string]any{"serviceId": "h3-minimax-h3-server-fl2va"}},
		},
	}, discardLogger(), "test")
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	if code, body := get(t, srv, "/api/releases/models/h3/slo"); code != http.StatusBadRequest {
		t.Fatalf("no serviceId on a video release must be a 400, got %d %v", code, body)
	}
	if code, body := get(t, srv, "/api/releases/models/h3/slo?serviceId=h3"); code != http.StatusNotFound {
		t.Fatalf("the release name is not one of a video release's pools, got %d %v", code, body)
	}
	if len(paths) != 0 {
		t.Fatalf("a refused call must not reach the SLO server: %v", paths)
	}
	if code, body := get(t, srv, "/api/releases/models/h3/slo?serviceId=h3-minimax-h3-server-fl2va"); code != 200 {
		t.Fatalf("its own pool must be reachable, got %d %v", code, body)
	}
	if strings.Join(paths, ",") != "GET /config/h3-minimax-h3-server-fl2va" {
		t.Errorf("only the pool reaches the SLO server: %v", paths)
	}
}
