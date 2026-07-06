package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestMCPGatewayInitializeAndToolsListExposeDirectoryToolsOnly(t *testing.T) {
	server := newTestServer(t)

	initReq := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	initReq.Header.Set("Authorization", "Bearer test-key")
	initReq.Header.Set("Content-Type", "application/json")
	initRec := httptest.NewRecorder()
	server.engine.ServeHTTP(initRec, initReq)

	if initRec.Code != http.StatusOK {
		t.Fatalf("initialize status = %d, body=%s", initRec.Code, initRec.Body.String())
	}
	if got := initRec.Header().Get("Mcp-Session-Id"); got == "" {
		t.Fatal("initialize response missing Mcp-Session-Id")
	}
	if !strings.Contains(initRec.Body.String(), `"name":"clirelay-mcp-directory"`) {
		t.Fatalf("initialize response missing server info: %s", initRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	listReq.Header.Set("Authorization", "Bearer test-key")
	listReq.Header.Set("Content-Type", "application/json")
	listRec := httptest.NewRecorder()
	server.engine.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("tools/list status = %d, body=%s", listRec.Code, listRec.Body.String())
	}
	body := listRec.Body.String()
	for _, want := range []string{"clirelay_mcp_routes", "clirelay_mcp_route_info"} {
		if !strings.Contains(body, want) {
			t.Fatalf("tools/list missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, "clirelay_video_create") {
		t.Fatalf("top-level /mcp must not expose concrete video tools: %s", body)
	}
}

func TestMCPGatewayRoutesListsZAIAndConfiguredCustomRoutes(t *testing.T) {
	server := newTestServer(t)
	server.cfg.MCPProxy.Servers = []config.MCPProxyServerConfig{
		{Name: "devspace", BaseURL: "http://127.0.0.1:7676/mcp"},
		{Name: "disabled", BaseURL: "http://127.0.0.1:7677/mcp", Disabled: true},
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"clirelay_mcp_routes","arguments":{}}}`))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "cliapi.029555.xyz")
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"https://cliapi.029555.xyz/mcp",
		"https://cliapi.029555.xyz/mcp/video",
		"https://cliapi.029555.xyz/mcp/zai/web-search-prime",
		"https://cliapi.029555.xyz/mcp/zai/web-reader",
		"https://cliapi.029555.xyz/mcp/custom/devspace",
		"clirelay_video_create",
		"web_search_prime",
		"web_reader",
		"Configure this route as a Streamable HTTP MCP server.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("routes response missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, "custom/disabled") {
		t.Fatalf("disabled custom route leaked into catalog: %s", body)
	}
}

func TestMCPGatewayGETReturnsCatalog(t *testing.T) {
	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /mcp status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, `"routes"`) || !strings.Contains(body, `/mcp/video`) {
		t.Fatalf("GET /mcp response missing route catalog: %s", body)
	}
}

func TestMCPGatewayRouteInfoRejectsUnknownConcreteTool(t *testing.T) {
	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"clirelay_video_create","arguments":{}}}`))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	errObj, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("response missing error: %s", rec.Body.String())
	}
	if got := errObj["message"]; !strings.Contains(got.(string), "unknown tool") {
		t.Fatalf("error message = %v, body=%s", got, rec.Body.String())
	}
}
