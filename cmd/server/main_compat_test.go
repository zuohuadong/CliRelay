package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

func argvEnablesBoolFlag(args []string, flagName string) bool {
	flagName = strings.TrimLeft(flagName, "-")
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" || !strings.HasPrefix(arg, "-") {
			return false
		}
		if arg == "--"+flagName || arg == "-"+flagName {
			return true
		}
		if strings.HasPrefix(arg, "--"+flagName+"=") {
			return strings.TrimPrefix(arg, "--"+flagName+"=") != "false"
		}
		if arg == "--config" || arg == "-config" {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
		}
	}
	return false
}

func shouldEnableExampleAPIKeySafeMode(cfg *config.Config, commandMode, tuiMode, standalone, cloudConfigMissing, homeMode bool) bool {
	if cfg == nil || commandMode || homeMode || cloudConfigMissing {
		return false
	}
	if tuiMode && !standalone {
		return false
	}
	for _, key := range cfg.APIKeys {
		if strings.EqualFold(strings.TrimSpace(key), "your-api-key-1") {
			return true
		}
	}
	return false
}

func resolveManagementBaseURL(flagURL string, cfg *config.Config) string {
	if value := strings.TrimSpace(flagURL); value != "" {
		return value
	}
	if cfg != nil && strings.TrimSpace(cfg.RemoteManagement.BaseURL) != "" {
		return strings.TrimSpace(cfg.RemoteManagement.BaseURL)
	}
	port := 8317
	if cfg != nil && cfg.Port > 0 {
		port = cfg.Port
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

func persistSeededConfig(ctx context.Context, path string, persist func(context.Context) error) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// Seed templates must be plain, canonical YAML. Anchors and aliases can
	// hide management secrets in unrelated keys, and ParseConfigBytes resolves
	// them into the runtime struct without preserving their provenance. Refuse
	// such templates instead of silently rewriting a potentially unsafe copy.
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		_ = os.Remove(path)
		return err
	}
	var hasAliasOrAnchor func(*yaml.Node) bool
	hasAliasOrAnchor = func(node *yaml.Node) bool {
		if node == nil {
			return false
		}
		if node.Anchor != "" || node.Alias != nil || node.Kind == yaml.AliasNode {
			return true
		}
		for _, child := range node.Content {
			if hasAliasOrAnchor(child) {
				return true
			}
		}
		return false
	}
	if hasAliasOrAnchor(&doc) {
		_ = os.Remove(path)
		return fmt.Errorf("seeded config must not use YAML anchors or aliases")
	}
	cfg, err := config.ParseConfigBytes(raw)
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	secret := strings.TrimSpace(cfg.RemoteManagement.SecretKey)
	if secret == "" {
		_ = os.Remove(path)
		return fmt.Errorf("seeded config has no management secret")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	cfg.RemoteManagement.SecretKey = string(hash)
	cfg.RemoteManagement.BaseURL = strings.TrimSpace(cfg.RemoteManagement.BaseURL)
	encoded, err := yaml.Marshal(cfg)
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return err
	}
	if persist != nil {
		if err := persist(ctx); err != nil {
			_ = os.Remove(path)
			return err
		}
	}
	return nil
}
