package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelsphere/swiss/internal/auth"
	"github.com/modelsphere/swiss/internal/cluster"
)

const (
	testUser = "admin"
	testPass = "hunter2"
)

// loginServer is a swissd with the login on, and a client that keeps cookies --
// which is how a browser talks to it.
func loginServer(t *testing.T, probe cluster.Probe) (*httptest.Server, *Server, *http.Client) {
	t.Helper()
	cfg := testConfig("prod-b300")
	cfg.Server.Auth.Disabled = false
	s := New(cfg, probe, discardLogger(), "test")
	s.SetCredentials(func() (auth.Credentials, error) {
		return auth.Credentials{Username: testUser, Password: testPass, TokenKey: []byte("test-key")}, nil
	})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return srv, s, &http.Client{Jar: jar}
}

func do(t *testing.T, c *http.Client, method, url string, body any) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func login(t *testing.T, srv *httptest.Server, c *http.Client) map[string]any {
	t.Helper()
	code, body := do(t, c, http.MethodPost, srv.URL+"/api/login",
		map[string]string{"username": testUser, "password": testPass})
	if code != 200 {
		t.Fatalf("login: %d %v", code, body)
	}
	return body
}

// The API is closed; the UI that has to render a login form is not.
func TestAPINeedsALoginAndTheUIDoesNot(t *testing.T) {
	srv, _, c := loginServer(t, liveProbe())

	for _, path := range []string{"/api/cluster", "/api/deployments", "/api/nodes", "/api/catalog"} {
		if code, _ := do(t, c, http.MethodGet, srv.URL+path, nil); code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", path, code)
		}
	}
	for _, path := range []string{"/healthz", "/readyz", "/api/session"} {
		if code, _ := do(t, c, http.MethodGet, srv.URL+path, nil); code != 200 {
			t.Errorf("%s must stay open, got %d", path, code)
		}
	}
}

func TestLoginOpensTheAPI(t *testing.T) {
	srv, _, c := loginServer(t, liveProbe())
	body := login(t, srv, c)

	// The app stores this reply as the session instead of re-asking, so it has
	// to be a session: a reply with no authenticated field reads as logged out
	// and lands back on the login.
	if body["authenticated"] != true {
		t.Errorf("the login reply must be a session: %v", body)
	}
	if body["user"] != testUser {
		t.Errorf("login says user = %v", body["user"])
	}
	if body["token"] == "" || body["token"] == nil {
		t.Error("the token is returned in the body as well, for callers with no cookie jar")
	}
	if code, out := do(t, c, http.MethodGet, srv.URL+"/api/cluster", nil); code != 200 {
		t.Fatalf("after login: %d %v", code, out)
	}

	code, session := do(t, c, http.MethodGet, srv.URL+"/api/session", nil)
	if code != 200 || session["authenticated"] != true || session["user"] != testUser {
		t.Fatalf("session: %d %v", code, session)
	}
}

// The cookie is the browser's; a Bearer token is for everything else.
func TestBearerTokenWorksWithoutACookie(t *testing.T) {
	srv, _, c := loginServer(t, liveProbe())
	token, _ := login(t, srv, c)["token"].(string)

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/cluster", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{}).Do(req) // no jar
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("bearer token refused: %d", resp.StatusCode)
	}
}

