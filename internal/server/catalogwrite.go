package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/modelsphere/swiss/internal/site"
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
	if !secretKey.MatchString(key) {
		writeError(w, http.StatusNotFound, fmt.Sprintf("%q is not a key swissd keeps: catalog.<name>.token", key))
		return
	}
	if code, msg := s.secretsWritable(); code != 0 {
		writeError(w, code, msg)
		return
	}
	var body struct {
		Value string `json:"value"`
	}
	if r.Method == http.MethodPut && (json.NewDecoder(r.Body).Decode(&body) != nil || body.Value == "") {
		writeError(w, http.StatusBadRequest, `want {"value": "<non-empty>"}`)
		return
	}
	keys, err := s.writeSecret(r.Context(), key, body.Value)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secret": s.secretRef(), "keys": keys})
}

// handleCatalogPut updates a catalog. The site profile holds its settings for
// now, so this takes only a writable catalog's token, kept once its
// catalog-server is reached and takes it; a refused one is not kept, and the one
// before stays. A dry run keeps nothing, and checks the token at url when given,
// so a catalog the profile does not hold yet, or not as the form has it, is
// checked before the profile is saved. It never sends a stored token.
func (s *Server) handleCatalogPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
		URL   string `json:"url"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	dryRun := slices.Contains([]string{"true", "1"}, r.URL.Query().Get("dryRun"))
	if err := dec.Decode(&body); err != nil || body.Token == "" || (!dryRun && body.URL != "") {
		writeError(w, http.StatusBadRequest, `want {"token": "<non-empty>"}, and "url" only with dryRun: the rest of a catalog is the site profile's`)
		return
	}
	var repo site.CatalogRepo
	if dryRun && body.URL != "" {
		if !strings.HasPrefix(body.URL, "http://") && !strings.HasPrefix(body.URL, "https://") {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("url %q must start with http:// or https://", body.URL))
			return
		}
		repo = site.CatalogRepo{Name: r.PathValue("catalog"), URL: body.URL}
	} else {
		var err error
		if repo, err = s.CatalogRepo(r.Context(), r.PathValue("catalog")); err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if !dryRun && !repo.Writable {
			writeError(w, http.StatusForbidden, fmt.Sprintf("catalog %s is not writable in the site profile", repo.Name))
			return
		}
	}
	if code, msg := s.secretsWritable(); code != 0 {
		writeError(w, code, msg)
		return
	}
	if code, msg := checkCatalogToken(r.Context(), repo, body.Token); code != 0 {
		writeError(w, code, msg)
		return
	}
	if dryRun {
		writeJSON(w, http.StatusOK, map[string]any{"name": repo.Name, "url": repo.URL, "dryRun": true})
		return
	}
	if _, err := s.writeSecret(r.Context(), catalogTokenKey(repo.Name), body.Token); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, struct {
		site.CatalogRepo
		TokenSet bool `json:"tokenSet"`
	}{repo, true})
}

// secretsWritable is why swissd cannot write its Secret, or 0.
func (s *Server) secretsWritable() (int, string) {
	switch {
	case s.secretRef() == "":
		return http.StatusBadRequest, "this swissd reads its profile from a file, so it keeps no Secret"
	case s.writer == nil:
		return http.StatusForbidden, "this swissd cannot write to the cluster"
	}
	return 0, ""
}

// writeSecret sets key to value in swissd's Secret, or removes it for "".
func (s *Server) writeSecret(ctx context.Context, key, value string) ([]string, error) {
	s.secretsMu.Lock()
	defer s.secretsMu.Unlock()
	data, err := s.readSecrets(ctx)
	if err != nil {
		return nil, err
	}
	data = maps.Clone(data)
	if value != "" {
		data[key] = value
	} else {
		delete(data, key)
	}
	if err := s.writer.PutSecret(ctx, s.secretRef(), data, map[string]string{"app.kubernetes.io/managed-by": "swiss"}); err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "secret written", "secret", s.secretRef(), "key", key, "removed", value == "")
	return secretKeys(data), nil
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
	target, err := catalogAPIURL(repo, r.PathValue("path"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
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

// catalogAPIURL is <url>api/v2/<p>, the catalog's catalog-server API. A URL
// naming its index file reads fine; the API is beside it.
func catalogAPIURL(repo site.CatalogRepo, p string) (*url.URL, error) {
	u, err := url.Parse(repo.URL)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(u.Path, ".json") {
		u.Path = path.Dir(u.Path)
	}
	return u.JoinPath("api/v2", p), nil
}

// checkCatalogToken asks the catalog's catalog-server whether it takes token:
// the status and reason to refuse it with, or 0.
func checkCatalogToken(ctx context.Context, repo site.CatalogRepo, token string) (int, string) {
	target, err := catalogAPIURL(repo, "models")
	if err != nil {
		return http.StatusInternalServerError, err.Error()
	}
	// A dry-run write: catalog-server checks its token first, and answers no
	// body with 400 before reaching the repository.
	target.RawQuery = "dryRun=true"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), http.NoBody)
	if err != nil {
		return http.StatusInternalServerError, err.Error()
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res, err := client.Do(req)
	if err != nil {
		return http.StatusBadGateway, fmt.Sprintf("cannot check the token with catalog %s: %v", repo.Name, err)
	}
	defer res.Body.Close()
	// Only catalog-server's own error, and short: url may name any server.
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &body) != nil || body.Error == "" {
		body.Error = res.Status
	}
	if r := []rune(body.Error); len(r) > 200 {
		body.Error = string(r[:200]) + "…"
	}
	switch res.StatusCode {
	case http.StatusBadRequest:
		return 0, ""
	// No entry of its tokensFile has the token, or it has no tokensFile.
	case http.StatusUnauthorized, http.StatusForbidden:
		return http.StatusUnprocessableEntity, fmt.Sprintf("catalog %s does not take this token: %s", repo.Name, body.Error)
	}
	return http.StatusBadGateway, fmt.Sprintf("cannot check the token with catalog %s: POST %s answered %s: %s", repo.Name, target.Redacted(), res.Status, body.Error)
}
