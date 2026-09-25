// Package config is the one configuration document, shared by `swiss` and
// `swissd`.
//
// One file rather than two, because the two binaries are one tool: they compose
// the same plans against the same catalog and the same cluster wiring, and a
// CLI that reads a different document from the server it talks to is a CLI that
// will eventually disagree with it. What differs is only which sections each
// one needs -- `server:` is ignored by the CLI, and a file-based profile is
// normal on a laptop and unusual in a pod.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const DefaultProfileKey = "profile.yaml"

type Config struct {
	// Catalog is an https base or a path. index.json is appended when the
	// location does not already name a .json file.
	Catalog string `yaml:"catalog"`

	Cluster Cluster `yaml:"cluster"`

	// Server is swissd's section. The CLI parses it and ignores it, so one file
	// can be mounted into a pod and checked into a repo unchanged.
	Server Server `yaml:"server,omitempty"`

	// Origin records where this was loaded from, for error messages.
	Origin string `yaml:"-"`
}

type Cluster struct {
	// Name labels every plan composed here and names this cluster in the
	// switcher. Required by swissd; the CLI falls back to the profile's name.
	Name    string  `yaml:"name,omitempty"`
	Profile Profile `yaml:"profile"`

	// Namespaces bounds what swissd reads and deploys. Empty means cluster-wide,
	// which needs a ClusterRole; a list must match the namespaces its Roles were
	// granted in, or every read is simply forbidden.
	Namespaces []string `yaml:"namespaces,omitempty"`

	// Kubeconfig empty means in-cluster first, then the usual loading rules.
	Kubeconfig string `yaml:"kubeconfig,omitempty"`
	Context    string `yaml:"context,omitempty"`
}

// Profile says where the site profile is read from. Exactly one source, named
// explicitly rather than sniffed: "swiss/site-profile" is a plausible relative
// path as well as a plausible ConfigMap reference, and guessing wrong means
// composing against another cluster's wiring.
type Profile struct {
	File      string `yaml:"file,omitempty"`      // a path, typical for the CLI
	ConfigMap string `yaml:"configMap,omitempty"` // "namespace/name", typical in-cluster
	Key       string `yaml:"key,omitempty"`       // key within the ConfigMap
}

type Server struct {
	Addr string `yaml:"addr,omitempty"`
	// Database path. Drafts, plans and the audit log; never the source of truth
	// for what is deployed.
	Database string `yaml:"database,omitempty"`
	// AllowDeploy gates every endpoint that changes a cluster. Off means swissd
	// is a read-only view, which is what it should be until its RBAC is
	// widened to match.
	AllowDeploy bool `yaml:"allowDeploy,omitempty"`
	// Binaries, when not on PATH.
	HelmBin     string `yaml:"helmBin,omitempty"`
	HelmfileBin string `yaml:"helmfileBin,omitempty"`
	// PlanHistory is how many previous plans to keep beside a release, one
	// Secret each, for rollback. Independent of helm's own historyMax: helm can
	// restore older values, but without a plan beside them swissd would be
	// describing something other than what is running.
	PlanHistory int `yaml:"planHistory,omitempty"`

	// CacheTTL bounds how long a fetched catalog or profile is reused.
	CacheTTL time.Duration `yaml:"cacheTTL,omitempty"`

	// Auth is the site login. On unless explicitly turned off: a default that
	// depends on whether a credential happens to be configured is a default
	// that ships open.
	Auth Auth `yaml:"auth,omitempty"`

	// GPUProductLabels maps extended resource names counted as GPUs on nodes
	// to candidate node label keys used to categorize each GPU SKU per vendor.
	// When empty, DefaultGPUProductLabels is used.
	GPUProductLabels map[string]StringList `yaml:"gpuProductLabels,omitempty"`
}

// GPUProductLabelsMap returns the configured GPUProductLabels as map[string][]string.
func (s Server) GPUProductLabelsMap() map[string][]string {
	if len(s.GPUProductLabels) == 0 {
		return nil
	}
	out := make(map[string][]string, len(s.GPUProductLabels))
	for k, v := range s.GPUProductLabels {
		out[k] = []string(v)
	}
	return out
}

// StringList unmarshals YAML from either a single string or a list of strings.
type StringList []string

func (s *StringList) UnmarshalYAML(value *yaml.Node) error {
	var single string
	if err := value.Decode(&single); err == nil {
		*s = []string{single}
		return nil
	}
	var list []string
	if err := value.Decode(&list); err == nil {
		*s = list
		return nil
	}
	return fmt.Errorf("expected string or list of strings")
}

