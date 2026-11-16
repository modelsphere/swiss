package catalog

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

// Input is one inputs entry. swiss stores the key and patcher as-is: catalog CI
// validates the kyaml key grammar and patcher. swiss does not evaluate keys
// against values and does not re-implement that grammar.
type Input struct {
	// Key is stored as-is. In the catalog it is a kyaml-style path.
	Key         string `yaml:"key" json:"key"`
	Title       string `yaml:"title" json:"title"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Type is string, integer or boolean. Empty is string.
	Type string `yaml:"type,omitempty" json:"type,omitempty"`
	// Pattern is what a whole string answer matches.
	Pattern string `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	// Example is the form's placeholder and what catalog CI renders with; never deployed.
	Example any `yaml:"example" json:"example"`
	// Default is the answer the web uses when the site profile has none. swiss does not apply it.
	Default any `yaml:"default,omitempty" json:"default,omitempty"`
	// Patcher is stored as-is. Catalog CI validates it.
	Patcher string `yaml:"patcher,omitempty" json:"patcher,omitempty"`
}

const (
	InputString  = "string"
	InputInteger = "integer"
	InputBoolean = "boolean"
)

func (i Input) TypeOrDefault() string {
	if i.Type == "" {
		return InputString
	}
	return i.Type
}

// Check reports why v does not answer i. A whole float64 is an integer: JSON decodes numbers so.
func (i Input) Check(v any) error {
	switch t := i.TypeOrDefault(); t {
	case InputString:
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("%v is not a string", v)
		}
		if i.Pattern == "" {
			return nil
		}
		re, err := i.pattern()
		if err != nil {
			return err
		}
		if !re.MatchString(s) {
			return fmt.Errorf("%q does not match %s", s, i.Pattern)
		}
	case InputInteger:
		if !isInteger(v) {
			return fmt.Errorf("%v is not an integer", v)
		}
	case InputBoolean:
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("%v is not a boolean", v)
		}
	default:
		return fmt.Errorf("type %q is not string, integer or boolean", t)
	}
	return nil
}

func (i Input) pattern() (*regexp.Regexp, error) {
	re, err := regexp.Compile(`^(?:` + i.Pattern + `)$`)
	if err != nil {
		return nil, fmt.Errorf("pattern %s does not compile: %w", i.Pattern, err)
	}
	return re, nil
}

func isInteger(v any) bool {
	switch n := v.(type) {
	case int, int64, uint64:
		return true
	case float64:
		return n == math.Trunc(n) && !math.IsInf(n, 0)
	}
	return false
}

// checkInputs checks what swiss still owns. Key and patcher are stored as-is.
// It does not look at variant values.
func checkInputs(inputs []Input) error {
	declared := map[string]bool{}
	for _, in := range inputs {
		where := "inputs " + in.Key
		switch {
		case strings.TrimSpace(in.Key) == "":
			return fmt.Errorf("inputs: key is required")
		case in.Key == "model.localPath":
			return fmt.Errorf("%s: swiss answers it from the site's model.pathTemplate, so it is never an input", where)
		case declared[in.Key]:
			return fmt.Errorf("%s: declared twice", where)
		case strings.TrimSpace(in.Title) == "":
			return fmt.Errorf("%s: title is required", where)
		case in.Pattern != "" && in.TypeOrDefault() != InputString:
			return fmt.Errorf("%s: pattern is only for a string input, and this one is %s", where, in.TypeOrDefault())
		}
		declared[in.Key] = true
		if in.Pattern != "" {
			if _, err := in.pattern(); err != nil {
				return fmt.Errorf("%s: %w", where, err)
			}
		}
		if in.Example == nil {
			return fmt.Errorf("%s: example is required", where)
		}
		if err := in.Check(in.Example); err != nil {
			return fmt.Errorf("%s: example %w", where, err)
		}
		if in.Default != nil {
			if err := in.Check(in.Default); err != nil {
				return fmt.Errorf("%s: default %w", where, err)
			}
		}
	}
	return nil
}
