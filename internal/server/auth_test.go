package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/modelsphere/swiss/internal/site"
)

func headersFor(t *testing.T, s *Server, cfg site.RouteAuth, req entrypointAuth) http.Header {
	t.Helper()
	h, err := s.entrypointHeaders(context.Background(), cfg, req)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// The default is the OpenAI convention, so a profile that names only a Secret
// works without spelling out a header.
func TestEntrypointKeyDefaultsToBearer(t *testing.T) {
	probe := liveProbe()
	probe.Secrets = map[string]map[string]string{
		"swiss/entrypoint": {"apiKey": "sk-live"},
	}
	s := New(testConfig("prod-b300"), probe, discardLogger(), "test")

	h := headersFor(t, s, site.RouteAuth{SecretRef: "swiss/entrypoint"}, entrypointAuth{})
	if got := h.Get("Authorization"); got != "Bearer sk-live" {
		t.Fatalf("Authorization = %q", got)
	}
}

// A custom header usually carries the key raw, with no scheme, so the Bearer
// prefix must not follow the key onto one.
func TestCustomHeaderCarriesTheBareKey(t *testing.T) {
	probe := liveProbe()
	probe.Secrets = map[string]map[string]string{"swiss/e": {"token": "abc"}}
	s := New(testConfig("prod-b300"), probe, discardLogger(), "test")

	h := headersFor(t, s, site.RouteAuth{
		Header: "X-Api-Key", SecretRef: "swiss/e", SecretKey: "token",
	}, entrypointAuth{})
	if got := h.Get("X-Api-Key"); got != "abc" {
		t.Fatalf("X-Api-Key = %q, want the bare key", got)
	}
	if h.Get("Authorization") != "" {
		t.Error("a custom header must not also set Authorization")
	}
}

// llm-openresty's own Secret holds "key1:owner1,key2:owner2"; only the key is a credential.
func TestOpenrestyKeyListSendsTheFirstKey(t *testing.T) {
	probe := liveProbe()
	probe.Secrets = map[string]map[string]string{"llm-route/openresty": {"keys": " sk-one:ops, sk-two:dev"}}
	s := New(testConfig("prod-b300"), probe, discardLogger(), "test")

	h := headersFor(t, s, site.RouteAuth{SecretRef: "llm-route/openresty", SecretKey: "keys"}, entrypointAuth{})
	if got := h.Get("Authorization"); got != "Bearer sk-one" {
		t.Fatalf("Authorization = %q, want the first key without its owner", got)
	}
}

// A request-supplied key wins outright: testing a credential before writing it
// into the cluster is the reason the override exists.
func TestRequestKeyOverridesTheProfile(t *testing.T) {
	probe := liveProbe()
	probe.Secrets = map[string]map[string]string{"swiss/e": {"apiKey": "from-secret"}}
	s := New(testConfig("prod-b300"), probe, discardLogger(), "test")

	h := headersFor(t, s,
		site.RouteAuth{SecretRef: "swiss/e"},
		entrypointAuth{APIKey: "from-operator"})
	if got := h.Get("Authorization"); got != "Bearer from-operator" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestRequestHeadersOverrideProfileHeaders(t *testing.T) {
	s := New(testConfig("prod-b300"), liveProbe(), discardLogger(), "test")

	h := headersFor(t, s,
		site.RouteAuth{Headers: map[string]string{"X-Tenant": "default", "X-Keep": "yes"}},
		entrypointAuth{Headers: map[string]string{"X-Tenant": "research"}})
	if h.Get("X-Tenant") != "research" {
		t.Errorf("the request's header should win: %q", h.Get("X-Tenant"))
	}
	if h.Get("X-Keep") != "yes" {
		t.Errorf("a profile header the request does not mention should survive: %q", h.Get("X-Keep"))
	}
}

// A profile naming a Secret that is missing, or a key that is not in it, must
// say so rather than quietly calling the entrypoint unauthenticated.
func TestMissingSecretIsAnError(t *testing.T) {
	s := New(testConfig("prod-b300"), liveProbe(), discardLogger(), "test")
	if _, err := s.entrypointHeaders(context.Background(),
		site.RouteAuth{SecretRef: "swiss/absent"}, entrypointAuth{}); err == nil {
		t.Error("a missing secret must fail the check, not send no key")
	}

	probe := liveProbe()
	probe.Secrets = map[string]map[string]string{"swiss/e": {"other": "x"}}
	s2 := New(testConfig("prod-b300"), probe, discardLogger(), "test")
	if _, err := s2.entrypointHeaders(context.Background(),
		site.RouteAuth{SecretRef: "swiss/e"}, entrypointAuth{}); err == nil {
		t.Error("a secret without the named key must fail")
	}
}

// No auth configured and none supplied is the ordinary case, and must stay a
// plain unauthenticated call rather than an error.
func TestNoAuthConfiguredSendsNothing(t *testing.T) {
	s := New(testConfig("prod-b300"), liveProbe(), discardLogger(), "test")
	h := headersFor(t, s, site.RouteAuth{}, entrypointAuth{})
	if len(h) != 0 {
		t.Fatalf("want no headers, got %v", h)
	}
}

// Results report header names so an operator can tell "no key was sent" from
// "the key was wrong". They must never carry the value.
func TestSentHeaderNamesCarryNoValues(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer sk-secret")
	h.Set("X-Tenant", "research")

	names := sentHeaderNames(h)
	if len(names) != 2 || names[0] != "Authorization" || names[1] != "X-Tenant" {
		t.Fatalf("want sorted names, got %v", names)
	}
	for _, n := range names {
		if n == "Bearer sk-secret" || n == "sk-secret" {
			t.Fatal("a header value reached the result")
		}
	}
}

// A profile that names the Secret without a namespace means swissd's own: the
// key is created for swissd to send, wherever the entrypoint happens to run.
func TestBareSecretRefReadsSwissdsOwnNamespace(t *testing.T) {
	probe := liveProbe()
	probe.Secrets = map[string]map[string]string{"swiss-system/llm-openresty": {"apiKey": "sk-live"}}
	s := New(testConfig("prod-b300"), probe, discardLogger(), "test")
	s.namespace = "swiss-system"

	h, err := s.entrypointHeaders(context.Background(),
		site.RouteAuth{SecretRef: "llm-openresty"}, entrypointAuth{})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.Get("Authorization"); got != "Bearer sk-live" {
		t.Fatalf("Authorization = %q", got)
	}
}
