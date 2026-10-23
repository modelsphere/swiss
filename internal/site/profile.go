// Package site holds the cluster-shaped half of a deploy: the values a public
// catalog cannot know and a deploy form should not have to retype.
//
// A profile lives in the private charts repo next to deploys/, because that is
// already where namespace, route and serviceId truth lives.
package site

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/modelsphere/swiss/internal/values"
	"gopkg.in/yaml.v3"
)

type Profile struct {
	Name string `yaml:"name" json:"name"`

	// Namespace a release lands in unless the deploy overrides it.
	Namespace string `yaml:"namespace,omitempty" json:"namespace,omitempty"`

	// ApplyWith is the backend an install naming none uses: "helm" or
	// "llmsvc". Empty means helm. A release already in the cluster stays on the
	// backend that owns it. llmsvc needs the LLMService CRD; when that CRD is
	// not served, discovery fails, or swissd has no llmservices grant, new
	// installs stay on helm and /api/cluster says so.
	ApplyWith string `yaml:"applyWith,omitempty" json:"applyWith,omitempty"`

	// Catalogs are the catalog repos this site deploys from, each under a name
	// the pages and plan requests use. Viewing the catalog or deploying picks
	// one; with a single repo there is nothing to pick, and with several the one
	// marked default is picked unless another is named.
	//
	// Set here they win over swissd's config file. Which catalogs a cluster
	// reads is a property of the cluster, like the chart repo and the image
	// mirror below, and this is the document an operator can edit without a
	// helm upgrade. The config file names one catalog, which stays the
	// install-time default and what `swiss` on a laptop uses: the CLI cannot
	// read this ConfigMap.
	Catalogs []CatalogRepo `yaml:"catalogs,omitempty" json:"catalogs,omitempty"`

	// Catalog is the single-catalog spelling from before there could be
	// several, read as one repo named "default". Setting both it and Catalogs
	// is refused rather than merged: which one wins would be a guess.
	Catalog string `yaml:"catalog,omitempty" json:"catalog,omitempty"`

	// ChartRepo is where charts are pulled from -- the registry the catalog
	// deliberately does not name. Empty means a local chart path, which is fine
	// for the CLI and not for a server.
	ChartRepo string `yaml:"chartRepo,omitempty" json:"chartRepo,omitempty"`
	ChartPath string `yaml:"chartPath,omitempty" json:"chartPath,omitempty"`

	// Registry rewrites engine image repositories to a mirror. The catalog pins
	// which build (image.tag); this says where it is pulled from.
	//
	// omitempty on the yaml tags of these sections, and not on the json ones:
	// the form editor round-trips a profile through Marshal, and a section
	// nobody filled in should come back absent rather than as `cache: {}`. The
	// JSON the web reads keeps them, so a form field has something to bind to.
	Registry Registry `yaml:"registry,omitempty" json:"registry"`

	Model    ModelPaths `yaml:"model" json:"model"`
	Schedule Schedule   `yaml:"schedule,omitempty" json:"schedule"`
	Cache    Cache      `yaml:"cache,omitempty" json:"cache"`
	Scaler   Scaler     `yaml:"scaler,omitempty" json:"scaler"`
	Route    Route      `yaml:"route,omitempty" json:"route"`
	Nodes    Nodes      `yaml:"nodes,omitempty" json:"nodes"`

	// Defaults are the site's preferences, composed beneath the catalog so a
	// variant overrides them. Every other section is a fact about this cluster
	// and composes above the catalog.
	Defaults Defaults `yaml:"defaults,omitempty" json:"defaults"`

	// Sites are the other clusters' swissd instances, for the nav switcher.
	//
	// In the profile rather than swissd's config because it is a fact about the
	// estate, and the profile is the half an operator can edit from the web --
	// a new cluster should not need a helm upgrade of every other one to become
	// reachable from them.
	//
	// Config, not discovery: there is no registry of swissd instances, and each
	// one serves this same switcher, so any of them is a valid entry point.
	Sites []Site `yaml:"sites,omitempty" json:"sites,omitempty"`

	Extra values.Tree `yaml:"extra,omitempty" json:"extra,omitempty"`
}

// Site is one other swissd, by name and address. The URL is where a browser
// goes, so it is an origin this one links to and never calls.
type Site struct {
	Name string `yaml:"name" json:"name"`
	URL  string `yaml:"url" json:"url"`
}

