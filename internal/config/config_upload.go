package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// PrepareConfigFileForUpload rewrites a local config before it is copied to a
// remote store. Plaintext management secrets are replaced with bcrypt hashes;
// ambiguous YAML layouts are rejected without modifying the file.
func PrepareConfigFileForUpload(configFile string) error {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}
	prepared, err := prepareConfigForUpload(data)
	if err != nil {
		return err
	}
	if bytes.Equal(prepared, data) {
		return nil
	}
	if err := os.WriteFile(configFile, prepared, 0o600); err != nil {
		return fmt.Errorf("failed to write prepared config file: %w", err)
	}
	return nil
}

func prepareConfigForUpload(data []byte) ([]byte, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) == 0 {
		return nil, errors.New("invalid yaml document structure")
	}

	root := expandUploadAliases(document.Content[0], nil)
	legacy := yamlPathUpload(root, "remote-management.secret-key")
	current := yamlPathUpload(root, "management.secret-key")
	effective := current
	if effective == nil {
		effective = legacy
	}

	plaintexts := make([]string, 0, 2)
	for _, node := range []*yaml.Node{legacy, current} {
		if node == nil || node.Kind != yaml.ScalarNode || node.Tag == "!!null" {
			continue
		}
		if node.Value != "" && !looksLikeBcrypt(node.Value) && !containsString(plaintexts, node.Value) {
			plaintexts = append(plaintexts, node.Value)
		}
	}

	if effective != nil && effective.Kind == yaml.ScalarNode && effective.Tag != "!!null" &&
		effective.Value != "" && !looksLikeBcrypt(effective.Value) {
		hashed, err := hashSecret(effective.Value)
		if err != nil {
			return nil, fmt.Errorf("failed to hash remote management key: %w", err)
		}
		path := "remote-management.secret-key"
		if current != nil {
			path = "management.secret-key"
		}
		updated, err := updateUploadScalar(document.Content[0], path, hashed)
		if err != nil {
			return nil, fmt.Errorf("failed to update remote management key: %w", err)
		}
		document.Content[0] = updated
	}

	// A v8 management section supersedes the legacy remote-management section.
	// Remove the legacy section only when it is a plain mapping; aliases and
	// shared anchors remain ambiguous and are rejected below.
	if yamlPathUpload(document.Content[0], "management.secret-key") != nil &&
		yamlPathUpload(document.Content[0], "remote-management.secret-key") != nil {
		removeUploadMapKey(document.Content[0], "remote-management")
	}

	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&document); err != nil {
		_ = enc.Close()
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	cleaned := NormalizeCommentIndentation(out.Bytes())
	for _, plaintext := range plaintexts {
		if yamlScalarUploadExists(cleaned, plaintext) {
			return nil, errors.New("plaintext remote management key is still present elsewhere in the config file")
		}
	}
	return cleaned, nil
}

func containsString(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func yamlPathUpload(root *yaml.Node, path string) *yaml.Node {
	node := root
	for _, part := range strings.Split(path, ".") {
		if node == nil || node.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == part {
				next = node.Content[i+1]
				break
			}
		}
		node = next
	}
	return node
}

func updateUploadScalar(root *yaml.Node, path, value string) (*yaml.Node, error) {
	parts := strings.Split(path, ".")
	node := root
	for i, part := range parts {
		if node.Kind != yaml.MappingNode {
			return nil, errors.New("invalid yaml mapping path")
		}
		var next *yaml.Node
		for j := 0; j+1 < len(node.Content); j += 2 {
			if node.Content[j].Value == part {
				next = node.Content[j+1]
				break
			}
		}
		if next == nil {
			if i == len(parts)-1 {
				node.Content = append(node.Content,
					&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: part},
					&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
				return root, nil
			}
			next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			node.Content = append(node.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: part}, next)
		}
		if i == len(parts)-1 {
			next.Kind = yaml.ScalarNode
			next.Tag = "!!str"
			next.Value = value
			next.Content = nil
			return root, nil
		}
		node = next
	}
	return root, nil
}

func removeUploadMapKey(root *yaml.Node, key string) {
	if root == nil || root.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			root.Content = append(root.Content[:i], root.Content[i+2:]...)
			return
		}
	}
}

func expandUploadAliases(node *yaml.Node, anchors map[string]*yaml.Node) *yaml.Node {
	if anchors == nil {
		anchors = make(map[string]*yaml.Node)
	}
	if node == nil {
		return nil
	}
	if node.Anchor != "" {
		anchors[node.Anchor] = node
	}
	if node.Kind == yaml.AliasNode && node.Alias != nil {
		return expandUploadAliases(cloneUploadNode(node.Alias), anchors)
	}
	copy := cloneUploadNode(node)
	for i, child := range copy.Content {
		copy.Content[i] = expandUploadAliases(child, anchors)
	}
	return copy
}

func cloneUploadNode(node *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	copy := *node
	copy.Content = append([]*yaml.Node(nil), node.Content...)
	return &copy
}

func yamlScalarUploadExists(data []byte, value string) bool {
	var document yaml.Node
	if yaml.Unmarshal(data, &document) != nil || len(document.Content) == 0 {
		return false
	}
	root := expandUploadAliases(document.Content[0], nil)
	return yamlScalarUploadExistsNode(root, value, false)
}

func yamlScalarUploadExistsNode(node *yaml.Node, value string, mappingKey bool) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.ScalarNode {
		return !mappingKey && node.Tag != "!!null" && node.Value == value
	}
	if node.Kind == yaml.MappingNode {
		for i, child := range node.Content {
			if yamlScalarUploadExistsNode(child, value, i%2 == 0) {
				return true
			}
		}
		return false
	}
	for _, child := range node.Content {
		if yamlScalarUploadExistsNode(child, value, false) {
			return true
		}
	}
	return false
}
