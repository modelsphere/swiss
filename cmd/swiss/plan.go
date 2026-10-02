package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/modelsphere/swiss/internal/chart"
	"github.com/modelsphere/swiss/internal/compose"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/render"
	"github.com/modelsphere/swiss/internal/site"
	"github.com/modelsphere/swiss/internal/values"
	"github.com/spf13/cobra"
)

func planCmd() *cobra.Command {
	var (
		model, variant, release, namespace, out string
		modelVersion, serviceID, localPath      string
		chartVersion                            string
		sets                                    []string
		explain, createNamespace                bool
	)
	c := &cobra.Command{
		Use:   "plan",
		Short: "Compose a model, a site profile and overrides into a plan",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := buildPlan(cmd.Context(), planInput{
				model: model, modelVersion: modelVersion, variant: variant,
				release: release, namespace: namespace, chartVersion: chartVersion,
				sets:            append(sets, kv("serviceId", serviceID), kv("model.localPath", localPath)),
				createNamespace: createNamespace,
			})
			if err != nil {
				return err
			}
			if explain {
				printExplain(p)
				return nil
			}
			b, err := json.MarshalIndent(p, "", "  ")
			if err != nil {
				return err
			}
			b = append(b, '\n')
			if out == "" || out == "-" {
				_, err = os.Stdout.Write(b)
				return err
			}
			return os.WriteFile(out, b, 0o644)
		},
	}
	c.Flags().StringVar(&model, "model", "", "catalog model name (required)")
	c.Flags().StringVar(&modelVersion, "model-version", "", "catalog model version; defaults to the latest published")
	c.Flags().StringVar(&variant, "variant", "", "variant id; defaults to the entry's default variant")
	c.Flags().StringVar(&chartVersion, "chart-version", "", chartVersionUsage)
	c.Flags().StringVar(&release, "release", "", "helm release name; defaults to the model name")
	c.Flags().StringVar(&namespace, "namespace", "", "namespace; defaults to the site profile's")
	c.Flags().StringVar(&serviceID, "service-id", "", "serviceId: the identity modelRoute, sloRequirement and the scaler key off")
	c.Flags().StringVar(&localPath, "local-path", "", "model.localPath, overriding the site's path template")
	c.Flags().StringArrayVar(&sets, "set", nil, "deploy-time override, key=value (repeatable)")
	c.Flags().StringVarP(&out, "output", "o", "-", "write the plan here")
	c.Flags().BoolVar(&explain, "explain", false, "print which layer set each value instead of the plan")
	c.Flags().BoolVar(&createNamespace, "create-namespace", false, "compose helmfile's createNamespace into the plan, so helm creates the namespace on install")
	_ = c.MarkFlagRequired("model")
	return c
}

func renderCmd() *cobra.Command {
	var (
		model, variant, release, namespace, chartRoot, planFile string
		modelVersion, chartVersion                              string
		sets                                                    []string
	)
	c := &cobra.Command{
		Use:   "render",
		Short: "helm template a plan (schema validation runs here)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var p *plan.Plan
			var err error
			if planFile != "" {
				p, err = readPlan(planFile)
			} else {
				p, err = buildPlan(cmd.Context(), planInput{
					model: model, modelVersion: modelVersion, variant: variant,
					release: release, namespace: namespace, chartVersion: chartVersion, sets: sets,
				})
			}
			if err != nil {
				return err
			}
			manifests, err := render.Exec{ChartRoot: chartRoot}.Template(cmd.Context(), p)
			if err != nil {
				return err
			}
			fmt.Print(manifests)
			return nil
		},
	}
	c.Flags().StringVar(&model, "model", "", "catalog model name")
	c.Flags().StringVar(&modelVersion, "model-version", "", "catalog model version")
	c.Flags().StringVar(&variant, "variant", "", "variant id")
	c.Flags().StringVar(&chartVersion, "chart-version", "", chartVersionUsage)
	c.Flags().StringVar(&release, "release", "", "helm release name")
	c.Flags().StringVar(&namespace, "namespace", "", "namespace")
	c.Flags().StringArrayVar(&sets, "set", nil, "deploy-time override, key=value")
	c.Flags().StringVar(&chartRoot, "chart-root", envOr("SWISS_CHART_ROOT", "../charts"), "directory holding the charts, when the profile names no repo")
	c.Flags().StringVarP(&planFile, "plan", "p", "", "render an existing plan file instead of composing one")
	return c
}

const chartVersionUsage = "chart version, when the variant's chart.version is a range; defaults to the newest in range"

