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
		"neither": "catalog: ./c\ncluster:\n  profile: {}\n",
		"both":    "catalog: ./c\ncluster:\n  profile: {file: ./p.yaml, configMap: swiss/p}\n",
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
	c, err := Load(write(t, dir, "swiss.yaml", "catalog: ./c\ncluster:\n  profile: {configMap: site-profile}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "namespace/name") {
		t.Fatalf("want a namespace/name error, got %v", err)
	}
}

// The cluster's name is the profile's name, set at setup. The server config
// does not carry a second one, before or after.
func TestServerConfigNeedsNoClusterName(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(write(t, dir, "swiss.yaml", "catalog: ./c\ncluster:\n  profile: {file: ./p.yaml}\nserver:\n  auth: {disabled: true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("the CLI should accept this: %v", err)
	}
	if err := c.ValidateServer(); err != nil {
		t.Errorf("swissd should accept a config with no cluster name: %v", err)
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
	c, err := Load(write(t, dir, "swiss.yaml", "catalog: ./c\ncluster:\n  profile: {configMap: swiss/p}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Cluster.Profile.Key != DefaultProfileKey || c.Server.Addr != ":8080" || c.Server.CacheTTL != time.Minute {
		t.Fatalf("defaults not applied: %+v %+v", c.Cluster.Profile, c.Server)
	}
}

// The login is on unless it is turned off. A default that depends on whether a
// credential happens to be configured is a default that ships open.
func TestSwissdRefusesToRunWithNoLoginUnlessItIsAskedTo(t *testing.T) {
	dir := t.TempDir()
	base := "catalog: ./c\ncluster:\n  profile: {configMap: swiss/p}\n"

	c, err := Load(write(t, dir, "bare.yaml", base))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ValidateServer(); err == nil {
		t.Error("a swissd config naming no credential must be refused")
	}

	mounted, err := Load(write(t, dir, "mounted.yaml", base+"server:\n  auth: {dir: /etc/swiss-auth}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := mounted.ValidateServer(); err != nil {
		t.Errorf("a mounted credential is all it needs: %v", err)
	}
	if mounted.Server.Auth.TokenTTL != 24*time.Hour {
		t.Errorf("tokenTTL default = %v, want a day", mounted.Server.Auth.TokenTTL)
	}

	off, err := Load(write(t, dir, "off.yaml", base+"server:\n  auth: {disabled: true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := off.ValidateServer(); err != nil {
		t.Errorf("turning the login off explicitly is allowed: %v", err)
	}
}

// The auth dir is discovered with the config, so a relative path means "next to
// this file" like every other path here.
func TestAuthDirResolvesAgainstTheConfigFile(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(write(t, dir, "swiss.yaml",
		"catalog: ./c\ncluster:\n  profile: {configMap: swiss/p}\nserver:\n  auth: {dir: ./auth}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.Auth.Dir != filepath.Join(dir, "auth") {
		t.Errorf("auth.dir = %q", c.Server.Auth.Dir)
	}
}

func TestGPUProductLabelsConfig(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(write(t, dir, "swiss.yaml", `
catalog: ./c
cluster:
  profile: {configMap: swiss/p}
server:
  auth: {disabled: true}
  gpuProductLabels:
    custom.com/npu: custom.com/npu.sku
    huawei.com/Ascend910:
      - accelerator/huawei-ascend910
      - accelerator-type
`))
	if err != nil {
		t.Fatal(err)
	}
	m := c.Server.GPUProductLabelsMap()
	if len(m["custom.com/npu"]) != 1 || m["custom.com/npu"][0] != "custom.com/npu.sku" {
		t.Errorf("unexpected custom.com/npu: %v", m["custom.com/npu"])
	}
	if len(m["huawei.com/Ascend910"]) != 2 || m["huawei.com/Ascend910"][0] != "accelerator/huawei-ascend910" {
		t.Errorf("unexpected huawei.com/Ascend910: %v", m["huawei.com/Ascend910"])
	}
}
