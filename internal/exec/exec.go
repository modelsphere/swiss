// Package exec runs helmfile against a workspace materialised from a plan.
package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/aceforeverd/swiss/internal/plan"
	"gopkg.in/yaml.v3"
)

type Runner struct {
	HelmfileBin string
	HelmBin     string
	// ChartRoot resolves a chart name to a local directory when the plan names
	// no repo.
	ChartRoot string
	// KeepWorkspace leaves the materialised directory on disk for debugging.
	KeepWorkspace bool
}

type Result struct {
	Changed bool
	Output  string
}

// Defaults mirror the repo's helmfile.yaml. They are not tunable knobs: a cold
// load here is 20-40 minutes, "failed" usually means "still loading", and an
// automatic rollback of that is the expensive wrong answer.
type helmDefaults struct {
	Wait          bool     `yaml:"wait"`
	Atomic        bool     `yaml:"atomic"`
	CleanupOnFail bool     `yaml:"cleanupOnFail"`
	CreateNS      bool     `yaml:"createNamespace"`
	HistoryMax    int      `yaml:"historyMax"`
	DiffArgs      []string `yaml:"diffArgs"`
}

func defaults() helmDefaults {
	return helmDefaults{
		Wait: false, Atomic: false, CleanupOnFail: false,
		CreateNS: true, HistoryMax: 20,
		DiffArgs: []string{"--three-way-merge"},
	}
}

type release struct {
	Name      string   `yaml:"name"`
	Namespace string   `yaml:"namespace"`
	Chart     string   `yaml:"chart"`
	Version   string   `yaml:"version,omitempty"`
	Values    []string `yaml:"values"`
}

type document struct {
	HelmDefaults helmDefaults `yaml:"helmDefaults"`
	Releases     []release    `yaml:"releases"`
}

func (r Runner) bin(which string) string {
	switch which {
	case "helm":
		if r.HelmBin != "" {
			return r.HelmBin
		}
		return "helm"
	default:
		if r.HelmfileBin != "" {
			return r.HelmfileBin
		}
		return "helmfile"
	}
}

// Workspace is a materialised plan: a values file and a one-release helmfile.
type Workspace struct {
	Dir      string
	Helmfile string
	cleanup  func()
}

func (w Workspace) Close() {
	if w.cleanup != nil {
		w.cleanup()
	}
}

// Materialize writes the plan to a temp directory. helmfile needs files, not a
// repository, so the server can produce them per operation and discard them.
func (r Runner) Materialize(p *plan.Plan) (Workspace, error) {
	chart, version, err := r.chartRef(p)
	if err != nil {
		return Workspace{}, err
	}

	dir, err := os.MkdirTemp("", "swiss-"+p.Release.Name+"-")
	if err != nil {
		return Workspace{}, err
	}
	ws := Workspace{Dir: dir, Helmfile: filepath.Join(dir, "helmfile.yaml")}
	ws.cleanup = func() {
		if !r.KeepWorkspace {
			os.RemoveAll(dir)
		}
	}

	vals, err := yaml.Marshal(p.Values)
	if err != nil {
		ws.Close()
		return Workspace{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "values.yaml"), vals, 0o600); err != nil {
		ws.Close()
		return Workspace{}, err
	}

	doc, err := yaml.Marshal(document{
		HelmDefaults: defaults(),
		Releases: []release{{
			Name:      p.Release.Name,
			Namespace: p.Release.Namespace,
			Chart:     chart,
			Version:   version,
			Values:    []string{"values.yaml"},
		}},
	})
	if err != nil {
		ws.Close()
		return Workspace{}, err
	}
	if err := os.WriteFile(ws.Helmfile, doc, 0o600); err != nil {
		ws.Close()
		return Workspace{}, err
	}
	return ws, nil
}

func (r Runner) chartRef(p *plan.Plan) (chart, version string, err error) {
	switch {
	case p.Chart.Repo != "":
		return strings.TrimSuffix(p.Chart.Repo, "/") + "/" + p.Chart.Name, p.Chart.Version, nil
	case p.Chart.Path != "":
		abs, err := filepath.Abs(filepath.Join(p.Chart.Path, p.Chart.Name))
		return abs, "", err
	case r.ChartRoot != "":
		abs, err := filepath.Abs(filepath.Join(r.ChartRoot, p.Chart.Name))
		return abs, "", err
	}
	return "", "", fmt.Errorf("no chart source: set chartRepo or chartPath in the site profile, or pass --chart-root")
}

// Diff reports what applying the plan would change. Changed is true when
// helm-diff reports differences.
func (r Runner) Diff(ctx context.Context, p *plan.Plan) (Result, error) {
	ws, err := r.Materialize(p)
	if err != nil {
		return Result{}, err
	}
	defer ws.Close()

	out, code, err := r.run(ctx, ws, "diff", "--detailed-exitcode")
	switch {
	case code == 2:
		return Result{Changed: true, Output: out}, nil
	case err != nil:
		return Result{Output: out}, err
	}
	return Result{Output: out}, nil
}

// Mode is the precondition an apply is allowed under.
type Mode int

const (
	// Upgrade requires the release to exist; Install requires that it does not.
	// helm's `upgrade --install` is forgiving, and that forgiveness is how a
	// mistyped namespace installs a second engine onto the same GPUs.
	Upgrade Mode = iota
	Install
)

func (r Runner) Apply(ctx context.Context, p *plan.Plan) (Result, error) {
	ws, err := r.Materialize(p)
	if err != nil {
		return Result{}, err
	}
	defer ws.Close()

	out, _, err := r.run(ctx, ws, "apply", "--suppress-diff")
	return Result{Output: out, Changed: err == nil}, err
}

func (r Runner) run(ctx context.Context, ws Workspace, args ...string) (string, int, error) {
	full := append([]string{"--file", ws.Helmfile, "--helm-binary", r.bin("helm")}, args...)
	cmd := exec.CommandContext(ctx, r.bin("helmfile"), full...)
	cmd.Dir = ws.Dir

	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()

	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
		if code == 2 {
			return buf.String(), code, nil
		}
	}
	if err != nil {
		return buf.String(), code, fmt.Errorf("helmfile %s: %w\n%s", strings.Join(args, " "), err, buf.String())
	}
	return buf.String(), code, nil
}
