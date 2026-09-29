package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelsphere/swiss/internal/auth"
	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/config"
	"github.com/modelsphere/swiss/internal/exec"
	"github.com/modelsphere/swiss/internal/site"
	"github.com/modelsphere/swiss/internal/store"
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
	// namespace is swissd's own, which is where a site profile naming a Secret
	// by bare name means.
	namespace string
	web       fs.FS
	store     *store.Store
	writer    cluster.Writer

	// creds overrides where the login is read from. Nil is the real thing: the
	// mounted Secret named by the config.
	creds  func() (auth.Credentials, error)
	logins failures

	// catCache survives this process: the index it holds is what a restart
	// renders when the catalog cannot be reached.
	catCache *catalog.Cache

	mu sync.Mutex
	// cats is every catalog opened so far, by location. Keyed on the location
	// rather than the name, so a profile that repoints a name opens the new
	// catalog instead of serving the old one for the rest of the TTL.
	cats      map[string]openCatalog
	profile   *site.Profile
	profileAt time.Time
}

type openCatalog struct {
	cat *catalog.Catalog
	at  time.Time
}

func New(cfg *config.Config, probe cluster.Probe, log *slog.Logger, version string) *Server {
	if k, ok := probe.(*cluster.Kube); ok && len(cfg.Server.GPUProductLabels) > 0 {
		k.GPUProductLabels = cfg.Server.GPUProductLabelsMap()
	}
	return &Server{
		cfg: cfg, probe: probe, log: log, version: version,
		namespace: cluster.SelfNamespace(),
		catCache:  catalog.NewCache(cfg.Server.CatalogCacheDir()),
		cats:      map[string]openCatalog{},
	}
}

// cached returns what has already been fetched, without fetching: the refs of
// the catalogs opened so far, and the profile.
func (s *Server) cached() ([]string, *site.Profile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	refs := make([]string, 0, len(s.cats))
	for _, oc := range s.cats {
		refs = append(refs, oc.cat.Ref)
	}
	sort.Strings(refs)
	return refs, s.profile
}

// SetStore installs the database. Without one swissd is read-only.
func (s *Server) SetStore(st *store.Store) { s.store = st }

// SetWriter enables the endpoints that change a cluster.
//
// Set whatever allowDeploy says: swissd writes its own site profile even when
// it deploys nothing, and which of the two a given install may actually do is
// RBAC's answer rather than this flag's.
func (s *Server) SetWriter(w cluster.Writer) { s.writer = w }

// SetCredentials overrides where the login comes from, for tests.
func (s *Server) SetCredentials(f func() (auth.Credentials, error)) { s.creds = f }

// Catalog opens a catalog by name; see CatalogRepo for what an empty name means.
func (s *Server) Catalog(ctx context.Context, name string) (*catalog.Catalog, site.CatalogRepo, error) {
	repo, err := s.CatalogRepo(ctx, name)
	if err != nil {
		return nil, repo, err
	}
	c, err := s.openCatalog(ctx, repo.URL)
	return c, repo, err
}

// openCatalog returns the catalog at loc, refetching once the TTL has passed.
//
// A catalog fetch is one HTTP GET of index.json, so the TTL is about not doing
// it per request rather than about the fetch being expensive. On failure the
// previous catalog is kept and served: a published catalog going briefly
// unreachable should not empty the marketplace.
func (s *Server) openCatalog(ctx context.Context, loc string) (*catalog.Catalog, error) {
	s.mu.Lock()
	cached, have := s.cats[loc]
	s.mu.Unlock()
	if have && time.Since(cached.at) < s.cfg.Server.CacheTTL {
		return cached.cat, nil
	}

	f, err := catalog.NewFetcher(loc)
	if err != nil {
		return nil, err
	}
	c, err := catalog.OpenFetcher(ctx, f)
	if err != nil {
		if have {
			s.log.WarnContext(ctx, "catalog refresh failed, serving previous", "err", err, "ref", cached.cat.Ref, "catalog", loc)
			return cached.cat, nil
		}
		// Nothing in memory for this location: this process may have just
		// started, which is exactly when an unreachable catalog would otherwise
		// mean an empty marketplace.
		if disk, wrote, derr := s.catCache.Open(f, loc); derr == nil {
			s.log.WarnContext(ctx, "catalog unreachable, serving the cached index",
				"err", err, "cachedAt", wrote, "ref", disk.Ref, "catalog", loc)
			s.keepCatalog(disk, loc)
			return disk, nil
		}
		return nil, err
	}
	if err := s.catCache.Save(loc, c); err != nil {
		s.log.WarnContext(ctx, "catalog index cache not written", "err", err, "dir", s.catCache.Dir())
	}
	s.keepCatalog(c, loc)
	return c, nil
}

