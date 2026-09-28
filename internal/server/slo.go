package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"time"

	"github.com/modelsphere/swiss/internal/site"
)

// handleSLO reads and edits the LLMSLORequirement thresholds for one release.
//
// The chart created the object at install. This handler talks to the SLO
// server named on the site profile, which merge-patches the SLO-owned fields
// only — swissd never writes the requirement itself, so the two owners do not
// share a field manager.
//
// That server does not speak the CRD exactly: it exposes spec.priority (0..10)
// as a boolean and rejects any top-level field it does not know. sloUpstream
// and sloClient translate in both directions, so this endpoint keeps the CRD's
// own vocabulary on its side of the wire.
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
		s.readSLO(w, base, target, token)
	case http.MethodPut:
		s.writeSLO(w, r, release, base, target, token)
	case http.MethodDelete:
		s.resetSLO(w, base, target, token)
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

func (s *Server) readSLO(w http.ResponseWriter, base, target, token string) {
	code, body, err := sloCall(http.MethodGet, target, token, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if code == http.StatusNotFound {
		if why := sloUnsynced(base); why != "" {
			writeError(w, http.StatusServiceUnavailable, why)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"found": false})
		return
	}
	if code != http.StatusOK {
		writeError(w, code, sloErr(body))
		return
	}
	body = sloClient(body)
	body["found"] = true
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) writeSLO(w http.ResponseWriter, r *http.Request, release, base, target, token string) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body == nil { // a literal `null` body decodes to a nil map
		body = map[string]any{}
	}
	if route, _ := body["route"].(string); route != "" && route != release {
		writeError(w, http.StatusBadRequest, "body route does not match this release")
		return
	}
	body["route"] = release
	up, err := sloUpstream(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	code, resp, err := sloCall(http.MethodPut, target, token, up)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if code != http.StatusOK {
		if code == http.StatusNotFound {
			if why := sloUnsynced(base); why != "" {
				writeError(w, http.StatusServiceUnavailable, why)
				return
			}
		}
		writeError(w, code, sloErr(resp))
		return
	}
	resp = sloClient(resp)
	resp["found"] = true
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) resetSLO(w http.ResponseWriter, base, target, token string) {
	code, body, err := sloCall(http.MethodDelete, target, token, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if code != http.StatusNoContent && code != http.StatusOK {
		if code == http.StatusNotFound {
			if why := sloUnsynced(base); why != "" {
				writeError(w, http.StatusServiceUnavailable, why)
				return
			}
		}
		writeError(w, code, sloErr(body))
		return
	}
	// The CR stays. slo-api clears the thresholds and restores the CRD
	// defaults, so the next read is that thin object.
	s.readSLO(w, base, target, token)
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
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
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

// The SLO server stores the CRD's spec.priority as a boolean: true is 10,
// false is 0. Values 1..9 exist in the CRD — decision-gen schedules by tier
// and preempts across tiers — but that API cannot carry them.
const (
	sloHighPriority = 10
	sloLowPriority  = 0
)

// sloUpstream rewrites a request body into what the SLO server accepts. It
// translates priority to highPriority and drops `found`, which swissd itself
// adds on a read and which the server would refuse as an unknown field.
func sloUpstream(body map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(body))
	for k, v := range body {
		if k == "found" {
			continue
		}
		out[k] = v
	}
	raw, ok := out["priority"]
	if !ok {
		return out, nil
	}
	delete(out, "priority")
	n, ok := raw.(float64)
	if !ok || n != math.Trunc(n) || n < sloLowPriority || n > sloHighPriority {
		return nil, fmt.Errorf("priority must be an integer between %d and %d", sloLowPriority, sloHighPriority)
	}
	// Refuse a tier the server would silently flatten rather than writing a
	// number the caller did not ask for.
	if n != sloHighPriority && n != sloLowPriority {
		return nil, fmt.Errorf(
			"the slo server stores priority as high (%d) or normal (%d) only, so %d cannot be set from here",
			sloHighPriority, sloLowPriority, int(n))
	}
	high := n == sloHighPriority
	if was, ok := out["highPriority"].(bool); ok && was != high {
		return nil, fmt.Errorf("priority %d and highPriority %v disagree", int(n), was)
	}
	out["highPriority"] = high
	return out, nil
}

// sloClient adds the CRD integer back onto a reply, so a caller that reads
// what it wrote sees one vocabulary. The server reports only the boolean, so a
// requirement parked at tier 1..9 by some other writer reads back as normal.
func sloClient(body map[string]any) map[string]any {
	if body == nil {
		return map[string]any{}
	}
	if _, ok := body["priority"]; ok {
		return body
	}
	if high, ok := body["highPriority"].(bool); ok {
		if high {
			body["priority"] = sloHighPriority
		} else {
			body["priority"] = sloLowPriority
		}
	}
	return body
}

// sloUnsynced explains why the SLO server can answer for no route at all, or
// "" when it is ready. Its 404 is ambiguous: from a ready server the release
// genuinely has no requirement, but from one whose CR watch never populated it
// means nothing at all is registered — most often because that server watches
// a different LLMSLORequirement API group than the CRD installed here.
func sloUnsynced(base string) string {
	code, body, err := sloCall(http.MethodGet, base+"/readyz", "", nil)
	// Only an explicit 503 is an answer. The call that brought us here worked,
	// so anything else — a transport error, or an older server with no
	// /readyz at all — is not grounds for inventing a second failure.
	if err != nil || code != http.StatusServiceUnavailable {
		return ""
	}
	reason, _ := body["reason"].(string)
	if reason == "" {
		reason = fmt.Sprintf("readyz returned %d", code)
	}
	return "slo server is not ready (" + reason + "): it cannot say whether this release has a requirement. " +
		"Check that it watches the same LLMSLORequirement API group as the CRD installed on this cluster."
}
