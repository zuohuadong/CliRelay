package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestProxyZAIMCPForwardsThroughBigModelCodingAuthWithCodexFingerprint(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotPath string
	var gotQuery string
	var gotBody string
	var gotHeaders http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotHeaders = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "session-upstream")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	}))
	defer upstream.Close()

	originalURLs := zaiMCPServerURLs
	zaiMCPServerURLs = map[string]string{"web-search-prime": upstream.URL + "/api/mcp/web_search_prime/mcp"}
	t.Cleanup(func() { zaiMCPServerURLs = originalURLs })

	cfg := &config.Config{
		IdentityFingerprint: config.IdentityFingerprintConfig{
			Codex: config.CodexIdentityFingerprintConfig{
				Enabled:     true,
				UserAgent:   "codex-tui/test",
				Version:     "0.137.0",
				Originator:  "codex-tui",
				SessionMode: "fixed",
				SessionID:   "server-session",
			},
		},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor.NewBigModelCodingExecutor(cfg))
	_, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "zai-1",
		Provider: config.DefaultBigModelCodingProviderName,
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"api_key":              "sk-zai",
			"base_url":             config.DefaultBigModelCodingBaseURL,
			"identity_fingerprint": "codex",
		},
	})
	if err != nil {
		t.Fatalf("register auth: %v", err)
	}

	server := &Server{
		cfg:      cfg,
		handlers: handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager),
	}
	router := gin.New()
	router.POST("/mcp/zai/:server", server.proxyZAIMCP)

	req := httptest.NewRequest(http.MethodPost, "/mcp/zai/web-search-prime?cursor=1", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer downstream-key")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2025-06-18")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/api/mcp/web_search_prime/mcp" {
		t.Fatalf("upstream path = %q", gotPath)
	}
	if gotQuery != "cursor=1" {
		t.Fatalf("upstream query = %q", gotQuery)
	}
	if gotBody != `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` {
		t.Fatalf("upstream body = %q", gotBody)
	}
	if got := gotHeaders.Get("Authorization"); got != "Bearer sk-zai" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := gotHeaders.Get("User-Agent"); got != "codex-tui/test" {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := gotHeaders.Get("Version"); got != "0.137.0" {
		t.Fatalf("Version = %q", got)
	}
	if got := gotHeaders.Get("Originator"); got != "codex-tui" {
		t.Fatalf("Originator = %q", got)
	}
	if got := gotHeaders.Get("Session_id"); got != "server-session" {
		t.Fatalf("Session_id = %q", got)
	}
	if got := gotHeaders.Get("Mcp-Protocol-Version"); got != "2025-06-18" {
		t.Fatalf("Mcp-Protocol-Version = %q", got)
	}
	if got := rec.Header().Get("Mcp-Session-Id"); got != "session-upstream" {
		t.Fatalf("response Mcp-Session-Id = %q", got)
	}
}

