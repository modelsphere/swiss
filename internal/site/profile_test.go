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
