package chart

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestVersionsFromARepoIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/charts/index.yaml" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, "apiVersion: v1\nentries:\n  sglang:\n    - version: 0.8.0\n    - version: 0.7.1\n  vllm:\n    - version: 1.0.0\n")
	}))
	t.Cleanup(srv.Close)

	got, err := Versions(t.Context(), srv.URL+"/charts/", "", "sglang")
	if err != nil || !reflect.DeepEqual(got, []string{"0.8.0", "0.7.1"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := Versions(t.Context(), srv.URL+"/charts", "", "nope"); err == nil || !strings.Contains(err.Error(), `no chart "nope"`) {
		t.Errorf("a chart the index lacks: %v", err)
	}
}

// A public registry refuses the first read with a Bearer challenge and hands
// out an anonymous token; tags come back a page at a time.
func TestVersionsFromAnOCIRegistry(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			if r.URL.Query().Get("scope") != "repository:org/charts/sglang:pull" {
				http.Error(w, "bad scope "+r.URL.Query().Get("scope"), http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `{"token":"t"}`)
		case r.Header.Get("Authorization") != "Bearer t":
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="reg",scope="repository:org/charts/sglang:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == "/v2/org/charts/sglang/tags/list" && r.URL.Query().Get("last") == "":
			w.Header().Set("Link", `</v2/org/charts/sglang/tags/list?last=0.7.1&n=2>; rel="next"`)
			fmt.Fprint(w, `{"tags":["0.7.0","0.7.1"]}`)
		case r.URL.Path == "/v2/org/charts/sglang/tags/list":
			fmt.Fprint(w, `{"tags":["0.8.0_build.1","sha256-abc.sig"]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	saved := client
	client = srv.Client()
	t.Cleanup(func() { client = saved })

	repo := "oci://" + strings.TrimPrefix(srv.URL, "https://") + "/org/charts"
	got, err := Versions(t.Context(), repo, "", "sglang")
	want := []string{"0.7.0", "0.7.1", "0.8.0+build.1", "sha256-abc.sig"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, %v; want %v", got, err, want)
	}
}

func TestVersionsFromALocalChart(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sglang"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sglang", "Chart.yaml"), []byte("name: sglang\nversion: 0.7.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Versions(t.Context(), "https://ignored.example.com", dir, "sglang")
	if err != nil || !reflect.DeepEqual(got, []string{"0.7.4"}) {
		t.Fatalf("got %v, %v; want the chart directory's own version", got, err)
	}
}

// A private registry, or a chart that is not there: the token endpoint refuses,
// and the error says so in words, with what the registry said.
func TestARefusedReadSaysItNeedsALogin(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"errors":[{"code":"DENIED","message":"requested access to the resource is denied"}]}`)
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="reg",scope="repository:org/charts/sglang:pull"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	saved := client
	client = srv.Client()
	t.Cleanup(func() { client = saved })

	repo := "oci://" + strings.TrimPrefix(srv.URL, "https://") + "/org/charts"
	_, err := Versions(t.Context(), repo, "", "sglang")
	var re *RepoError
	if !errors.As(err, &re) || !re.NeedsLogin() {
		t.Fatalf("got %v, want a RepoError needing a login", err)
	}
	for _, want := range []string{repo + "/sglang", "needs a login", "DENIED: requested access to the resource is denied"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not say %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "/token") {
		t.Errorf("%q names the token endpoint rather than the repository", err)
	}
}

func TestARegistryAskingForBasicAuthNeedsALogin(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="harbor"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	saved := client
	client = srv.Client()
	t.Cleanup(func() { client = saved })

	_, err := Versions(t.Context(), "oci://"+strings.TrimPrefix(srv.URL, "https://")+"/org", "", "sglang")
	var re *RepoError
	if !errors.As(err, &re) || !re.NeedsLogin() || !strings.Contains(err.Error(), "Basic auth") {
		t.Fatalf("got %v", err)
	}
}

func TestAMissingIndexIsReportedByStatus(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	_, err := Versions(t.Context(), srv.URL+"/charts", "", "sglang")
	if err == nil || err.Error() != srv.URL+"/charts/index.yaml: 404 Not Found -- the repository says: 404 page not found" {
		t.Errorf("got %v", err)
	}
}
