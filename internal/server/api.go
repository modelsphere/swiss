package server

import (
	"net/http"
	"time"

	"github.com/aceforeverd/swiss/internal/catalog"
	"github.com/aceforeverd/swiss/internal/config"
)

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.version})
}

// handleReadyz reports whether this swissd can actually do its job: reach the
// cluster, read its profile, and fetch the catalog. Each is reported separately
// -- "not ready" without saying which dependency is down is a page someone has
// to go and investigate by hand.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	checks := map[string]string{}
	ready := true
	if _, err := s.probe.Releases(ctx); err != nil {
		checks["cluster"], ready = err.Error(), false
	} else {
		checks["cluster"] = "ok"
	}
	if _, err := s.Profile(ctx); err != nil {
		checks["profile"], ready = err.Error(), false
	} else {
		checks["profile"] = "ok"
	}
	if c, err := s.Catalog(ctx); err != nil {
		checks["catalog"], ready = err.Error(), false
	} else {
		checks["catalog"] = c.Ref
	}

	code := http.StatusOK
	if !ready {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]any{"ready": ready, "checks": checks})
}

type clusterInfo struct {
	Name        string        `json:"name"`
	Profile     string        `json:"profile"`
	ProfileName string        `json:"profileName,omitempty"`
	Namespace   string        `json:"namespace,omitempty"`
	ChartRepo   string        `json:"chartRepo,omitempty"`
	Catalog     string        `json:"catalog"`
	CatalogRef  string        `json:"catalogRef,omitempty"`
	Version     string        `json:"version"`
	Peers       []config.Peer `json:"peers,omitempty"`
	Warnings    []string      `json:"warnings,omitempty"`
}

// handleCluster is what the nav header renders: which cluster this is, what it
// is wired to, and which swissd version is answering. The version matters --
// N instances will drift, and seeing that in the switcher beats debugging a bug
// report that is really a stale deploy.
func (s *Server) handleCluster(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	info := clusterInfo{
		Name:    s.cfg.Cluster.Name,
		Profile: s.cfg.Cluster.Profile.Ref(),
		Catalog: s.cfg.Catalog,
		Version: s.version,
		Peers:   s.cfg.Server.Peers,
	}
	if p, err := s.Profile(ctx); err == nil {
		info.ProfileName, info.Namespace, info.ChartRepo = p.Name, p.Namespace, p.ChartRepo
	} else {
		info.Warnings = append(info.Warnings, "profile: "+err.Error())
	}
	if c, err := s.Catalog(ctx); err == nil {
		info.CatalogRef = c.Ref
	} else {
		info.Warnings = append(info.Warnings, "catalog: "+err.Error())
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handlePeers(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"self":  s.cfg.Cluster.Name,
		"peers": s.cfg.Server.Peers,
	})
}

func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()
	c, err := s.Catalog(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	// The index only. A marketplace listing must not cost one fetch per model.
	writeJSON(w, http.StatusOK, map[string]any{"ref": c.Ref, "source": c.Fetcher.String(), "index": c.Index})
}

func (s *Server) handleCatalogModel(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()
	c, err := s.Catalog(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	e, err := c.Entry(ctx, r.PathValue("model"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ref": c.Ref, "entry": e})
}

func (s *Server) handleReleases(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	rel, err := s.probe.Releases(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cluster": s.cfg.Cluster.Name, "releases": rel})
}

func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	nodes, err := s.probe.Nodes(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cluster": s.cfg.Cluster.Name, "nodes": nodes})
}

type deployment struct {
	Release    string `json:"release"`
	Namespace  string `json:"namespace"`
	Chart      string `json:"chart,omitempty"`
	Status     string `json:"status,omitempty"`
	Revision   int    `json:"revision"`
	Updated    string `json:"updated,omitempty"`
	Managed    bool   `json:"managed"`
	Model      string `json:"model,omitempty"`
	Variant    string `json:"variant,omitempty"`
	CatalogRef string `json:"catalogRef,omitempty"`
	Drift      string `json:"drift,omitempty"`
}

// handleDeployments is the reconciliation view: every live release, and whether
// Swiss knows where it came from.
//
// The row that matters is the unmanaged one. A release live in the cluster with
// no plan beside it was installed by hand, and it is exactly what the adoption
// hazard looks like from the outside -- two engines on one set of GPUs starts
// with one release nobody's inventory knows about.
func (s *Server) handleDeployments(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()

	releases, err := s.probe.Releases(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	cat, catErr := s.Catalog(ctx)

	out := make([]deployment, 0, len(releases))
	var unmanaged, behind int
	for _, rel := range releases {
		d := deployment{
			Release: rel.Name, Namespace: rel.Namespace, Chart: rel.Chart,
			Status: rel.Status, Revision: rel.Revision,
		}
		if !rel.Updated.IsZero() {
			d.Updated = rel.Updated.UTC().Format(time.RFC3339)
		}
		if rel.SwissPlan == nil {
			unmanaged++
			d.Drift = "untracked: no plan recorded beside this release"
			out = append(out, d)
			continue
		}
		d.Managed = true
		if p, err := parsePlanSummary(rel.SwissPlan); err == nil {
			d.Model, d.Variant, d.CatalogRef = p.Model, p.Variant, p.Ref
			if catErr == nil && p.Ref != "" && p.Ref != cat.Ref {
				behind++
				d.Drift = "catalog moved since this was deployed"
			}
		} else {
			d.Drift = "plan unreadable: " + err.Error()
		}
		out = append(out, d)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"cluster":     s.cfg.Cluster.Name,
		"catalogRef":  refOrEmpty(cat, catErr),
		"deployments": out,
		"summary": map[string]int{
			"total": len(out), "untracked": unmanaged, "catalogBehind": behind,
		},
	})
}

func refOrEmpty(c *catalog.Catalog, err error) string {
	if err != nil || c == nil {
		return ""
	}
	return c.Ref
}
