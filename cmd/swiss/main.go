// Command swiss composes a model from the catalog, a site profile and a set of
// deploy-time overrides into a plan, and renders it.
//
// P0 is deliberately the read half: compose, render, diff. It writes nothing to
// a cluster and nothing to the charts repo. The exit criterion for this phase is
// that the three live releases can be reproduced from catalog plus profile with
// an empty `helmfile diff` -- which is a thing you can only check with a tool
// that cannot yet change anything.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/aceforeverd/swiss/internal/config"
	"github.com/aceforeverd/swiss/internal/version"
	"github.com/spf13/cobra"
)

// exitErr carries a specific exit code: `swiss diff` returns 2 when something
// would change, so it can gate a pipeline.
type exitErr struct {
	code int
	msg  string
}

func (e exitErr) Error() string { return e.msg }

func main() {
	root := &cobra.Command{
		Use:           "swiss",
		Short:         "Compose and deploy models from a swiss catalog",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.Version,
	}
	// One config document, shared with swissd. The flags stay as overrides, so
	// a one-off run against another catalog or profile needs no file -- but the
	// common case is a file both binaries read, and a CLI that reads a
	// different document from the server it talks to will eventually disagree
	// with it.
	root.PersistentFlags().StringVar(&flagConfig, "config", "", "config file (default: $SWISS_CONFIG, ./swiss.yaml, ~/.config/swiss/swiss.yaml)")
	root.PersistentFlags().StringVar(&flagCatalog, "catalog", os.Getenv("SWISS_CATALOG"), "catalog location, overriding the config")
	root.PersistentFlags().StringVar(&flagProfile, "profile", os.Getenv("SWISS_PROFILE"), "site profile file, overriding the config")

	root.AddCommand(catalogCmd(), planCmd(), renderCmd(), diffCmd(), applyCmd(Upgrade), applyCmd(Install),
		notYet("emit", "write the values file and the helmfile entry, then open a PR"))

	if err := root.Execute(); err != nil {
		if ec, ok := errors.AsType[exitErr](err); ok {
			if ec.msg != "" {
				fmt.Fprintln(os.Stderr, "swiss: "+ec.msg)
			}
			os.Exit(ec.code)
		}
		fmt.Fprintln(os.Stderr, "swiss: "+err.Error())
		os.Exit(1)
	}
}

var (
	flagConfig  string
	flagCatalog string
	flagProfile string
)

// resolve merges the config file with the flags that override it. Precedence is
// flag, then environment, then file -- and a missing file is not an error, so
// `swiss --catalog ... --profile ...` still works with no config at all.
func resolve() (catalog, profileFile string, err error) {
	c, e := resolveConfig()
	if e != nil {
		return "", "", e
	}
	return c.catalog, c.profileFile, nil
}

type resolved struct {
	catalog     string
	profileFile string
	cfg         *config.Config
}

func resolveConfig() (resolved, error) {
	var out resolved
	if path := config.Find(flagConfig); path != "" {
		cfg, err := config.Load(path)
		if err != nil {
			return out, err
		}
		out.cfg = cfg
		out.catalog, out.profileFile = cfg.Catalog, cfg.Cluster.Profile.File
		if cfg.Cluster.Profile.ConfigMap != "" && out.profileFile == "" {
			return out, fmt.Errorf("%s: cluster.profile is a configMap, which only swissd can read -- pass --profile with a file for the CLI", path)
		}
	}
	if flagCatalog != "" {
		out.catalog = flagCatalog
	}
	if flagProfile != "" {
		out.profileFile = flagProfile
	}
	if out.catalog == "" {
		return out, fmt.Errorf("no catalog: set it in a config file, or pass --catalog")
	}
	return out, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// notYet is a command that exists so the shape of the CLI is visible, and fails
// loudly rather than pretending. A subcommand that silently does nothing is
// worse than one that is missing.
func notYet(use, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short + " (not implemented)",
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("%s is not implemented yet -- P0 is compose and render only", use)
		},
	}
}
