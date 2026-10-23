// Package cases loads one YAML document per test case.
//
// A name ending in ".yaml" is a case. A name ending in ".yml" is an error.
// Any other name is ignored. Each file is one mapping: empty, comment-only,
// null, and a second document fail. Decoding is strict, keys are
// case-sensitive against json names, and a duplicate key fails. Name, when
// the case type has that field, must equal the file stem and must not be
// empty.
package cases

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	yamlv3 "gopkg.in/yaml.v3"
	"sigs.k8s.io/yaml"
)

// File is one decoded case and where it was read from.
type File[T any] struct {
	Rel  string
	Stem string
	Case T
}

// LoadFiles reads and decodes every .yaml file under dir, ordered by path.
func LoadFiles[T any](dir string) ([]File[T], error) {
	rels, err := yamlRels(dir)
	if err != nil {
		return nil, err
	}
	out := make([]File[T], 0, len(rels))
	for _, rel := range rels {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("cases: read %s: %w", rel, err)
		}
		tc, err := decodeCase[T](rel, body)
		if err != nil {
			return nil, err
		}
		stem := strings.TrimSuffix(filepath.Base(rel), ".yaml")
		if err := checkName(tc, stem, rel); err != nil {
			return nil, err
		}
		out = append(out, File[T]{Rel: rel, Stem: stem, Case: tc})
	}
	return out, nil
}

// Run loads dir and calls fn as one subtest per file. The subtest name is
// the case Description when that field is set, and the file stem otherwise.
// Every .yaml file under dir is executed.
func Run[T any](t *testing.T, dir string, fn func(t *testing.T, tc T)) {
	t.Helper()
	files, err := LoadFiles[T](dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("cases: %s has no yaml files", dir)
	}
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f.Stem] {
			t.Fatalf("cases: duplicate stem %s", f.Stem)
		}
		seen[f.Stem] = true
		t.Run(specName(f.Case, f.Stem), func(t *testing.T) {
			fn(t, f.Case)
		})
	}
	if err := sameFiles(dir, seen); err != nil {
		t.Fatal(err)
	}
}

func decodeCase[T any](rel string, body []byte) (T, error) {
	var zero T
	if err := oneMapping(body); err != nil {
		return zero, fmt.Errorf("cases: decode %s: %w", rel, err)
	}
	var tc T
	if err := yaml.UnmarshalStrict(body, &tc); err != nil {
		return zero, fmt.Errorf("cases: decode %s: %w", rel, err)
	}
	var tree any
	if err := yaml.Unmarshal(body, &tree); err != nil {
		return zero, fmt.Errorf("cases: decode %s: %w", rel, err)
	}
	if err := checkKeys(tree, reflect.TypeOf(tc)); err != nil {
		return zero, fmt.Errorf("cases: decode %s: %w", rel, err)
	}
	return tc, nil
}

func oneMapping(body []byte) error {
	dec := yamlv3.NewDecoder(bytes.NewReader(body))
	var (
		count int
		doc   yamlv3.Node
	)
	for {
		var next yamlv3.Node
		err := dec.Decode(&next)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		count++
		doc = next
	}
	if count == 0 {
		return fmt.Errorf("no document")
	}
	if count != 1 {
		return fmt.Errorf("%d documents, want 1", count)
	}
	if doc.Kind != yamlv3.DocumentNode || len(doc.Content) != 1 {
		return fmt.Errorf("document is empty")
	}
	n := doc.Content[0]
	if n.Tag == "!!null" || n.Kind == yamlv3.ScalarNode && (n.Value == "" || n.Value == "null" || n.Value == "~") && n.Tag == "!!null" {
		return fmt.Errorf("document is null")
	}
	if n.Tag == "!!null" {
		return fmt.Errorf("document is null")
	}
	if n.Kind != yamlv3.MappingNode {
		return fmt.Errorf("document must be a mapping")
	}
	return dupKeys(n)
}

func dupKeys(n *yamlv3.Node) error {
	if n == nil {
		return nil
	}
	switch n.Kind {
	case yamlv3.DocumentNode, yamlv3.SequenceNode:
		for _, c := range n.Content {
			if err := dupKeys(c); err != nil {
				return err
			}
		}
	case yamlv3.MappingNode:
		seen := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			if seen[k] {
				return fmt.Errorf("duplicate key %q", k)
			}
			seen[k] = true
			if err := dupKeys(n.Content[i+1]); err != nil {
				return err
			}
		}
	}
	return nil
}

func yamlRels(dir string) ([]string, error) {
	var rels []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(d.Name(), ".yml") {
			return fmt.Errorf("%s: .yml files are not loaded; rename it to .yaml", rel)
		}
		if !strings.HasSuffix(d.Name(), ".yaml") {
			return nil
		}
		rels = append(rels, rel)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("cases: read %s: %w", dir, err)
	}
	if len(rels) == 0 {
		return nil, fmt.Errorf("cases: %s has no yaml files", dir)
	}
	slices.Sort(rels)
	return rels, nil
}

func checkName(tc any, stem, rel string) error {
	name, ok := stringField(tc, "Name")
	if !ok {
		return nil
	}
	if name == "" {
		return fmt.Errorf("cases: %s: name is empty", rel)
	}
	if name != stem {
		return fmt.Errorf("cases: %s: name %q must equal the file stem %q", rel, name, stem)
	}
	return nil
}

func specName(tc any, stem string) string {
	if d, ok := stringField(tc, "Description"); ok && d != "" {
		return d
	}
	return stem
}

func stringField(tc any, field string) (string, bool) {
	v := reflect.ValueOf(tc)
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return "", false
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return "", false
	}
	f := v.FieldByName(field)
	if !f.IsValid() || f.Kind() != reflect.String || !f.CanInterface() {
		return "", false
	}
	return f.String(), true
}

// sameFiles checks the directory listing against the stems Run executed.
func sameFiles(dir string, executed map[string]bool) error {
	rels, err := yamlRels(dir)
	if err != nil {
		return err
	}
	if len(rels) != len(executed) {
		return fmt.Errorf("cases: %s has %d yaml files, executed %d", dir, len(rels), len(executed))
	}
	for _, rel := range rels {
		stem := strings.TrimSuffix(filepath.Base(rel), ".yaml")
		if !executed[stem] {
			return fmt.Errorf("cases: %s was not executed", rel)
		}
	}
	return nil
}
