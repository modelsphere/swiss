package site

import "testing"

func TestSchemaVersionOrDefault(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "v1"},
		{"v1", "v1"},
		{"  v1  ", "v1"},
		{"v2", "v2"},
		{"  v2", "v2"},
	}
	for _, tc := range cases {
		got := (CatalogRepo{SchemaVersion: tc.in}).SchemaVersionOrDefault()
		if got != tc.want {
			t.Errorf("SchemaVersion %q: got %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSchemaVersionValidation(t *testing.T) {
	const head = "name: prod\nmodel:\n  pathTemplate: /models/{{name}}\n"
	ok, err := Parse([]byte(head+`catalogs:
  - name: public
    url: /mnt/a
  - name: next
    url: /mnt/b
    schemaVersion: v2
`), "test")
	if err != nil {
		t.Fatal(err)
	}
	repos := ok.CatalogRepos()
	if repos[0].SchemaVersionOrDefault() != "v1" || repos[1].SchemaVersionOrDefault() != "v2" {
		t.Fatalf("mixed profile defaults: %+v", repos)
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
