package server

import (
	"context"
	"net/http"
	"time"

	"gopkg.in/yaml.v3"
)

// planSummary is the handful of fields a reconciliation row needs out of a
// stored plan. Decoded loosely on purpose: the plan beside a release was written
// by whichever swissd deployed it, possibly an older one, and a field this
// version does not recognise is not a reason to report the release as unknown.
type planSummary struct {
	Model   string
	Version string
	Digest  string
	Variant string
	Ref     string
	Profile string
	Hash    string
}

type statusSummary struct {
	Phase    string `yaml:"phase"`
	Revision int    `yaml:"revision"`
	Error    string `yaml:"error"`
}

func parseStatus(raw []byte) statusSummary {
	var st statusSummary
	_ = yaml.Unmarshal(raw, &st)
	return st
}

func parsePlanSummary(raw []byte) (planSummary, error) {
	var doc struct {
		Source struct {
			Model   string `yaml:"model" json:"model"`
			Version string `yaml:"version" json:"version"`
			Digest  string `yaml:"digest" json:"digest"`
			Variant string `yaml:"variant" json:"variant"`
			Ref     string `yaml:"ref" json:"ref"`
		} `yaml:"source" json:"source"`
		Profile string `yaml:"profile" json:"profile"`
		Hash    string `yaml:"hash" json:"hash"`
	}
	// YAML is a superset of JSON, so this reads a plan stored either way --
	// which matters because the CLI writes JSON today and the ConfigMap is YAML.
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return planSummary{}, err
	}
	return planSummary{
		Model: doc.Source.Model, Version: doc.Source.Version, Digest: doc.Source.Digest,
		Variant: doc.Source.Variant, Ref: doc.Source.Ref,
		Profile: doc.Profile, Hash: doc.Hash,
	}, nil
}

// contextWithTimeout bounds one request. Handlers set their own budget rather
// than the server setting one WriteTimeout for all of them: listing the catalog
// and diffing against a live cluster are not the same kind of slow.
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
