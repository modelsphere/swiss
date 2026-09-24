// Package site holds the cluster-shaped half of a deploy: the values a public
// catalog cannot know and a deploy form should not have to retype.
//
// A profile lives in the private charts repo next to deploys/, because that is
// already where namespace, route and serviceId truth lives.
package site

import (
	"fmt"
	"os"
	"strings"

	"github.com/modelsphere/swiss/internal/values"
	"gopkg.in/yaml.v3"
)

type Profile struct {
	Name string `yaml:"name" json:"name"`

	// Namespace a release lands in unless the deploy overrides it.
	Namespace string `yaml:"namespace,omitempty" json:"namespace,omitempty"`

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

	// CreateNamespace passes --create-namespace. Off by default: under helm v4
	// that applies the Namespace object server-side, so it needs patch on
	// namespaces even when the namespace already exists -- a cluster-scoped
	// privilege swissd has no other reason to hold, for namespaces an admin
	// already created for it.
	CreateNamespace bool        `yaml:"createNamespace,omitempty" json:"createNamespace,omitempty"`
	Extra           values.Tree `yaml:"extra,omitempty" json:"extra,omitempty"`
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
	// ServerAddress is the decision server or the Prometheus query API,
	// depending on which provider a deploy selects.
	ServerAddress string            `yaml:"serverAddress,omitempty" json:"serverAddress,omitempty"`
	ServerHeaders map[string]string `yaml:"serverHeaders,omitempty" json:"serverHeaders,omitempty"`
}

type Route struct {
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
	return &p, nil
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
