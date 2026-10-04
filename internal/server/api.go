package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/site"
	"gopkg.in/yaml.v3"
)

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.version})
}

// handleReadyz reports whether swissd can reach its cluster. The catalog and the
// profile are shown but do not gate readiness: they are fetched lazily, a
// probe every ten seconds must not drive a refetch forever on an idle server,
// and a catalog that goes briefly unreachable should not take swissd out of the
// Service when everything it reads from the cluster still works.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 5*time.Second)
	defer cancel()

	checks := map[string]string{}
	ready := true
	if err := s.probe.Ping(ctx); err != nil {
		checks["cluster"], ready = err.Error(), false
	} else {
		checks["cluster"] = "ok"
	}

	refs, prof := s.cached()
	checks["catalog"] = "not fetched"
	if len(refs) > 0 {
		checks["catalog"] = strings.Join(refs, ", ")
	}
	checks["profile"] = "not fetched"
	if prof != nil {
		checks["profile"] = prof.Name
	}

	code := http.StatusOK
	if !ready {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]any{"ready": ready, "checks": checks})
}

type clusterInfo struct {
	Name        string `json:"name"`
	Profile     string `json:"profile"`
	ProfileName string `json:"profileName,omitempty"`
	// The scheduling defaults this cluster applies, so a deploy form can show
	// what it will get rather than an empty box.
	PriorityClassName string `json:"priorityClassName,omitempty"`
	SchedulerName     string `json:"schedulerName,omitempty"`
	Namespace         string `json:"namespace,omitempty"`
	ChartRepo         string `json:"chartRepo,omitempty"`
	// Catalog and CatalogRef are the default catalog's -- or the first's, with
	// no default -- for a page that predates the list below.
	Catalog string `json:"catalog"`
	// CatalogFrom is which document named the catalogs, "profile" or "config":
	// with two places to set them, the UI has to say which one is in force.
	CatalogFrom string `json:"catalogFrom,omitempty"`
	CatalogRef  string `json:"catalogRef,omitempty"`
	// Catalogs is every catalog a page may select, in the profile's order.
	Catalogs    []catalogInfo `json:"catalogs,omitempty"`
	Version     string        `json:"version"`
	AllowDeploy bool          `json:"allowDeploy"`
	Sites       []site.Site   `json:"sites,omitempty"`
	Warnings    []string      `json:"warnings,omitempty"`
}

// catalogInfo is one selectable catalog. Source is its location as a plan
// records it, so a page can tell which catalog a release came from.
type catalogInfo struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Source string `json:"source"`
	// Ref is empty when the catalog could not be read; a warning says why.
	Ref string `json:"ref,omitempty"`
	// Default is the catalog a page opens in when none is named.
	Default bool `json:"default,omitempty"`
}

// clusterName is the profile's name. Empty until setup has written one.
func (s *Server) clusterName(ctx context.Context) string {
	p, err := s.Profile(ctx)
	if err != nil || p == nil {
		return ""
	}
	return p.Name
}

// handleCluster is what the nav header renders: which cluster this is, what it
// is wired to, and which swissd version is answering. The version matters --
// N instances will drift, and seeing that in the switcher beats debugging a bug
// report that is really a stale deploy.
func (s *Server) handleCluster(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	repos, from := s.Catalogs(ctx)
	info := clusterInfo{
		Profile:     s.cfg.Cluster.Profile.Ref(),
		CatalogFrom: from,
		Version:     s.version,
		AllowDeploy: s.cfg.Server.AllowDeploy,
	}
	if p, err := s.Profile(ctx); err == nil {
		// One instance, one profile. Its name is the cluster name.
		info.Name = p.Name
		info.ProfileName, info.Namespace, info.ChartRepo = p.Name, p.Namespace, p.ChartRepo
		info.PriorityClassName = p.Schedule.PriorityClassName
		info.SchedulerName = p.Schedule.SchedulerName
		for _, st := range p.Sites {
			if st.Name != p.Name {
				info.Sites = append(info.Sites, st)
			}
		}
	} else {
		info.Warnings = append(info.Warnings, "profile: "+err.Error())
	}
	if len(repos) == 0 {
		_, err := s.CatalogRepo(ctx, "")
		info.Warnings = append(info.Warnings, "catalog: "+err.Error())
	}
	for _, r := range repos {
		ci := catalogInfo{Name: r.Name, URL: r.URL, Source: catalogSource(r.URL), Default: r.Default}
		if c, err := s.openCatalog(ctx, r.URL); err == nil {
			ci.Ref = c.Ref
		} else {
			info.Warnings = append(info.Warnings, fmt.Sprintf("catalog %s: %v", r.Name, err))
		}
		info.Catalogs = append(info.Catalogs, ci)
	}
	for i, c := range info.Catalogs {
		if i == 0 || c.Default {
			info.Catalog, info.CatalogRef = c.URL, c.Ref
		}
	}
	writeJSON(w, http.StatusOK, info)
}

