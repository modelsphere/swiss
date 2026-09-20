package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/aceforeverd/swiss/internal/catalog"
	"github.com/aceforeverd/swiss/internal/cluster"
	"github.com/aceforeverd/swiss/internal/config"
	"github.com/aceforeverd/swiss/internal/exec"
	"github.com/aceforeverd/swiss/internal/site"
	"github.com/aceforeverd/swiss/internal/store"
)

// Server is the read path. It holds no database: every answer comes from the
// cluster or the catalog, both of which are authoritative and neither of which
// this process owns. Persistence arrives with drafts and the audit log, not
// before -- a cache added ahead of a thing to cache is just a way to be wrong.
type Server struct {
	cfg     *config.Config
	probe   cluster.Probe
	log     *slog.Logger
	version string
	web     fs.FS
	store   *store.Store
	writer  cluster.Writer

	mu        sync.Mutex
	cat       *catalog.Catalog
	catAt     time.Time
	profile   *site.Profile
	profileAt time.Time
}

func New(cfg *config.Config, probe cluster.Probe, log *slog.Logger, version string) *Server {
	return &Server{cfg: cfg, probe: probe, log: log, version: version}
}

// cached returns what has already been fetched, without fetching.
func (s *Server) cached() (*catalog.Catalog, *site.Profile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cat, s.profile
}

// SetStore installs the database. Without one swissd is read-only.
func (s *Server) SetStore(st *store.Store) { s.store = st }

// SetWriter enables the endpoints that change a cluster.
func (s *Server) SetWriter(w cluster.Writer) { s.writer = w }

// Catalog returns the opened catalog, refetching once the TTL has passed.
//
// A catalog fetch is one HTTP GET of index.json, so the TTL is about not doing
// it per request rather than about the fetch being expensive. On failure the
// previous catalog is kept and served: a published catalog going briefly
// unreachable should not empty the marketplace.
func (s *Server) Catalog(ctx context.Context) (*catalog.Catalog, error) {
	s.mu.Lock()
	cached, at := s.cat, s.catAt
	s.mu.Unlock()
	if cached != nil && time.Since(at) < s.cfg.Server.CacheTTL {
		return cached, nil
	}

	c, err := catalog.Open(ctx, s.cfg.Catalog)
	if err != nil {
		if cached != nil {
			s.log.WarnContext(ctx, "catalog refresh failed, serving previous", "err", err, "ref", cached.Ref)
			return cached, nil
		}
		return nil, err
	}
	s.mu.Lock()
	s.cat, s.catAt = c, time.Now()
	s.mu.Unlock()
	return c, nil
}

// Profile reads the site profile from the ConfigMap in this cluster.
func (s *Server) Profile(ctx context.Context) (*site.Profile, error) {
	s.mu.Lock()
	cached, at := s.profile, s.profileAt
	s.mu.Unlock()
	if cached != nil && time.Since(at) < s.cfg.Server.CacheTTL {
		return cached, nil
	}

	raw, origin, err := s.readProfile(ctx)
	if err != nil {
		return nil, err
	}
	p, err := site.Parse(raw, origin)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.profile, s.profileAt = p, time.Now()
	s.mu.Unlock()
	return p, nil
}

// readProfile fetches the site profile from wherever the config says it lives.
// A ConfigMap is the normal in-cluster case; a file is what a swissd run from a
// laptop uses, and what a chart mounting the profile as a volume would use.
func (s *Server) readProfile(ctx context.Context) (raw []byte, origin string, err error) {
	pc := s.cfg.Cluster.Profile
	if pc.File != "" {
		b, err := os.ReadFile(pc.File)
		if err != nil {
			return nil, "", fmt.Errorf("site profile: %w", err)
		}
		return b, pc.File, nil
	}
	data, err := s.probe.ConfigMap(ctx, pc.ConfigMap)
	if err != nil {
		return nil, "", fmt.Errorf("site profile %s: %w", pc.ConfigMap, err)
	}
	v, ok := data[pc.Key]
	if !ok {
		return nil, "", fmt.Errorf("site profile %s has no key %q", pc.ConfigMap, pc.Key)
	}
	return []byte(v), pc.ConfigMap, nil
}

// Handler builds the mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("GET /api/cluster", s.handleCluster)
	mux.HandleFunc("GET /api/peers", s.handlePeers)
	mux.HandleFunc("GET /api/catalog", s.handleCatalog)
	mux.HandleFunc("GET /api/catalog/{model}", s.handleCatalogModel)
	mux.HandleFunc("GET /api/releases", s.handleReleases)
	mux.HandleFunc("GET /api/deployments", s.handleDeployments)
	mux.HandleFunc("GET /api/nodes", s.handleNodes)
	mux.HandleFunc("GET /api/runs", s.handleRuns)

	if s.cfg.Server.AllowDeploy {
		mux.HandleFunc("POST /api/plans", s.handlePlan)
		mux.HandleFunc("POST /api/diff", s.handleDiff)
		mux.HandleFunc("POST /api/apply", s.handleApply(exec.Upgrade))
		mux.HandleFunc("POST /api/install", s.handleApply(exec.Install))
	} else {
		for _, p := range []string{"POST /api/plans", "POST /api/diff", "POST /api/apply", "POST /api/install"} {
			mux.HandleFunc(p, func(w http.ResponseWriter, _ *http.Request) {
				writeError(w, http.StatusForbidden, "this swissd is read-only: set server.allowDeploy and grant rbac.allowDeploy")
			})
		}
	}
	// Least specific, so it only sees what the routes above did not match.
	mux.HandleFunc("GET /", s.spa)
	return s.recover(s.logRequests(mux))
}

// Run serves until ctx is cancelled, then drains.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.Server.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: a diff against a live cluster is the slowest thing
		// here and a fixed deadline would cut it off mid-render. Per-request
		// deadlines belong on the handlers that need them.
		IdleTimeout: 60 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		s.log.Info("swissd listening", "addr", s.cfg.Server.Addr, "cluster", s.cfg.Cluster.Name, "version", s.version)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		s.log.Info("shutting down")
		down, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		return srv.Shutdown(down)
	}
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Info("request", "method", r.Method, "path", r.URL.Path,
			"status", rec.status, "ms", time.Since(start).Milliseconds())
	})
}

// recover keeps one bad request from taking the process down. A panic here is a
// bug to fix, not a reason for every other cluster view to go dark.
func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("panic", "path", r.URL.Path, "value", v)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
