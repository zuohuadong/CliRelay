package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func testRoutingConfig() *config.Config {
	return &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeyEntries: []config.APIKeyEntry{
				{
					Key:                  "sk-team-a",
					Name:                 "Team A",
					AllowedChannelGroups: []string{"team-a"},
					AllowedChannels:      []string{"Team A Codex"},
				},
			},
		},
		CodexKey: []config.CodexKey{
			{APIKey: "sk-pro", Name: "Pro Codex", Prefix: "pro"},
			{APIKey: "sk-team-a", Name: "Team A Codex", Prefix: "pro"},
			{APIKey: "sk-default", Name: "Default Codex"},
		},
		Routing: config.RoutingConfig{
			IncludeDefaultGroup: true,
			ChannelGroups: []config.RoutingChannelGroup{
				{
					Name:        "pro",
					Description: "Pro channels",
					Priority:    100,
					Match: config.ChannelGroupMatch{
						Prefixes: []string{"pro"},
					},
				},
				{
					Name:        "team-a",
					Description: "Team A channels",
					Priority:    50,
					Match: config.ChannelGroupMatch{
						Channels: []string{"Team A Codex"},
					},
				},
			},
			PathRoutes: []config.RoutingPathRoute{
				{Path: "/pro", Group: "pro", StripPrefix: true},
				{Path: "/team-a", Group: "team-a", StripPrefix: true},
			},
		},
	}
}

func TestBuildChannelGroupItemsIncludesExplicitImplicitAndRoutes(t *testing.T) {
	items := buildChannelGroupItems(testRoutingConfig(), nil)
	if len(items) < 3 {
		t.Fatalf("expected at least 3 groups, got %d", len(items))
	}

	byName := make(map[string]channelGroupItem, len(items))
	for _, item := range items {
		byName[item.Name] = item
	}

	pro, ok := byName["pro"]
	if !ok {
		t.Fatal("expected pro group")
	}
	if pro.Implicit {
		t.Fatal("expected pro group to be explicit")
	}
	if pro.Priority != 100 {
		t.Fatalf("pro priority = %d, want 100", pro.Priority)
	}
	if strings.Join(pro.PathRoutes, ",") != "/pro" {
		t.Fatalf("pro path-routes = %v, want [/pro]", pro.PathRoutes)
	}
	if !containsString(pro.Channels, "Pro Codex") || !containsString(pro.Channels, "Team A Codex") {
		t.Fatalf("pro channels = %v, want both prefixed channels", pro.Channels)
	}

	teamA, ok := byName["team-a"]
	if !ok {
		t.Fatal("expected team-a group")
	}
	if teamA.Implicit {
		t.Fatal("expected team-a group to be explicit")
	}
	if strings.Join(teamA.PathRoutes, ",") != "/team-a" {
		t.Fatalf("team-a path-routes = %v, want [/team-a]", teamA.PathRoutes)
	}
	if !containsString(teamA.Channels, "Team A Codex") {
		t.Fatalf("team-a channels = %v, want Team A Codex", teamA.Channels)
	}

	defaultGroup, ok := byName["default"]
	if !ok {
		t.Fatal("expected default group")
	}
	if !defaultGroup.Implicit {
		t.Fatal("expected default group to be implicit")
	}
	if !containsString(defaultGroup.Channels, "Default Codex") {
		t.Fatalf("default channels = %v, want Default Codex", defaultGroup.Channels)
	}
}