// Schedule is what this cluster puts a GPU workload on by default. Both are
// form-owned, so a deploy overrides either; absent means the chart's default.
type Schedule struct {
	PriorityClassName string `yaml:"priorityClassName,omitempty" json:"priorityClassName,omitempty"`
	SchedulerName     string `yaml:"schedulerName,omitempty" json:"schedulerName,omitempty"`
}

type Registry struct {
	// Mirror replaces the registry host/org of an image repository. "" disables
	// the rewrite and the catalog's repository is used as-is.
	Mirror string `yaml:"mirror,omitempty" json:"mirror,omitempty"`
}

type ModelPaths struct {
	// PathTemplate builds model.localPath from the catalog's source.hf.
	// {{hf}} is the full repo id, {{org}} and {{name}} its halves, {{model}} the
	// catalog entry name. A template rather than a per-model table: a site that
	// has to add a row here for every model in a public catalog will not keep up.
	PathTemplate string `yaml:"pathTemplate" json:"pathTemplate"`
	// Overrides, keyed by catalog model name, for weights that do not sit where
	// the template says. Real layouts are not uniform; a template with no escape
	// hatch just means the first irregular model cannot be deployed at all.
	Overrides map[string]string `yaml:"overrides,omitempty" json:"overrides,omitempty"`
}

