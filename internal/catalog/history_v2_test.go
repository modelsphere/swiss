package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const (
	commit1 = "1111111111111111111111111111111111111111"
	commit2 = "2222222222222222222222222222222222222222"
)

// repo serves a v2 catalog site under /site/ and the repository it was built
// from under /raw/<commit>/, the way GitHub Pages and raw.githubusercontent.com
// do, counting requests by path.
type repo struct {
	t     *testing.T
	srv   *httptest.Server
	mu    sync.Mutex
	files map[string][]byte
	hits  map[string]int
}

func newRepo(t *testing.T) *repo {
	r := &repo{t: t, files: map[string][]byte{}, hits: map[string]int{}}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.hits[req.URL.Path]++
		b, ok := r.files[req.URL.Path]
		if !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *repo) put(path string, b []byte) { r.mu.Lock(); r.files[path] = b; r.mu.Unlock() }

func (r *repo) putJSON(path string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		r.t.Fatal(err)
	}
	r.put(path, b)
}

func (r *repo) hit(path string) int { r.mu.Lock(); defer r.mu.Unlock(); return r.hits[path] }

// b's file at 1.1.0, committed at commit1, and at 1.2.0, committed at commit2
// and current. history and index are what the Pages build would publish;
// edit lets a test break one before the catalog is opened.
func historyCatalog(t *testing.T, edit func(r *repo, index map[string]any, history map[string]any)) (Catalog, *repo) {
	t.Helper()
	r := newRepo(t)
	old := variantYAML(v2Variant{id: "b", version: "1.1.0", extra: "values: {extraArgs: [--old]}\n"})
	cur := variantYAML(v2Variant{id: "b", version: "1.2.0", extra: "values: {extraArgs: [--new]}\n"})
	r.put("/raw/"+commit1+"/models/m/b.yaml", old)
	r.put("/raw/"+commit2+"/models/m/b.yaml", cur)
	r.put("/site/models/m/b.yaml", cur)

	history := map[string]any{
		"model": "m", "id": "b", "path": "models/m/b.yaml",
		"revisions": []map[string]any{
			{"version": "1.2.0", "releaseDate": "2026-10-02", "commit": commit2, "digest": contentRef(cur), "subject": "b: two"},
			{"version": "1.1.0", "releaseDate": "2026-10-01", "commit": commit1, "digest": contentRef(old), "subject": "b: one"},
		},
	}
	index := map[string]any{
		"apiVersion": APIVersionV2, "count": 1, "raw": r.srv.URL + "/raw/", "commit": commit2,
		"models": []any{map[string]any{
			"name": "m", "source": map[string]any{"hf": "org/m"},
			"variants": []any{map[string]any{
				"id": "b", "version": "1.2.0", "path": "models/m/b.yaml", "digest": contentRef(cur),
				"history": "models/m/b.history.json", "commit": commit2,
				"engine": "sglang", "chart": map[string]any{"name": "sglang", "version": "0.8.0"}, "requires": map[string]any{"gpus": 1},
			}},
		}},
	}
	if edit != nil {
		edit(r, index, history)
	}
	r.putJSON("/site/index-v2.json", index)
	r.putJSON("/site/models/m/b.history.json", history)
	c, err := OpenWithSchema(context.Background(), r.srv.URL+"/site/", SchemaV2)
	if err != nil {
		t.Fatal(err)
	}
	return c, r
}

func revisions(h map[string]any) []map[string]any { return h["revisions"].([]map[string]any) }

func TestV2HistoryListsRevisionsNewestFirst(t *testing.T) {
	c, r := historyCatalog(t, nil)
	ctx := context.Background()
	for range 2 {
		revs, err := c.History(ctx, "m", "b")
		if err != nil {
			t.Fatal(err)
		}
		want := []Revision{
			{Version: "1.2.0", ReleaseDate: "2026-10-02", Commit: commit2, Subject: "b: two", Current: true},
			{Version: "1.1.0", ReleaseDate: "2026-10-01", Commit: commit1, Subject: "b: one"},
		}
		for i := range want {
			want[i].Digest = revs[i].Digest
		}
		if len(revs) != 2 || revs[0] != want[0] || revs[1] != want[1] {
			t.Fatalf("history = %+v", revs)
		}
	}
	if n := r.hit("/site/models/m/b.history.json"); n != 1 {
		t.Errorf("history.json read %d times, want once per opened index", n)
	}
	if revs, err := c.History(ctx, "m", ""); err != nil || len(revs) != 2 {
		t.Errorf("the default variant's history = %+v, %v", revs, err)
	}
	var nf *NotFoundError
	for _, args := range [][2]string{{"nope", ""}, {"m", "nope"}} {
		if _, err := c.History(ctx, args[0], args[1]); !errors.As(err, &nf) {
			t.Errorf("%v: want a NotFoundError, got %v", args, err)
		}
	}
}

