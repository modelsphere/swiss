package site

import (
	"bytes"

	"gopkg.in/yaml.v3"
)

// migrations bring a stored profile up to date, each filling in a field a
// profile from before it has no value for, and saying what it did.
var migrations = []func(root *yaml.Node) (string, error){
	addGPUs,
}

// Migrate runs every migration over a stored profile. It returns what changed,
// in words for the log; when nothing did, raw comes back as it was. A changed
// document is re-encoded, which keeps comments but not the original spacing.
func Migrate(raw []byte) ([]byte, []string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return raw, nil, nil
	}
	var changes []string
	for _, m := range migrations {
		change, err := m(doc.Content[0])
		if err != nil {
			return nil, nil, err
		}
		if change != "" {
			changes = append(changes, change)
		}
	}
	if len(changes) == 0 {
		return raw, nil, nil
	}

	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, nil, err
	}
	return out.Bytes(), changes, nil
}

// addGPUs gives a profile from before nodes.gpus the well-known GPU resources:
// only when nodes.gpus is missing or null. Any other value, empty or malformed,
// is the operator's to fix, and is left for Parse to refuse.
func addGPUs(root *yaml.Node) (string, error) {
	var defaults yaml.Node
	if err := yaml.Unmarshal([]byte(defaultGPUs), &defaults); err != nil {
		return "", err
	}
	gpus := defaults.Content[0]
	nodes := mapValue(root, "nodes")
	switch {
	case nodes == nil:
		root.Content = append(root.Content, scalar("nodes"), mapping(scalar("gpus"), gpus))
	case isNull(nodes):
		replace(nodes, mapping(scalar("gpus"), gpus))
	case nodes.Kind != yaml.MappingNode:
		return "", nil
	default:
		switch v := mapValue(nodes, "gpus"); {
		case v == nil:
			nodes.Content = append(nodes.Content, scalar("gpus"), gpus)
		case isNull(v):
			replace(v, gpus)
		default:
			return "", nil
		}
	}
	return "added nodes.gpus: the well-known GPU resources", nil
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func isNull(n *yaml.Node) bool { return n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null" }

func scalar(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }

func mapping(kv ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: kv}
}

// replace swaps n's value for v's, keeping the comments written beside n.
func replace(n, v *yaml.Node) {
	head, line, foot := n.HeadComment, n.LineComment, n.FootComment
	*n = *v
	n.HeadComment, n.LineComment, n.FootComment = head, line, foot
}
