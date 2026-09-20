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
	"github.com/aceforeverd/swiss/internal/values"
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
	doc, err := p.RenderHelmfile(r.ChartRoot)
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

	// One document per layer, in merge order, so the workspace shows the
	// layering rather than a flattened result helm would have produced anyway.
	layers := p.LayerValues()
	if len(layers) == 0 {
		layers = map[string]values.Tree{"values": p.Values}
	}
	for name, tree := range layers {
		vals, err := yaml.Marshal(tree)
		if err != nil {
			ws.Close()
			return Workspace{}, err
		}
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), vals, 0o600); err != nil {
			ws.Close()
			return Workspace{}, err
		}
	}

	if err := os.WriteFile(ws.Helmfile, []byte(doc), 0o600); err != nil {
		ws.Close()
		return Workspace{}, err
	}
	return ws, nil
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