// An old version is fetched from the repository at its commit and verified
// against the digest its history records.
func TestV2ResolvesAnOldRevision(t *testing.T) {
	c, r := historyCatalog(t, nil)
	ctx := context.Background()
	got, err := c.Resolve(ctx, "m", "b", "1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	revs, _ := c.History(ctx, "m", "b")
	if got.Version != "1.1.0" || got.Digest != revs[1].Digest || got.Variant.Values["extraArgs"].([]any)[0] != "--old" {
		t.Errorf("resolved = %+v", got)
	}
	cur, err := c.Resolve(ctx, "m", "b", "")
	if err != nil || cur.Version != "1.2.0" || cur.Variant.Values["extraArgs"].([]any)[0] != "--new" {
		t.Errorf("current = %+v, %v", cur, err)
	}
	if r.hit("/raw/"+commit2+"/models/m/b.yaml") != 0 || r.hit("/site/models/m/b.yaml") != 1 {
		t.Error("the current file is read beside the index, not from the repository")
	}
	if _, err := c.Resolve(ctx, "m", "b", "1.1.0"); err != nil || r.hit("/raw/"+commit1+"/models/m/b.yaml") != 1 {
		t.Errorf("a verified revision is read once: %v", err)
	}
	if _, err := c.Resolve(ctx, "m", "b", "1.0.0"); err == nil || !strings.Contains(err.Error(), "has no version 1.0.0 (have: 1.2.0, 1.1.0)") {
		t.Errorf("a version the history does not list: %v", err)
	}
}

func TestV2OldRevisionIsRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(r *repo, index, history map[string]any)
		want string
	}{
		"bytes rewritten": {func(r *repo, _, _ map[string]any) {
			r.put("/raw/"+commit1+"/models/m/b.yaml", variantYAML(v2Variant{id: "b", version: "1.1.0", extra: "values: {extraArgs: [--evil]}\n"}))
		}, "does not match the catalog's"},
		"history stale": {func(_ *repo, _, h map[string]any) {
			h["revisions"] = revisions(h)[1:]
		}, "history is stale"},
		"history of another file": {func(_ *repo, _, h map[string]any) {
			h["id"] = "c"
		}, "history is of m/c"},
		"never committed": {func(_ *repo, _, h map[string]any) {
			delete(revisions(h)[1], "commit")
		}, "was never committed"},
		"not a commit": {func(_ *repo, _, h map[string]any) {
			revisions(h)[1]["commit"] = "../../etc"
		}, "is not a 40-character sha"},
		"no raw location": {func(_ *repo, idx, _ map[string]any) {
			delete(idx, "raw")
		}, "names no http(s) raw location"},
		"raw not http": {func(_ *repo, idx, _ map[string]any) {
			idx["raw"] = "file:///etc/"
		}, "names no http(s) raw location"},
		"no history published": {func(_ *repo, idx, _ map[string]any) {
			delete(idx["models"].([]any)[0].(map[string]any)["variants"].([]any)[0].(map[string]any), "history")
		}, "publishes no history"},
		"file is another version": {func(r *repo, _, h map[string]any) {
			b := variantYAML(v2Variant{id: "b", version: "1.0.9"})
			r.put("/raw/"+commit1+"/models/m/b.yaml", b)
			revisions(h)[1]["digest"] = contentRef(b)
		}, "file says m/b 1.0.9, the catalog m/b 1.1.0 -- index is stale"},
	} {
		c, _ := historyCatalog(t, tc.edit)
		if _, err := c.Resolve(context.Background(), "m", "b", "1.1.0"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", name, err, tc.want)
		}
	}
}