func (s *Server) keepCatalog(c *catalog.Catalog, loc string) {
	s.mu.Lock()
	s.cats[loc] = openCatalog{cat: c, at: time.Now()}
	s.mu.Unlock()
}

// Catalogs is the catalog repos this instance deploys from, and where the list
// was said: the site profile when it names any, otherwise the config file's one
// catalog, under the name "default". The profile is the document an operator
// can edit, so it wins; the config value is the install-time default and what
// the CLI, which cannot read the profile's ConfigMap, has to go on.
//
// An unreadable profile is not a reason to lose the catalogs: the configured
// default stands in, and the caller that needed the profile reports that
// separately.
func (s *Server) Catalogs(ctx context.Context) (repos []site.CatalogRepo, from string) {
	if p, err := s.Profile(ctx); err == nil {
		if repos := p.CatalogRepos(); len(repos) > 0 {
			return repos, "profile"
		}
	}
	if s.cfg.Catalog == "" {
		return nil, ""
	}
	return []site.CatalogRepo{{Name: site.DefaultCatalogName, URL: s.cfg.Catalog}}, "config"
}

// CatalogRepo resolves a catalog by name. An empty name is the catalog when
// there is exactly one, and otherwise the one the profile marks default. With
// several and no default it is refused, so neither a look at the catalog nor a
// deploy lands on whichever one happens to be listed first.
func (s *Server) CatalogRepo(ctx context.Context, name string) (site.CatalogRepo, error) {
	repos, _ := s.Catalogs(ctx)
	if len(repos) == 0 {
		return site.CatalogRepo{}, fmt.Errorf("no catalog: set catalogs in the site profile, or catalog in %s", s.cfg.Origin)
	}
	if name == "" {
		if len(repos) == 1 {
			return repos[0], nil
		}
		for _, r := range repos {
			if r.Default {
				return r, nil
			}
		}
		return site.CatalogRepo{}, fmt.Errorf("%d catalogs are configured (%s) and none is the default: name one", len(repos), catalogNames(repos))
	}
	for _, r := range repos {
		if r.Name == name {
			return r, nil
		}
	}
	return site.CatalogRepo{}, fmt.Errorf("no catalog named %q (have: %s)", name, catalogNames(repos))
}

// CatalogFrom finds the configured repo a plan was composed from. A plan
// records the catalog's source -- the location as the fetcher normalized it,
// index.json appended and a path made absolute -- so that is what is compared.
func (s *Server) CatalogFrom(ctx context.Context, source string) (site.CatalogRepo, bool) {
	repos, _ := s.Catalogs(ctx)
	for _, r := range repos {
		if catalogSource(r.URL) == source {
			return r, true
		}
	}
	return site.CatalogRepo{}, false
}

// ReleaseCatalog is the configured catalog a release belongs to, from what its
// plan recorded: the catalog it names; for a plan that names none, the one at
// the location it records; and otherwise the default, or the only catalog.
// That last step is the migration path: a release deployed before this site
// listed catalogs -- or from the CLI, which records no name -- keeps upgrading
// from the site's default without anyone editing its plan.
//
// A plan that names a catalog the site no longer lists is refused rather than
// sent to the default: it says where it came from, and that is not there.
func (s *Server) ReleaseCatalog(ctx context.Context, name, source string) (site.CatalogRepo, error) {
	repos, _ := s.Catalogs(ctx)
	if name != "" {
		for _, r := range repos {
			if r.Name == name {
				return r, nil
			}
		}
		return site.CatalogRepo{}, fmt.Errorf("it was deployed from catalog %q, which this site no longer lists -- add it back to catalogs in the site profile", name)
	}
	if source != "" {
		if r, ok := s.CatalogFrom(ctx, source); ok {
			return r, nil
		}
	}
	r, err := s.CatalogRepo(ctx, "")
	if err != nil {
		if len(repos) > 1 {
			return r, fmt.Errorf("its plan names no catalog this site lists (it records %q) and there is no default to stand in -- mark one catalog default in the site profile", source)
		}
		return r, err
	}
	return r, nil
}

