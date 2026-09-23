package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/aceforeverd/swiss/internal/catalog"
	"github.com/aceforeverd/swiss/internal/compose"
	"github.com/aceforeverd/swiss/internal/plan"
	"github.com/aceforeverd/swiss/internal/render"
	"github.com/aceforeverd/swiss/internal/site"
	"github.com/aceforeverd/swiss/internal/values"
	"github.com/spf13/cobra"
)

func planCmd() *cobra.Command {
	var (
		model, variant, release, namespace, out string
		modelVersion, serviceID, localPath      string
		sets                                    []string
		explain                                 bool
	)
	c := &cobra.Command{
		Use:   "plan",
		Short: "Compose a model, a site profile and overrides into a plan",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := buildPlan(cmd.Context(), model, modelVersion, variant, release, namespace,
				append(sets, kv("serviceId", serviceID), kv("model.localPath", localPath)))
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
	c.Flags().StringVar(&release, "release", "", "helm release name; defaults to the model name")
	c.Flags().StringVar(&namespace, "namespace", "", "namespace; defaults to the site profile's")
	c.Flags().StringVar(&serviceID, "service-id", "", "serviceId: the identity modelRoute, sloRequirement and the scaler key off")
	c.Flags().StringVar(&localPath, "local-path", "", "model.localPath, overriding the site's path template")
	c.Flags().StringArrayVar(&sets, "set", nil, "deploy-time override, key=value (repeatable)")
	c.Flags().StringVarP(&out, "output", "o", "-", "write the plan here")
	c.Flags().BoolVar(&explain, "explain", false, "print which layer set each value instead of the plan")
	_ = c.MarkFlagRequired("model")
	return c
}

func renderCmd() *cobra.Command {
	var (
		model, variant, release, namespace, chartRoot, planFile string
		modelVersion                                            string
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
				p, err = buildPlan(cmd.Context(), model, modelVersion, variant, release, namespace, sets)
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
	c.Flags().StringVar(&release, "release", "", "helm release name")
	c.Flags().StringVar(&namespace, "namespace", "", "namespace")
	c.Flags().StringArrayVar(&sets, "set", nil, "deploy-time override, key=value")
	c.Flags().StringVar(&chartRoot, "chart-root", envOr("SWISS_CHART_ROOT", "../charts"), "directory holding the charts, when the profile names no repo")
	c.Flags().StringVarP(&planFile, "plan", "p", "", "render an existing plan file instead of composing one")
	return c
}

func buildPlan(ctx context.Context, model, modelVersion, variant, release, namespace string, sets []string) (*plan.Plan, error) {
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
	entry, err := cat.Entry(ctx, model, modelVersion)
	if err != nil {
		return nil, err
	}

	var v catalog.Variant
	if variant == "" {
		v, err = entry.DefaultVariant()
	} else {
		v, err = entry.Variant(variant)
	}
	if err != nil {
		return nil, err
	}

	overrides := values.Tree{}
	for _, s := range sets {
		if s == "" {
			continue
		}
		t, err := values.ParseSet(s)
		if err != nil {
			return nil, err
		}
		values.Merge(overrides, t, values.LayerForm, nil)
	}

	if release == "" {
		release = entry.Name
	}
	return compose.Compose(compose.Input{
		Catalog:   cat.Fetcher.String(),
		Ref:       cat.Ref,
		Entry:     entry,
		Variant:   v,
		Profile:   *prof,
		Release:   release,
		Namespace: namespace,
		Overrides: overrides,
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
	for _, layer := range []string{values.LayerCatalog, values.LayerSite, values.LayerDerived, values.LayerForm} {
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
}
