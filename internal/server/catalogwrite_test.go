package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/modelsphere/swiss/internal/cluster"
)

type seen struct {
	method, path, query, body string
	header                    http.Header
}

// writableCatalog is a swissd whose profile lists "internal", writable, at a
// fake catalog-server that takes the token "tok", and "public", not, at a
// static host; secrets seeds swissd's Secret. index, when given, is the file
// internal's URL names.
func writableCatalog(t *testing.T, secrets map[string]string, index ...string) (*httptest.Server, *Server, *[]seen) {
	t.Helper()
	var got []seen
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, seen{r.Method, r.URL.Path, r.URL.RawQuery, string(b), r.Header.Clone()})
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/pub/"):
			http.NotFound(w, r)
		// As catalog-server has it: a write's token first, then its body.
		case r.Method != "GET" && r.Header.Get("Authorization") != "Bearer tok":
			w.Header().Set("WWW-Authenticate", `Bearer realm="catalog"`)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"that token is not in the server's tokensFile"}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/api/v2/models") && len(b) == 0:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"want JSON"}`))
		default:
			w.Header().Set("ETag", `"sha256:new"`)
			_, _ = w.Write([]byte(`{"commit":"abc"}`))
		}
	}))
	t.Cleanup(up.Close)
	probe := cluster.Fake{
		Maps: map[string]map[string]string{"swiss/site-profile": {"profile.yaml": profileYAML + "catalogs:\n" +
			"  - name: internal\n    url: " + up.URL + "/base/" + strings.Join(index, "") + "\n    schemaVersion: v2\n    writable: true\n" +
			"  - name: public\n    url: " + up.URL + "/pub/\n    schemaVersion: v2\n"}},
		Secrets: map[string]map[string]string{},
	}
	if secrets != nil {
		probe.Secrets["swiss/site-profile-secrets"] = secrets
	}
	s := New(testConfig("c"), probe, discardLogger(), "test")
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, s, &got
}

