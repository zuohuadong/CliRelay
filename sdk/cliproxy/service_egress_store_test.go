package cliproxy

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestBuilderUsesDedicatedLockedEgressDatabase(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &config.Config{}
	cfg.EgressNetwork.Enabled = true
	service, err := NewBuilder().WithConfig(cfg).WithConfigPath(configPath).Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	t.Cleanup(func() { _ = service.egressService.Close() })
	t.Cleanup(func() { _ = service.videoService.Close() })

	egressPath := filepath.Join(filepath.Dir(configPath), "data", "egress.db")
	if _, err = os.Stat(egressPath); err != nil {
		t.Fatalf("egress database was not created at %s: %v", egressPath, err)
	}
	if _, err = os.Stat(filepath.Join(filepath.Dir(configPath), "data", "usage.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("builder unexpectedly created usage.db: %v", err)
	}
	if _, err = NewBuilder().WithConfig(cfg).WithConfigPath(configPath).Build(); !errors.Is(err, egress.ErrStoreLocked) {
		t.Fatalf("second Build() error = %v, want ErrStoreLocked", err)
	}
}

func TestBuilderWithoutEgressConfigurationDoesNotCreateOrLockEgressStore(t *testing.T) {
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.yaml")
	first, err := NewBuilder().WithConfig(&config.Config{}).WithConfigPath(configPath).Build()
	if err != nil {
		t.Fatalf("first Build() error = %v", err)
	}
	if first.egressService != nil {
		t.Fatal("unconfigured builder initialized egress service")
	}
	t.Cleanup(func() { _ = first.videoService.Close() })
	if _, err = os.Stat(filepath.Join(configDir, "data", "egress.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unconfigured builder created egress.db: %v", err)
	}
	if _, err = os.Stat(filepath.Join(configDir, "data", "egress.db.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unconfigured builder created egress lock: %v", err)
	}
	second, err := NewBuilder().WithConfig(&config.Config{}).WithConfigPath(configPath).Build()
	if err != nil {
		t.Fatalf("second Build() error = %v", err)
	}
	if second.egressService != nil {
		t.Fatal("second unconfigured builder initialized egress service")
	}
	t.Cleanup(func() { _ = second.videoService.Close() })
}

func TestBuilderDisabledEgressIgnoresHealthConfiguration(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &config.Config{}
	cfg.EgressNetwork.EndpointCheckInterval = "30s"
	cfg.EgressNetwork.EndpointHealthTTL = "1m"
	service, err := NewBuilder().WithConfig(cfg).WithConfigPath(configPath).Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if service.egressService != nil {
		t.Fatal("disabled egress initialized a store from health-only configuration")
	}
	t.Cleanup(func() { _ = service.videoService.Close() })
}