// planInput is what composing a plan takes from a command line, named rather
// than positional: half of it is optional strings that read the same way.
type planInput struct {
	model, modelVersion, variant, release, namespace string
	chartVersion                                     string
	sets                                             []string
	createNamespace                                  bool
}

func buildPlan(ctx context.Context, in planInput) (*plan.Plan, error) {
	catalogLoc, profileFile, err := resolve()
	if err != nil {
		return nil, err
	}
	if profileFile == "" {
		return nil, fmt.Errorf("no site profile: set cluster.profile.file in the config, or pass --profile")
	}
	prof, err := site.Load(profileFile)
	if err != nil {
		return nil, err
	}
	cat, err := catalog.Open(ctx, catalogLoc)
	if err != nil {
		return nil, err
	}
	entry, err := cat.Entry(ctx, in.model, in.modelVersion)
	if err != nil {
		return nil, err
	}

	var v catalog.Variant
	if in.variant == "" {
		v, err = entry.DefaultVariant()
	} else {
		v, err = entry.Variant(in.variant)
	}
	if err != nil {
		return nil, err
	}

	spec, err := chart.ParseSpec(v.Chart.Version)
	if err != nil {
		return nil, err
	}
	chartVersion, err := chart.Resolve(ctx, spec, in.chartVersion, "", func(ctx context.Context) ([]string, error) {
		return chart.Versions(ctx, prof.ChartRepo, prof.ChartPath, v.Chart.Name)
	})
	if err != nil {
		return nil, err
	}

	overrides := values.Tree{}
	for _, s := range in.sets {
		if s == "" {
			continue
		}
		t, err := values.ParseSet(s)
		if err != nil {
			return nil, err
		}
		values.Merge(overrides, t, values.LayerForm, nil)
	}

	release := in.release
	if release == "" {
		release = entry.Name
	}
	return compose.Compose(compose.Input{
		Catalog:         cat.Fetcher.String(),
		Ref:             cat.Ref,
		Entry:           entry,
		Variant:         v,
		ChartVersion:    chartVersion,
		Profile:         *prof,
		Release:         release,
		Namespace:       in.namespace,
		Overrides:       overrides,
		CreateNamespace: in.createNamespace,
	})
}

// kv renders a named flag as a --set assignment, or "" when unset.
func kv(key, value string) string {
	if value == "" {
		return ""
	}
	return key + "=" + value
}

func readPlan(path string) (*plan.Plan, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p plan.Plan
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if p.APIVersion != plan.APIVersion {
		return nil, fmt.Errorf("%s: apiVersion %q, want %q", path, p.APIVersion, plan.APIVersion)
	}
	// A plan carries a hash of what it claims to render. Recomputing it here is
	// what makes a hand-edited plan an error rather than a surprise in a cluster.
	want := p.Hash
	if err := p.ComputeHash(); err != nil {
		return nil, err
	}
	if want != "" && want != p.Hash {
		return nil, fmt.Errorf("%s: hash mismatch -- the plan was edited after it was composed", path)
	}
	return &p, nil
}

// printExplain shows the composed document grouped by the layer that set each
// value. This is the answer to "why is mem-fraction-static 0.85 here", and the
// reason derived values are not magic.
func printExplain(p *plan.Plan) {
	fmt.Printf("%s/%s -> release %s in %s\n", p.Source.Model, p.Source.Variant, p.Release.Name, p.Release.Namespace)
	fmt.Printf("chart %s-%s   engine %s   profile %s\n%s\n\n", p.Chart.Name, p.Chart.Version, p.Engine, p.Profile, p.Hash)

	byLayer := p.ByLayer()
	for _, layer := range []string{values.LayerCatalog, values.LayerSite, values.LayerDerived, values.LayerForm, values.LayerEdit} {
		paths := byLayer[layer]
		if len(paths) == 0 {
			continue
		}
		fmt.Printf("%s (%d)\n", layer, len(paths))
		sort.Strings(paths)
		for _, path := range paths {
			v, _ := values.Get(p.Values(), path)
			fmt.Printf("  %-40s %v\n", path, v)
		}
		fmt.Println()
	}
	printShadowed(p)
}

// printShadowed names the values a later layer took over. Never an error -- the
// merge order is the rule -- but the layer that lost is usually the one the
// reader thought they were configuring, so it is the last thing printed rather
// than something to go looking for.
func printShadowed(p *plan.Plan) {
	sh := p.Shadowed()
	if len(sh) == 0 {
		return
	}
	fmt.Printf("shadowed (%d)\n", len(sh))
	for _, s := range sh {
		fmt.Printf("  %-40s %s -> %s\n", s.Path, strings.Join(s.Under, " -> "), s.By)
	}
	fmt.Println()
}