func TestValidateRoutingAndAPIKeyRestrictions(t *testing.T) {
	t.Run("accepts valid config", func(t *testing.T) {
		if err := validateRoutingAndAPIKeyRestrictions(testRoutingConfig(), nil); err != nil {
			t.Fatalf("unexpected validation error: %v", err)
		}
	})

	testCases := []struct {
		name        string
		mutate      func(*config.Config)
		wantMessage string
	}{
		{
			name: "duplicate group names are rejected",
			mutate: func(cfg *config.Config) {
				cfg.Routing.ChannelGroups = append(cfg.Routing.ChannelGroups, config.RoutingChannelGroup{
					Name:  "PRO",
					Match: config.ChannelGroupMatch{Prefixes: []string{"pro"}},
				})
			},
			wantMessage: `duplicate channel group "PRO"`,
		},
		{
			name: "group must match known channel",
			mutate: func(cfg *config.Config) {
				cfg.Routing.ChannelGroups = append(cfg.Routing.ChannelGroups, config.RoutingChannelGroup{
					Name:  "ghost",
					Match: config.ChannelGroupMatch{Prefixes: []string{"ghost"}},
				})
			},
			wantMessage: `channel group "ghost" does not match any known channel`,
		},
		{
			name: "duplicate path routes are rejected",
			mutate: func(cfg *config.Config) {
				cfg.Routing.PathRoutes = append(cfg.Routing.PathRoutes, config.RoutingPathRoute{
					Path:  "/pro",
					Group: "team-a",
				})
			},
			wantMessage: `duplicate path route "/pro"`,
		},
		{
			name: "reserved path routes are rejected",
			mutate: func(cfg *config.Config) {
				cfg.Routing.PathRoutes = append(cfg.Routing.PathRoutes, config.RoutingPathRoute{
					Path:  "/v1",
					Group: "pro",
				})
			},
			wantMessage: `path route "/v1" conflicts with reserved internal path`,
		},
		{
			name: "path route group must exist",
			mutate: func(cfg *config.Config) {
				cfg.Routing.PathRoutes = append(cfg.Routing.PathRoutes, config.RoutingPathRoute{
					Path:  "/free",
					Group: "free",
				})
			},
			wantMessage: `path route "/free" references unknown channel group "free"`,
		},
		{
			name: "api key groups must exist",
			mutate: func(cfg *config.Config) {
				cfg.APIKeyEntries[0].AllowedChannelGroups = []string{"missing"}
			},
			wantMessage: `api-key "Team A" references unknown channel group "missing"`,
		},
		{
			name: "api key channel and group restrictions must intersect",
			mutate: func(cfg *config.Config) {
				cfg.APIKeyEntries[0].AllowedChannels = []string{"Pro Codex"}
			},
			wantMessage: `api-key "Team A" allowed-channels do not belong to allowed-channel-groups`,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cfg := testRoutingConfig()
			tc.mutate(cfg)

			err := validateRoutingAndAPIKeyRestrictions(cfg, nil)
			if err == nil {
				t.Fatalf("expected validation error containing %q", tc.wantMessage)
			}
			if !strings.Contains(err.Error(), tc.wantMessage) {
				t.Fatalf("validation error = %q, want substring %q", err.Error(), tc.wantMessage)
			}
		})
	}
}

func TestGetChannelGroupsReturnsGroupMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/channel-groups", nil)

	h := NewHandler(testRoutingConfig(), "", nil)
	h.GetChannelGroups(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body struct {
		Items []channelGroupItem `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(body.Items) < 3 {
		t.Fatalf("expected at least 3 group items, got %d", len(body.Items))
	}
}

func TestPutConfigYAMLRejectsInvalidRoutingRestrictions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := strings.NewReader(`
codex-api-key:
  - api-key: "sk-pro"
    name: "Pro Codex"
    prefix: "pro"
routing:
  include-default-group: true
  channel-groups:
    - name: "pro"
      match:
        prefixes: ["pro"]
  path-routes:
    - path: "/v1"
      group: "pro"
`)
	c.Request = httptest.NewRequest(http.MethodPut, "/config.yaml", body)

	h := NewHandler(&config.Config{}, configPath, nil)
	h.PutConfigYAML(c)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusUnprocessableEntity, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "reserved internal path") {
		t.Fatalf("expected reserved path validation error, got %s", rec.Body.String())
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
