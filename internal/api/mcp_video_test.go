package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestVideoMCPInitializeAndToolsList(t *testing.T) {
	server := newTestServer(t)

	initReq := httptest.NewRequest(http.MethodPost, "/mcp/video", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
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
	if !strings.Contains(initRec.Body.String(), `"name":"clirelay-video"`) {
		t.Fatalf("initialize response missing server info: %s", initRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodPost, "/mcp/video", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	listReq.Header.Set("Authorization", "Bearer test-key")
	listReq.Header.Set("Content-Type", "application/json")
	listRec := httptest.NewRecorder()
	server.engine.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("tools/list status = %d, body=%s", listRec.Code, listRec.Body.String())
	}
	body := listRec.Body.String()
	for _, want := range []string{"clirelay_video_models", "clirelay_video_create", "clirelay_video_status", "clirelay_video_content_url"} {
		if !strings.Contains(body, want) {
			t.Fatalf("tools/list missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, "timeout_seconds") || strings.Contains(body, "poll_interval_seconds") || strings.Contains(body, `"wait"`) {
		t.Fatalf("tools/list must not expose server-side wait options: %s", body)
	}
}

func TestVideoMCPGETIsNotEmptySuccess(t *testing.T) {
	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/mcp/video", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /mcp/video status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestVideoMCPRejectsMissingPromptBeforeCreate(t *testing.T) {
	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/mcp/video", strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"clirelay_video_create","arguments":{}}}`))
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
	if got := errObj["message"]; got != "prompt is required" {
		t.Fatalf("error message = %v, body=%s", got, rec.Body.String())
	}
}

func TestVideoMCPRejectsDisallowedModelBeforeCreate(t *testing.T) {
	server := newTestServer(t)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("accessMetadata", map[string]string{"allowed-models": "other-video"})
	_, rpcErr := server.callVideoMCPTool(c, mcpGatewayToolCallParams{
		Name: "clirelay_video_create",
		Arguments: map[string]any{
			"prompt": "make a video",
			"model":  "agnes-video-v2.0",
		},
	})
	if rpcErr == nil || !strings.Contains(rpcErr.Message, "model not allowed") {
		t.Fatalf("rpcErr = %#v, want model not allowed", rpcErr)
	}
}

func TestVideoMCPContentURLUsesForwardedHost(t *testing.T) {
	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/mcp/video", strings.NewReader(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"clirelay_video_content_url","arguments":{"video_id":"video_123"}}}`))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "cliapi.029555.xyz")
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "https://cliapi.029555.xyz/v1/videos/video_123/content") {
		t.Fatalf("content url response = %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"structuredContent"`) {
		t.Fatalf("content url response missing Apps SDK structuredContent: %s", rec.Body.String())
	}
}
