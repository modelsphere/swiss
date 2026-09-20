package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func webServer(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := testConfig("c")
	s := New(cfg, fakeProbe(), discardLogger(), "test")
	s.SetWeb(fstest.MapFS{
		"index.html":          {Data: []byte("<!doctype html><div id=root>")},
		"assets/index-abc.js": {Data: []byte("console.log(1)")},
	})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func fetch(t *testing.T, srv *httptest.Server, path string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// A hard refresh on a client route must return the app, not a 404.
func TestClientRoutesFallBackToIndex(t *testing.T) {
	srv := webServer(t)
	for _, p := range []string{"/", "/catalog", "/catalog/glm5.1"} {
		resp, body := fetch(t, srv, p)
		if resp.StatusCode != 200 || body == "" {
			t.Errorf("%s: status %d body %q", p, resp.StatusCode, body)
		}
		if resp.Header.Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: index.html names fingerprinted bundles and must not be cached", p)
		}
	}
}

// Handing an HTML page to a fetch() produces a parse error at the call site and
// hides which request actually failed.
func TestUnknownAPIPathReturnsJSONNotTheApp(t *testing.T) {
	srv := webServer(t)
	resp, body := fetch(t, srv, "/api/does-not-exist")
	if resp.StatusCode != 404 {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct[:16] != "application/json" {
		t.Fatalf("want JSON, got %q: %s", ct, body)
	}
}

// The failure that shows up as "the page loads but nothing renders".
func TestMissingAssetIs404NotIndex(t *testing.T) {
	srv := webServer(t)
	resp, _ := fetch(t, srv, "/assets/index-STALE.js")
	if resp.StatusCode != 404 {
		t.Fatalf("a stale asset reference must 404, got %d", resp.StatusCode)
	}
}

func TestFingerprintedAssetsAreCachedHard(t *testing.T) {
	srv := webServer(t)
	resp, _ := fetch(t, srv, "/assets/index-abc.js")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if resp.Header.Get("Cache-Control") == "" {
		t.Error("fingerprinted assets should be immutable-cacheable")
	}
}

// The API must keep working in a build with no UI in it.
func TestNoWebBuildStillServesAPI(t *testing.T) {
	cfg := testConfig("c")
	srv := httptest.NewServer(New(cfg, fakeProbe(), discardLogger(), "test").Handler())
	defer srv.Close()

	if resp, _ := fetch(t, srv, "/healthz"); resp.StatusCode != 200 {
		t.Errorf("API must work without a UI, got %d", resp.StatusCode)
	}
	resp, body := fetch(t, srv, "/")
	if resp.StatusCode != 404 {
		t.Errorf("want an explanatory 404, got %d", resp.StatusCode)
	}
	if body == "" {
		t.Error("the 404 should say how to build the UI")
	}
}