// catalogSource is a location as a plan records it. Empty when it does not
// parse, which matches nothing.
func catalogSource(loc string) string {
	f, err := catalog.NewFetcher(loc)
	if err != nil {
		return ""
	}
	return f.String()
}

func catalogNames(repos []site.CatalogRepo) string {
	names := make([]string, len(repos))
	for i, r := range repos {
		names[i] = r.Name
	}
	return strings.Join(names, ", ")
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
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/session", s.handleSession)
	mux.HandleFunc("GET /api/cluster", s.handleCluster)
	mux.HandleFunc("GET /api/sites", s.handleSites)
	mux.HandleFunc("GET /api/catalog", s.handleCatalog)
	mux.HandleFunc("GET /api/catalog/{model}", s.handleCatalogModel)
	mux.HandleFunc("GET /api/deployments", s.handleDeployments)
	mux.HandleFunc("GET /api/nodes", s.handleNodes)
	mux.HandleFunc("GET /api/profile", s.handleProfile)
	// swissd owns the profile now: the chart no longer writes one, so this is
	// both the first-run setup and every later edit. One endpoint, because a
	// setup path that writes through different code is one that drifts from
	// the editor.
	mux.HandleFunc("PUT /api/profile", s.handleSaveProfile)
	mux.HandleFunc("GET /api/profile/template", s.handleProfileTemplate)
	mux.HandleFunc("GET /api/runs", s.handleRuns)
	mux.HandleFunc("GET /api/runs/{id}", s.handleRun)
	mux.HandleFunc("GET /api/releases/{namespace}/{release}/plan", s.handleReleasePlan)
	mux.HandleFunc("GET /api/releases/{namespace}/{release}/status", s.handleStatus)
	mux.HandleFunc("GET /api/releases/{namespace}/{release}/revisions", s.handleRevisions)
	mux.HandleFunc("GET /api/releases/{namespace}/{release}/revisions/{revision}/values", s.handleRevisionValues)
	mux.HandleFunc("GET /api/releases/{namespace}/{release}/revisions/{revision}/plan", s.handleRevisionPlan)
	mux.HandleFunc("POST /api/releases/{namespace}/{release}/probe", s.handleProbe)
	// Not behind allowDeploy: it changes nothing in the cluster. It spends a few
	// tokens of GPU time, which is the same bargain as the /v1/models probe.
	mux.HandleFunc("POST /api/releases/{namespace}/{release}/chat", s.handleChat)
	// The chart created the requirement. Thresholds are a later edit, made
	// through the SLO server so helm's field manager is not asked to give
	// them up. GET is a read; PUT is refused below when deploys are off.
	mux.HandleFunc("GET /api/releases/{namespace}/{release}/slo", s.handleSLO)
	mux.HandleFunc("PUT /api/releases/{namespace}/{release}/slo", s.handleSLO)
	mux.HandleFunc("DELETE /api/releases/{namespace}/{release}/slo", s.handleSLO)

	if s.cfg.Server.AllowDeploy {
		mux.HandleFunc("POST /api/plans", s.handlePlan)
		mux.HandleFunc("POST /api/diff", s.handleDiff)
		mux.HandleFunc("POST /api/apply", s.handleApply(exec.Upgrade))
		mux.HandleFunc("POST /api/install", s.handleApply(exec.Install))
		mux.HandleFunc("DELETE /api/releases/{namespace}/{release}", s.handleUninstall)
		mux.HandleFunc("POST /api/releases/{namespace}/{release}/revisions/{revision}/diff", s.handleRevisionDiff)
		mux.HandleFunc("POST /api/releases/{namespace}/{release}/rollback", s.handleRollback)
	} else {
		for _, p := range []string{
			"POST /api/plans", "POST /api/diff", "POST /api/apply", "POST /api/install",
			"DELETE /api/releases/{namespace}/{release}",
			"POST /api/releases/{namespace}/{release}/rollback",
			"POST /api/releases/{namespace}/{release}/revisions/{revision}/diff",
		} {
			mux.HandleFunc(p, func(w http.ResponseWriter, _ *http.Request) {
				writeError(w, http.StatusForbidden, "this swissd is read-only: set server.allowDeploy and grant rbac.allowDeploy")
			})
		}
	}
	// Least specific, so it only sees what the routes above did not match.
	mux.HandleFunc("GET /", s.spa)
	return s.recover(s.logRequests(s.requireAuth(mux)))
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
		s.log.Info("swissd listening", "addr", s.cfg.Server.Addr, "version", s.version)
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
