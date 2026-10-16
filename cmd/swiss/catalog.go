package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/spf13/cobra"
)

func catalogCmd() *cobra.Command {
	c := &cobra.Command{Use: "catalog", Short: "Inspect the model catalog"}

	c.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List models and their variants",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			loc, _, err := resolve()
			if err != nil {
				return err
			}
			cat, err := catalog.Open(cmd.Context(), loc)
			if err != nil {
				return err
			}
			// The index alone, deliberately: listing must not cost one fetch
			// per model.
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "MODEL\tVERSION\tVARIANT\tENGINE\tGPUS\tTOPOLOGY\tCHART")
			for _, m := range cat.Models() {
				for _, v := range m.Variants {
					gpus := fmt.Sprintf("%d", v.Requires.GPUs)
					if n := v.Requires.NodesOrDefault(); n > 1 {
						gpus = fmt.Sprintf("%dx%d", n, v.Requires.GPUs)
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s-%s\n",
						m.Name, v.Version, v.ID, v.Engine, gpus, v.Requires.TopologyOrDefault(), v.Chart.Name, v.Chart.Version)
				}
			}
			if err := w.Flush(); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "\ncatalog %s\n%s\n", cat.Source(), cat.Ref())
			return nil
		},
	})

	c.AddCommand(&cobra.Command{
		Use:   "show <model>",
		Short: "Show one model's entry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			loc, _, err := resolve()
			if err != nil {
				return err
			}
			cat, err := catalog.Open(cmd.Context(), loc)
			if err != nil {
				return err
			}
			m, ok := cat.Model(args[0])
			if !ok {
				return fmt.Errorf("no model %q in catalog %s", args[0], cat.Source())
			}
			// The default variant, fetched and verified: a model shown is one
			// that would deploy.
			r, err := cat.Resolve(cmd.Context(), args[0], "", "")
			if err != nil {
				return err
			}
			fmt.Printf("%s", describe(m, r))
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "history <model> [variant]",
		Short: "List a variant's published versions, newest first",
		Long: "List a variant's published versions, newest first -- the model's default variant\n" +
			"when none is named. Any of them can be planned with --version.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			loc, _, err := resolve()
			if err != nil {
				return err
			}
			cat, err := catalog.Open(cmd.Context(), loc)
			if err != nil {
				return err
			}
			variant := ""
			if len(args) == 2 {
				variant = args[1]
			}
			revs, err := cat.History(cmd.Context(), args[0], variant)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "  VERSION\tDATE\tCOMMIT\tDIGEST\tSUBJECT")
			for _, r := range revs {
				mark := " "
				if r.Current {
					mark = "*"
				}
				fmt.Fprintf(w, "%s %s\t%s\t%s\t%s\t%s\n", mark, r.Version, r.ReleaseDate, prefix(r.Commit, 12), prefix(r.Digest, 19), r.Subject)
			}
			return w.Flush()
		},
	})
	return c
}

func describe(m catalog.Model, def catalog.Resolved) string {
	var b strings.Builder
	name := m.DisplayName
	if name == "" {
		name = m.Name
	}
	fmt.Fprintf(&b, "%s (%s)\n", name, m.Name)
	if m.Description != "" {
		fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(m.Description))
	}
	weights := m.Source.HF
	if weights == "" {
		weights = "none"
	}
	fmt.Fprintf(&b, "\nweights   %s", weights)
	if m.Source.SizeGiB > 0 {
		fmt.Fprintf(&b, "  (%.0f GiB)", m.Source.SizeGiB)
	}
	fmt.Fprintf(&b, "\nversion   %s", def.Version)
	if def.Digest != "" {
		fmt.Fprintf(&b, "  %s", def.Digest[:19])
	}
	fmt.Fprintf(&b, "\nserved as %s\n\nvariants:\n", def.ServedName)
	for _, v := range m.Variants {
		mark := " "
		if v.Default {
			mark = "*"
		}
		fmt.Fprintf(&b, "  %s %s  %s  %d GPU", mark, v.ID, v.Engine, v.Requires.GPUs)
		if n := v.Requires.NodesOrDefault(); n > 1 {
			fmt.Fprintf(&b, " x %d nodes", n)
		}
		if len(v.Requires.GPUProduct) > 0 {
			fmt.Fprintf(&b, "  [%s]", strings.Join(v.Requires.GPUProduct, ", "))
		}
		// A v2 variant is versioned and named on its own; v1's are the entry's.
		if v.Version != def.Version {
			fmt.Fprintf(&b, "  v%s", v.Version)
		}
		if v.ServedName != "" && v.ServedName != def.ServedName {
			fmt.Fprintf(&b, "  served as %s", v.ServedName)
		}
		b.WriteString("\n")
		if v.Description != "" {
			fmt.Fprintf(&b, "      %s\n", v.Description)
		}
	}
	return b.String()
}

func prefix(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
