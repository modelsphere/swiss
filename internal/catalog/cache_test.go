package catalog

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const indexJSON = `{
  "apiVersion": "catalog.swiss/v1",
  "count": 1,
  "models": [
    {
      "name": "glm-5",
      "latest": "1.0.0",
      "source": {"hf": "zai/glm-5"},
      "versions": [
        {"version": "1.0.0", "path": "entries/glm-5-1.0.0.yaml", "digest": "sha256:abc"}
      ]
    }
  ]
}`

func writeCatalog(t *testing.T, body string) (dir string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCacheRoundTripsTheIndexBytes(t *testing.T) {
	dir := writeCatalog(t, indexJSON)
	c, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	cache := NewCache(t.TempDir())
	if err := cache.Save(dir, c); err != nil {
		t.Fatal(err)
	}

	f, err := NewFetcher(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, at, err := cache.Open(f, dir)
	if err != nil {
		t.Fatal(err)
	}
	// The ref is a digest of the index bytes, so an equal ref is the whole
	// assertion: what comes back is byte-identical to what was published.
	if got.Ref != c.Ref {
		t.Errorf("ref %s, want %s", got.Ref, c.Ref)
	}
	if at.IsZero() {
		t.Error("the cache has to report when it was written")
	}
	if _, ok := got.Index.Model("glm-5"); !ok {
		t.Error("the cached index did not parse into a usable catalog")
	}
}

// Two locations must not share a slot: a cache that answered for the wrong
// catalog would serve one cluster's models as another's.
func TestCacheKeysOnLocation(t *testing.T) {
	a, b := writeCatalog(t, indexJSON), writeCatalog(t, indexJSON)
	cache := NewCache(t.TempDir())
	ca, err := Open(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Save(a, ca); err != nil {
		t.Fatal(err)
	}
	f, err := NewFetcher(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := cache.Open(f, b); err == nil {
		t.Error("a location that was never saved must miss, not answer with another's index")
	}
}

// Having been in a cache is not a reason to trust what is in it: the bytes are
// validated on the way out exactly as a fetched index is.
func TestCacheRejectsCorruptedBytes(t *testing.T) {
	dir := writeCatalog(t, indexJSON)
	cache := NewCache(t.TempDir())
	c, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Save(dir, c); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache.file(dir), []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := NewFetcher(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := cache.Open(f, dir); err == nil {
		t.Error("a corrupted cache file must be an error, not a catalog")
	}
}

// One file per location, and the catalog is now repointed from a form: without
// a sweep the directory grows by an index every time somebody changes it.
func TestCacheSweepsWhatIsNoLongerRead(t *testing.T) {
	dir := writeCatalog(t, indexJSON)
	cacheDir := t.TempDir()
	cache := NewCache(cacheDir)

	stale := filepath.Join(cacheDir, "index-0badc0de0badc0de.json")
	recent := filepath.Join(cacheDir, "index-1111111111111111.json")
	deadTmp := filepath.Join(cacheDir, "index-999.tmp")
	for _, f := range []string{stale, recent, deadTmp} {
		if err := os.WriteFile(f, []byte(indexJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	for _, f := range []string{stale, deadTmp} {
		if err := os.Chtimes(f, old, old); err != nil {
			t.Fatal(err)
		}
	}

	c, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Save(dir, c); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("an index for a location nothing reads any more must be swept")
	}
	if _, err := os.Stat(deadTmp); !os.IsNotExist(err) {
		t.Error("a temp file whose writer was killed must be swept")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Errorf("a recent index must be kept -- a repoint may come straight back: %v", err)
	}
	if _, err := os.Stat(cache.file(dir)); err != nil {
		t.Errorf("the index just saved must survive its own sweep: %v", err)
	}
}

// The location in use is never swept, however old: a catalog unreachable for a
// fortnight is when the last good index matters most, and its file stops being
// rewritten for exactly as long as the fetches keep failing.
func TestCacheKeepsTheIndexInUseHoweverOld(t *testing.T) {
	dir := writeCatalog(t, indexJSON)
	cacheDir := t.TempDir()
	cache := NewCache(cacheDir)
	c, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Save(dir, c); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-90 * 24 * time.Hour)
	if err := os.Chtimes(cache.file(dir), old, old); err != nil {
		t.Fatal(err)
	}
	// A second save for the same location: the sweep it triggers sees its own
	// target, which must be exempt rather than aged out.
	if err := cache.Save(dir, c); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cache.Open(mustFetcher(t, dir), dir); err != nil {
		t.Errorf("the index in use was swept: %v", err)
	}
}

func mustFetcher(t *testing.T, loc string) Fetcher {
	t.Helper()
	f, err := NewFetcher(loc)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// A nil cache is the "nowhere to write" case, and every caller has to be able
// to use it without a branch of its own.
func TestNilCacheIsUsable(t *testing.T) {
	var c *Cache
	if got := NewCache(""); got != nil {
		t.Errorf("an empty dir must turn the cache off, got %#v", got)
	}
	if err := c.Save("loc", &Catalog{}); err != nil {
		t.Errorf("saving to a nil cache must be a no-op, got %v", err)
	}
	if c.Dir() != "" {
		t.Error("a nil cache has no directory")
	}
	if _, _, err := c.Open(nil, "loc"); err == nil {
		t.Error("opening from a nil cache must say there is no cache")
	}
}
