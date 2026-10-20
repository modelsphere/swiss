package server

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/modelsphere/swiss/internal/catalog"
)

// secretKey is every key swissd keeps in its Secret: a writable catalog's
// token is the only kind yet.
var secretKey = regexp.MustCompile(`^catalog\.[a-z0-9][a-z0-9._-]*\.token$`)

func catalogTokenKey(catalog string) string { return "catalog." + catalog + ".token" }

// secretRef is the one Secret swissd writes and reads, beside the profile:
// "<profile ConfigMap>-secrets". Empty for a profile read from a file.
func (s *Server) secretRef() string {
	if ref := s.cfg.Cluster.Profile.ConfigMap; ref != "" {
		return ref + "-secrets"
	}
	return ""
}

func (s *Server) readSecrets(ctx context.Context) (map[string]string, error) {
	if s.secretRef() == "" {
		return map[string]string{}, nil
	}
	data, err := s.probe.Secret(ctx, s.secretRef())
	if apierrors.IsNotFound(err) {
		return map[string]string{}, nil
	}
	return data, err
}

// secretKeys is never nil: no Secret yet is an empty list, not JSON null.
func secretKeys(data map[string]string) []string {
	return append([]string{}, slices.Sorted(maps.Keys(data))...)
}

// handleSecrets lists what is set, never a value.
func (s *Server) handleSecrets(w http.ResponseWriter, r *http.Request) {
	data, err := s.readSecrets(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secret": s.secretRef(), "keys": secretKeys(data)})
}

// handleSecret sets (PUT {"value": ...}) or removes (DELETE) one key.
func (s *Server) handleSecret(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	switch {
	case !secretKey.MatchString(key):
		writeError(w, http.StatusNotFound, fmt.Sprintf("%q is not a key swissd keeps: catalog.<name>.token", key))
		return
	case s.secretRef() == "":
		writeError(w, http.StatusBadRequest, "this swissd reads its profile from a file, so it keeps no Secret")
		return
	case s.writer == nil:
		writeError(w, http.StatusForbidden, "this swissd cannot write to the cluster")
		return
	}
	var body struct {
		Value string `json:"value"`
	}
	if r.Method == http.MethodPut && (json.NewDecoder(r.Body).Decode(&body) != nil || body.Value == "") {
		writeError(w, http.StatusBadRequest, `want {"value": "<non-empty>"}`)
		return
	}

	s.secretsMu.Lock()
	defer s.secretsMu.Unlock()
	data, err := s.readSecrets(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	data = maps.Clone(data)
	if r.Method == http.MethodPut {
		data[key] = body.Value
	} else {
		delete(data, key)
	}
	if err := s.writer.PutSecret(r.Context(), s.secretRef(), data, map[string]string{"app.kubernetes.io/managed-by": "swiss"}); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.log.InfoContext(r.Context(), "secret written", "secret", s.secretRef(), "key", key, "removed", r.Method == http.MethodDelete)
	writeJSON(w, http.StatusOK, map[string]any{"secret": s.secretRef(), "keys": secretKeys(data)})
}

// handleCatalogAPI is a writable catalog's catalog-server API, passed through
// as it is: /api/catalogs/<name>/api/v2/<path> is <url>api/v2/<path>, with the
// catalog's token. catalog-server checks every change, so this only routes it.
func (s *Server) handleCatalogAPI(w http.ResponseWriter, r *http.Request) {
	repo, err := s.CatalogRepo(r.Context(), r.PathValue("catalog"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if !repo.Writable {
		writeError(w, http.StatusForbidden, fmt.Sprintf("catalog %s is not writable in the site profile", repo.Name))
		return
	}
	secrets, err := s.readSecrets(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	token := secrets[catalogTokenKey(repo.Name)]
	if token == "" {
		writeError(w, http.StatusConflict, fmt.Sprintf("catalog %s has no write token: set it on the site profile page", repo.Name))
		return
	}
	target, err := url.Parse(repo.URL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// A URL naming its index file reads fine; catalog-server's API is beside it.
	if strings.HasSuffix(target.Path, ".json") {
		target.Path = path.Dir(target.Path)
	}
	target = target.JoinPath("api/v2", r.PathValue("path"))
	target.RawQuery = r.URL.RawQuery

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL, pr.Out.Host = target, ""
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Set("Authorization", "Bearer "+token)
		},
		// The commit is the catalog's new state; serving the old index for the
		// rest of the TTL would show the edit as lost.
		ModifyResponse: func(res *http.Response) error {
			if r.Method == http.MethodGet {
				return nil
			}
			if res.StatusCode < 300 {
				s.mu.Lock()
				delete(s.cats, catalog.CacheKey(repo.URL, repo.SchemaVersion))
				s.mu.Unlock()
			}
			s.log.InfoContext(r.Context(), "catalog write", "catalog", repo.Name, "method", r.Method, "url", target.String(), "status", res.StatusCode)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, http.StatusBadGateway, fmt.Sprintf("catalog %s: %v", repo.Name, err))
		},
	}
	proxy.ServeHTTP(w, r)
}
