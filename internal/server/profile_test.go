package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aceforeverd/swiss/internal/cluster"
)

func TestProfileEndpointServesTheParsedProfile(t *testing.T) {
	srv := testServer(t, liveProbe())
	code, body := get(t, srv, "/api/profile")
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	if body["source"] != "swiss/site-profile" {
		t.Errorf("the page must say where the profile came from: %v", body["source"])
	}

	p, ok := body["profile"].(map[string]any)
	if !ok {
		t.Fatalf("no profile in %v", body)
	}
	// json tags, not Go field names: the rest of the API is json-tagged.
	if _, present := p["Name"]; present {
		t.Error("Go field names leaked into the API")
	}
	if p["name"] == nil || p["model"] == nil {
		t.Fatalf("expected json-tagged fields: %v", p)
	}
}

// The profile names a Secret; resolving it is the checks' job, not this
// endpoint's. A key must never reach the page.
func TestProfileEndpointNeverResolvesTheSecret(t *testing.T) {
	probe := liveProbe()
	probe.Secrets = map[string]map[string]string{
		"swiss/entrypoint": {"apiKey": "sk-must-not-appear"},
	}
	srv := testServer(t, probe)

	code, body := get(t, srv, "/api/profile")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if strContains(body, "sk-must-not-appear") {
		t.Fatal("the entrypoint key reached the profile response")
	}
}

func TestProfileEndpointReportsAnUnreadableProfile(t *testing.T) {
	srv := testServer(t, brokenProfile{})
	if code, _ := get(t, srv, "/api/profile"); code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", code)
	}
}

func strContains(v any, needle string) bool {
	b, _ := json.Marshal(v)
	return strings.Contains(string(b), needle)
}

type brokenProfile struct{ cluster.Fake }

func (brokenProfile) ConfigMap(context.Context, string) (map[string]string, error) {
	return nil, errors.New("configmaps is forbidden")
}
