package config

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// ProjectV8ConfigAliases is retained for SDK compatibility. The current
// configuration model already stores the public v8 layout directly.
func ProjectV8ConfigAliases(_ *yaml.Node, _ string) {}

// NormalizeV8ConfigAliases is retained for callers that accept historical
// layouts. Provider and runtime normalization happens during Config decoding.
func NormalizeV8ConfigAliases(root *yaml.Node) error {
	if root == nil {
		return nil
	}
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return nil
		}
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("config root must be a mapping")
	}
	for _, alias := range []struct {
		old string
		new string
	}{
		{"server.host", "host"},
		{"server.port", "port"},
		{"server.max-request-body-bytes", "max-request-body-bytes"},
		{"server.trusted-proxies", "trusted-proxies"},
		{"server.github-token", "github-token"},
		{"management", "remote-management"},
		{"access.api-keys", "api-keys"},
	} {
		if value := yamlPathCompat(root, alias.old); value != nil {
			setYAMLPathCompat(root, alias.new, deepCopyYAMLCompat(value))
		}
	}
	return nil
}

func yamlPathCompat(root *yaml.Node, path string) *yaml.Node {
	node := root
	for _, part := range bytes.Split([]byte(path), []byte(".")) {
		if node == nil || node.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == string(part) {
				next = node.Content[i+1]
				break
			}
		}
		node = next
	}
	return node
}

func setYAMLPathCompat(root *yaml.Node, path string, value *yaml.Node) {
	parts := bytes.Split([]byte(path), []byte("."))
	node := root
	for _, part := range parts[:len(parts)-1] {
		key := string(part)
		var next *yaml.Node
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				next = node.Content[i+1]
				break
			}
		}
		if next == nil {
			next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, next)
		}
		node = next
	}
	key := string(parts[len(parts)-1])
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content[i+1] = value
			return
		}
	}
	node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

func deepCopyYAMLCompat(node *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	copy := *node
	copy.Content = make([]*yaml.Node, len(node.Content))
	for i, child := range node.Content {
		copy.Content[i] = deepCopyYAMLCompat(child)
	}
	return &copy
}
