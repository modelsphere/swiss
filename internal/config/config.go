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
	// CacheTTL bounds how long a fetched catalog or profile is reused.
	CacheTTL time.Duration `yaml:"cacheTTL,omitempty"`
	// Peers are the other clusters' swissd instances, for the nav switcher.
	// Config, not discovery.
	Peers []Peer `yaml:"peers,omitempty"`
}

type Peer struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
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
	if c.Server.CacheTTL == 0 {
		c.Server.CacheTTL = 60 * time.Second
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
	for i, p := range c.Server.Peers {
		if p.Name == "" || p.URL == "" {
			return fmt.Errorf("%s: server.peers[%d] needs both name and url", c.origin(), i)
		}
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
