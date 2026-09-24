package server

import (
	"net/http"
	"strings"
	"testing"
)

// A profile stored by an older swissd, carrying a field this version removed.
// Strict decoding refuses it -- correctly, the value would otherwise be read as
// meaning something and mean nothing -- but the site is then unconfigured, not
// broken, and the web has to land somewhere it can be fixed.
//
// Answering /api/profile with a 502 made the profile page render a dead error.
// It now comes back without `profile`, with the reason, and with the document
// itself so the offending line can be deleted rather than the whole profile
// retyped.
func staleProfileServer(t *testing.T) (string, map[string]any) {
	t.Helper()
	stale := liveProbe()
	stale.Maps["swiss/site-profile"] = map[string]string{
		"profile.yaml": profileYAML + "createNamespace: true\n",
	}
	srv := testServer(t, stale)
	code, body := get(t, srv, "/api/profile")
	if code != http.StatusOK {
		t.Fatalf("an unparseable profile is unconfigured, not an error: %d %v", code, body)
	}
	return srv.URL, body
}

func TestStaleProfileIsUnconfiguredNotAnError(t *testing.T) {
	_, body := staleProfileServer(t)

	if body["profile"] != nil {
		t.Errorf("nothing parsed, so there is no profile to show: %v", body["profile"])
	}
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "createNamespace") {
		t.Errorf("the reason must name the offending field: %q", msg)
	}
	// The document has to come back, or the only way to fix one bad line is to
	// retype the profile from the default.
	raw, _ := body["yaml"].(string)
	if !strings.Contains(raw, "createNamespace") || !strings.Contains(raw, "pathTemplate") {
		t.Errorf("the stored document must come back intact: %q", raw)
	}
}

// The gate keys off this: not initialised sends the browser to setup, which is
// the page that can write a new profile.
func TestStaleProfileReportsTheSiteUninitialised(t *testing.T) {
	stale := liveProbe()
	stale.Maps["swiss/site-profile"] = map[string]string{
		"profile.yaml": profileYAML + "createNamespace: true\n",
	}
	srv := testServer(t, stale)

	_, body := get(t, srv, "/api/session")
	if body["initialized"] != false {
		t.Errorf("a profile that does not parse is not a configured site: %v", body)
	}
}
