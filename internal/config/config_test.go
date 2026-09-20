package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A config is discovered -- it may come from ~/.config/swiss while the shell is
// anywhere -- so a relative path in it means "next to this file", not "next to
// wherever you happened to run from".
func TestRelativePathsResolveAgainstTheConfigFile(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "swiss.yaml", `
catalog: ./catalog
cluster:
  name: c
  profile:
    file: ./profile.yaml
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Catalog != filepath.Join(dir, "catalog") {
		t.Errorf("catalog = %q, want it next to the config", c.Catalog)
	}
	if c.Cluster.Profile.File != filepath.Join(dir, "profile.yaml") {
		t.Errorf("profile.file = %q, want it next to the config", c.Cluster.Profile.File)
	}
}

func TestURLAndAbsolutePathsAreLeftAlone(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "swiss.yaml", `
catalog: https://models.example.com/catalog/
cluster:
  name: c
  profile:
    file: /etc/swiss/profile.yaml
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Catalog != "https://models.example.com/catalog/" {
		t.Errorf("a URL must not be path-joined: %q", c.Catalog)
	}
	if c.Cluster.Profile.File != "/etc/swiss/profile.yaml" {
		t.Errorf("an absolute path must not be rewritten: %q", c.Cluster.Profile.File)
	}
}

// Naming the source explicitly beats sniffing it: "swiss/site-profile" is a
// plausible relative path as well as a plausible ConfigMap reference, and
// guessing wrong composes against another cluster's wiring.
func TestProfileNeedsExactlyOneSource(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"neither": "catalog: ./c\ncluster:\n  name: c\n  profile: {}\n",
		"both":    "catalog: ./c\ncluster:\n  name: c\n  profile: {file: ./p.yaml, configMap: swiss/p}\n",
	} {
		c, err := Load(write(t, dir, name+".yaml", body))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected a refusal", name)
		}
	}
}

func TestConfigMapMustBeNamespaced(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(write(t, dir, "swiss.yaml", "catalog: ./c\ncluster:\n  name: c\n  profile: {configMap: site-profile}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "namespace/name") {
		t.Fatalf("want a namespace/name error, got %v", err)
	}
}

// The CLI does not need a cluster name; swissd records it against every plan.
func TestOnlyTheServerRequiresAClusterName(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(write(t, dir, "swiss.yaml", "catalog: ./c\ncluster:\n  profile: {file: ./p.yaml}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("the CLI should accept this: %v", err)
	}
	if err := c.ValidateServer(); err == nil {
		t.Error("swissd should refuse a config with no cluster.name")
	}
}

func TestUnknownKeyIsATypoNotASettingThatDoesNothing(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(write(t, dir, "swiss.yaml", "catalog: ./c\ncacheTTL: 60s\n")); err == nil {
		t.Fatal("cacheTTL moved under server:; the old spelling must be rejected, not ignored")
	}
}

func TestDefaults(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(write(t, dir, "swiss.yaml", "catalog: ./c\ncluster:\n  name: c\n  profile: {configMap: swiss/p}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Cluster.Profile.Key != DefaultProfileKey || c.Server.Addr != ":8080" || c.Server.CacheTTL != time.Minute {
		t.Fatalf("defaults not applied: %+v %+v", c.Cluster.Profile, c.Server)
	}
}