// Auth is the site login: one account, read from a mounted Secret.
//
// A directory rather than a ConfigMap or an API read, because that is what a
// Secret volume is: the kubelet keeps the files current, so a rotated password
// takes effect without a restart and swissd needs no Secret grant of its own to
// log anybody in.
type Auth struct {
	// Dir holds one file per key: username, password, and optionally tokenKey.
	Dir string `yaml:"dir,omitempty"`
	// TokenTTL is how long a login lasts. One day by default: long enough not
	// to interrupt a 40-minute model load being watched, short enough that a
	// forgotten browser tab is not a standing key.
	TokenTTL time.Duration `yaml:"tokenTTL,omitempty"`
	// Disabled runs swissd with no login at all. For a laptop; it is logged at
	// every boot, because an unauthenticated swissd reachable from a Service is
	// a deploy button for whoever finds it.
	Disabled bool `yaml:"disabled,omitempty"`
	// CookieSecure is auto, always or never. Auto sets the flag when the
	// request arrived over TLS, which is right behind an ingress that
	// terminates it and right in a plain-http port-forward.
	CookieSecure string `yaml:"cookieSecure,omitempty"`
}

// Load reads a config file. A missing file is reported as such; callers that
// treat the file as optional check with Find first.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true) // an unknown key is a typo, not a setting that does nothing
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.Origin = path
	c.applyDefaults()
	c.resolvePaths(filepath.Dir(path))
	return &c, nil
}

// Find locates a config without requiring one: an explicit path, then
// SWISS_CONFIG, then swiss.yaml here, then ~/.config/swiss/swiss.yaml. Returns
// "" when there is none, which is not an error -- the CLI works from flags
// alone.
func Find(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if v := os.Getenv("SWISS_CONFIG"); v != "" {
		return v
	}
	candidates := []string{"swiss.yaml"}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".config", "swiss", "swiss.yaml"))
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func (c *Config) applyDefaults() {
	if c.Cluster.Profile.Key == "" {
		c.Cluster.Profile.Key = DefaultProfileKey
	}
	if c.Server.Addr == "" {
		c.Server.Addr = ":8080"
	}
	if c.Server.PlanHistory == 0 {
		c.Server.PlanHistory = 5
	}
	if c.Server.CacheTTL == 0 {
		c.Server.CacheTTL = 60 * time.Second
	}
	if c.Server.Database == "" {
		c.Server.Database = "/tmp/swiss/swiss.db"
	}
	if c.Server.Auth.TokenTTL == 0 {
		c.Server.Auth.TokenTTL = 24 * time.Hour
	}
	if c.Server.Auth.CookieSecure == "" {
		c.Server.Auth.CookieSecure = "auto"
	}
}

// resolvePaths makes relative locations relative to the CONFIG FILE, not to the
// working directory.
//
// The file is discovered -- it may come from ~/.config/swiss/swiss.yaml while
// the shell is anywhere at all -- so a path read out of it means "next to this
// file", which is also the only reading that survives the config being moved or
// mounted somewhere else. An https catalog is left alone.
func (c *Config) resolvePaths(dir string) {
	if c.Catalog != "" && !isURL(c.Catalog) && !filepath.IsAbs(c.Catalog) {
		c.Catalog = filepath.Join(dir, c.Catalog)
	}
	if f := c.Cluster.Profile.File; f != "" && !filepath.IsAbs(f) {
		c.Cluster.Profile.File = filepath.Join(dir, f)
	}
	if d := c.Server.Auth.Dir; d != "" && !filepath.IsAbs(d) {
		c.Server.Auth.Dir = filepath.Join(dir, d)
	}
}

func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func (c *Config) origin() string {
	if c.Origin == "" {
		return "config"
	}
	return c.Origin
}

// Validate covers what both binaries need.
func (c *Config) Validate() error {
	if c.Catalog == "" {
		return fmt.Errorf("%s: catalog is required", c.origin())
	}
	if err := c.Cluster.Profile.validate(c.origin()); err != nil {
		return err
	}
	return nil
}

// ValidateServer adds what only swissd needs.
func (c *Config) ValidateServer() error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.Cluster.Name == "" {
		return fmt.Errorf("%s: cluster.name is required for swissd -- it labels every plan composed here", c.origin())
	}
	if !c.Server.Auth.Disabled && c.Server.Auth.Dir == "" {
		return fmt.Errorf("%s: server.auth.dir is required -- mount the credential Secret, or set server.auth.disabled to run without a login", c.origin())
	}
	switch c.Server.Auth.CookieSecure {
	case "auto", "always", "never":
	default:
		return fmt.Errorf("%s: server.auth.cookieSecure %q is not auto, always or never", c.origin(), c.Server.Auth.CookieSecure)
	}
	return nil
}

func (p Profile) validate(origin string) error {
	switch {
	case p.File == "" && p.ConfigMap == "":
		return fmt.Errorf("%s: cluster.profile needs one of file or configMap", origin)
	case p.File != "" && p.ConfigMap != "":
		return fmt.Errorf("%s: cluster.profile has both file and configMap; exactly one", origin)
	case p.ConfigMap != "" && !strings.Contains(p.ConfigMap, "/"):
		return fmt.Errorf("%s: cluster.profile.configMap %q is not namespace/name", origin, p.ConfigMap)
	}
	return nil
}

// Ref is the profile's location as a single string, for display.
func (p Profile) Ref() string {
	if p.File != "" {
		return p.File
	}
	return p.ConfigMap
}