// handleSites is the switcher's list: the other swissd instances this site
// knows about, from the profile. Each is an origin a browser navigates to, not
// one this server calls.
func (s *Server) handleSites(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	self := ""
	var sites []site.Site
	if p, err := s.Profile(ctx); err == nil {
		self = p.Name
		for _, st := range p.Sites {
			if st.Name != self {
				sites = append(sites, st)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"self":  self,
		"sites": sites,
	})
}

func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()
	c, repo, ok := s.catalogFor(ctx, w, r)
	if !ok {
		return
	}
	// The index only. A marketplace listing must not cost one fetch per model.
	writeJSON(w, http.StatusOK, map[string]any{
		"name": repo.Name, "ref": c.Ref, "source": c.Fetcher.String(), "index": c.Index,
	})
}

// catalogFor opens the catalog a request names with ?catalog=. Naming none is
// fine when there is one; with several it is a 400, as is a name that is not
// configured -- the request is wrong, not the catalog. A catalog that cannot be
// read is a 502.
func (s *Server) catalogFor(ctx context.Context, w http.ResponseWriter, r *http.Request) (*catalog.Catalog, site.CatalogRepo, bool) {
	repo, err := s.CatalogRepo(ctx, r.URL.Query().Get("catalog"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return nil, repo, false
	}
	c, err := s.openCatalog(ctx, repo.URL)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return nil, repo, false
	}
	return c, repo, true
}

