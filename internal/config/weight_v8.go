package config

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/credentialweight"
	"gopkg.in/yaml.v3"
)

// validateWeightSequenceNode validates the weight fields in a v8 API-key
// collection while the YAML document is being normalized.
func validateWeightSequenceNode(sequence *yaml.Node, path string) error {
	if sequence == nil || sequence.Kind != yaml.SequenceNode {
		return nil
	}
	for index, item := range sequence.Content {
		if err := validateWeightMappingNode(item, fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func validateWeightMappingNode(mapping *yaml.Node, path string) error {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value != "weight" {
			continue
		}
		value := mapping.Content[index+1]
		if value.Kind != yaml.ScalarNode || value.Tag != "!!int" {
			return fmt.Errorf("%s.weight: weight must be an integer", path)
		}
		var weight int64
		if err := value.Decode(&weight); err != nil {
			return fmt.Errorf("%s.weight: weight must be an integer", path)
		}
		if _, err := credentialweight.Normalize(weight); err != nil {
			return fmt.Errorf("%s.weight: %w", path, err)
		}
	}
	return nil
}
