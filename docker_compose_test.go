package main

import (
	"os"
	"strings"
	"testing"
)

func composeForbiddenTerm(parts ...string) string {
	return strings.Join(parts, "")
}

func TestRepositoryComposeUsesProjectDirForDefaultDataMounts(t *testing.T) {
	data, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatalf("read docker-compose.yml: %v", err)
	}
	content := string(data)

	for _, want := range []string{
		"${CLI_PROXY_CONFIG_PATH:-${CLIRELAY_PROJECT_DIR:-${PWD:-.}}/config.yaml}:/CLIProxyAPI/config.yaml",
		"${CLI_PROXY_AUTH_PATH:-${CLIRELAY_PROJECT_DIR:-${PWD:-.}}/auths}:${AUTH_PATH:-/root/.cli-proxy-api}",
		"${CLI_PROXY_LOG_PATH:-${CLIRELAY_PROJECT_DIR:-${PWD:-.}}/logs}:/CLIProxyAPI/logs",
		"${CLI_PROXY_DATA_PATH:-${CLIRELAY_PROJECT_DIR:-${PWD:-.}}/data}:/CLIProxyAPI/data",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("docker-compose.yml missing %q", want)
		}
	}
}

func TestRepositoryComposePassesContainerAuthPath(t *testing.T) {
	data, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatalf("read docker-compose.yml: %v", err)
	}
	content := string(data)

	want := "AUTH_PATH: ${AUTH_PATH:-/root/.cli-proxy-api}"
	if !strings.Contains(content, want) {
		t.Fatalf("docker-compose.yml missing %q", want)
	}
}

func TestRepositoryComposeDoesNotIncludeUpdaterSidecar(t *testing.T) {
	data, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatalf("read docker-compose.yml: %v", err)
	}
	content := string(data)

	for _, forbidden := range []string{
		composeForbiddenTerm("clirelay", "-updater"),
		composeForbiddenTerm("CLIRELAY", "_UPDATER"),
		"CLIRELAY_COMPOSE_FILE",
		"CLIRELAY_ENV_FILE",
		"CLIRELAY_TARGET_SERVICE",
		composeForbiddenTerm("CLIRELAY", "_UPDATE", "_CHANNEL"),
		"/workspace/docker-compose.yml",
		"/workspace/.env",
	} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("docker-compose.yml still contains removed sidecar config %q", forbidden)
		}
	}
}

func TestRepositoryComposeDoesNotBindUpdaterEnvFile(t *testing.T) {
	data, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatalf("read docker-compose.yml: %v", err)
	}
	content := string(data)

	for _, forbidden := range []string{
		"./.env:${CLIRELAY_PROJECT_DIR:-${PWD:-.}}/.env",
	} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("docker-compose.yml should not default updater to missing .env bind %q", forbidden)
		}
	}
}
