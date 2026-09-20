// Command swissd serves the Swiss API for one cluster.
//
// One instance per cluster, running inside the cluster it manages: in-cluster
// ServiceAccount, its own profile, its own view. The "one web across clusters"
// is a nav switcher over `peers` in the config, not a central server holding
// credentials to every cluster.
//
// This is the read path. It writes nothing -- to a cluster or anywhere else.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/aceforeverd/swiss/internal/cluster"
	"github.com/aceforeverd/swiss/internal/config"
	"github.com/aceforeverd/swiss/internal/server"
	"github.com/aceforeverd/swiss/internal/store"
	"github.com/aceforeverd/swiss/web"
)

var version = "0.0.0-dev"

func main() {
	var (
		configPath = flag.String("config", "", "config file (default: $SWISS_CONFIG, ./swiss.yaml, ~/.config/swiss/swiss.yaml)")
		addr       = flag.String("addr", os.Getenv("SWISSD_ADDR"), "listen address (overrides config)")
		logLevel   = flag.String("log-level", envOr("SWISSD_LOG_LEVEL", "info"), "debug|info|warn|error")
		webDir     = flag.String("web-dir", os.Getenv("SWISSD_WEB_DIR"), "serve the UI from this directory instead of the embedded build")
		showVer    = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Println(version)
		return
	}
	if err := run(*configPath, *addr, *logLevel, *webDir); err != nil {
		fmt.Fprintln(os.Stderr, "swissd: "+err.Error())
		os.Exit(1)
	}
}

func run(configPath, addr, logLevel, webDir string) error {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLevel(logLevel)}))

	path := config.Find(configPath)
	if path == "" {
		return fmt.Errorf("no config file: pass --config, or set SWISS_CONFIG")
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	if addr != "" {
		cfg.Server.Addr = addr
	}
	if err := cfg.ValidateServer(); err != nil {
		return err
	}

	// Empty kubeconfig means in-cluster, which is the normal deployment. The
	// home cluster is not special-cased anywhere: same probe, same code path.
	probe, err := cluster.NewKube(cfg.Cluster.Kubeconfig, cfg.Cluster.Context)
	if err != nil {
		return fmt.Errorf("cluster access: %w", err)
	}

	// Signals cancel the context; Run then drains in-flight requests. A diff can
	// be in progress, and cutting it off mid-render tells the operator nothing
	// about whether anything changed.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := server.New(cfg, probe, log, version)

	if cfg.Server.AllowDeploy {
		if err := os.MkdirAll(filepath.Dir(cfg.Server.Database), 0o700); err != nil {
			return err
		}
		db, err := store.Open(cfg.Server.Database)
		if err != nil {
			return fmt.Errorf("database: %w", err)
		}
		defer db.Close()
		srv.SetStore(db)
		srv.SetWriter(probe)
		log.Info("deploy enabled", "database", cfg.Server.Database)
	}

	if webDir != "" {
		f, err := server.WebFromDir(webDir)
		if err != nil {
			return fmt.Errorf("-web-dir %s: %w", webDir, err)
		}
		srv.SetWeb(f)
		log.Info("serving UI from disk", "dir", webDir)
	} else if f := web.FS(); f != nil {
		srv.SetWeb(f)
	} else {
		log.Warn("no web UI embedded in this build; API only")
	}

	return srv.Run(ctx)
}

func parseLevel(s string) slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return slog.LevelInfo
	}
	return l
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
