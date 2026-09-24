package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelsphere/swiss/internal/auth"
)

// cookieName is the session. HttpOnly, so a script that gets onto the page
// cannot read it -- which is the whole reason it is a cookie rather than
// something the app holds in localStorage.
const cookieName = "swiss_session"

// loginFailWindow and loginFailMax bound password guessing. One account is a
// small target to aim at, and swissd is reachable from wherever its Service is.
const (
	loginFailWindow = time.Minute
	loginFailMax    = 10
	// loginFailDelay is paid on every failure, so an automated guesser gets
	// tens of attempts a minute rather than thousands.
	loginFailDelay = time.Second
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// credentials is the login as currently mounted, re-read rather than cached:
// the kubelet updates a Secret volume in place, so rotating the password takes
// effect at the next login instead of at the next restart.
func (s *Server) credentials() (auth.Credentials, error) {
	if s.creds != nil {
		return s.creds()
	}
	return auth.Load(s.cfg.Server.Auth.Dir)
}

func (s *Server) authDisabled() bool { return s.cfg.Server.Auth.Disabled }

// handleLogin exchanges the password for a token.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	if s.authDisabled() {
		writeError(w, http.StatusBadRequest, "this swissd runs with no login")
		return
	}
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	who := clientIP(r)
	if !s.logins.allow(who) {
		writeError(w, http.StatusTooManyRequests, "too many failed logins; wait a minute")
		return
	}

	creds, err := s.credentials()
	if err != nil {
		// The mount is swissd's own deployment, not something the caller can
		// fix or should learn about.
		s.log.ErrorContext(ctx, "credentials unreadable", "dir", s.cfg.Server.Auth.Dir, "err", err)
		writeError(w, http.StatusInternalServerError, "the login credentials are not readable; check swissd's logs")
		return
	}
	if !creds.Matches(req.Username, req.Password) {
		s.logins.fail(who)
		time.Sleep(loginFailDelay)
		s.log.WarnContext(ctx, "login refused", "user", req.Username, "from", who)
		writeError(w, http.StatusUnauthorized, "wrong username or password")
		return
	}
	s.logins.succeed(who)

	token, exp, err := s.issue(creds)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.setSession(w, r, token, exp)
	// The same shape /api/session answers with, authenticated included: the app
	// stores this reply as the session rather than re-asking, so a reply that
	// leaves the field out reads as logged out and bounces straight back to the
	// login it just came from.
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"user":          creds.Username,
		"expiresAt":     exp.UTC().Format(time.RFC3339),
		"initialized":   s.initialized(ctx),
		// Returned in the body as well as set as a cookie, so curl and any
		// future CLI can hold one without a cookie jar.
		"token": token,
	})
}

// handleLogout drops the cookie.
//
// The token itself stays valid until it expires -- that is what stateless
// means. The revocation that actually works is changing the password, which
// changes the version every outstanding token was signed against.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cookieSecure(r), SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSession is what the app asks before it renders anything: whether this
// browser is logged in, and whether the site has been configured yet.
//
// It answers 200 either way rather than 401, because "not logged in" is the
// expected first answer and an error status for the normal case makes every
// other 401 harder to read.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	if s.authDisabled() {
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated": true, "authDisabled": true, "initialized": s.initialized(ctx),
		})
		return
	}
	claims, ok := s.verify(r)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"user":          claims.Sub,
		"expiresAt":     time.Unix(claims.Exp, 0).UTC().Format(time.RFC3339),
		"initialized":   s.initialized(ctx),
	})
}

// requireAuth guards the API. Everything else -- the health endpoints, the
// login, and the static UI -- is open: the app has to load before it can show a
// login form, and a probe carries no credential.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authDisabled() || !guarded(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		claims, ok := s.verify(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "not logged in")
			return
		}
		// Sliding: a session in active use is extended rather than cut off
		// mid-deploy, and one left alone still ends on schedule.
		if time.Until(time.Unix(claims.Exp, 0)) < s.cfg.Server.Auth.TokenTTL/2 {
			if creds, err := s.credentials(); err == nil {
				if token, exp, err := s.issue(creds); err == nil {
					s.setSession(w, r, token, exp)
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// guarded is every API path but the login and the session probe.
func guarded(path string) bool {
	if !strings.HasPrefix(path, "/api/") {
		return false
	}
	switch path {
	case "/api/login", "/api/logout", "/api/session":
		return false
	}
	return true
}

// verify reads the token from the cookie, or from an Authorization header for
// callers that are not a browser.
func (s *Server) verify(r *http.Request) (auth.Claims, bool) {
	token := ""
	if c, err := r.Cookie(cookieName); err == nil {
		token = c.Value
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		token = strings.TrimPrefix(h, "Bearer ")
	}
	if token == "" {
		return auth.Claims{}, false
	}
	creds, err := s.credentials()
	if err != nil {
		return auth.Claims{}, false
	}
	claims, err := auth.Verify(creds.TokenKey, token, time.Now())
	if err != nil {
		return auth.Claims{}, false
	}
	// The password has changed since this was issued.
	if claims.PV != creds.Version() {
		return auth.Claims{}, false
	}
	return claims, true
}

func (s *Server) issue(creds auth.Credentials) (string, time.Time, error) {
	now := time.Now()
	exp := now.Add(s.cfg.Server.Auth.TokenTTL)
	token, err := auth.Sign(creds.TokenKey, auth.Claims{
		Sub: creds.Username, Iat: now.Unix(), Exp: exp.Unix(), PV: creds.Version(),
	})
	return token, exp, err
}

func (s *Server) setSession(w http.ResponseWriter, r *http.Request, token string, exp time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/", Expires: exp,
		HttpOnly: true, Secure: s.cookieSecure(r), SameSite: http.SameSiteStrictMode,
	})
}

// cookieSecure decides the Secure flag. Auto is right in both of the two
// deployments that exist: behind an ingress terminating TLS, and a plain-http
// port-forward, where an unconditional Secure flag would mean the cookie is set
// and never sent back.
func (s *Server) cookieSecure(r *http.Request) bool {
	switch s.cfg.Server.Auth.CookieSecure {
	case "always":
		return true
	case "never":
		return false
	}
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// initialized reports whether the site profile exists and parses. A fresh
// install has none -- the chart no longer writes one -- so this is what sends
// the first login to the setup page.
func (s *Server) initialized(ctx context.Context) bool {
	_, err := s.Profile(ctx)
	return err == nil
}

func clientIP(r *http.Request) string {
	if h := r.Header.Get("X-Forwarded-For"); h != "" {
		if first, _, ok := strings.Cut(h, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(h)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// failures counts recent failed logins per caller. In memory and per process,
// which is all one replica needs; it slows guessing rather than pretending to
// be an account lockout.
type failures struct {
	mu sync.Mutex
	at map[string][]time.Time
}

func (f *failures) recent(who string, now time.Time) []time.Time {
	kept := f.at[who][:0]
	for _, t := range f.at[who] {
		if now.Sub(t) < loginFailWindow {
			kept = append(kept, t)
		}
	}
	f.at[who] = kept
	return kept
}

func (f *failures) allow(who string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.at == nil {
		f.at = map[string][]time.Time{}
	}
	return len(f.recent(who, time.Now())) < loginFailMax
}

func (f *failures) fail(who string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.at == nil {
		f.at = map[string][]time.Time{}
	}
	now := time.Now()
	f.at[who] = append(f.recent(who, now), now)
}

func (f *failures) succeed(who string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.at, who)
}