func TestProxyZAIMCPRejectsUnsupportedServer(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	server := &Server{cfg: cfg, handlers: handlers.NewBaseAPIHandlers(&cfg.SDKConfig, coreauth.NewManager(nil, nil, nil))}
	router := gin.New()
	router.POST("/mcp/zai/:server", server.proxyZAIMCP)

	req := httptest.NewRequest(http.MethodPost, "/mcp/zai/vision", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestProxyZAIMCPAppliesDefaultCodexFingerprintWhenConfigDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotHeaders http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	}))
	defer upstream.Close()

	originalURLs := zaiMCPServerURLs
	zaiMCPServerURLs = map[string]string{"zread": upstream.URL + "/api/mcp/zread/mcp"}
	t.Cleanup(func() { zaiMCPServerURLs = originalURLs })

	cfg := &config.Config{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor.NewBigModelCodingExecutor(cfg))
	_, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "zai-1",
		Provider: config.DefaultBigModelCodingProviderName,
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"api_key":  "sk-zai",
			"base_url": config.DefaultBigModelCodingBaseURL,
		},
	})
	if err != nil {
		t.Fatalf("register auth: %v", err)
	}

	server := &Server{cfg: cfg, handlers: handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)}
	router := gin.New()
	router.POST("/mcp/zai/:server", server.proxyZAIMCP)

	req := httptest.NewRequest(http.MethodPost, "/mcp/zai/zread", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := gotHeaders.Get("User-Agent"); got != config.DefaultCodexFingerprintUserAgent {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := gotHeaders.Get("Version"); got != config.DefaultCodexFingerprintVersion {
		t.Fatalf("Version = %q", got)
	}
	if got := gotHeaders.Get("Originator"); got != config.DefaultCodexFingerprintOriginator {
		t.Fatalf("Originator = %q", got)
	}
	if got := gotHeaders.Get("Session_id"); got == "" {
		t.Fatal("Session_id is empty")
	}
}

func TestProxyZAIMCPStandaloneRouteRequiresClientAuthAndForwardsMCP(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotHeaders http.Header
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "standalone-session")
		if strings.Contains(gotBody, `"method":"tools/call"`) {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"ok"}],"isError":false}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"web_search_prime","inputSchema":{"type":"object","properties":{"search_query":{"type":"string"},"search_result_count":{"type":"integer"}}}}]}}`))
	}))
	defer upstream.Close()

	originalURLs := zaiMCPServerURLs
	zaiMCPServerURLs = map[string]string{"web-search-prime": upstream.URL + "/api/mcp/web_search_prime/mcp"}
	t.Cleanup(func() { zaiMCPServerURLs = originalURLs })

	cfg := newMCPProxyRouteTestConfig(t)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(executor.NewBigModelCodingExecutor(cfg))
	registerTestAuth(t, manager, &coreauth.Auth{
		ID:       "zai-standalone-route",
		Provider: config.DefaultBigModelCodingProviderName,
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"api_key":      "sk-zai-standalone",
			"base_url":     config.DefaultBigModelCodingBaseURL,
			"provider_key": config.DefaultBigModelCodingProviderName,
			"compat_name":  config.DefaultBigModelCodingProviderName,
		},
	})

	server := newMCPProxyRouteTestServer(t, cfg, manager)

	unauthorizedReq := httptest.NewRequest(http.MethodPost, "/mcp/zai/web-search-prime", strings.NewReader(`{}`))
	unauthorizedRec := httptest.NewRecorder()
	server.engine.ServeHTTP(unauthorizedRec, unauthorizedReq)
	if unauthorizedRec.Code != http.StatusUnauthorized {
		t.Fatalf("missing client auth status = %d, body=%s", unauthorizedRec.Code, unauthorizedRec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp/zai/web-search-prime", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Protocol-Version", "2025-06-18")
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Mcp-Session-Id"); got != "standalone-session" {
		t.Fatalf("response Mcp-Session-Id = %q", got)
	}
	if got := gotHeaders.Get("Authorization"); got != "Bearer sk-zai-standalone" {
		t.Fatalf("upstream Authorization = %q", got)
	}
	if got := gotHeaders.Get("User-Agent"); got != config.DefaultCodexFingerprintUserAgent {
		t.Fatalf("upstream User-Agent = %q", got)
	}
	if got := gotHeaders.Get("Mcp-Protocol-Version"); got != "2025-06-18" {
		t.Fatalf("upstream Mcp-Protocol-Version = %q", got)
	}
	if gotBody != `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` {
		t.Fatalf("upstream body = %q", gotBody)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"web_search_prime"`) || !strings.Contains(body, `"search_query"`) {
		t.Fatalf("response missing MCP tool name: %s", body)
	}

	callBody := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"web_search_prime","arguments":{"search_query":"OpenAI Codex MCP test","search_result_count":1}}}`
	callReq := httptest.NewRequest(http.MethodPost, "/mcp/zai/web-search-prime", strings.NewReader(callBody))
	callReq.Header.Set("Authorization", "Bearer test-key")
	callReq.Header.Set("Content-Type", "application/json")
	callRec := httptest.NewRecorder()
	server.engine.ServeHTTP(callRec, callReq)

	if callRec.Code != http.StatusOK {
		t.Fatalf("call status = %d, body=%s", callRec.Code, callRec.Body.String())
	}
	if gotBody != callBody {
		t.Fatalf("upstream call body = %q", gotBody)
	}
	if !strings.Contains(callRec.Body.String(), `"isError":false`) {
		t.Fatalf("call response missing successful MCP result: %s", callRec.Body.String())
	}
}

func TestAstronModelRequestAndZAIMCPRouteUseSeparateProviders(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var astronPath string
	var astronAuth string
	var astronBody string
	astronUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		astronPath = r.URL.Path
		astronAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		astronBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","model":"astron-code-latest","choices":[{"index":0,"message":{"role":"assistant","content":"astron ok"},"finish_reason":"stop"}]}`))
	}))
	defer astronUpstream.Close()

	var mcpAuth string
	var mcpUserAgent string
	var mcpBody string
	zaiMCPUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mcpAuth = r.Header.Get("Authorization")
		mcpUserAgent = r.Header.Get("User-Agent")
		body, _ := io.ReadAll(r.Body)
		mcpBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"web_reader"}]}}`))
	}))
	defer zaiMCPUpstream.Close()

	originalURLs := zaiMCPServerURLs
	zaiMCPServerURLs = map[string]string{"web-reader": zaiMCPUpstream.URL + "/api/mcp/web_reader/mcp"}
	t.Cleanup(func() { zaiMCPServerURLs = originalURLs })

	cfg := newMCPProxyRouteTestConfig(t)
	cfg.AstronCodeAPIKey = []config.OpenAICompatibility{{
		Name:    config.DefaultAstronCodeProviderName,
		BaseURL: astronUpstream.URL + "/v2",
		APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
			APIKey: "sk-astron",
		}},
		Models: []config.OpenAICompatibilityModel{{
			Name:  config.DefaultAstronCodeModel,
			Alias: config.DefaultAstronCodeAlias,
		}},
	}}
	cfg.BigModelCodingAPIKey = []config.OpenAICompatibility{{
		Name:    config.DefaultBigModelCodingProviderName,
		BaseURL: config.DefaultBigModelCodingBaseURL,
		APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
			APIKey: "sk-zai",
		}},
		Models: []config.OpenAICompatibilityModel{{
			Name:  config.DefaultBigModelCodingModel,
			Alias: config.DefaultBigModelCodingAlias,
		}},
	}}
	cfg.SanitizeAstronCode()
	cfg.SanitizeBigModelCoding()

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(executor.NewAstronCodeExecutor(cfg))
	manager.RegisterExecutor(executor.NewBigModelCodingExecutor(cfg))
	registerTestAuth(t, manager, &coreauth.Auth{
		ID:       "astron-route-model",
		Provider: config.DefaultAstronCodeProviderName,
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"api_key":              "sk-astron",
			"base_url":             astronUpstream.URL + "/v2",
			"provider_key":         config.DefaultAstronCodeProviderName,
			"compat_name":          config.DefaultAstronCodeProviderName,
			"identity_fingerprint": "codex",
		},
	})
	registerTestAuth(t, manager, &coreauth.Auth{
		ID:       "zai-route-mcp",
		Provider: config.DefaultBigModelCodingProviderName,
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"api_key":              "sk-zai",
			"base_url":             config.DefaultBigModelCodingBaseURL,
			"provider_key":         config.DefaultBigModelCodingProviderName,
			"compat_name":          config.DefaultBigModelCodingProviderName,
			"identity_fingerprint": "codex",
		},
	})
	manager.SetConfig(cfg)

	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient("astron-route-model", config.DefaultAstronCodeProviderName, []*registry.ModelInfo{{
		ID:      config.DefaultAstronCodeAlias,
		Object:  "model",
		OwnedBy: config.DefaultAstronCodeProviderName,
		Type:    config.DefaultAstronCodeProviderName,
	}})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient("astron-route-model")
	})

	server := newMCPProxyRouteTestServer(t, cfg, manager)

	chatReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.3-codex","messages":[{"role":"user","content":"ping"}],"stream":false}`))
	chatReq.Header.Set("Authorization", "Bearer test-key")
	chatReq.Header.Set("Content-Type", "application/json")
	chatRec := httptest.NewRecorder()
	server.engine.ServeHTTP(chatRec, chatReq)

	if chatRec.Code != http.StatusOK {
		t.Fatalf("chat status = %d, body=%s", chatRec.Code, chatRec.Body.String())
	}
	if astronPath != "/v2/chat/completions" {
		t.Fatalf("astron path = %q", astronPath)
	}
	if astronAuth != "Bearer sk-astron" {
		t.Fatalf("astron Authorization = %q", astronAuth)
	}
	if !strings.Contains(astronBody, `"model":"astron-code-latest"`) {
		t.Fatalf("astron body did not use upstream model: %s", astronBody)
	}
	if !strings.Contains(chatRec.Body.String(), "astron ok") {
		t.Fatalf("chat response missing astron content: %s", chatRec.Body.String())
	}

	mcpReq := httptest.NewRequest(http.MethodPost, "/mcp/zai/web-reader", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	mcpReq.Header.Set("Authorization", "Bearer test-key")
	mcpReq.Header.Set("Content-Type", "application/json")
	mcpRec := httptest.NewRecorder()
	server.engine.ServeHTTP(mcpRec, mcpReq)

	if mcpRec.Code != http.StatusOK {
		t.Fatalf("mcp status = %d, body=%s", mcpRec.Code, mcpRec.Body.String())
	}
	if mcpAuth != "Bearer sk-zai" {
		t.Fatalf("mcp Authorization = %q", mcpAuth)
	}
	if mcpUserAgent == "" {
		t.Fatal("mcp User-Agent is empty")
	}
	if mcpBody != `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` {
		t.Fatalf("mcp body = %q", mcpBody)
	}
	if !strings.Contains(mcpRec.Body.String(), `"web_reader"`) {
		t.Fatalf("mcp response missing Z.AI tool name: %s", mcpRec.Body.String())
	}
}

