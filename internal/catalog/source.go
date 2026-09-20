package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// maxFetch caps any single document. An index or an entry is a few KB; anything
// approaching this is a misconfigured URL pointing at something else entirely,
// and streaming it into memory helps nobody.
const maxFetch = 8 << 20

// Fetcher is where a catalog is read from: a local directory or an https base.
// Both resolve entry paths relative to where index.json was found, which is what
// lets the same catalog be served from a checkout, a static bucket or a plain
// web server with no code change.
type Fetcher interface {
	// Index fetches index.json and returns it with the raw bytes it was parsed
	// from -- the bytes are what the catalog reference is computed over.
	Index(ctx context.Context) (*Index, []byte, error)
	// Fetch reads one path relative to the index.
	Fetch(ctx context.Context, rel string) ([]byte, error)
	String() string
}

// NewFetcher picks an implementation from a location:
//
//	https://models.example.com/catalog/      -> https, index.json appended
//	https://models.example.com/index.json    -> https
//	../swiss-catalog                         -> local directory
//	../swiss-catalog/index.json              -> local file
//
// There is no git support and no cloning. A catalog is a published index plus
// the documents it points at; making the consumer speak git would buy nothing a
// static file server does not already give it.
func NewFetcher(loc string) (Fetcher, error) {
	if loc == "" {
		return nil, fmt.Errorf("no catalog location")
	}
	if strings.HasPrefix(loc, "http://") || strings.HasPrefix(loc, "https://") {
		u, err := url.Parse(loc)
		if err != nil {
			return nil, fmt.Errorf("catalog url %q: %w", loc, err)
		}
		if !strings.HasSuffix(u.Path, ".json") {
			u = u.JoinPath("index.json")
		}
		return &httpSource{index: u, client: &http.Client{Timeout: 30 * time.Second}}, nil
	}
	abs, err := filepath.Abs(loc)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
		return &fileSource{index: filepath.Join(abs, "index.json")}, nil
	}
	return &fileSource{index: abs}, nil
}

// safeRel rejects an index whose paths would read outside the catalog. The index
// is remote input; a "../../etc/passwd" in it should be an error, not a fetch.
func safeRel(rel string) error {
	if rel == "" {
		return fmt.Errorf("empty path")
	}
	if strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") {
		return fmt.Errorf("path %q must be relative", rel)
	}
	clean := path.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("path %q escapes the catalog", rel)
	}
	return nil
}

type fileSource struct{ index string }

func (s *fileSource) String() string { return s.index }

func (s *fileSource) Index(ctx context.Context) (*Index, []byte, error) {
	b, err := os.ReadFile(s.index)
	if err != nil {
		return nil, nil, fmt.Errorf("catalog index: %w", err)
	}
	idx, err := parseIndex(b)
	return idx, b, err
}

func (s *fileSource) Fetch(_ context.Context, rel string) ([]byte, error) {
	if err := safeRel(rel); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(filepath.Dir(s.index), filepath.FromSlash(rel)))
}

type httpSource struct {
	index  *url.URL
	client *http.Client
}

func (s *httpSource) String() string { return s.index.String() }

func (s *httpSource) Index(ctx context.Context) (*Index, []byte, error) {
	b, err := s.get(ctx, s.index)
	if err != nil {
		return nil, nil, fmt.Errorf("catalog index: %w", err)
	}
	idx, err := parseIndex(b)
	return idx, b, err
}

func (s *httpSource) Fetch(ctx context.Context, rel string) ([]byte, error) {
	if err := safeRel(rel); err != nil {
		return nil, err
	}
	base := *s.index
	base.Path = path.Dir(base.Path)
	return s.get(ctx, base.JoinPath(rel))
}

func (s *httpSource) get(ctx context.Context, u *url.URL) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, application/yaml, text/yaml, */*")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxFetch+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxFetch {
		return nil, fmt.Errorf("GET %s: larger than %d bytes", u, maxFetch)
	}
	return b, nil
}

// contentRef is the catalog reference: a digest of index.json.
//
// A git SHA is not available over plain HTTP, and an ETag is the server's
// opinion rather than the content's. Hashing what was actually read gives every
// source the same kind of reference, and two consumers that fetched the same
// bytes agree on it without coordinating.
func contentRef(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