func (s *Server) handleCatalogModel(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()
	c, _, ok := s.catalogFor(ctx, w, r)
	if !ok {
		return
	}
	e, err := c.Entry(ctx, r.PathValue("model"), r.URL.Query().Get("version"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	// The path this model's weights default to, so a form shows what it will
	// get rather than a sentence about the template. The template comes too: it
	// is what there is to show when the entry's own hf cannot be substituted
	// into it. Best effort -- an unreadable profile is the profile page's
	// problem, not this endpoint's.
	out := map[string]any{"ref": c.Ref, "entry": e}
	if prof, err := s.Profile(ctx); err == nil {
		out["pathTemplate"] = prof.Model.PathTemplate
		if path, err := prof.LocalPath(e.Source.HF, e.Name); err == nil {
			out["localPath"] = path
		}
		// The repository each variant's image is pulled from after the site's
		// mirror rewrite, by variant id. Resolved here so the rule has one
		// implementation and a form can offer the rewrite as a choice.
		images := map[string]string{}
		for _, v := range e.Variants {
			if v.Image != nil {
				images[v.ID] = prof.MirrorImage(v.Image.Repository)
			}
		}
		if len(images) > 0 {
			out["imageRepository"] = images
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleProfile serves the site profile as parsed, not as stored: what swissd
// is actually composing against, after defaults.
//
// A document that does not parse comes back without `profile` and with the
// reason, rather than as an error. It is the same state as having no profile at
// all -- swissd cannot compose against it -- and the site is already reported as
// uninitialised, so the web sends it to setup. Answering with a 502 instead made
// the one page that could fix it render a dead error, and the text is here so
// the offending line can be deleted rather than the whole profile retyped.
func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	raw, _, rawErr := s.readProfile(ctx)
	if rawErr != nil {
		writeError(w, http.StatusBadGateway, rawErr.Error())
		return
	}
	out := map[string]any{
		"source": s.cfg.Cluster.Profile.Ref(),
		// The stored text, which is what the editor edits. The parsed view
		// above is what swissd composes against; showing only that would mean
		// an edit round trip silently dropping every comment in the file.
		"yaml": string(raw),
	}
	if p, err := s.Profile(ctx); err == nil {
		out["profile"] = p
		out["cluster"] = p.Name
	} else {
		out["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

// handleProfileTemplate is the profile a site that has none starts from. Served
// rather than duplicated in the app: the default and the thing that validates
// it have to be one definition.
func (s *Server) handleProfileTemplate(w http.ResponseWriter, _ *http.Request) {
	raw := site.DefaultYAML("")
	// Parsed as well as raw: the setup page opens on the form, which binds to
	// an object, and the text is what its YAML tab shows. A default that does
	// not parse is a bug this endpoint should report rather than hide.
	p, err := site.Parse([]byte(raw), "the default profile")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"source":  s.cfg.Cluster.Profile.Ref(),
		"profile": p,
		"yaml":    raw,
	})
}

// saveProfileRequest is one of two things, never both.
//
// The form editor sends the profile as an object and swissd renders it, so the
// document is written by the same library that reads it. The YAML editor sends
// text, which is kept byte for byte -- comments included, which is the reason
// that editor still exists.
type saveProfileRequest struct {
	YAML    string        `json:"yaml,omitempty"`
	Profile *site.Profile `json:"profile,omitempty"`
}

// handleSaveProfile writes the site profile, which swissd owns.
//
// It is stored as the operator wrote it, comments and all, and parsed before it
// is stored: a profile that does not parse would take the deploy form down
// until somebody edited a ConfigMap by hand, and the request that broke it is
// the right place to refuse.
func (s *Server) handleSaveProfile(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()

	ref := s.cfg.Cluster.Profile.ConfigMap
	if ref == "" {
		writeError(w, http.StatusBadRequest,
			"this swissd reads its profile from a file, not from the cluster; edit "+s.cfg.Cluster.Profile.File)
		return
	}
	if s.writer == nil {
		writeError(w, http.StatusForbidden, "this swissd cannot write to the cluster")
		return
	}

	var req saveProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	body, err := req.document()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Parsed even when it was rendered from an object a moment ago: the
	// required fields and the ownership rules on extra are checked in one
	// place, and both editors arrive at it.
	p, err := site.Parse([]byte(body), "the submitted profile")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	data := map[string]string{s.cfg.Cluster.Profile.Key: body}
	if err := s.writer.PutConfigMap(ctx, ref, data); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	// The cache is a TTL over a ConfigMap that only just changed, and the whole
	// point of the write was to compose against the new one.
	s.mu.Lock()
	s.profile, s.profileAt = p, time.Now()
	s.mu.Unlock()

	s.log.InfoContext(ctx, "site profile saved", "configMap", ref, "name", p.Name)
	writeJSON(w, http.StatusOK, map[string]any{"source": ref, "profile": p, "yaml": body})
}

// document is the profile text to store, from whichever half of the request
// carried it.
func (r saveProfileRequest) document() (string, error) {
	text := strings.TrimSpace(r.YAML) != ""
	switch {
	case text && r.Profile != nil:
		return "", fmt.Errorf("send the profile as yaml or as an object, not both")
	case text:
		return r.YAML, nil
	case r.Profile != nil:
		out, err := yaml.Marshal(r.Profile)
		if err != nil {
			return "", err
		}
		return string(out), nil
	}
	return "", fmt.Errorf("the profile is empty")
}

type nodeView struct {
	cluster.Node
	// Used is GPUs held by pods on this node, and Free what is left. Both are
	// omitted when the allocation read was refused, so the page can say
	// "unknown" rather than print a zero it did not measure.
	Used *int             `json:"gpusUsed,omitempty"`
	Free *int             `json:"gpusFree,omitempty"`
	Pods []cluster.GPUPod `json:"gpuPods,omitempty"`
}

// handleNodes is the GPU inventory: what each node has, what is holding it.
//
// Kubernetes publishes capacity and allocatable but never allocated, so usage
// is summed from pod requests -- which needs a cluster-wide pod list. That grant
// can be withheld, and a node view that silently undercounts is worse than one
// that says it does not know, so the usage fields are absent rather than zero
// when the read fails.
func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	nodes, err := s.probe.Nodes(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	alloc, allocErr := s.probe.GPUAllocations(ctx)
	if allocErr != nil {
		s.log.WarnContext(ctx, "GPU usage unavailable", "err", allocErr)
	}

	out := make([]nodeView, 0, len(nodes))
	var totalGPUs, usedGPUs int
	for _, n := range nodes {
		v := nodeView{Node: n}
		totalGPUs += n.GPUs
		if allocErr == nil {
			pods := alloc[n.Name]
			used := 0
			for _, p := range pods {
				used += p.GPUs
			}
			free := max(n.GPUs-used, 0)
			v.Used, v.Free, v.Pods = &used, &free, pods
			usedGPUs += used
		}
		out = append(out, v)
	}

	body := map[string]any{
		"cluster": s.clusterName(ctx),
		"nodes":   out,
		"summary": map[string]any{"nodes": len(out), "gpus": totalGPUs, "gpusUsed": usedGPUs},
	}
	if allocErr != nil {
		body["usageError"] = allocErr.Error()
	}
	writeJSON(w, http.StatusOK, body)
}

type deployment struct {
	Release    string `json:"release"`
	Namespace  string `json:"namespace"`
	Chart      string `json:"chart,omitempty"`
	Status     string `json:"status,omitempty"`
	Revision   int    `json:"revision"`
	Updated    string `json:"updated,omitempty"`
	Model      string `json:"model,omitempty"`
	Variant    string `json:"variant,omitempty"`
	CatalogRef string `json:"catalogRef,omitempty"`
	// Catalog is the name of the configured catalog this was deployed from.
	// Empty when the site no longer lists it, or the plan predates recording it.
	Catalog string `json:"catalog,omitempty"`
	Version string `json:"version,omitempty"`
	Phase   string `json:"phase,omitempty"`
	Drift   string `json:"drift,omitempty"`
	// Route is the path the entrypoint publishes this release on, derived from
	// the plan the way the detail view derives it. Empty when the plan names no
	// route.
	Route string `json:"route,omitempty"`

	// What the plan recorded about its catalog, for finding the configured
	// catalog the release belongs to.
	source, catalogName string
}

// Paging bounds. A page is what the request pays for: the managed set is a name
// list, and the expensive per-release reads -- the plan ConfigMap and helm's own
// storage -- happen only for the rows being returned. So the cost of this
// endpoint is set by perPage, not by how many releases the cluster holds.
const (
	defaultPerPage = 25
	maxPerPage     = 100
)

// deployFanout is how many releases are resolved at once. Each one is two round
// trips, so serial is a page-sized multiple of the API server's latency; the
// cap is there because this is a shared apiserver and a reconciliation view is
// not entitled to all of it.
const deployFanout = 8

// handleDeployments is the reconciliation view: the releases swiss deployed,
// and whether each still matches the catalog it came from.
//
// The plan ConfigMap is the link. swiss writes one per release, so the set of
// those ConfigMaps IS the managed set -- enumerated with a metadata-only list,
// which costs a name list rather than every plan's values documents. helm's own
// storage is never scanned here: reading it means pulling and gunzipping every
// release in scope, most of which swiss did not deploy, to then throw them away.
//
// A release with no plan beside it is not reported at all. Installing over one
// is still refused: exec.Lookup reads helm directly, so a name already taken by
// a hand-installed release is a conflict rather than an adoption.
func (s *Server) handleDeployments(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()

	refs, err := s.probe.ManagedRefs(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	// Each release is compared with the catalog it belongs to, as that catalog
	// reads now: its ref, by name. The entry digest is not compared here; that
	// is one catalog read per release, and this page is a list.
	repos, _ := s.Catalogs(ctx)
	current := map[string]string{}
	for _, repo := range repos {
		if c, err := s.openCatalog(ctx, repo.URL); err == nil {
			current[repo.Name] = c.Ref
		}
	}

	total := len(refs)
	page, perPage := pageParams(r)
	from := min((page-1)*perPage, total)
	to := min(from+perPage, total)
	window := refs[from:to]

	// Resolved in parallel into a slice indexed by position, so the order is the
	// ref order rather than whichever read finished first.
	rows := make([]*deployment, len(window))
	var wg sync.WaitGroup
	sem := make(chan struct{}, deployFanout)
	for i, ref := range window {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			rel, err := s.probe.Release(ctx, ref.Namespace, ref.Name)
			if err != nil {
				// The plan named it a moment ago. Report the row with what is
				// known rather than failing the page over one release.
				s.log.WarnContext(ctx, "release not resolved",
					"namespace", ref.Namespace, "release", ref.Name, "err", err)
				rows[i] = &deployment{
					Release: ref.Name, Namespace: ref.Namespace,
					Drift: "could not read this release: " + err.Error(),
				}
				return
			}
			if rel == nil {
				rows[i] = &deployment{
					Release: ref.Name, Namespace: ref.Namespace,
					Drift: "a plan is recorded but the release is gone",
				}
				return
			}
			rows[i] = s.deploymentRow(ctx, *rel)
		})
	}
	wg.Wait()

	out := make([]deployment, 0, len(rows))
	var behind int
	for _, d := range rows {
		if d == nil {
			continue
		}
		// The same resolution an upgrade uses, so the row names the catalog an
		// upgrade would compose from.
		if repo, err := s.ReleaseCatalog(ctx, d.catalogName, d.source); err == nil {
			d.Catalog = repo.Name
		}
		if ref, ok := current[d.Catalog]; ok && d.CatalogRef != "" && d.CatalogRef != ref {
			behind++
			if d.Drift == "" {
				d.Drift = "catalog moved since this was deployed"
			}
		}
		out = append(out, *d)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"cluster":     s.clusterName(ctx),
		"catalogRef":  singleRef(repos, current),
		"page":        page,
		"perPage":     perPage,
		"deployments": out,
		"summary": map[string]int{
			// total is every managed release; catalogBehind is only the ones on
			// this page, because knowing it for the rest means reading their
			// plans, which is the cost paging exists to avoid.
			"total": total, "catalogBehind": behind,
		},
	})
}

// deploymentRow is one release as the view reports it.
func (s *Server) deploymentRow(ctx context.Context, rel cluster.Release) *deployment {
	d := &deployment{
		Release: rel.Name, Namespace: rel.Namespace, Chart: rel.Chart,
		Status: rel.Status, Revision: rel.Revision,
	}
	if !rel.Updated.IsZero() {
		d.Updated = rel.Updated.UTC().Format(time.RFC3339)
	}
	if st := parseStatus(rel.SwissStatus); st.Phase != "" {
		d.Phase = st.Phase
		switch st.Phase {
		case phaseFailed:
			d.Drift = "last apply failed: " + st.Error
		case phaseApplying:
			d.Drift = "an apply was started and never completed"
		}
	}
	// The route is read off the composed values rather than the summary: it is a
	// value like any other, and where it came from is the plan's own layering. A
	// plan this version cannot decode costs the route and nothing else -- but it
	// is logged, because an empty route otherwise reads as "this release
	// publishes none", which is a different fact.
	if p, err := plan.FromFiles(rel.SwissFiles); err == nil {
		d.Route = routeOf(p)
	} else {
		s.log.WarnContext(ctx, "route not derived: plan does not decode",
			"namespace", rel.Namespace, "release", rel.Name, "err", err)
	}
	if p, err := parsePlanSummary([]byte(rel.SwissFiles[plan.MetaFile])); err == nil {
		d.Model, d.Variant, d.CatalogRef, d.Version = p.Model, p.Variant, p.Ref, p.Version
		d.source, d.catalogName = p.Catalog, p.CatalogName
	} else {
		d.Drift = "plan unreadable: " + err.Error()
	}
	return d
}

// digestDrift is the warning for a version that was rewritten in place: the
// catalog still lists that version and variant, under a different entry digest
// than the one this release was composed from. Empty when there is nothing to
// say, including a plan that predates recording a digest.
func digestDrift(idx *catalog.Index, model, version, variant, pinned string) string {
	if idx == nil || model == "" || version == "" || variant == "" || pinned == "" {
		return ""
	}
	m, ok := idx.Model(model)
	if !ok {
		return ""
	}
	iv, err := m.Version(version)
	if err != nil || iv.Digest == "" || iv.Digest == pinned {
		return ""
	}
	for _, v := range iv.Variants {
		if v.ID == variant {
			return fmt.Sprintf("version %s variant %s: upstream digest changed since this was deployed", version, variant)
		}
	}
	return ""
}

// pageParams reads page and perPage, clamped. Anything unparseable is the
// default rather than an error: a bad query string should not cost the view.
func pageParams(r *http.Request) (page, perPage int) {
	page, perPage = 1, defaultPerPage
	if n, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && n > 1 {
		page = n
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("perPage")); err == nil && n > 0 {
		perPage = min(n, maxPerPage)
	}
	return page, perPage
}

// singleRef is the catalog's ref when there is one catalog. With several, no
// one ref describes the page; each row carries its own.
func singleRef(repos []site.CatalogRepo, current map[string]string) string {
	if len(repos) != 1 {
		return ""
	}
	return current[repos[0].Name]
}
