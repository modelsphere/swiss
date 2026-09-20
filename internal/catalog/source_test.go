package catalog

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testEntry = `
apiVersion: catalog.swiss/v1
name: m
version: 1.0.0
source: {hf: org/m}
variants:
  - id: v
    engine: sglang
    chart: {name: sglang, version: "0.8.0"}
    requires: {gpus: 2}
    values:
      extraArgs: [--tp-size=2]
`

const testEntryV2 = `
apiVersion: catalog.swiss/v1
name: m
version: 1.1.0
source: {hf: org/m}
variants:
  - id: v
    engine: sglang
    chart: {name: sglang, version: "0.8.0"}
    requires: {gpus: 2}
    values:
      extraArgs: [--tp-size=2, --mem-fraction-static=0.9]
`

func index(versions map[string]string) string {
	rows := make([]string, 0, len(versions))
	for v, body := range versions {
		rows = append(rows, fmt.Sprintf(
			`{"version":%q,"path":"models/m/%s.yaml","digest":%q,
			  "variants":[{"id":"v","engine":"sglang","chart":{"name":"sglang","version":"0.8.0"},"requires":{"gpus":2}}]}`,
			v, v, contentRef([]byte(body))))
	}
	return fmt.Sprintf(`{
	  "apiVersion": "catalog.swiss/v1",
	  "count": 1,
	  "models": [{"name":"m","source":{"hf":"org/m"},"latest":"1.1.0","versions":[%s]}]
	}`, strings.Join(rows, ","))
}

func serve(t *testing.T, files map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for p, body := range files {
		mux.HandleFunc("/"+p, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		})
	}
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func catalogServer(t *testing.T) *httptest.Server {
	return serve(t, map[string]string{
		"catalog/index.json":          index(map[string]string{"1.0.0": testEntry, "1.1.0": testEntryV2}),
		"catalog/models/m/1.0.0.yaml": testEntry,
		"catalog/models/m/1.1.0.yaml": testEntryV2,
	})
}

func TestOpenOverHTTPAndFetchEntry(t *testing.T) {
	srv := catalogServer(t)
	for _, loc := range []string{srv.URL + "/catalog", srv.URL + "/catalog/", srv.URL + "/catalog/index.json"} {
		c, err := Open(context.Background(), loc)
		if err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		e, err := c.Entry(context.Background(), "m", "")
		if err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		if e.Variants[0].Values == nil {
			t.Errorf("%s: entry fetched without its values", loc)
		}
	}
}

// An empty version is the latest published one; a pin gets exactly what it asked
// for, which is the whole point of publishing versions.
func TestVersionPinning(t *testing.T) {
	c, err := Open(context.Background(), catalogServer(t).URL+"/catalog")
	if err != nil {
		t.Fatal(err)
	}
	latest, err := c.Entry(context.Background(), "m", "")
	if err != nil {
		t.Fatal(err)
	}
	if latest.Version != "1.1.0" {
		t.Fatalf("empty version should resolve to latest, got %s", latest.Version)
	}

	pinned, err := c.Entry(context.Background(), "m", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if pinned.Version != "1.0.0" || len(pinned.Variants[0].Values["extraArgs"].([]any)) != 1 {
		t.Fatalf("pin did not get the older entry: %+v", pinned)
	}
	if pinned.Digest == "" || pinned.Digest == latest.Digest {
		t.Error("each version carries its own digest")
	}

	if _, err := c.Entry(context.Background(), "m", "9.9.9"); err == nil {
		t.Error("an unpublished version must be refused")
	}
}

// The lock: a published version is immutable, so bytes that no longer match the
// index are a rewritten release rather than a new one.
func TestDigestMismatchIsRefused(t *testing.T) {
	srv := serve(t, map[string]string{
		"catalog/index.json":          index(map[string]string{"1.1.0": testEntryV2}),
		"catalog/models/m/1.1.0.yaml": testEntryV2 + "\n# rewritten after publishing\n",
	})
	c, err := Open(context.Background(), srv.URL+"/catalog")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Entry(context.Background(), "m", "1.1.0")
	if err == nil || !strings.Contains(err.Error(), "rewritten") {
		t.Fatalf("got %v", err)
	}
}

func TestRefIsContentAddressed(t *testing.T) {
	srv := catalogServer(t)
	a, err := Open(context.Background(), srv.URL+"/catalog")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(context.Background(), srv.URL+"/catalog/index.json")
	if err != nil {
		t.Fatal(err)
	}
	if a.Ref != b.Ref || !strings.HasPrefix(a.Ref, "sha256:") {
		t.Fatalf("refs differ or malformed: %q vs %q", a.Ref, b.Ref)
	}
}

func TestIndexPathsCannotEscapeTheCatalog(t *testing.T) {
	bad := strings.Replace(index(map[string]string{"1.1.0": testEntryV2}), `"models/m/1.1.0.yaml"`, `"../../../etc/passwd"`, 1)
	srv := serve(t, map[string]string{"catalog/index.json": bad})
	if _, err := Open(context.Background(), srv.URL+"/catalog"); err == nil {
		t.Fatal("an index is remote input; a path escaping the catalog must be refused")
	}
}

func TestIndexRejectsAVersionWithoutADigest(t *testing.T) {
	bad := strings.Replace(index(map[string]string{"1.1.0": testEntryV2}), `"digest":"sha256:`, `"nodigest":"sha256:`, 1)
	if _, err := parseIndex([]byte(bad)); err == nil {
		t.Fatal("a pin cannot be verified without a digest")
	}
}

func TestIndexRejectsAnUnpublishedLatest(t *testing.T) {
	// index() always names 1.1.0 as latest; publishing only 1.0.0 makes it dangle.
	if _, err := parseIndex([]byte(index(map[string]string{"1.0.0": testEntry}))); err == nil {
		t.Fatal("latest must name a published version")
	}
}

func TestStaleIndexIsDetected(t *testing.T) {
	renamed := strings.Replace(testEntryV2, "name: m", "name: something-else", 1)
	srv := serve(t, map[string]string{
		"catalog/index.json":          index(map[string]string{"1.1.0": renamed}),
		"catalog/models/m/1.1.0.yaml": renamed,
	})
	c, err := Open(context.Background(), srv.URL+"/catalog")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Entry(context.Background(), "m", "1.1.0"); err == nil {
		t.Fatal("expected a stale-index error")
	}
}

func TestEntryValidationRunsOnFetch(t *testing.T) {
	bad := strings.Replace(testEntryV2, "extraArgs: [--tp-size=2, --mem-fraction-static=0.9]", "model: {localPath: /mnt/x}", 1)
	srv := serve(t, map[string]string{
		"catalog/index.json":          index(map[string]string{"1.1.0": bad}),
		"catalog/models/m/1.1.0.yaml": bad,
	})
	c, err := Open(context.Background(), srv.URL+"/catalog")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Entry(context.Background(), "m", "1.1.0")
	if err == nil || !strings.Contains(err.Error(), "localPath") {
		t.Fatalf("fetched entries must be validated, got %v", err)
	}
}

func TestLocalDirectoryAndFileBothWork(t *testing.T) {
	for _, loc := range []string{"../../../swiss-catalog", "../../../swiss-catalog/index.json"} {
		c, err := Open(context.Background(), loc)
		if err != nil {
			t.Skipf("example catalog not present: %v", err)
		}
		if _, err := c.Entry(context.Background(), "modelforge", ""); err != nil {
			t.Errorf("%s: %v", loc, err)
		}
	}
}
