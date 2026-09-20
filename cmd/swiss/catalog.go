package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/aceforeverd/swiss/internal/catalog"
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
			fmt.Fprintln(w, "MODEL\tVARIANT\tENGINE\tGPUS\tTOPOLOGY\tCHART")
			for _, e := range cat.Index.Models {
				for _, v := range e.Variants {
					gpus := fmt.Sprintf("%d", v.Requires.GPUs)
					if n := v.Requires.NodesOrDefault(); n > 1 {
						gpus = fmt.Sprintf("%dx%d", n, v.Requires.GPUs)
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s-%s\n",
						e.Name, v.ID, v.Engine, gpus, v.Requires.TopologyOrDefault(), v.Chart.Name, v.Chart.Version)
				}
			}
			if err := w.Flush(); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "\ncatalog %s\n%s\n", cat.Fetcher, cat.Ref)
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
			e, err := cat.Entry(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Printf("%s", describe(e))
			return nil
		},
	})
	return c
}

func describe(e catalog.Entry) string {
	var b strings.Builder
	name := e.DisplayName
	if name == "" {
		name = e.Name
	}
	fmt.Fprintf(&b, "%s (%s)\n", name, e.Name)
	if e.Description != "" {
		fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(e.Description))
	}
	fmt.Fprintf(&b, "\nweights   %s", e.Source.HF)
	if e.Source.SizeGiB > 0 {
		fmt.Fprintf(&b, "  (%.0f GiB)", e.Source.SizeGiB)
	}
	fmt.Fprintf(&b, "\nserved as %s\n\nvariants:\n", e.ServedModelName())
	for _, v := range e.Variants {
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
		b.WriteString("\n")
		if v.Description != "" {
			fmt.Fprintf(&b, "      %s\n", v.Description)
		}
	}
	return b.String()
}
