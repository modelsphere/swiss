package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// strictNSServer behaves like a real apiserver about namespaces: a write into
// one that does not exist is NotFound. Only `exists` are there to start with.
func strictNSServer(t *testing.T, exists ...string) (*httptest.Server, *fakeWriter) {
	t.Helper()
	// A profile naming a chart repo, so the plan renders a helmfile and the
	// apply gets as far as running one.
	probe := liveProbe()
	probe.Maps["swiss/site-profile"] = map[string]string{
		"profile.yaml": profileYAML + "chartRepo: oci://ghcr.io/modelsphere/charts\n",
	}
	srv, s := deployServerWith(t, probe, true)
	s.cfg.Server.HelmBin, s.cfg.Server.HelmfileBin = stubHelm(t, 0), stubHelm(t, 0)

	w := &fakeWriter{strictNS: true, namespaces: map[string]bool{}}
	for _, ns := range exists {
		w.namespaces[ns] = true
	}
	s.SetWriter(w)
	return srv, w
}

// An install into a namespace nobody has created yet.
//
// swiss records the plan in the release's own namespace BEFORE it applies
// anything, and with createNamespace that namespace is helm's to create -- which
// happens during the apply. So the write-ahead ran first, into a namespace that
// did not exist, and the install failed with
//
//	plan not recorded, nothing applied: namespaces "..." not found
//
// every time. createNamespace could never actually create a namespace.
func TestInstallCreatesTheNamespaceBeforeRecordingThePlan(t *testing.T) {
	srv, w := strictNSServer(t, "models")

	code, body := post(t, srv, "/api/plans", map[string]any{
		"model": "kimi-k2.5", "release": "fresh", "serviceId": "fresh",
		"namespace": "test-sw-ns", "createNamespace": true,
	})
	hash, _ := body["hash"].(string)
	if code != 200 || hash == "" {
		t.Fatalf("plan failed: %d %v", code, body)
	}

	code, body = post(t, srv, "/api/install", map[string]any{"planHash": hash, "note": "new namespace"})
	if code != 200 {
		t.Fatalf("install into a new namespace must work: %d %v", code, body)
	}
	if len(w.ensured) != 1 || w.ensured[0] != "test-sw-ns" {
		t.Errorf("the namespace must be created before the plan is recorded: %v", w.ensured)
	}
	if _, ok := w.written["test-sw-ns/swiss-plan-fresh"]; !ok {
		t.Errorf("the plan should be recorded beside the release, got %v", keysOf(w.written))
	}
}

// Without createNamespace the write still fails -- correct, swiss must not
// invent namespaces nobody asked for -- but the message has to say what to do,
// because the flag lives on the plan and setting it on the apply is ignored.
func TestInstallIntoAMissingNamespaceExplainsItself(t *testing.T) {
	srv, w := strictNSServer(t, "models")

	code, body := post(t, srv, "/api/plans", map[string]any{
		"model": "kimi-k2.5", "release": "fresh", "serviceId": "fresh",
		"namespace": "test-sw-ns",
	})
	hash, _ := body["hash"].(string)
	if code != 200 || hash == "" {
		t.Fatalf("plan failed: %d %v", code, body)
	}

	code, body = post(t, srv, "/api/install", map[string]any{"planHash": hash})
	if code != http.StatusInternalServerError {
		t.Fatalf("want a refusal: %d %v", code, body)
	}
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "createNamespace") {
		t.Errorf("the error must name the flag that fixes it: %q", msg)
	}
	if len(w.ensured) != 0 {
		t.Errorf("no namespace may be created without being asked: %v", w.ensured)
	}
}

func keysOf(m map[string]map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
