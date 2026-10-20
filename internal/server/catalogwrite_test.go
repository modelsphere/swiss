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

// writableCatalog is a swissd whose profile lists "internal", writable, and
// "public", not, both at a fake catalog-server; secrets seeds swissd's Secret.
func writableCatalog(t *testing.T, secrets map[string]string) (*httptest.Server, *Server, *[]seen) {
	t.Helper()
	var got []seen
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, seen{r.Method, r.URL.Path, r.URL.RawQuery, string(b), r.Header.Clone()})
		w.Header().Set("ETag", `"sha256:new"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"commit":"abc"}`))
	}))
	t.Cleanup(up.Close)
	probe := cluster.Fake{
		Maps: map[string]map[string]string{"swiss/site-profile": {"profile.yaml": profileYAML + "catalogs:\n" +
			"  - name: internal\n    url: " + up.URL + "/base/\n    schemaVersion: v2\n    writable: true\n" +
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

// A write goes to the catalog's own api/v2 as sent, with the catalog's token,
// and swissd drops the index it cached so the edit shows on the next read.
func TestCatalogVariantWritePassesThrough(t *testing.T) {
	srv, s, got := writableCatalog(t, map[string]string{"catalog.internal.token": "tok"})
	repo, err := s.CatalogRepo(t.Context(), "internal")
	if err != nil {
		t.Fatal(err)
	}
	key := catalog.CacheKey(repo.URL, repo.SchemaVersion)
	s.cats[key] = openCatalog{}

	res := send(t, "PUT", srv.URL+"/api/catalog/kimi/variants/tp8?catalog=internal&dryRun=true&message=%E8%B0%83", "version: 1.0.1\n",
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

func TestCatalogVariantWriteRefusals(t *testing.T) {
	srv, _, got := writableCatalog(t, nil)
	for path, want := range map[string]int{
		"/api/catalog/kimi/variants/tp8?catalog=public":   http.StatusForbidden, // not writable
		"/api/catalog/kimi/variants/tp8?catalog=internal": http.StatusConflict,  // no token yet
		"/api/catalog/kimi/variants/tp8?catalog=absent":   http.StatusNotFound,
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
	if code, _ := put(t, srv, "/api/secrets/kubeconfig", map[string]string{"value": "x"}); code != http.StatusNotFound {
		t.Errorf("an unknown key: %d", code)
	}
	if code, _ := put(t, srv, "/api/secrets/catalog.internal.token", map[string]string{"value": ""}); code != http.StatusBadRequest {
		t.Errorf("an empty value: %d", code)
	}
}
