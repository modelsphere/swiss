package site

import "testing"

func profile() Profile {
	return Profile{
		Name: "prod",
		Model: ModelPaths{
			PathTemplate: "/mnt/disk0/models/{{org}}/{{name}}",
			Overrides:    map[string]string{"kimi-k2.5": "/mnt/disk0/models/Kimi-K2.5"},
		},
	}
}

func TestLocalPathUsesTheTemplate(t *testing.T) {
	got, err := profile().LocalPath("modelforge/Qwen3.6-35B-A3B-793303", "modelforge")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/mnt/disk0/models/modelforge/Qwen3.6-35B-A3B-793303" {
		t.Fatalf("got %q", got)
	}
}

// Real layouts are not uniform: two of these live under modelforge/ and one
// does not, so a template with no escape hatch cannot express the cluster.
func TestOverrideWinsOverTheTemplate(t *testing.T) {
	got, err := profile().LocalPath("moonshotai/Kimi-K2.5", "kimi-k2.5")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/mnt/disk0/models/Kimi-K2.5" {
		t.Fatalf("got %q", got)
	}
}

func TestUnknownPlaceholderIsAnError(t *testing.T) {
	p := profile()
	p.Model.PathTemplate = "/models/{{nope}}"
	if _, err := p.LocalPath("a/b", "m"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestSecretReferenceQualifiesABareName(t *testing.T) {
	a := RouteAuth{SecretRef: "llm-openresty"}
	if got := a.SecretReference("swiss-system"); got != "swiss-system/llm-openresty" {
		t.Errorf("bare name: %q", got)
	}
	if got := (RouteAuth{SecretRef: "other/key"}).SecretReference("swiss-system"); got != "other/key" {
		t.Errorf("an explicit namespace must win: %q", got)
	}
	if got := (RouteAuth{}).SecretReference("swiss-system"); got != "" {
		t.Errorf("no ref stays no ref: %q", got)
	}
}

func TestModelURLJoinsTheGatewayAndRoute(t *testing.T) {
	r := Route{Gateway: "https://llm.example.com/"}
	if got := r.ModelURL("/kimi"); got != "https://llm.example.com/kimi" {
		t.Errorf("one slash between them: %q", got)
	}
	if got := (Route{}).ModelURL("kimi"); got != "" {
		t.Errorf("no gateway, no URL: %q", got)
	}
	if got := r.ModelURL(""); got != "" {
		t.Errorf("no route, no URL: %q", got)
	}
}

// The catalog set here wins over swissd's config file, so what it may say is
// checked when the profile is parsed rather than when a fetch fails.
func TestCatalogValidation(t *testing.T) {
	const head = "name: prod\nmodel:\n  pathTemplate: /models/{{name}}\n"

	for name, want := range map[string]string{
		"https://models.example.com/swiss-catalog/": "https://models.example.com/swiss-catalog/",
		"http://models.internal/catalog/":           "http://models.internal/catalog/",
		"/mnt/disk0/swiss-catalog":                  "/mnt/disk0/swiss-catalog",
		"  https://models.example.com/c/  ":         "https://models.example.com/c/",
		"":                                          "",
	} {
		p, err := Parse([]byte(head+"catalog: \""+name+"\"\n"), "test")
		if err != nil {
			t.Errorf("catalog %q must parse: %v", name, err)
			continue
		}
		if p.Catalog != want {
			t.Errorf("catalog %q parsed to %q, want %q", name, p.Catalog, want)
		}
	}

	// This document is read from a ConfigMap, so a relative path has nothing to
	// be relative to -- refused rather than resolved against whatever directory
	// swissd happens to be started in.
	for _, bad := range []string{"swiss-catalog", "./swiss-catalog", "../catalog", "oci://harbor/catalog"} {
		if _, err := Parse([]byte(head+"catalog: \""+bad+"\"\n"), "test"); err == nil {
			t.Errorf("catalog %q must be refused", bad)
		}
	}
}

func TestSitesValidation(t *testing.T) {
	valid := `
name: prod
model:
  pathTemplate: /models/{{name}}
sites:
  - name: " dev "
    url: " https://swiss.dev.internal "
`
	p, err := Parse([]byte(valid), "test")
	if err != nil {
		t.Fatalf("valid sites must parse: %v", err)
	}
	if len(p.Sites) != 1 || p.Sites[0].Name != "dev" || p.Sites[0].URL != "https://swiss.dev.internal" {
		t.Fatalf("sites not parsed/trimmed: %+v", p.Sites)
	}

	for name, raw := range map[string]string{
		"missing name": `
name: prod
model:
  pathTemplate: /models/{{name}}
sites:
  - url: https://swiss.dev.internal
`,
		"missing url": `
name: prod
model:
  pathTemplate: /models/{{name}}
sites:
  - name: dev
`,
		"bad scheme": `
name: prod
model:
  pathTemplate: /models/{{name}}
sites:
  - name: dev
    url: swiss.dev.internal
`,
	} {
		if _, err := Parse([]byte(raw), name); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}
