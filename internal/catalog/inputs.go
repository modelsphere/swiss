package catalog

import (
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/modelsphere/swiss/internal/values"
)

// Input is one requires.inputs entry. Its rules repeat the catalog's
// hack/lib/inputs.js: the catalog is untrusted where it is used.
type Input struct {
	// Path is the chart value, dotted: storageKVOffload.mooncake.master.
	Path        string `yaml:"path" json:"path"`
	Title       string `yaml:"title" json:"title"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Type is string, integer or boolean. Empty is string.
	Type string `yaml:"type,omitempty" json:"type,omitempty"`
	// Pattern is what a whole string answer matches.
	Pattern string `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	// Example is the form's placeholder and what catalog CI renders with; never deployed.
	Example any `yaml:"example" json:"example"`
}

const (
	InputString  = "string"
	InputInteger = "integer"
	InputBoolean = "boolean"
)

var inputPath = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*(\.[A-Za-z_][A-Za-z0-9_-]*)*$`)

// ValidInputPath is shared with the site profile, which keys its answers by these paths.
func ValidInputPath(p string) bool { return inputPath.MatchString(p) }

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

// checkInputs: values leaves every input unset, since an input with a default is not one.
func checkInputs(inputs []Input, vals values.Tree) error {
	declared := map[string]bool{}
	for _, in := range inputs {
		where := "requires.inputs " + in.Path
		switch {
		case !ValidInputPath(in.Path):
			return fmt.Errorf("requires.inputs: path %q is not a dotted chart path", in.Path)
		case in.Path == "model.localPath":
			return fmt.Errorf("%s: swiss answers it from the site's model.pathTemplate, so it is never an input", where)
		case declared[in.Path]:
			return fmt.Errorf("%s: declared twice", where)
		case strings.TrimSpace(in.Title) == "":
			return fmt.Errorf("%s: title is required", where)
		case in.Pattern != "" && in.TypeOrDefault() != InputString:
			return fmt.Errorf("%s: pattern is only for a string input, and this one is %s", where, in.TypeOrDefault())
		}
		declared[in.Path] = true
		if at := setAt(vals, strings.Split(in.Path, ".")); at != "" {
			return fmt.Errorf("%s: values.%s sets it; values leaves an input unset, for the deploy to answer", where, at)
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
	}
	return nil
}

// setAt is where vals already says something about keys: the value, under it, or a scalar above it.
func setAt(vals values.Tree, keys []string) string {
	node := map[string]any(vals)
	for i, k := range keys {
		next, ok := node[k]
		if !ok {
			return ""
		}
		if i == len(keys)-1 {
			return strings.Join(keys, ".")
		}
		switch m := next.(type) {
		case map[string]any:
			node = m
		case values.Tree:
			node = m
		default:
			return strings.Join(keys[:i+1], ".")
		}
	}
	return ""
}