type Cache struct {
	Enabled  bool   `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	HostPath string `yaml:"hostPath,omitempty" json:"hostPath,omitempty"`
}

type Scaler struct {
	// ServerAddress is what the chart receives: the decision server or the
	// Prometheus query API, depending on which provider a deploy selects.
	ServerAddress string `yaml:"serverAddress,omitempty" json:"serverAddress,omitempty"`
	// SLOAddress is the SLO API swissd calls to read and edit LLMSLORequirement
	// thresholds. Same service as ServerAddress. It is not copied into chart
	// values. Empty means DefaultSLOAddress.
	SLOAddress string `yaml:"sloAddress,omitempty" json:"sloAddress,omitempty"`
	// SLOTokenSecret is "namespace/name" of the Secret holding the bearer
	// token, SLOTokenKey the key within it. A bare name is swissd's own
	// namespace. The profile is a ConfigMap, so the token is not written here.
	SLOTokenSecret string            `yaml:"sloTokenSecret,omitempty" json:"sloTokenSecret,omitempty"`
	SLOTokenKey    string            `yaml:"sloTokenKey,omitempty" json:"sloTokenKey,omitempty"`
	ServerHeaders  map[string]string `yaml:"serverHeaders,omitempty" json:"serverHeaders,omitempty"`
	// AutoscalePeriod is how often swissd asks decision-gen for a PD slice
	// count. A Go duration. Empty is 30s.
	AutoscalePeriod string `yaml:"autoscalePeriod,omitempty" json:"autoscalePeriod,omitempty"`
}

type Defaults struct {
	Features FeatureDefaults `yaml:"features,omitempty" json:"features"`
}

// FeatureDefaults are per-site <section>.enabled defaults. Unset is on.
// modelRoute is not one: it is route.enabled, a site fact the variant cannot
// override.
type FeatureDefaults struct {
	Cart           *bool `yaml:"cart,omitempty" json:"cart,omitempty"`
	ServiceMonitor *bool `yaml:"serviceMonitor,omitempty" json:"serviceMonitor,omitempty"`
	SLORequirement *bool `yaml:"sloRequirement,omitempty" json:"sloRequirement,omitempty"`
	Scaler         *bool `yaml:"scaler,omitempty" json:"scaler,omitempty"`
}

// Resolve gives each feature's default by chart section.
func (f FeatureDefaults) Resolve() map[string]bool {
	on := func(b *bool) bool { return b == nil || *b }
	return map[string]bool{
		"cart":           on(f.Cart),
		"serviceMonitor": on(f.ServiceMonitor),
		"sloRequirement": on(f.SLORequirement),
		"scaler":         on(f.Scaler),
	}
}

// DefaultSLOAddress is the in-cluster SLO API when the profile names none.
const DefaultSLOAddress = "http://slo-api.llm-scaler.svc:80"

// DefaultDecisionAddress is the in-cluster decision server when the profile
// names none.
const DefaultDecisionAddress = "http://decision-gen.llm-scaler.svc:80"

// DecisionServer is the address swissd calls for PD slice decisions.
func (sc Scaler) DecisionServer() string {
	if a := strings.TrimRight(strings.TrimSpace(sc.ServerAddress), "/"); a != "" {
		return a
	}
	return DefaultDecisionAddress
}

// SLOServer is the address swissd calls, defaulted.
func (sc Scaler) SLOServer() string {
	if a := strings.TrimRight(strings.TrimSpace(sc.SLOAddress), "/"); a != "" {
		return a
	}
	return DefaultSLOAddress
}

// SLOTokenRef qualifies a bare secret name with swissd's namespace.
func (sc Scaler) SLOTokenRef(ns string) string {
	if sc.SLOTokenSecret == "" || ns == "" || strings.Contains(sc.SLOTokenSecret, "/") {
		return sc.SLOTokenSecret
	}
	return ns + "/" + sc.SLOTokenSecret
}

// SLOTokenKeyName is the data key the token is read from.
func (sc Scaler) SLOTokenKeyName() string {
	if sc.SLOTokenKey != "" {
		return sc.SLOTokenKey
	}
	return "token"
}

type Route struct {
	// Enabled is modelRoute.enabled for every deploy here, above the catalog
	// so no variant can publish a route on its own. A deploy can still turn
	// routing on or off. Unset is off.
	Enabled bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// NginxConfigMap is the shared openresty ConfigMap ("ns/name") every model
	// on one entrypoint writes a key into. The route-collision preflight is
	// scoped to this value.
	NginxConfigMap   string `yaml:"nginxConfigMap,omitempty" json:"nginxConfigMap,omitempty"`
	NginxService     string `yaml:"nginxService,omitempty" json:"nginxService,omitempty"`
	NginxSelector    string `yaml:"nginxSelector,omitempty" json:"nginxSelector,omitempty"`
	MonitorConfigMap string `yaml:"monitorConfigMap,omitempty" json:"monitorConfigMap,omitempty"`
	// Gateway is the public base URL openresty is served on from outside the
	// cluster, e.g. https://llm.example.com. Display only: a model's URL is
	// <gateway>/<route>. Every check still calls nginxService in-cluster, so a
	// wrong or absent gateway changes nothing but what the status page shows.
	Gateway string `yaml:"gateway,omitempty" json:"gateway,omitempty"`
	// NginxPort is the entrypoint's port; the readiness check calls
	// http://<nginxService>:<port>/<route>/v1/models. Defaults to 8080.
	NginxPort int `yaml:"nginxPort,omitempty" json:"nginxPort,omitempty"`
	// Auth is how swissd authenticates when it calls the entrypoint. Only the
	// serving and health checks do; nothing else here talks to a model.
	Auth RouteAuth `yaml:"auth,omitempty" json:"auth"`
}

// ModelURL is where a route is reached from outside, empty when the profile
// names no gateway.
func (r Route) ModelURL(route string) string {
	if r.Gateway == "" || route == "" {
		return ""
	}
	return strings.TrimSuffix(r.Gateway, "/") + "/" + strings.TrimPrefix(route, "/")
}

// RouteAuth names a credential rather than holding one. The site profile is a
// ConfigMap, so a bearer token written here would be readable by anyone who can
// read the profile -- the Secret indirection is the whole point of the type.
type RouteAuth struct {
	// Header the key is sent in and the prefix before it. The defaults are the
	// OpenAI convention: `Authorization: Bearer <key>`.
	Header string `yaml:"header,omitempty" json:"header,omitempty"`
	Prefix string `yaml:"prefix,omitempty" json:"prefix,omitempty"`
	// SecretRef is "namespace/name" of the Secret holding the key, SecretKey
	// the key within it. A bare name is swissd's own namespace. The namespace
	// must be one swissd was granted, or the read fails the way any other
	// unlisted namespace does.
	SecretRef string `yaml:"secretRef,omitempty" json:"secretRef,omitempty"`
	SecretKey string `yaml:"secretKey,omitempty" json:"secretKey,omitempty"`
	// Headers are sent on every call to the entrypoint. Not a place for
	// credentials, for the same reason as above.
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
}

// HeaderName is where the key goes, defaulted.
func (a RouteAuth) HeaderName() string {
	if a.Header != "" {
		return a.Header
	}
	return "Authorization"
}

// SecretReference is SecretRef as "namespace/name", qualifying a bare name with
// ns -- swissd's own namespace, where a key written for swissd to send is
// created. Where the entrypoint runs is a separate fact and not a default.
func (a RouteAuth) SecretReference(ns string) string {
	if a.SecretRef == "" || ns == "" || strings.Contains(a.SecretRef, "/") {
		return a.SecretRef
	}
	return ns + "/" + a.SecretRef
}

// KeyPrefix is what precedes the key. An explicitly empty prefix on a custom
// header is meaningful -- `X-Api-Key: <key>` carries no scheme -- so it is only
// defaulted when the header is too.
func (a RouteAuth) KeyPrefix() string {
	if a.Prefix != "" {
		return a.Prefix
	}
	if a.Header == "" {
		return "Bearer "
	}
	return ""
}

type Nodes struct {
	// GPUsPerNode is what a full GPU node has, used to derive
	// cache.maxSlotsPerNode. 0 leaves that value to the chart's default.
	GPUsPerNode int `yaml:"gpusPerNode,omitempty" json:"gpusPerNode,omitempty"`
	// GPUProductAliases names GPU products by node labels, for a vendor install
	// that does not label its nodes with the catalog's product name: product ->
	// selectors, each "key=value[,key=value...]" with every pair required. A
	// node matching aliases of two products takes the one with more pairs.
	GPUProductAliases map[string][]string `yaml:"gpuProductAliases,omitempty" json:"gpuProductAliases,omitempty"`
}

// GPUProductAliasesMap is gpuProductAliases as label sets per product. Parse
// has refused a malformed selector, so none is dropped here.
func (n Nodes) GPUProductAliasesMap() map[string][]map[string]string {
	if len(n.GPUProductAliases) == 0 {
		return nil
	}
	out := make(map[string][]map[string]string, len(n.GPUProductAliases))
	for product, sels := range n.GPUProductAliases {
		for _, sel := range sels {
			if labels, err := parseLabelSelector(sel); err == nil {
				out[product] = append(out[product], labels)
			}
		}
	}
	return out
}

func parseLabelSelector(sel string) (map[string]string, error) {
	labels := map[string]string{}
	for _, pair := range strings.Split(sel, ",") {
		k, v, ok := strings.Cut(pair, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("%q is not key=value", strings.TrimSpace(pair))
		}
		if prev, dup := labels[k]; dup && prev != v {
			return nil, fmt.Errorf("%q sets %s twice", sel, k)
		}
		labels[k] = v
	}
	return labels, nil
}

// A node matching selectors of two products takes the one with more pairs, so
// two of equal size that a single node could satisfy are refused. Product names
// are trimmed like catalog and site names, since the site page writes this map.
func (n *Nodes) checkGPUProductAliases() error {
	if len(n.GPUProductAliases) == 0 {
		return nil
	}
	type alias struct {
		product, sel string
		labels       map[string]string
	}
	var all []alias
	trimmed := make(map[string][]string, len(n.GPUProductAliases))
	for _, product := range slices.Sorted(maps.Keys(n.GPUProductAliases)) {
		sels := n.GPUProductAliases[product]
		name := strings.TrimSpace(product)
		switch {
		case name == "":
			return fmt.Errorf("nodes.gpuProductAliases: empty product name")
		case trimmed[name] != nil:
			return fmt.Errorf("nodes.gpuProductAliases: %s is listed twice", name)
		case len(sels) == 0:
			return fmt.Errorf("nodes.gpuProductAliases.%s: needs at least one key=value selector", name)
		}
		trimmed[name] = sels
		for _, sel := range sels {
			labels, err := parseLabelSelector(sel)
			if err != nil {
				return fmt.Errorf("nodes.gpuProductAliases.%s: %w", name, err)
			}
			all = append(all, alias{name, sel, labels})
		}
	}
	for i, a := range all {
		for _, b := range all[i+1:] {
			if a.product != b.product && len(a.labels) == len(b.labels) && compatible(a.labels, b.labels) {
				return fmt.Errorf("nodes.gpuProductAliases: a node can match both %s (%q) and %s (%q) -- add a label to one", a.product, a.sel, b.product, b.sel)
			}
		}
	}
	n.GPUProductAliases = trimmed
	return nil
}

func compatible(a, b map[string]string) bool {
	for k, v := range a {
		if w, ok := b[k]; ok && w != v {
			return false
		}
	}
	return true
}

// Load reads a profile file.
func Load(path string) (*Profile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw, path)
}

// Parse decodes a profile from bytes. The CLI reads a file and the server reads
// a ConfigMap out of the cluster the profile describes; both land here, so the
// two cannot validate differently.
//
// This package deliberately does not know about Kubernetes. The server fetches
// the ConfigMap through its cluster probe and hands the bytes over, which keeps
// profile parsing testable without a cluster or a fake for one.
func Parse(raw []byte, origin string) (*Profile, error) {
	var p Profile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("%s: %w", origin, err)
	}
	if p.Name == "" {
		return nil, fmt.Errorf("%s: name is required", origin)
	}
	if p.Model.PathTemplate == "" {
		return nil, fmt.Errorf("%s: model.pathTemplate is required -- the catalog gives an identity, not a path", origin)
	}
	p.ApplyWith = strings.TrimSpace(p.ApplyWith)
	switch p.ApplyWith {
	case "", "helm", "llmsvc":
	default:
		return nil, fmt.Errorf("%s: applyWith %q is not helm or llmsvc", origin, p.ApplyWith)
	}
	p.Catalog = strings.TrimSpace(p.Catalog)
	if p.Catalog != "" && len(p.Catalogs) > 0 {
		return nil, fmt.Errorf("%s: catalog and catalogs are both set -- move catalog into catalogs", origin)
	}
	if err := checkCatalogURL(p.Catalog); err != nil {
		return nil, fmt.Errorf("%s: catalog %w", origin, err)
	}
	// A name is what a page and a plan request select by, so it has to be
	// unique; one index listed twice is two names for one catalog, which a
	// deployment row could only report under one of them. One base url can
	// serve both schemas, though -- index.json and index-v2.json side by side,
	// v1 models staying in the first -- and those are two catalogs.
	names, keys := map[string]bool{}, map[string]string{}
	dflt := ""
	for i, c := range p.Catalogs {
		c.Name, c.URL = strings.TrimSpace(c.Name), strings.TrimSpace(c.URL)
		schema, err := catalog.NormalizeSchema(c.SchemaVersion)
		if err != nil {
			return nil, fmt.Errorf("%s: catalogs[%d]: %w", origin, i, err)
		}
		c.SchemaVersion = schema
		key := catalog.CacheKey(c.URL, schema)
		switch {
		case c.Name == "" || c.URL == "":
			return nil, fmt.Errorf("%s: catalogs[%d] needs both name and url", origin, i)
		case !validCatalogName(c.Name):
			return nil, fmt.Errorf("%s: catalogs[%d] name %q must be lowercase letters, digits, '.', '-' or '_', starting with a letter or digit", origin, i, c.Name)
		case names[c.Name]:
			return nil, fmt.Errorf("%s: catalogs[%d]: name %q is listed twice", origin, i, c.Name)
		case keys[key] != "":
			return nil, fmt.Errorf("%s: catalogs[%d]: %s is already listed as %q", origin, i, c.URL, keys[key])
		}
		if err := checkCatalogURL(c.URL); err != nil {
			return nil, fmt.Errorf("%s: catalogs[%d] url %w", origin, i, err)
		}
		if err := checkWritable(c); err != nil {
			return nil, fmt.Errorf("%s: catalogs[%d] %w", origin, i, err)
		}
		p.Catalogs[i] = c
		names[c.Name], keys[key] = true, c.Name
		// Two defaults is no default: which one a page opens in would be a guess.
		if c.Default {
			if dflt != "" {
				return nil, fmt.Errorf("%s: catalogs %q and %q are both marked default -- at most one is", origin, dflt, c.Name)
			}
			dflt = c.Name
		}
	}
	// A half-filled site is a dead entry in the switcher: a name that goes
	// nowhere, or an address with nothing to click. Refused here rather than
	// rendered, because the web writes this list now.
	for i, s := range p.Sites {
		s.Name = strings.TrimSpace(s.Name)
		s.URL = strings.TrimSpace(s.URL)
		p.Sites[i] = s
		if s.Name == "" || s.URL == "" {
			return nil, fmt.Errorf("%s: sites[%d] needs both name and url", origin, i)
		}
		if !strings.HasPrefix(s.URL, "http://") && !strings.HasPrefix(s.URL, "https://") {
			return nil, fmt.Errorf("%s: sites[%d] url %q must start with http:// or https://", origin, i, s.URL)
		}
	}
	if err := p.Nodes.checkGPUProductAliases(); err != nil {
		return nil, fmt.Errorf("%s: %w", origin, err)
	}
	return &p, nil
}

// CatalogRepo is one catalog a site deploys from.
type CatalogRepo struct {
	// Name is what the pages, a plan request and a deployment row call it.
	Name string `yaml:"name" json:"name"`
	// URL is an https base or an absolute path, the shape the config file takes.
	URL string `yaml:"url" json:"url"`
	// SchemaVersion selects which published index the URL resolves to: "v2"
	// reads index-v2.json, "v1" index.json. Unset reads index.json as before,
	// or a URL naming its .json file by that document's own apiVersion, so an
	// existing profile means what it always did.
	SchemaVersion string `yaml:"schemaVersion,omitempty" json:"schemaVersion,omitempty"`
	// Default is the catalog a page opens in, and a request uses, when none is
	// named. At most one is; with none, several catalogs have to be named.
	Default bool `yaml:"default,omitempty" json:"default,omitempty"`
	// Writable marks a catalog-server catalog the console edits through its
	// api/v2; swissd itself only reads it. The token is the console's, not
	// written here: the profile is a ConfigMap.
	Writable bool `yaml:"writable,omitempty" json:"writable,omitempty"`
}

// DefaultCatalogName is what a lone catalog is called: the profile's legacy
// catalog field, or the one in swissd's config file.
const DefaultCatalogName = "default"

// CatalogRepos is the list a site deploys from: Catalogs, or a lone Catalog
// read as one repo named "default". Empty when the profile names none.
func (p Profile) CatalogRepos() []CatalogRepo {
	if len(p.Catalogs) > 0 {
		return p.Catalogs
	}
	if p.Catalog != "" {
		return []CatalogRepo{{Name: DefaultCatalogName, URL: p.Catalog}}
	}
	return nil
}

// checkCatalogURL refuses a relative path. It has no meaning here: this
// document is read from a ConfigMap, so there is no file for it to be relative
// to, and resolving it against whatever swissd's working directory happens to
// be would be a guess.
func checkCatalogURL(c string) error {
	if c != "" && !strings.HasPrefix(c, "http://") && !strings.HasPrefix(c, "https://") && !strings.HasPrefix(c, "/") {
		return fmt.Errorf("%q must be an http:// or https:// url, or an absolute path", c)
	}
	return nil
}

// checkWritable guards for writable catalog: must be in schema v2 and HTTP(s) protocol
func checkWritable(c CatalogRepo) error {
	switch {
	case !c.Writable:
		return nil
	case c.SchemaVersion != "v2":
		return fmt.Errorf("writable needs schemaVersion v2: catalog-server serves v2 only")
	case !strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://"):
		return fmt.Errorf("writable needs an http:// or https:// url: writes go to the catalog's api/v2")
	}
	return nil
}

func validCatalogName(n string) bool {
	for i, r := range n {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case i > 0 && (r == '.' || r == '-' || r == '_'):
		default:
			return false
		}
	}
	return n != "" && len(n) <= 63
}

// LocalPath renders model.localPath for one model.
func (p Profile) LocalPath(hf, model string) (string, error) {
	if v, ok := p.Model.Overrides[model]; ok {
		return v, nil
	}
	org, name, ok := strings.Cut(hf, "/")
	if !ok {
		return "", fmt.Errorf("source.hf %q is not org/name", hf)
	}
	r := strings.NewReplacer("{{hf}}", hf, "{{org}}", org, "{{name}}", name, "{{model}}", model)
	out := r.Replace(p.Model.PathTemplate)
	if strings.Contains(out, "{{") {
		return "", fmt.Errorf("model.pathTemplate %q has an unknown placeholder", p.Model.PathTemplate)
	}
	return out, nil
}

// MirrorImage rewrites an image repository to the site's mirror, keeping the
// final path element. Returns the input unchanged when no mirror is configured.
func (p Profile) MirrorImage(repository string) string {
	if p.Registry.Mirror == "" {
		return repository
	}
	parts := strings.Split(repository, "/")
	return strings.TrimSuffix(p.Registry.Mirror, "/") + "/" + parts[len(parts)-1]
}
