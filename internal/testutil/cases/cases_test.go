package cases

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type sample struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Count       int    `json:"count,omitempty"`
}

type embedded struct {
	A int `json:"a"`
}

type withEmbed struct {
	Name string `json:"name"`
	embedded
}

type leaf struct {
	raw string
}

func (l *leaf) UnmarshalJSON(b []byte) error {
	l.raw = string(b)
	return nil
}

type withLeaf struct {
	Name string `json:"name"`
	Blob leaf   `json:"blob"`
}

func TestRunExecutesEveryFile(t *testing.T) {
	dir := t.TempDir()
	writeCase(t, dir, "one.yaml", "name: one\ndescription: first\ncount: 1\n")
	writeCase(t, dir, "two.yaml", "name: two\ncount: 2\n")
	var got []string
	Run(t, dir, func(t *testing.T, tc sample) {
		got = append(got, tc.Name)
	})
	if strings.Join(got, ",") != "one,two" {
		t.Fatalf("executed %v", got)
	}
}

func TestUnknownField(t *testing.T) {
	dir := t.TempDir()
	writeCase(t, dir, "one.yaml", "name: one\nextra: true\n")
	_, err := LoadFiles[sample](dir)
	if err == nil || !strings.Contains(err.Error(), "extra") {
		t.Fatalf("want unknown field, got %v", err)
	}
}

func TestNameMismatchAndEmpty(t *testing.T) {
	dir := t.TempDir()
	writeCase(t, dir, "one.yaml", "name: other\n")
	_, err := LoadFiles[sample](dir)
	if err == nil || !strings.Contains(err.Error(), "must equal") {
		t.Fatalf("want name mismatch, got %v", err)
	}

	dir = t.TempDir()
	writeCase(t, dir, "one.yaml", "name: \"\"\ncount: 1\n")
	_, err = LoadFiles[sample](dir)
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("want empty name, got %v", err)
	}
}

func TestYMLIsAnError(t *testing.T) {
	dir := t.TempDir()
	writeCase(t, dir, "one.yaml", "name: one\n")
	writeCase(t, dir, "two.yml", "name: two\n")
	_, err := LoadFiles[sample](dir)
	if err == nil || !strings.Contains(err.Error(), ".yml") {
		t.Fatalf("want .yml refusal, got %v", err)
	}
}

func TestCaseInsensitiveKey(t *testing.T) {
	dir := t.TempDir()
	writeCase(t, dir, "one.yaml", "Name: one\n")
	_, err := LoadFiles[sample](dir)
	if err == nil || !strings.Contains(err.Error(), "case-sensitive") {
		t.Fatalf("want case-sensitive key refusal, got %v", err)
	}
}

func TestDuplicateNullAndSecondDocument(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"dup", "name: dup\nname: dup\n", "duplicate"},
		{"empty", "", "no document"},
		{"comment", "# only\n", "no document"},
		{"null", "null\n", "null"},
		{"second", "name: second\n---\nname: other\n", "documents"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeCase(t, dir, tc.name+".yaml", tc.body)
			_, err := LoadFiles[sample](dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestAnonymousEmbedAndUnmarshalJSONLeaf(t *testing.T) {
	dir := t.TempDir()
	writeCase(t, dir, "embed.yaml", "name: embed\na: 3\n")
	files, err := LoadFiles[withEmbed](dir)
	if err != nil {
		t.Fatal(err)
	}
	if files[0].Case.A != 3 {
		t.Fatalf("embedded field: %+v", files[0].Case)
	}

	dir = t.TempDir()
	writeCase(t, dir, "bad.yaml", "name: bad\nA: 3\n")
	if _, err := LoadFiles[withEmbed](dir); err == nil || !strings.Contains(err.Error(), "case-sensitive") {
		t.Fatalf("promoted key is case-sensitive, got %v", err)
	}

	dir = t.TempDir()
	writeCase(t, dir, "leaf.yaml", "name: leaf\nblob:\n  notAField: 1\n")
	got, err := LoadFiles[withLeaf](dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got[0].Case.Blob.raw, "notAField") {
		t.Fatalf("leaf decoder did not keep the value: %s", got[0].Case.Blob.raw)
	}
}

func writeCase(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
