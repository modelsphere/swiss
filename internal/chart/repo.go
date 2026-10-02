package chart

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const maxIndex = 32 << 20

var client = &http.Client{Timeout: 30 * time.Second}

// RepoError is a chart repository failing a read. Repo is the location as the
// site profile names it, not the URL that failed (a token endpoint, a page).
type RepoError struct {
	Repo   string
	Status int    // 0 when there was no response
	Detail string // the repository's own message, when it sent one
	Err    error
}

func (e *RepoError) Error() string {
	var b strings.Builder
	switch {
	case e.NeedsLogin():
		// Registries answer 403 for a repository that does not exist too, so
		// the message cannot tell the two apart.
		fmt.Fprintf(&b, "%s refused an anonymous read (%d %s): it needs a login, which swissd does not have, or the chart is not there",
			e.Repo, e.Status, http.StatusText(e.Status))
	case e.Status != 0:
		fmt.Fprintf(&b, "%s: %d %s", e.Repo, e.Status, http.StatusText(e.Status))
	default:
		fmt.Fprintf(&b, "%s: %v", e.Repo, e.Err)
	}
	if e.Detail != "" {
		fmt.Fprintf(&b, " -- the repository says: %s", e.Detail)
	}
	return b.String()
}

func (e *RepoError) Unwrap() error { return e.Err }

func (e *RepoError) NeedsLogin() bool {
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

// Versions lists the versions of chart name at the profile's chart source:
//
//	repo oci://host/path   the registry's tags, read anonymously
//	repo https://...       the repository's index.yaml
//	path                   the chart directory's own version
func Versions(ctx context.Context, repo, path, name string) (vs []string, err error) {
	var where string
	defer func() {
		var re *RepoError
		if errors.As(err, &re) && re.Repo == "" {
			re.Repo = where
		}
	}()
	switch {
	case path != "":
		return localVersion(filepath.Join(path, name))
	case strings.HasPrefix(repo, "oci://"):
		where = strings.TrimSuffix(repo, "/") + "/" + name
		return ociTags(ctx, strings.TrimPrefix(where, "oci://"))
	case repo != "":
		where = strings.TrimSuffix(repo, "/") + "/index.yaml"
		return indexVersions(ctx, where, name)
	}
	return nil, fmt.Errorf("no chart source: set chartRepo or chartPath in the site profile")
}

func localVersion(dir string) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "Chart.yaml"))
	if err != nil {
		return nil, err
	}
	var c struct {
		Version string `yaml:"version"`
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s/Chart.yaml: %w", dir, err)
	}
	return []string{c.Version}, nil
}

func indexVersions(ctx context.Context, index, name string) ([]string, error) {
	body, _, err := get(ctx, index, "")
	if err != nil {
		return nil, err
	}
	var idx struct {
		Entries map[string][]struct {
			Version string `yaml:"version"`
		} `yaml:"entries"`
	}
	if err := yaml.Unmarshal(body, &idx); err != nil {
		return nil, &RepoError{Err: fmt.Errorf("not a chart repository index: %w", err)}
	}
	entries, ok := idx.Entries[name]
	if !ok {
		return nil, fmt.Errorf("%s has no chart %q", index, name)
	}
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Version
	}
	return out, nil
}

func ociTags(ctx context.Context, ref string) ([]string, error) {
	host, repo, ok := strings.Cut(ref, "/")
	if !ok {
		return nil, fmt.Errorf("oci://%s names no repository", ref)
	}
	next := "https://" + host + "/v2/" + repo + "/tags/list"
	var token string
	var tags []string
	for next != "" {
		body, h, err := get(ctx, next, token)
		var re *RepoError
		if errors.As(err, &re) && re.Status == http.StatusUnauthorized && token == "" {
			if token, err = anonymousToken(ctx, h.Get("WWW-Authenticate")); err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		var page struct {
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, &RepoError{Err: fmt.Errorf("not a tag list: %w", err)}
		}
		tags = append(tags, page.Tags...)
		next = nextPage(next, h.Get("Link"))
	}
	// helm pushes a version's + as _, since a tag cannot carry one.
	for i, t := range tags {
		tags[i] = strings.ReplaceAll(t, "_", "+")
	}
	return tags, nil
}

func anonymousToken(ctx context.Context, challenge string) (string, error) {
	scheme, params, _ := strings.Cut(challenge, " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return "", &RepoError{Status: http.StatusUnauthorized, Detail: "it asks for " + scheme + " auth"}
	}
	p := map[string]string{}
	for _, kv := range strings.Split(params, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(kv), "=")
		p[k] = strings.Trim(v, `"`)
	}
	realm, err := url.Parse(p["realm"])
	if err != nil || realm.Scheme == "" {
		return "", &RepoError{Err: fmt.Errorf("its auth challenge names no token realm: %q", challenge)}
	}
	q := realm.Query()
	for _, k := range []string{"service", "scope"} {
		if p[k] != "" {
			q.Set(k, p[k])
		}
	}
	realm.RawQuery = q.Encode()
	body, _, err := get(ctx, realm.String(), "")
	if err != nil {
		return "", err
	}
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &t); err != nil || (t.Token == "" && t.AccessToken == "") {
		return "", &RepoError{Err: errors.New("its token endpoint returned no token")}
	}
	if t.Token != "" {
		return t.Token, nil
	}
	return t.AccessToken, nil
}

// nextPage follows a Link: <...>; rel="next" header, relative to the page.
func nextPage(page, link string) string {
	if !strings.Contains(link, `rel="next"`) {
		return ""
	}
	start, end := strings.Index(link, "<"), strings.Index(link, ">")
	if start < 0 || end < start {
		return ""
	}
	base, err := url.Parse(page)
	if err != nil {
		return ""
	}
	u, err := base.Parse(link[start+1 : end])
	if err != nil {
		return ""
	}
	return u.String()
}

// get reads u, failing with a *RepoError that carries the status and whatever
// the repository said about it.
func get(ctx context.Context, u, token string) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, &RepoError{Err: err}
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		// Repo already names where; *url.Error would say it again.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, nil, &RepoError{Err: err}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxIndex+1))
	if err != nil {
		return nil, resp.Header, &RepoError{Status: resp.StatusCode, Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.Header, &RepoError{Status: resp.StatusCode, Detail: detail(b)}
	}
	if len(b) > maxIndex {
		return nil, resp.Header, &RepoError{Err: fmt.Errorf("%s is larger than %d bytes", u, maxIndex)}
	}
	return b, resp.Header, nil
}

// detail is the message in an error body: the distribution API's errors[], or
// a short line of plain text. An HTML error page says nothing worth relaying.
func detail(body []byte) string {
	var oci struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &oci) == nil && len(oci.Errors) > 0 {
		msgs := make([]string, len(oci.Errors))
		for i, e := range oci.Errors {
			msgs[i] = strings.TrimSpace(e.Code + ": " + e.Message)
		}
		return strings.Join(msgs, "; ")
	}
	s := strings.TrimSpace(string(body))
	if s == "" || len(s) > 200 || !utf8.ValidString(s) || strings.HasPrefix(s, "<") || strings.ContainsAny(s, "\n\r") {
		return ""
	}
	return s
}
