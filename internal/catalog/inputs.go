package catalog

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

// Input is one inputs entry. Its rules repeat the catalog's
// hack/lib/inputs.js: the catalog is untrusted where it is used.
// swiss does not evaluate keys against values.
type Input struct {
	// Key is a kyaml-style path (segments joined by ".", brackets kept whole), not merely a dotted key.
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
	// Patcher wraps a string answer. It contains "{value}" exactly once.
	Patcher string `yaml:"patcher,omitempty" json:"patcher,omitempty"`
}

const (
	InputString  = "string"
	InputInteger = "integer"
	InputBoolean = "boolean"
)

var inputKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*(\.([A-Za-z_][A-Za-z0-9_-]*|0|[1-9][0-9]*|\[[A-Za-z_][A-Za-z0-9_-]*=[^\]]*\]|\[=[^\]]*\]|\[\^=[^\]]+\]))*$`)

func validInputKey(k string) bool { return inputKey.MatchString(k) }

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

// checkInputs is static syntax: key grammar, patcher, and that the example
// and default are valid answers. It does not look at variant values.
func checkInputs(inputs []Input) error {
	declared := map[string]bool{}
	for _, in := range inputs {
		where := "inputs " + in.Key
		switch {
		case !validInputKey(in.Key):
			return fmt.Errorf("inputs: key %q is not a kyaml-style chart key", in.Key)
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
		if in.Patcher != "" {
			if parts := strings.Split(in.Patcher, "{value}"); len(parts) != 2 {
				return fmt.Errorf("%s: patcher must contain {value} exactly once", where)
			}
			if in.TypeOrDefault() != InputString {
				return fmt.Errorf("%s: patcher is only for a string input, and this one is %s", where, in.TypeOrDefault())
			}
		}
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
