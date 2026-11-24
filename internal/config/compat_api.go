package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

var (
	configTopLevelKeysOnce  sync.Once
	configTopLevelKeysValue map[string]struct{}
)

func configTopLevelKeys() map[string]struct{} {
	configTopLevelKeysOnce.Do(func() {
		keys := make(map[string]struct{})
		var visit func(reflect.Type)
		visit = func(typ reflect.Type) {
			if typ.Kind() == reflect.Pointer {
				typ = typ.Elem()
			}
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				tag := strings.Split(field.Tag.Get("yaml"), ",")[0]
				if tag == "-" {
					continue
				}
				if field.Anonymous || tag == "" || tag == ",inline" {
					if field.Type.Kind() == reflect.Struct {
						visit(field.Type)
					}
					continue
				}
				keys[tag] = struct{}{}
			}
		}
		visit(reflect.TypeOf(Config{}))
		configTopLevelKeysValue = keys
	})
	return configTopLevelKeysValue
}

// ComputeSourceHash returns the stable ETag digest used by management config
// endpoints.
func ComputeSourceHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ParseConfigBytes parses a configuration document without touching disk.
func ParseConfigBytes(data []byte) (*Config, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	if err := NormalizeV8ConfigAliases(&document); err != nil {
		return nil, err
	}
	var normalized bytes.Buffer
	encoder := yaml.NewEncoder(&normalized)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		_ = encoder.Close()
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(normalized.Bytes(), &cfg); err != nil {
		return nil, err
	}
	cfg.SanitizeOpenAICompatibility()
	cfg.SanitizeOAuthModelAlias()
	return &cfg, nil
}

// NormalizeConfigLayout applies v8 aliases and returns the normalized YAML.
func NormalizeConfigLayout(data []byte, migrate bool) ([]byte, bool, error) {
	if !migrate {
		return data, false, nil
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, false, err
	}
	if err := NormalizeV8ConfigAliases(&root); err != nil {
		return nil, false, err
	}
	rootNode := &root
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		rootNode = root.Content[0]
	}
	if rootNode.Kind == yaml.MappingNode {
		known := configTopLevelKeys()
		for i := 0; i+1 < len(rootNode.Content); i += 2 {
			key := rootNode.Content[i].Value
			if _, ok := known[key]; !ok {
				warnV8Migration(key, fmt.Sprintf("unknown configuration section %q was preserved during migration", key))
			}
		}
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		_ = enc.Close()
		return nil, false, err
	}
	if err := enc.Close(); err != nil {
		return nil, false, err
	}
	normalized := out.Bytes()
	return normalized, !bytes.Equal(normalized, data), nil
}

func NormalizeCollectionNodeStyles(_ *yaml.Node) {}

func ValidateV8Config(data []byte) error {
	_, err := ParseConfigBytes(data)
	return err
}
