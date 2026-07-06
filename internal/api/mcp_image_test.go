package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func TestImageMCPInitializeAndToolsList(t *testing.T) {
	server := newTestServer(t)

	initReq := httptest.NewRequest(http.MethodPost, "/mcp/image", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
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
	if !strings.Contains(initRec.Body.String(), `"name":"clirelay-image"`) {
		t.Fatalf("initialize response missing server info: %s", initRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodPost, "/mcp/image", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	listReq.Header.Set("Authorization", "Bearer test-key")
	listReq.Header.Set("Content-Type", "application/json")
	listRec := httptest.NewRecorder()
	server.engine.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("tools/list status = %d, body=%s", listRec.Code, listRec.Body.String())
	}
	body := listRec.Body.String()
	for _, want := range []string{"clirelay_image_models", "clirelay_image_generate", "clirelay_image_edit"} {
		if !strings.Contains(body, want) {
			t.Fatalf("tools/list missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, "stream") || strings.Contains(body, "partial image events") {
		t.Fatalf("tools/list should keep streaming on direct image endpoints, not MCP tool args: %s", body)
	}
}

func TestImageMCPGETIsNotEmptySuccess(t *testing.T) {
	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/mcp/image", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /mcp/image status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestImageMCPRejectsMissingPromptBeforeGenerate(t *testing.T) {
	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/mcp/image", strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"clirelay_image_generate","arguments":{}}}`))
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

func TestImageMCPRejectsMissingImageBeforeEdit(t *testing.T) {
	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/mcp/image", strings.NewReader(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"clirelay_image_edit","arguments":{"prompt":"edit it"}}}`))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := gjson.GetBytes(rec.Body.Bytes(), "error.message").String(); got != "image_url or images is required" {
		t.Fatalf("error message = %q, body=%s", got, rec.Body.String())
	}
}

func TestImageMCPRejectsDisallowedModelBeforeGenerate(t *testing.T) {
	server := newTestServer(t)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("accessMetadata", map[string]string{"allowed-models": "other-image"})
	_, rpcErr := server.callImageMCPTool(c, mcpGatewayToolCallParams{
		Name: "clirelay_image_generate",
		Arguments: map[string]any{
			"prompt": "make an image",
			"model":  "gpt-image-2",
		},
	})
	if rpcErr == nil || !strings.Contains(rpcErr.Message, "model not allowed") {
		t.Fatalf("rpcErr = %#v, want model not allowed", rpcErr)
	}
}

func TestImageMCPModelListIncludesBuiltinImageModels(t *testing.T) {
	server := newTestServer(t)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	models := server.imageMCPModelList(c)
	raw, err := json.Marshal(models)
	if err != nil {
		t.Fatalf("marshal models: %v", err)
	}
	body := string(raw)
	for _, want := range []string{"gpt-image-1.5", "gpt-image-2", "grok-imagine-image", "grok-imagine-image-quality"} {
		if !strings.Contains(body, want) {
			t.Fatalf("model list missing %s: %s", want, body)
		}
	}
}

func TestBuildImageGeneratePayloadPreservesOptions(t *testing.T) {
	payload, rpcErr := buildImageGeneratePayload(map[string]any{
		"prompt":             "draw",
		"model":              "gpt-image-1.5",
		"size":               "1024x1024",
		"quality":            "high",
		"output_format":      "jpeg",
		"response_format":    "url",
		"n":                  float64(2),
		"output_compression": float64(70),
	})
	if rpcErr != nil {
		t.Fatalf("buildImageGeneratePayload rpcErr = %#v", rpcErr)
	}
	if payload["model"] != "gpt-image-1.5" || payload["prompt"] != "draw" || payload["n"] != 2 || payload["output_compression"] != 70 {
		t.Fatalf("unexpected payload = %#v", payload)
	}
}

func TestBuildImageEditPayloadNormalizesImages(t *testing.T) {
	payload, rpcErr := buildImageEditPayload(map[string]any{
		"prompt":         "edit",
		"image_url":      "data:image/png;base64,AA==",
		"mask_image_url": "data:image/png;base64,BB==",
	})
	if rpcErr != nil {
		t.Fatalf("buildImageEditPayload rpcErr = %#v", rpcErr)
	}
	images, ok := payload["images"].([]map[string]any)
	if !ok || len(images) != 1 || images[0]["image_url"] != "data:image/png;base64,AA==" {
		t.Fatalf("unexpected images payload = %#v", payload["images"])
	}
	mask, ok := payload["mask"].(map[string]any)
	if !ok || mask["image_url"] != "data:image/png;base64,BB==" {
		t.Fatalf("unexpected mask payload = %#v", payload["mask"])
	}
}
