package server

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const openAPIPath = "../../docs/openapi.yaml"

// The spec and the mux name the same routes, both ways.
func TestOpenAPICoversEveryRoute(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	routes := map[string]bool{}
	for _, m := range regexp.MustCompile(`"(GET|POST|PUT|DELETE|PATCH) (/[^"]*)"`).FindAllStringSubmatch(string(src), -1) {
		if m[2] != "/" { // the web app
			routes[m[1]+" "+m[2]] = true
		}
	}
	if len(routes) == 0 {
		t.Fatal("no routes found in server.go")
	}

	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	raw, err := os.ReadFile(filepath.FromSlash(openAPIPath))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	for path, ops := range spec.Paths {
		for method := range ops {
			if method != "parameters" {
				documented[strings.ToUpper(method)+" "+path] = true
			}
		}
	}

	if missing := minus(routes, documented); len(missing) > 0 {
		t.Errorf("routes missing from %s:\n  %s", openAPIPath, strings.Join(missing, "\n  "))
	}
	if extra := minus(documented, routes); len(extra) > 0 {
		t.Errorf("%s documents routes swissd does not serve:\n  %s", openAPIPath, strings.Join(extra, "\n  "))
	}
}

func TestOpenAPIRefsResolve(t *testing.T) {
	raw, err := os.ReadFile(filepath.FromSlash(openAPIPath))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var broken []string
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok && !resolves(doc, ref) {
				broken = append(broken, ref)
			}
			for _, x := range v {
				walk(x)
			}
		case []any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	walk(doc)
	if len(broken) > 0 {
		slices.Sort(broken)
		t.Errorf("unresolved $ref:\n  %s", strings.Join(slices.Compact(broken), "\n  "))
	}
}

func resolves(doc map[string]any, ref string) bool {
	path, ok := strings.CutPrefix(ref, "#/")
	if !ok {
		return false
	}
	var cur any = doc
	for _, key := range strings.Split(path, "/") {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		if cur, ok = m[key]; !ok {
			return false
		}
	}
	return true
}

func minus(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}
