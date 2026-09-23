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
	"strconv"
	"strings"

	"github.com/aceforeverd/swiss/internal/plan"
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
	// The same directory the plan ConfigMap holds, so a mounted ConfigMap and a
	// temp workspace are the same tree rather than two renderings of one plan.
	files, err := p.Files(r.ChartRoot)
	if err != nil {
		return Workspace{}, err
	}
	if _, ok := files[plan.HelmfileFile]; !ok {
		return Workspace{}, fmt.Errorf("no chart source: set chartRepo or chartPath in the site profile, or pass --chart-root")
	}

	dir, err := os.MkdirTemp("", "swiss-"+p.Release.Name+"-")
	if err != nil {
		return Workspace{}, err
	}
	ws := Workspace{Dir: dir, Helmfile: filepath.Join(dir, plan.HelmfileFile)}
	ws.cleanup = func() {
		if !r.KeepWorkspace {
			os.RemoveAll(dir)
		}
	}

	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			ws.Close()
			return Workspace{}, err
		}
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

// Apply leaves helmfile's own diff in the output rather than suppressing it.
// An apply already computes one to decide what to send; printing it makes the
// audit entry say what changed, which is the only record of it once the
// workspace is gone.
func (r Runner) Apply(ctx context.Context, p *plan.Plan) (Result, error) {
	ws, err := r.Materialize(p)
	if err != nil {
		return Result{}, err
	}
	defer ws.Close()

	out, _, err := r.run(ctx, ws, "apply")
	return Result{Output: out, Changed: err == nil}, err
}

// Uninstall removes a release with helm directly, rather than through a
// materialised workspace like every other verb here.
//
// An uninstall needs no values: it names a release and a namespace and nothing
// else. Routing it through a plan would mean a release whose plan is missing or
// unreadable -- the untracked row, the one most likely to need cleaning up --
// could not be removed at all.
func (r Runner) Uninstall(ctx context.Context, namespace, release string) (Result, error) {
	out, err := r.runHelm(ctx, "uninstall", release, "--namespace", namespace)
	return Result{Output: out, Changed: err == nil}, err
}

// Values reads back what helm itself holds for a revision, chart defaults
// included, as the yaml it would print.
//
// Direct through helm rather than from the archived plan beside the release:
// the plan is what swiss composed, and this is what the cluster was actually
// given. The two disagreeing is the thing worth being able to see.
func (r Runner) Values(ctx context.Context, namespace, release string, revision int) (string, error) {
	args := []string{"get", "values", release, "--namespace", namespace, "--all", "--output", "yaml"}
	if revision > 0 {
		args = append(args, "--revision", strconv.Itoa(revision))
	}
	return r.runHelm(ctx, args...)
}

func (r Runner) runHelm(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.bin("helm"), args...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		return buf.String(), fmt.Errorf("helm %s: %w\n%s", strings.Join(args, " "), err, buf.String())
	}
	return buf.String(), nil
}

func (r Runner) run(ctx context.Context, ws Workspace, args ...string) (string, int, error) {
	full := append([]string{"--file", ws.Helmfile, "--helm-binary", r.bin("helm")}, args...)
	cmd := exec.CommandContext(ctx, r.bin("helmfile"), full...)
	cmd.Dir = ws.Dir

	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()

	code := 0
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
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
