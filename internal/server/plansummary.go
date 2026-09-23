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
	// Chart is "name-version", the way helm names a chart everywhere else here.
	Chart string
}

// parseStatus reads the status key written beside a release. It decodes into
// the same type the apply path writes, so the reconciliation row and the detail
// view cannot disagree about what a phase means.
func parseStatus(raw []byte) planStatus {
	var st planStatus
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
		Chart struct {
			Name    string `yaml:"name" json:"name"`
			Version string `yaml:"version" json:"version"`
		} `yaml:"chart" json:"chart"`
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
		Chart: chartRef(doc.Chart.Name, doc.Chart.Version),
	}, nil
}

// chartRef names a chart the way helm does.
func chartRef(name, version string) string {
	return name + "-" + version
}

// contextWithTimeout bounds one request. Handlers set their own budget rather
// than the server setting one WriteTimeout for all of them: listing the catalog
// and diffing against a live cluster are not the same kind of slow.
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