func TestTheSessionCookieIsNotReadableByScript(t *testing.T) {
	srv, _, c := loginServer(t, liveProbe())
	resp, err := c.Post(srv.URL+"/api/login", "application/json",
		bytes.NewReader([]byte(`{"username":"admin","password":"hunter2"}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	for _, ck := range resp.Cookies() {
		if ck.Name != cookieName {
			continue
		}
		if !ck.HttpOnly {
			t.Error("the session cookie must be HttpOnly")
		}
		if ck.SameSite != http.SameSiteStrictMode {
			t.Error("the session cookie must be SameSite=Strict")
		}
		return
	}
	t.Fatal("no session cookie was set")
}

func TestWrongPasswordIsRefused(t *testing.T) {
	srv, _, c := loginServer(t, liveProbe())
	code, _ := do(t, c, http.MethodPost, srv.URL+"/api/login",
		map[string]string{"username": testUser, "password": "wrong"})
	if code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", code)
	}
	if code, _ := do(t, c, http.MethodGet, srv.URL+"/api/cluster", nil); code != http.StatusUnauthorized {
		t.Error("a refused login must leave the API closed")
	}
}

func TestLogoutClearsTheSession(t *testing.T) {
	srv, _, c := loginServer(t, liveProbe())
	login(t, srv, c)
	if code, _ := do(t, c, http.MethodPost, srv.URL+"/api/logout", nil); code != 200 {
		t.Fatal("logout failed")
	}
	if code, _ := do(t, c, http.MethodGet, srv.URL+"/api/cluster", nil); code != http.StatusUnauthorized {
		t.Error("the API must be closed again after logout")
	}
}

// A stateless token cannot be withdrawn, so changing the password is the
// revocation that actually works.
func TestChangingThePasswordInvalidatesOutstandingTokens(t *testing.T) {
	srv, s, c := loginServer(t, liveProbe())
	login(t, srv, c)

	s.SetCredentials(func() (auth.Credentials, error) {
		return auth.Credentials{Username: testUser, Password: "rotated", TokenKey: []byte("test-key")}, nil
	})
	if code, _ := do(t, c, http.MethodGet, srv.URL+"/api/cluster", nil); code != http.StatusUnauthorized {
		t.Error("a token issued against the old password must stop working")
	}
}

func TestAnExpiredTokenIsRefused(t *testing.T) {
	srv, s, c := loginServer(t, liveProbe())
	creds, _ := s.credentials()
	stale, err := auth.Sign(creds.TokenKey, auth.Claims{
		Sub: testUser, Exp: time.Now().Add(-time.Minute).Unix(), PV: creds.Version(),
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/cluster", nil)
	req.Header.Set("Authorization", "Bearer "+stale)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an expired token was accepted: %d", resp.StatusCode)
	}
}

// One account is a small target. Guessing is slowed rather than pretending to
// be an account lockout.
func TestRepeatedFailuresAreThrottled(t *testing.T) {
	srv, s, c := loginServer(t, liveProbe())
	// Pre-load the counter rather than paying the per-failure delay here.
	for range loginFailMax {
		s.logins.fail("127.0.0.1")
	}
	code, _ := do(t, c, http.MethodPost, srv.URL+"/api/login",
		map[string]string{"username": testUser, "password": "wrong"})
	if code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", code)
	}
}

// A fresh install has no profile: the chart no longer writes one, and the
// first login has to land somewhere that says so.
func TestSessionReportsWhetherTheSiteIsConfigured(t *testing.T) {
	bare := liveProbe()
	delete(bare.Maps, "swiss/site-profile")
	srv, _, c := loginServer(t, bare)

	if body := login(t, srv, c); body["initialized"] != false {
		t.Fatalf("a site with no profile is not initialized: %v", body)
	}

	_, session := do(t, c, http.MethodGet, srv.URL+"/api/session", nil)
	if session["initialized"] != false {
		t.Errorf("session: %v", session)
	}
}

func proxied(t *testing.T, url, key, user string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if user != "" {
		req.Header.Set("X-Remote-User", user)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// Behind console: its key is the login, and it says who the user is.
func TestTheFrontProxyKeyOpensTheAPI(t *testing.T) {
	srv, s, _ := loginServer(t, liveProbe())
	s.SetCredentials(func() (auth.Credentials, error) {
		return auth.Credentials{Username: testUser, Password: testPass, TokenKey: []byte("test-key"), ProxyKey: "proxy-key"}, nil
	})

	if code, body := proxied(t, srv.URL+"/api/cluster", "proxy-key", "alice"); code != 200 {
		t.Fatalf("proxy key refused: %d %v", code, body)
	}
	_, session := proxied(t, srv.URL+"/api/session", "proxy-key", "alice")
	if session["authenticated"] != true || session["user"] != "alice" {
		t.Fatalf("session = %v, want alice, authenticated", session)
	}
	if _, has := session["expiresAt"]; has {
		t.Fatalf("a proxied session has no expiry of swissd's: %v", session)
	}
}

// The header alone is a claim anyone in the cluster can make.
func TestTheProxyIsTrustedOnlyWithItsKey(t *testing.T) {
	srv, s, _ := loginServer(t, liveProbe())
	s.SetCredentials(func() (auth.Credentials, error) {
		return auth.Credentials{Username: testUser, Password: testPass, TokenKey: []byte("test-key"), ProxyKey: "proxy-key"}, nil
	})
	for name, key := range map[string]string{"no key": "", "wrong key": "not-the-key"} {
		if code, _ := proxied(t, srv.URL+"/api/cluster", key, "admin"); code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, code)
		}
	}
}

// With no key mounted, no bearer -- the empty one included -- is the proxy.
func TestNoProxyKeyTrustsNoProxy(t *testing.T) {
	srv, _, _ := loginServer(t, liveProbe())
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/cluster", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer ")
	req.Header.Set("X-Remote-User", "admin")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("empty bearer with no proxy key: %d, want 401", resp.StatusCode)
	}
}
