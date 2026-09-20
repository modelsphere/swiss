package main

import (
	"fmt"
	"os"

	"github.com/aceforeverd/swiss/internal/cluster"
	"github.com/aceforeverd/swiss/internal/exec"
	"github.com/aceforeverd/swiss/internal/plan"
	"github.com/spf13/cobra"
)

const (
	Upgrade = exec.Upgrade
	Install = exec.Install
)

type deployFlags struct {
	model, variant, release, namespace, planFile, chartRoot string
	serviceID, localPath                                    string
	sets                                                    []string
	keepWorkspace                                           bool
	yes                                                     bool
	revision                                                int
}

func (d *deployFlags) bind(c *cobra.Command) {
	f := c.Flags()
	f.StringVar(&d.model, "model", "", "catalog model name")
	f.StringVar(&d.variant, "variant", "", "variant id")
	f.StringVar(&d.release, "release", "", "helm release name")
	f.StringVar(&d.namespace, "namespace", "", "namespace")
	f.StringVar(&d.serviceID, "service-id", "", "serviceId for this release")
	f.StringVar(&d.localPath, "local-path", "", "model.localPath, overriding the site's path template")
	f.StringArrayVar(&d.sets, "set", nil, "deploy-time override, key=value")
	f.StringVarP(&d.planFile, "plan", "p", "", "use an existing plan file")
	f.StringVar(&d.chartRoot, "chart-root", os.Getenv("SWISS_CHART_ROOT"), "chart directory, when the profile names no repo")
	f.BoolVar(&d.keepWorkspace, "keep-workspace", false, "leave the generated helmfile on disk")
}

func (d *deployFlags) plan(cmd *cobra.Command) (*plan.Plan, error) {
	if d.planFile != "" {
		return readPlan(d.planFile)
	}
	if d.model == "" {
		return nil, fmt.Errorf("pass --model, or --plan with an existing plan")
	}
	return buildPlan(cmd.Context(), d.model, d.variant, d.release, d.namespace,
		append(d.sets, kv("serviceId", d.serviceID), kv("model.localPath", d.localPath)))
}

func (d *deployFlags) runner() exec.Runner {
	return exec.Runner{ChartRoot: d.chartRoot, KeepWorkspace: d.keepWorkspace}
}

func probe() (cluster.Probe, error) {
	r, err := resolveConfig()
	if err != nil {
		return nil, err
	}
	kubeconfig, context := "", ""
	var namespaces []string
	if r.cfg != nil {
		kubeconfig, context = r.cfg.Cluster.Kubeconfig, r.cfg.Cluster.Context
		namespaces = r.cfg.Cluster.Namespaces
	}
	return cluster.NewKube(kubeconfig, context, namespaces...)
}

func diffCmd() *cobra.Command {
	var d deployFlags
	c := &cobra.Command{
		Use:   "diff",
		Short: "Show what applying a plan would change (exit 2 when it would)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := d.plan(cmd)
			if err != nil {
				return err
			}
			res, err := d.runner().Diff(cmd.Context(), p)
			if err != nil {
				return err
			}
			fmt.Print(res.Output)
			if res.Changed {
				return exitErr{code: 2}
			}
			fmt.Fprintln(os.Stderr, "no changes")
			return nil
		},
	}
	d.bind(c)
	return c
}

func applyCmd(mode exec.Mode) *cobra.Command {
	var d deployFlags
	use, short := "apply", "Upgrade an existing release"
	if mode == Install {
		use, short = "install", "Create a release that does not exist yet"
	}
	c := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := d.plan(cmd)
			if err != nil {
				return err
			}
			pr, err := probe()
			if err != nil {
				return err
			}
			st, err := exec.Lookup(cmd.Context(), pr, p.Release.Namespace, p.Release.Name)
			if err != nil {
				return err
			}
			if err := exec.Check(st, mode, p); err != nil {
				return err
			}
			if err := exec.CheckRevision(st, d.revision); err != nil {
				return err
			}

			res, err := d.runner().Apply(cmd.Context(), p)
			fmt.Print(res.Output)
			if err != nil {
				return err
			}
			// Deliberately not waiting: a cold load is 20-40 minutes and helm
			// would either block or report a timeout for a release that is fine.
			fmt.Fprintf(os.Stderr, "\n%s/%s submitted (%s)\nwatch it: kubectl -n %s get pods -l app=%s-%s -w\n",
				p.Release.Namespace, p.Release.Name, p.Hash[:19],
				p.Release.Namespace, p.Release.Name, p.Engine)
			return nil
		},
	}
	d.bind(c)
	c.Flags().IntVar(&d.revision, "expect-revision", 0, "refuse if the live release is not at this revision")
	return c
}
