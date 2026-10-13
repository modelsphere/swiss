package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Cache keeps the last index bytes each location served, so a restart while the
// catalog is unreachable renders the marketplace it rendered yesterday instead
// of an empty one. The in-memory copy covers a running process; this covers the
// restart, which is when an unreachable catalog actually hurts.
//
// The index only. An entry is fetched when a model is deployed and checked
// against the digest the index publishes, so a cached entry would buy a deploy
// nothing it does not already have to verify -- and the weights, which are what
// an offline deploy really needs, are not here either.
type Cache struct{ dir string }

// NewCache roots a cache at dir. A nil Cache is usable and does nothing, so a
// caller with nowhere to write needs no branch of its own.
func NewCache(dir string) *Cache {
	if dir == "" {
		return nil
	}
	return &Cache{dir: dir}
}

// Dir is where this cache writes, for logs and diagnostics.
func (c *Cache) Dir() string {
	if c == nil {
		return ""
	}
	return c.dir
}

// CacheKey identifies one configured catalog: the slot its index is cached in,
// and what two catalogs in one profile must not share. v1 and unset are the
// location alone, which is what the key was before v2 -- an index cached by an
// older swissd is still found, and a profile valid then is valid now. v2 on a
// base location is its own slot, since index-v2.json sits beside index.json;
// a location naming its .json is one document whatever the schema.
func CacheKey(loc, schema string) string {
	if strings.TrimSpace(schema) == SchemaV2 && !namesJSON(loc) {
		return SchemaV2 + "|" + loc
	}
	return loc
}

// One file per key, named by a digest of it: two catalogs configured on one
// host do not share a slot, and the name carries no part of a URL that could
// escape the directory.
func (c *Cache) file(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(c.dir, "index-"+hex.EncodeToString(sum[:8])+".json")
}

// Save records the index a catalog was opened from, under the location and
// schemaVersion it was configured with. Best effort by contract: a cache that
// cannot be written is a warning, never a failed request.
func (c *Cache) Save(loc, schema string, cat Catalog) error {
	if c == nil || cat == nil || len(cat.indexBytes()) == 0 {
		return nil
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return err
	}
	// Written to a sibling and renamed: a torn write would otherwise be
	// indistinguishable from a good index the next time one is read.
	tmp, err := os.CreateTemp(c.dir, "index-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(cat.indexBytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	target := c.file(CacheKey(loc, schema))
	if err := os.Rename(tmp.Name(), target); err != nil {
		return err
	}
	c.prune(target)
	return nil
}

const (
	// How long an index is kept for a location nothing reads any more. A
	// catalog repointed away from for a week is not this cluster's catalog, and
	// the cost of being wrong is one fetch.
	indexRetention = 7 * 24 * time.Hour
	// A temp file this old is a rename that never happened: the process writing
	// it was killed. Long enough that a slow write in progress is never the one
	// being swept.
	tmpRetention = time.Hour
)

// prune drops indexes for locations this instance no longer reads, and temp
// files a killed process left behind. Without it the directory grows by one
// index every time the catalog is repointed, and repointing is a form field.
//
// Called after a successful save, so it runs about once per cache TTL against
// a directory holding a handful of files -- cheaper to scan than to track.
//
// The index for the location in use is never pruned, however old it is: it is
// the one file here with a job to do, and a catalog that has been unreachable
// for a fortnight is exactly when the last good index matters most.
func (c *Cache) prune(keep string) {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	now := time.Now()
	for _, e := range entries {
		name := filepath.Join(c.dir, e.Name())
		if e.IsDir() || name == keep {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		age := now.Sub(info.ModTime())
		switch {
		case strings.HasSuffix(e.Name(), ".tmp"):
			if age > tmpRetention {
				os.Remove(name)
			}
		case strings.HasPrefix(e.Name(), "index-") && strings.HasSuffix(e.Name(), ".json"):
			if age > indexRetention {
				os.Remove(name)
			}
		}
	}
}

// Open builds a catalog from the cached index for loc and schemaVersion, with
// the time it was written. The bytes are parsed and validated exactly as a
// fetched index is: having been in a cache is not a reason to trust what is in
// it.
func (c *Cache) Open(f Fetcher, loc, schema string) (Catalog, time.Time, error) {
	if c == nil {
		return nil, time.Time{}, fmt.Errorf("no catalog cache configured")
	}
	name := c.file(CacheKey(loc, schema))
	raw, err := os.ReadFile(name)
	if err != nil {
		return nil, time.Time{}, err
	}
	cat, err := open(f, raw)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("cached index %s: %w", name, err)
	}
	var at time.Time
	if fi, err := os.Stat(name); err == nil {
		at = fi.ModTime()
	}
	return cat, at, nil
}
