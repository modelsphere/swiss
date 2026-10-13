package site

import "testing"

func TestSchemaVersionValidation(t *testing.T) {
	const head = "name: prod\nmodel:\n  pathTemplate: /models/{{name}}\n"
	ok, err := Parse([]byte(head+`catalogs:
  - name: public
    url: /mnt/a
  - name: next
    url: /mnt/b
    schemaVersion: " v2 "
`), "test")
	if err != nil {
		t.Fatal(err)
	}
	repos := ok.CatalogRepos()
	if repos[0].SchemaVersion != "" || repos[1].SchemaVersion != "v2" {
		t.Fatalf("unset must stay unset and v2 be trimmed: %+v", repos)
	}

	for name, raw := range map[string]string{
		"V2":  "catalogs:\n  - name: a\n    url: /mnt/a\n    schemaVersion: V2\n",
		"1":   "catalogs:\n  - name: a\n    url: /mnt/a\n    schemaVersion: \"1\"\n",
		"bad": "catalogs:\n  - name: a\n    url: /mnt/a\n    schemaVersion: swiss\n",
	} {
		if _, err := Parse([]byte(head+raw), "test"); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// One base url serves index.json and index-v2.json side by side, v1 models
// staying in the first: the mixed profile examples/site-prod.yaml documents.
func TestOneURLOncePerSchema(t *testing.T) {
	const head = "name: prod\nmodel:\n  pathTemplate: /models/{{name}}\n"
	p, err := Parse([]byte(head+`catalogs:
  - name: public
    url: https://modelsphere.github.io/model-catalog/
    schemaVersion: v2
  - name: archive
    url: https://modelsphere.github.io/model-catalog/
`), "test")
	if err != nil {
		t.Fatalf("the documented mixed profile must parse: %v", err)
	}
	if len(p.CatalogRepos()) != 2 {
		t.Fatalf("want both catalogs: %+v", p.CatalogRepos())
	}
	// As before v2: two spellings of one v1 index are two names, not refused.
	if _, err := Parse([]byte(head+"catalogs:\n  - name: a\n    url: /mnt/c\n  - name: b\n    url: /mnt/c/index.json\n"), "test"); err != nil {
		t.Errorf("a profile valid before v2 must stay valid: %v", err)
	}

	for name, raw := range map[string]string{
		// Unset and v1 read the same index.json.
		"unset and v1": "catalogs:\n  - name: a\n    url: https://x.example/c/\n  - name: b\n    url: https://x.example/c/\n    schemaVersion: v1\n",
		"v2 twice":     "catalogs:\n  - name: a\n    url: /mnt/c\n    schemaVersion: v2\n  - name: b\n    url: /mnt/c\n    schemaVersion: v2\n",
		// A url naming its .json is one document whatever the schema says.
		"explicit json": "catalogs:\n  - name: a\n    url: https://x.example/c/index.json\n  - name: b\n    url: https://x.example/c/index.json\n    schemaVersion: v2\n",
	} {
		if _, err := Parse([]byte(head+raw), "test"); err == nil {
			t.Errorf("%s: one index listed twice must be refused", name)
		}
	}
}
