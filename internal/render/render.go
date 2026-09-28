// Package render turns a plan into Kubernetes manifests.
//
// The interface exists so that the exec implementation below can be replaced by
// the helm SDK in process without anything upstream noticing. Exec first because
// it is honest about what it does and needs no dependency tree; helm.sh/helm/v4
// is the eventual home, and v4 matters -- docs/deploy.md warns that the helm
// binary in this environment is v4 while much of the helmfile ecosystem still
// assumes v3.
//
// Whichever implementation is in play, values.schema.json runs as part of
// `helm template`. That is not a bonus: it is the check that a misspelled key is
// an error rather than a setting that quietly does nothing.
package render

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/modelsphere/swiss/internal/plan"
	"gopkg.in/yaml.v3"
)

type Renderer interface {
	Template(ctx context.Context, p *plan.Plan) (string, error)
}

// Exec shells out to a helm binary.
type Exec struct {
	Bin string // defaults to "helm"
	// ChartRoot resolves a chart name to a local path when the plan carries no
	// repo. Local paths are fine for the CLI and not for a server, which has no
	// checkout to point at.
	ChartRoot string
}

func (e Exec) bin() string {
	if e.Bin != "" {
		return e.Bin
	}
	return "helm"
}

func (e Exec) Template(ctx context.Context, p *plan.Plan) (string, error) {
	chart, err := e.chartRef(p)
	if err != nil {
		return "", err
	}

	vals, err := yaml.Marshal(p.Values())
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "swiss-values-*.yaml")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(vals); err != nil {
		f.Close()
		return "", err
	}
	f.Close()

	args := []string{
		"template", p.Release.Name, chart,
		"--namespace", p.Release.Namespace,
		"--values", f.Name(),
	}
	if p.Chart.Repo != "" {
		args = append(args, "--version", p.Chart.Version)
	}

	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, e.bin(), args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s template: %w\n%s", e.bin(), err, errb.String())
	}
	return out.String(), nil
}

func (e Exec) chartRef(p *plan.Plan) (string, error) {
	switch {
	case p.Chart.Path != "":
		return filepath.Join(p.Chart.Path, p.Chart.Name), nil
	case p.Chart.Repo != "":
		return p.Chart.Repo + "/" + p.Chart.Name, nil
	case e.ChartRoot != "":
		return filepath.Join(e.ChartRoot, p.Chart.Name), nil
	}
	return "", fmt.Errorf("no chart source: set chartRepo or chartPath in the site profile, or pass --chart-root")
}
