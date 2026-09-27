package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/modelsphere/swiss/internal/site"
)

// handleSLO reads and edits the LLMSLORequirement thresholds for one release.
//
// The chart created the object at install. This handler talks to the SLO
// server named on the site profile, which merge-patches spec.ttft and
// spec.otps only — swissd never writes the requirement itself, so the two
// owners do not share a field manager.
func (s *Server) handleSLO(w http.ResponseWriter, r *http.Request) {
	if (r.Method == http.MethodPut || r.Method == http.MethodDelete) && !s.cfg.Server.AllowDeploy {
		writeError(w, http.StatusForbidden, "this swissd is read-only: set server.allowDeploy and grant rbac.allowDeploy")
		return
	}
	ns, release := r.PathValue("namespace"), r.PathValue("release")
	if ns == "" || release == "" {
		writeError(w, http.StatusBadRequest, "namespace and release are required")
		return
	}

	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	prof, err := s.Profile(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	base := prof.Scaler.SLOServer()
	token, err := s.sloToken(ctx, prof.Scaler)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	// slo-api addresses a requirement by spec.serviceId, which is the release name.
	target := base + "/config/" + url.PathEscape(release)

	switch r.Method {
	case http.MethodGet:
		s.readSLO(w, target, token)
	case http.MethodPut:
		s.writeSLO(w, r, release, target, token)
	case http.MethodDelete:
		s.resetSLO(w, target, token)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// sloToken reads the bearer token. The API refuses anonymous calls, and the
// profile only names where the token lives.
func (s *Server) sloToken(ctx context.Context, sc site.Scaler) (string, error) {
	if sc.SLOTokenSecret == "" {
		return "", fmt.Errorf("scaler.sloTokenSecret is required")
	}
	ref := sc.SLOTokenRef(s.namespace)
	data, err := s.probe.Secret(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("scaler.sloTokenSecret %s: %w", ref, err)
	}
	key := sc.SLOTokenKeyName()
	token := data[key]
	if token == "" {
		return "", fmt.Errorf("secret %s has no key %q", ref, key)
	}
	return token, nil
}

func (s *Server) readSLO(w http.ResponseWriter, target, token string) {
	code, body, err := sloCall(http.MethodGet, target, token, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if code == http.StatusNotFound {
		writeJSON(w, http.StatusOK, map[string]any{"found": false})
		return
	}
	if code != http.StatusOK {
		writeError(w, code, sloErr(body))
		return
	}
	body["found"] = true
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) writeSLO(w http.ResponseWriter, r *http.Request, release, target, token string) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if route, _ := body["route"].(string); route != "" && route != release {
		writeError(w, http.StatusBadRequest, "body route does not match this release")
		return
	}
	body["route"] = release
	code, resp, err := sloCall(http.MethodPut, target, token, body)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if code != http.StatusOK {
		writeError(w, code, sloErr(resp))
		return
	}
	resp["found"] = true
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) resetSLO(w http.ResponseWriter, target, token string) {
	code, body, err := sloCall(http.MethodDelete, target, token, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if code != http.StatusNoContent && code != http.StatusOK {
		writeError(w, code, sloErr(body))
		return
	}
	// The CR stays. slo-api clears the thresholds and restores the CRD
	// defaults, so the next read is that thin object.
	s.readSLO(w, target, token)
}

func sloCall(method, rawURL, token string, body any) (int, map[string]any, error) {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, rawURL, rdr)
	if err != nil {
		return 0, nil, fmt.Errorf("slo server: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("slo server: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out, nil
}

func sloErr(body map[string]any) string {
	if body == nil {
		return "slo server refused the request"
	}
	switch e := body["error"].(type) {
	case string:
		if e != "" {
			return e
		}
	case map[string]any:
		msg, _ := e["message"].(string)
		field, _ := e["field"].(string)
		if field != "" && msg != "" {
			return field + ": " + msg
		}
		if msg != "" {
			return msg
		}
	}
	return "slo server refused the request"
}