func TestProxyConfiguredMCPStandaloneRouteRequiresClientAuthAndForwardsMCP(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotPath string
	var gotQuery string
	var gotBody string
	var gotHeaders http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotHeaders = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "devspace-session")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"open_workspace"}]}}`))
	}))
	defer upstream.Close()

	cfg := newMCPProxyRouteTestConfig(t)
	cfg.MCPProxy.Servers = []config.MCPProxyServerConfig{{
		Name:    "devspace",
		BaseURL: upstream.URL + "/mcp",
		Headers: map[string]string{
			"Authorization": "Bearer upstream-devspace-token",
			"X-Devspace":    "enabled",
		},
	}}
	manager := coreauth.NewManager(nil, nil, nil)
	server := newMCPProxyRouteTestServer(t, cfg, manager)

	unauthorizedReq := httptest.NewRequest(http.MethodPost, "/mcp/custom/devspace", strings.NewReader(`{}`))
	unauthorizedRec := httptest.NewRecorder()
	server.engine.ServeHTTP(unauthorizedRec, unauthorizedReq)
	if unauthorizedRec.Code != http.StatusUnauthorized {
		t.Fatalf("missing client auth status = %d, body=%s", unauthorizedRec.Code, unauthorizedRec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp/custom/devspace/sessions?cursor=1", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer downstream-client-key")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Protocol-Version", "2025-06-18")
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/mcp/sessions" {
		t.Fatalf("upstream path = %q", gotPath)
	}
	if gotQuery != "cursor=1" {
		t.Fatalf("upstream query = %q", gotQuery)
	}
	if gotBody != `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` {
		t.Fatalf("upstream body = %q", gotBody)
	}
	if got := gotHeaders.Get("Authorization"); got != "Bearer upstream-devspace-token" {
		t.Fatalf("upstream Authorization = %q", got)
	}
	if got := gotHeaders.Get("X-Devspace"); got != "enabled" {
		t.Fatalf("upstream X-Devspace = %q", got)
	}
	if got := gotHeaders.Get("Mcp-Protocol-Version"); got != "2025-06-18" {
		t.Fatalf("upstream Mcp-Protocol-Version = %q", got)
	}
	if got := rec.Header().Get("Mcp-Session-Id"); got != "devspace-session" {
		t.Fatalf("response Mcp-Session-Id = %q", got)
	}
	if !strings.Contains(rec.Body.String(), `"open_workspace"`) {
		t.Fatalf("response missing MCP tool name: %s", rec.Body.String())
	}
}

func TestProxyConfiguredMCPRejectsUnsupportedServer(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := newMCPProxyRouteTestConfig(t)
	cfg.MCPProxy.Servers = []config.MCPProxyServerConfig{{
		Name:     "disabled-devspace",
		BaseURL:  "http://127.0.0.1:7676/mcp",
		Disabled: true,
	}}
	server := newMCPProxyRouteTestServer(t, cfg, nil)

	req := httptest.NewRequest(http.MethodPost, "/mcp/custom/devspace", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func newMCPProxyRouteTestConfig(t *testing.T) *config.Config {
	t.Helper()

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("create auth dir: %v", err)
	}
	return &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"test-key"},
		},
		Port:                   0,
		AuthDir:                authDir,
		Debug:                  true,
		LoggingToFile:          false,
		UsageStatisticsEnabled: false,
	}
}

func newMCPProxyRouteTestServer(t *testing.T, cfg *config.Config, manager *coreauth.Manager) *Server {
	t.Helper()

	if cfg == nil {
		cfg = newMCPProxyRouteTestConfig(t)
	}
	if manager == nil {
		manager = coreauth.NewManager(nil, nil, nil)
	}
	manager.SetConfig(cfg)
	accessManager := sdkaccess.NewManager()
	return NewServer(cfg, manager, accessManager, filepath.Join(t.TempDir(), "config.yaml"))
}

func registerTestAuth(t *testing.T, manager *coreauth.Manager, auth *coreauth.Auth) {
	t.Helper()

	if manager == nil {
		t.Fatal("auth manager is nil")
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		encoded, _ := json.Marshal(auth)
		t.Fatalf("register auth %s: %v", encoded, err)
	}
}
