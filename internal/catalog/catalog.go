package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Catalog is an opened catalog, whatever its schema: everything swissd and the
// CLI read from one. V1 and V2 implement it over their own documents, and this
// is the one place they meet:
//
//	v1  index.json      a model version pins one entry file holding every variant
//	v2  index-v2.json   each variant file is pinned on its own version
//
// It is shaped on a variant at a version. v1 fits that -- each variant is at its
// entry's version -- where v2 has no model version to fit v1's shape.
type Catalog interface {
	// Schema is SchemaV1 or SchemaV2, the document this was opened from.
	Schema() string
	// Ref is a digest of the index bytes, recorded in every plan.
	Ref() string
	// Source is the index location, as a plan records it.
	Source() string

	// Models is every model as it is now, in index order.
	Models() []Model
	Model(name string) (Model, bool)
	// History lists a variant's published revisions, newest first; variant ""
	// is the model's default. A model or variant the catalog does not list is
	// a *NotFoundError.
	History(ctx context.Context, model, variant string) ([]Revision, error)

	// Resolve is one variant of one model at one version, fetched and
	// verified. variant "" is the model's default. version "" is the
	// variant's current version; any other must be one in its History.
	Resolve(ctx context.Context, model, variant, version string) (Resolved, error)

	// Published is the digest the catalog publishes now for a variant at a
	// version, and whether it still publishes that pin.
	Published(model, variant, version string) (string, bool)

	// indexBytes is the index as published, for a Cache. Unexported, so V1 and
	// V2 are the only implementations.
	indexBytes() []byte
}

// Model is one model as its index lists it now, the same for every schema.
type Model struct {
	Name        string      `json:"name"`
	DisplayName string      `json:"displayName,omitempty"`
	Description string      `json:"description,omitempty"`
	Family      string      `json:"family,omitempty"`
	Tags        []string    `json:"tags,omitempty"`
	Deprecated  any         `json:"deprecated,omitempty"`
	Source      IndexSource `json:"source"`
	// DefaultVariant is the id resolved when none is named.
	DefaultVariant string         `json:"defaultVariant"`
	Variants       []ModelVariant `json:"variants"`
	Tuning         []Tuning       `json:"tuning,omitempty"`
}

// ModelVariant is one variant as it is now: its pin, and enough to list it.
type ModelVariant struct {
	ID          string   `json:"id"`
	Version     string   `json:"version"`
	Digest      string   `json:"digest"`
	Engine      string   `json:"engine"`
	Default     bool     `json:"default,omitempty"`
	Description string   `json:"description,omitempty"`
	Link        string   `json:"link,omitempty"`
	Chart       Chart    `json:"chart"`
	Requires    Requires `json:"requires"`
	// v2 only: v1 names and deprecates whole models, and renames nothing.
	ServedName string `json:"servedName,omitempty"`
	Deprecated any    `json:"deprecated,omitempty"`
	Replaces   string `json:"replaces,omitempty"`
}

// Tuning is a measured result: a tuned variant against a baseline, each at the
// version it was measured at. Display only, and not checked against the
// variants: the UI skips a pair it cannot find.
type Tuning struct {
	Baseline  VariantRef      `json:"baseline"`
	Optimized VariantRef      `json:"optimized"`
	Uplift    *float64        `json:"uplift,omitempty"`
	Workloads []IndexWorkload `json:"workloads,omitempty"`
	Report    string          `json:"report,omitempty"`
}

// VariantRef names a variant at a version. Version is empty when the catalog
// recorded none.
type VariantRef struct {
	ID      string `json:"id"`
	Version string `json:"version,omitempty"`
}

// Revision is one published version of a variant.
type Revision struct {
	Version string `json:"version"`
	// Digest is the sha256 of the file a deploy pins: on v1 the entry, on v2
	// the variant file at this revision.
	Digest string `json:"digest"`
	// ReleaseDate, Commit and Subject are v2's, from git: the commit's date in
	// UTC, the commit the file is fetched at, and its subject line.
	ReleaseDate string `json:"releaseDate,omitempty"`
	Commit      string `json:"commit,omitempty"`
	Subject     string `json:"subject,omitempty"`
	// Current is the revision the index lists now.
	Current bool `json:"current,omitempty"`
}

// NotFoundError is a model or variant the catalog does not list: the request's
// mistake, where any other error is the catalog's.
type NotFoundError struct{ msg string }

func (e *NotFoundError) Error() string { return e.msg }

func notFound(format string, args ...any) error {
	return &NotFoundError{fmt.Sprintf(format, args...)}
}

// Resolved is one variant of one model at one version: what compose needs, the
// same for every schema.
type Resolved struct {
	Model      string
	Source     Source
	ServedName string
	// Version and Digest are the pin a plan records: on v1 the entry file's,
	// which every variant of that version shares; on v2 the variant file's.
	Version string
	Digest  string
	Variant Variant
}

// Open fetches and validates the index at loc with schemaVersion unset: a base
// location reads index.json as before, and a location naming its .json is read
// by that document's apiVersion.
func Open(ctx context.Context, loc string) (Catalog, error) {
	return OpenWithSchema(ctx, loc, "")
}

// OpenWithSchema opens a catalog at loc for a site profile's schemaVersion.
func OpenWithSchema(ctx context.Context, loc, schema string) (Catalog, error) {
	f, err := NewFetcherWithSchema(loc, schema)
	if err != nil {
		return nil, err
	}
	return OpenFetcher(ctx, f)
}

// OpenFetcher is Open against an already-built Fetcher, for tests and for
// callers holding a configured HTTP client.
func OpenFetcher(ctx context.Context, f Fetcher) (Catalog, error) {
	raw, err := f.Index(ctx)
	if err != nil {
		return nil, err
	}
	return open(f, raw)
}

// open parses an index by the fetcher's schemaVersion. Unset follows the
// document's own apiVersion, which is what lets an explicit .../index-v2.json
// load without one.
func open(f Fetcher, raw []byte) (Catalog, error) {
	var head struct {
		APIVersion string `json:"apiVersion"`
	}
	_ = json.Unmarshal(raw, &head) // a document that does not decode gets the v1 parser's error
	v2 := head.APIVersion == APIVersionV2
	switch schema := f.Schema(); {
	case schema == SchemaV2 || (schema == "" && v2):
		idx, err := parseIndexV2(raw)
		if err != nil {
			return nil, err
		}
		return &V2{
			Index: idx, fetcher: f, ref: contentRef(raw), raw: raw,
			client:    &http.Client{Timeout: 30 * time.Second},
			files:     map[string]VariantV2{},
			histories: map[string]HistoryV2{},
		}, nil
	case v2:
		return nil, fmt.Errorf("index is %s but the catalog is set to schemaVersion %s -- set schemaVersion: v2", APIVersionV2, schema)
	}
	idx, err := parseIndex(raw)
	if err != nil {
		return nil, err
	}
	return &V1{Index: idx, fetcher: f, ref: contentRef(raw), raw: raw, entries: map[string]Entry{}}, nil
}
