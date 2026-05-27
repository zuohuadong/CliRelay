package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internalrouting "github.com/router-for-me/CLIProxyAPI/v7/internal/routing"
)

func TestPanelRoutingConfigRoundTripChannelGroups(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("routing:\n  strategy: round-robin\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: configPath,
	}

	body := `{
		"strategy":"fill-first",
		"include-default-group":true,
		"channel-groups":[{
			"name":"team-a",
			"description":"Team channels",
			"strategy":"round-robin",
			"match":{"channels":["Alpha"],"tags":["Pro Plan"]},
			"channel-priorities":{"Alpha":10},
			"allowed-models":["gpt-5"]
		}],
		"path-routes":[{"path":"/team-a","group":"team-a","strip-prefix":true,"fallback":"none"}]
	}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPut, "/v0/management/routing-config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PutRoutingConfig(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := h.cfg.Routing.Strategy; got != "fill-first" {
		t.Fatalf("strategy = %q, want fill-first", got)
	}
	if len(h.cfg.Routing.ChannelGroups) != 1 {
		t.Fatalf("channel groups = %#v, want one", h.cfg.Routing.ChannelGroups)
	}
	group := h.cfg.Routing.ChannelGroups[0]
	if group.Name != "team-a" {
		t.Fatalf("group name = %q, want team-a", group.Name)
	}
	if len(group.Match.Tags) != 1 || group.Match.Tags[0] != "pro-plan" {
		t.Fatalf("group match tags = %#v, want pro-plan", group.Match.Tags)
	}
	if got := group.ChannelPriorities["Alpha"]; got != 10 {
		t.Fatalf("channel priority = %d, want 10", got)
	}
	if len(h.cfg.Routing.PathRoutes) != 1 || h.cfg.Routing.PathRoutes[0].Path != "/team-a" {
		t.Fatalf("path routes = %#v, want /team-a", h.cfg.Routing.PathRoutes)
	}

	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/routing-config", nil)
	h.GetRoutingConfig(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"channel-groups"`) ||
		!strings.Contains(rec.Body.String(), `"tags":["pro-plan"]`) {
		t.Fatalf("routing config response missing channel group tags: %s", rec.Body.String())
	}
}

func TestPanelRoutingConfigRejectsReservedPathRoute(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: filepath.Join(t.TempDir(), "config.yaml"),
	}
	body := `{"channel-groups":[{"name":"team","match":{"channels":["Alpha"]}}],"path-routes":[{"path":"/v1","group":"team"}]}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPut, "/v0/management/routing-config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PutRoutingConfig(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), internalrouting.NormalizeNamespacePath("/team")) {
		t.Fatalf("unexpected unrelated route detail in response: %s", rec.Body.String())
	}
}
