package server

import (
	"net/http"
	"time"

	"github.com/aceforeverd/swiss/internal/catalog"
	"github.com/aceforeverd/swiss/internal/cluster"
	"github.com/aceforeverd/swiss/internal/config"
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

	cat, prof := s.cached()
	checks["catalog"] = "not fetched"
	if cat != nil {
		checks["catalog"] = cat.Ref
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
	Name        string        `json:"name"`
	Profile     string        `json:"profile"`
	ProfileName string        `json:"profileName,omitempty"`
	Namespace   string        `json:"namespace,omitempty"`
	ChartRepo   string        `json:"chartRepo,omitempty"`
	Catalog     string        `json:"catalog"`
	CatalogRef  string        `json:"catalogRef,omitempty"`
	Version     string        `json:"version"`
	AllowDeploy bool          `json:"allowDeploy"`
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
		Name:        s.cfg.Cluster.Name,
		Profile:     s.cfg.Cluster.Profile.Ref(),
		Catalog:     s.cfg.Catalog,
		Version:     s.version,
		AllowDeploy: s.cfg.Server.AllowDeploy,
		Peers:       s.cfg.Server.Peers,
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
	e, err := c.Entry(ctx, r.PathValue("model"), r.URL.Query().Get("version"))
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

// handleProfile serves the site profile as parsed, not as stored: what swissd
// is actually composing against, after defaults.
func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	p, err := s.Profile(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"source":  s.cfg.Cluster.Profile.Ref(),
		"cluster": s.cfg.Cluster.Name,
		"profile": p,
	})
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
			free := n.GPUs - used
			if free < 0 {
				free = 0
			}
			v.Used, v.Free, v.Pods = &used, &free, pods
			usedGPUs += used
		}
		out = append(out, v)
	}

	body := map[string]any{
		"cluster": s.cfg.Cluster.Name,
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
	Managed    bool   `json:"managed"`
	Model      string `json:"model,omitempty"`
	Variant    string `json:"variant,omitempty"`
	CatalogRef string `json:"catalogRef,omitempty"`
	Version    string `json:"version,omitempty"`
	Phase      string `json:"phase,omitempty"`
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
		if st := parseStatus(rel.SwissStatus); st.Phase != "" {
			d.Phase = st.Phase
			switch st.Phase {
			case phaseFailed:
				d.Drift = "last apply failed: " + st.Error
			case phaseApplying:
				d.Drift = "an apply was started and never completed"
			}
		}
		if p, err := parsePlanSummary(rel.SwissPlan); err == nil {
			d.Model, d.Variant, d.CatalogRef, d.Version = p.Model, p.Variant, p.Ref, p.Version
			if catErr == nil && p.Ref != "" && p.Ref != cat.Ref {
				behind++
				if d.Drift == "" {
					d.Drift = "catalog moved since this was deployed"
				}
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
