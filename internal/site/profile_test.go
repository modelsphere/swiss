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
	got, err := profile().LocalPath("Qwen/Qwen3.6-35B-A3B", "qwen3.6-35b-a3b")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/mnt/disk0/models/Qwen/Qwen3.6-35B-A3B" {
		t.Fatalf("got %q", got)
	}
}

// Real layouts are not uniform: two of these live under an org directory and one
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
	for _, bad := range []string{"swiss-catalog", "./swiss-catalog", "../catalog", "oci://ghcr.io/modelsphere/catalog"} {
		if _, err := Parse([]byte(head+"catalog: \""+bad+"\"\n"), "test"); err == nil {
			t.Errorf("catalog %q must be refused", bad)
		}
	}
}

// A catalog list is what a page selects from, so each entry needs a usable,
// unique name and a location the single-catalog field would also accept.
func TestCatalogsValidation(t *testing.T) {
	const head = "name: prod\nmodel:\n  pathTemplate: /models/{{name}}\n"

	p, err := Parse([]byte(head+`catalogs:
  - name: " public "
    url: " https://models.example.com/swiss-catalog/ "
  - name: internal
    url: /mnt/disk0/internal-catalog
`), "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.CatalogRepos(); len(got) != 2 || got[0] != (CatalogRepo{Name: "public", URL: "https://models.example.com/swiss-catalog/"}) {
		t.Errorf("catalogs parsed to %+v, want both, trimmed", got)
	}

	for name, raw := range map[string]string{
		"both spellings": "catalog: /mnt/a\ncatalogs:\n  - name: b\n    url: /mnt/b\n",
		"no url":         "catalogs:\n  - name: a\n",
		"no name":        "catalogs:\n  - url: /mnt/a\n",
		"bad name":       "catalogs:\n  - name: Public Models\n    url: /mnt/a\n",
		"relative url":   "catalogs:\n  - name: a\n    url: ./catalog\n",
		"name twice":     "catalogs:\n  - name: a\n    url: /mnt/a\n  - name: a\n    url: /mnt/b\n",
		"url twice":      "catalogs:\n  - name: a\n    url: /mnt/a\n  - name: b\n    url: /mnt/a\n",
		"two defaults":   "catalogs:\n  - name: a\n    url: /mnt/a\n    default: true\n  - name: b\n    url: /mnt/b\n    default: true\n",
	} {
		if _, err := Parse([]byte(head+raw), "test"); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// One catalog may be marked default; it is the one a page opens in.
func TestCatalogDefault(t *testing.T) {
	p, err := Parse([]byte("name: prod\nmodel:\n  pathTemplate: /models/{{name}}\n"+
		"catalogs:\n  - name: a\n    url: /mnt/a\n  - name: b\n    url: /mnt/b\n    default: true\n"), "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.CatalogRepos(); got[0].Default || !got[1].Default {
		t.Errorf("got %+v, want only b marked default", got)
	}
}

// A profile from before the list reads as one catalog named "default", so an
// existing site keeps working without editing its profile.
func TestLoneCatalogIsTheDefault(t *testing.T) {
	p, err := Parse([]byte("name: prod\nmodel:\n  pathTemplate: /models/{{name}}\ncatalog: /mnt/c\n"), "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.CatalogRepos(); len(got) != 1 || got[0] != (CatalogRepo{Name: DefaultCatalogName, URL: "/mnt/c"}) {
		t.Errorf("got %+v, want one catalog named default", got)
	}
	none, _ := Parse([]byte("name: prod\nmodel:\n  pathTemplate: /models/{{name}}\n"), "test")
	if got := none.CatalogRepos(); len(got) != 0 {
		t.Errorf("a profile naming no catalog lists none, got %+v", got)
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