func send(t *testing.T, method, url, body string, header map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

// A write goes to the catalog's own api/v2, path and query as sent, with the catalog's token,
// and swissd drops the index it cached so the edit shows on the next read.
func TestCatalogVariantWritePassesThrough(t *testing.T) {
	srv, s, got := writableCatalog(t, map[string]string{"catalog.internal.token": "tok"})
	repo, err := s.CatalogRepo(t.Context(), "internal")
	if err != nil {
		t.Fatal(err)
	}
	key := catalog.CacheKey(repo.URL, repo.SchemaVersion)
	s.cats[key] = openCatalog{}

	res := send(t, "PUT", srv.URL+"/api/catalogs/internal/api/v2/models/kimi/variants/tp8?dryRun=true&message=%E8%B0%83", "version: 1.0.1\n",
		map[string]string{"If-Match": `"sha256:old"`, "Cookie": "swiss_session=x"})
	if res.StatusCode != 200 || res.Header.Get("ETag") != `"sha256:new"` {
		t.Fatalf("response not passed back: %d %q", res.StatusCode, res.Header.Get("ETag"))
	}
	if len(*got) != 1 {
		t.Fatalf("catalog hits: %d", len(*got))
	}
	g := (*got)[0]
	if g.method != "PUT" || g.path != "/base/api/v2/models/kimi/variants/tp8" || g.query != "dryRun=true&message=%E8%B0%83" || g.body != "version: 1.0.1\n" {
		t.Fatalf("not forwarded as sent: %+v", g)
	}
	if g.header.Get("Authorization") != "Bearer tok" || g.header.Get("If-Match") != `"sha256:old"` || g.header.Get("Cookie") != "" {
		t.Fatalf("headers: %v", g.header)
	}
	if _, ok := s.cats[key]; ok {
		t.Fatal("the catalog's cached index outlived the write")
	}
}

// A new model goes to the catalog's model collection, its JSON body as sent.
func TestCatalogNewModelPassesThrough(t *testing.T) {
	srv, _, got := writableCatalog(t, map[string]string{"catalog.internal.token": "tok"})
	body := `{"metadata":"name: m\n","variants":["id: a\n"]}`
	res := send(t, "POST", srv.URL+"/api/catalogs/internal/api/v2/models?dryRun=true", body, map[string]string{"Content-Type": "application/json"})
	if res.StatusCode != 200 || len(*got) != 1 {
		t.Fatalf("status %d, catalog hits %d", res.StatusCode, len(*got))
	}
	if g := (*got)[0]; g.method != "POST" || g.path != "/base/api/v2/models" || g.query != "dryRun=true" || g.body != body || g.header.Get("Authorization") != "Bearer tok" {
		t.Fatalf("not forwarded as sent: %+v", g)
	}
}

// A catalog URL may name its index file for reads; writes go to the API beside it.
func TestCatalogWriteBesideANamedIndex(t *testing.T) {
	srv, _, got := writableCatalog(t, map[string]string{"catalog.internal.token": "tok"}, "index-v2.json")
	send(t, "POST", srv.URL+"/api/catalogs/internal/api/v2/models", "{}", nil)
	if len(*got) != 1 || (*got)[0].path != "/base/api/v2/models" {
		t.Fatalf("forwarded to %+v", *got)
	}
}

// Settings, setup and a model's metadata are catalog-server's API too: every
// api/v2 path goes through as it is, with no route of swissd's own.
func TestCatalogAPIPassesEveryPath(t *testing.T) {
	srv, _, got := writableCatalog(t, map[string]string{"catalog.internal.token": "tok"})
	calls := []string{"GET init", "POST init", "GET catalog", "PUT catalog", "GET models/kimi/metadata", "PUT models/kimi/metadata"}
	for _, c := range calls {
		method, path, _ := strings.Cut(c, " ")
		send(t, method, srv.URL+"/api/catalogs/internal/api/v2/"+path, "", nil)
	}
	var reached []string
	for _, g := range *got {
		reached = append(reached, g.method+" "+strings.TrimPrefix(g.path, "/base/api/v2/"))
	}
	if strings.Join(reached, ",") != strings.Join(calls, ",") {
		t.Fatalf("reached %v, want %v", reached, calls)
	}
}

func TestCatalogVariantWriteRefusals(t *testing.T) {
	srv, _, got := writableCatalog(t, nil)
	for path, want := range map[string]int{
		"/api/catalogs/public/api/v2/models/kimi/variants/tp8":   http.StatusForbidden, // not writable
		"/api/catalogs/internal/api/v2/models/kimi/variants/tp8": http.StatusConflict,  // no token yet
		"/api/catalogs/absent/api/v2/models/kimi/variants/tp8":   http.StatusNotFound,
	} {
		if res := send(t, "DELETE", srv.URL+path, "", nil); res.StatusCode != want {
			t.Errorf("%s: %d, want %d", path, res.StatusCode, want)
		}
	}
	if len(*got) != 0 {
		t.Fatalf("a refused write reached the catalog: %+v", *got)
	}
}

// swissd's Secret is written whole from what it holds, one key at a time, and
// listed by key only.
func TestSecrets(t *testing.T) {
	srv, s, _ := writableCatalog(t, map[string]string{"catalog.public.token": "old"})
	w := &fakeWriter{}
	s.SetWriter(w)

	code, body := get(t, srv, "/api/secrets")
	if code != 200 || body["secret"] != "swiss/site-profile-secrets" || len(body["keys"].([]any)) != 1 {
		t.Fatalf("list: %d %v", code, body)
	}
	if raw, _ := io.ReadAll(send(t, "GET", srv.URL+"/api/secrets", "", nil).Body); strings.Contains(string(raw), "old") {
		t.Fatalf("a value was listed: %s", raw)
	}
	if code, out := put(t, srv, "/api/secrets/catalog.internal.token", map[string]string{"value": "new"}); code != 200 {
		t.Fatalf("set: %d %v", code, out)
	}
	if got := w.secrets["swiss/site-profile-secrets"]; got["catalog.internal.token"] != "new" || got["catalog.public.token"] != "old" {
		t.Fatalf("written: %v", got)
	}
	if res := send(t, "DELETE", srv.URL+"/api/secrets/catalog.public.token", "", nil); res.StatusCode != 200 {
		t.Fatalf("remove: %d", res.StatusCode)
	}
	if got := w.secrets["swiss/site-profile-secrets"]; len(got) != 0 {
		t.Fatalf("after removing the only stored key: %v", got)
	}
	// No Secret yet lists no keys as a list: a page reads it before any is set.
	empty, _, _ := writableCatalog(t, nil)
	if raw, _ := io.ReadAll(send(t, "GET", empty.URL+"/api/secrets", "", nil).Body); !strings.Contains(string(raw), `"keys": []`) {
		t.Fatalf("an absent Secret: %s", raw)
	}
	if code, _ := put(t, srv, "/api/secrets/kubeconfig", map[string]string{"value": "x"}); code != http.StatusNotFound {
		t.Errorf("an unknown key: %d", code)
	}
	if code, _ := put(t, srv, "/api/secrets/catalog.internal.token", map[string]string{"value": ""}); code != http.StatusBadRequest {
		t.Errorf("an empty value: %d", code)
	}
}

// A catalog's token is set by updating the catalog, kept only once its
// catalog-server takes it in a dry-run write; one it refuses, or that cannot
// be asked, is not written, and the token held before stays.
func TestCatalogPutToken(t *testing.T) {
	srv, s, got := writableCatalog(t, map[string]string{"catalog.internal.token": "old"})
	w := &fakeWriter{}
	s.SetWriter(w)

	if code, out := put(t, srv, "/api/catalogs/internal", map[string]string{"token": "tok"}); code != 200 || out["name"] != "internal" || out["tokenSet"] != true {
		t.Fatalf("a token the catalog takes: %d %v", code, out)
	}
	if len(*got) != 1 {
		t.Fatalf("catalog hits: %+v", *got)
	}
	if g := (*got)[0]; g.method != "POST" || g.path != "/base/api/v2/models" || g.query != "dryRun=true" || g.body != "" || g.header.Get("Authorization") != "Bearer tok" {
		t.Fatalf("asked with: %+v", g)
	}
	if got := w.secrets["swiss/site-profile-secrets"]; got["catalog.internal.token"] != "tok" {
		t.Fatalf("written: %v", got)
	}

	w.secrets = nil
	for _, c := range []struct {
		catalog string
		body    map[string]string
		code    int
		says    string
	}{
		{"internal", map[string]string{"token": "typo"}, http.StatusUnprocessableEntity, "catalog internal does not take this token: that token is not in the server's tokensFile"},
		{"internal", map[string]string{"token": ""}, http.StatusBadRequest, "want"},
		// The rest of a catalog is the site profile's yet.
		{"internal", map[string]string{"token": "tok", "url": "https://elsewhere/"}, http.StatusBadRequest, "site profile"},
		{"public", map[string]string{"token": "tok"}, http.StatusForbidden, "not writable"},
		{"absent", map[string]string{"token": "tok"}, http.StatusNotFound, `no catalog named "absent"`},
	} {
		code, out := put(t, srv, "/api/catalogs/"+c.catalog, c.body)
		if msg, _ := out["error"].(string); code != c.code || !strings.Contains(msg, c.says) {
			t.Errorf("%s %v: %d %v, want %d saying %q", c.catalog, c.body, code, out, c.code, c.says)
		}
	}
	if w.secrets != nil {
		t.Fatalf("a refused token was written: %v", w.secrets)
	}
}

// A dry run keeps nothing, and checks a catalog at the url it is given, so one
// the site profile does not hold yet is checked before the profile is saved.
func TestCatalogPutDryRun(t *testing.T) {
	srv, s, got := writableCatalog(t, nil)
	w := &fakeWriter{}
	s.SetWriter(w)
	repo, err := s.CatalogRepo(t.Context(), "internal")
	if err != nil {
		t.Fatal(err)
	}
	upstream := strings.TrimSuffix(repo.URL, "/base/")

	code, out := put(t, srv, "/api/catalogs/new?dryRun=true", map[string]string{"token": "tok", "url": upstream + "/base/"})
	if code != 200 || out["dryRun"] != true || out["url"] != upstream+"/base/" {
		t.Fatalf("a new catalog, checked: %d %v", code, out)
	}
	if len(*got) != 1 || (*got)[0].path != "/base/api/v2/models" || (*got)[0].header.Get("Authorization") != "Bearer tok" {
		t.Fatalf("asked with: %+v", *got)
	}
	if code, out := put(t, srv, "/api/catalogs/new?dryRun=true", map[string]string{"token": "typo", "url": upstream + "/base/"}); code != http.StatusUnprocessableEntity {
		t.Errorf("a refused token, checked: %d %v", code, out)
	}
	// The profile's url when none is given, writable or not yet: a form may be ticking it.
	if code, out := put(t, srv, "/api/catalogs/internal?dryRun=1", map[string]string{"token": "tok"}); code != 200 {
		t.Errorf("a catalog the profile holds, checked: %d %v", code, out)
	}
	if code, out := put(t, srv, "/api/catalogs/public?dryRun=true", map[string]string{"token": "tok"}); code != http.StatusBadGateway {
		t.Errorf("a catalog not writable yet is asked, here a static host: %d %v", code, out)
	}
	if code, _ := put(t, srv, "/api/catalogs/new?dryRun=true", map[string]string{"token": "tok"}); code != http.StatusNotFound {
		t.Errorf("a new catalog with no url: %d", code)
	}
	if code, _ := put(t, srv, "/api/catalogs/internal", map[string]string{"token": "tok", "url": upstream + "/base/"}); code != http.StatusBadRequest {
		t.Errorf("a url outside a dry run: %d", code)
	}
	if w.secrets != nil {
		t.Fatalf("a dry run wrote: %v", w.secrets)
	}
}

// A token stored is checked and carried over too: a dry run without a token
// checks the one stored, as when only the url changes, and a rename carries the
// one stored for the old name.
func TestCatalogPutStoredToken(t *testing.T) {
	srv, s, got := writableCatalog(t, map[string]string{"catalog.before.token": "tok", "catalog.internal.token": "stale"})
	w := &fakeWriter{}
	s.SetWriter(w)
	repo, err := s.CatalogRepo(t.Context(), "internal")
	if err != nil {
		t.Fatal(err)
	}
	moved := strings.TrimSuffix(repo.URL, "base/") + "moved/"

	if code, out := put(t, srv, "/api/catalogs/internal?dryRun=true", map[string]string{"url": moved}); code != http.StatusUnprocessableEntity {
		t.Errorf("the stored token, at the url it moves to: %d %v", code, out)
	}
	if g := (*got)[0]; g.path != "/moved/api/v2/models" || g.header.Get("Authorization") != "Bearer stale" {
		t.Fatalf("asked with: %+v", g)
	}
	if code, out := put(t, srv, "/api/catalogs/internal?dryRun=true", map[string]string{"renamedFrom": "before", "url": moved}); code != 200 {
		t.Errorf("a rename, checked: %d %v", code, out)
	}
	if code, out := put(t, srv, "/api/catalogs/internal", map[string]string{"renamedFrom": "before"}); code != 200 {
		t.Fatalf("a rename: %d %v", code, out)
	}
	if got := w.secrets["swiss/site-profile-secrets"]; got["catalog.internal.token"] != "tok" {
		t.Fatalf("the old name's token is not carried over: %v", got)
	}
	for _, c := range []struct {
		path string
		body map[string]string
		code int
	}{
		{"/api/catalogs/internal?dryRun=true", map[string]string{"renamedFrom": "absent"}, http.StatusConflict},
		{"/api/catalogs/internal", map[string]string{}, http.StatusBadRequest},
		{"/api/catalogs/internal", map[string]string{"token": "tok", "renamedFrom": "before"}, http.StatusBadRequest},
		{"/api/catalogs/internal", map[string]string{"renamedFrom": "Not A Name"}, http.StatusBadRequest},
	} {
		if code, out := put(t, srv, c.path, c.body); code != c.code {
			t.Errorf("%s %v: %d %v, want %d", c.path, c.body, code, out, c.code)
		}
	}
}

// A catalog that is not a catalog-server cannot have a token checked, so none is kept.
func TestCatalogPutNeedsACatalogServer(t *testing.T) {
	up := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(up.Close)
	probe := cluster.Fake{
		Maps:    map[string]map[string]string{"swiss/site-profile": {"profile.yaml": profileYAML + "catalogs:\n  - name: old\n    url: " + up.URL + "/\n    schemaVersion: v2\n    writable: true\n"}},
		Secrets: map[string]map[string]string{},
	}
	s := New(testConfig("c"), probe, discardLogger(), "test")
	w := &fakeWriter{}
	s.SetWriter(w)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	code, out := put(t, srv, "/api/catalogs/old", map[string]string{"token": "tok"})
	if msg, _ := out["error"].(string); code != http.StatusBadGateway || !strings.Contains(msg, "cannot check the token with catalog old") || w.secrets != nil {
		t.Fatalf("%d %v, written %v", code, out, w.secrets)
	}
}
