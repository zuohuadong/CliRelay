package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// mcpTestContext is a gin context whose body is body and whose Accept header is
// accept, as an MCP client sends them.
func mcpTestContext(t *testing.T, body, accept string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/mcp/image", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	c.Request = req
	return c, recorder
}

// mcpRPC runs the JSON-RPC envelope for one request body and returns the decoded
// message the client would receive.
func mcpRPC(t *testing.T, handler gin.HandlerFunc, body, accept string) map[string]any {
	t.Helper()
	c, recorder := mcpTestContext(t, body, accept)
	handler(c)
	out := recorder.Body.Bytes()
	if strings.Contains(recorder.Header().Get("Content-Type"), "text/event-stream") {
		text := string(out)
		index := strings.Index(text, "data: ")
		if index < 0 {
			t.Fatalf("SSE body without a data line: %q", text)
		}
		out = []byte(strings.TrimSpace(strings.TrimPrefix(text[index+len("data: "):], "")))
	}
	var message map[string]any
	if err := json.Unmarshal(out, &message); err != nil {
		t.Fatalf("decode %q: %v", string(out), err)
	}
	return message
}

func TestMCPImageInitializeAdvertisesTools(t *testing.T) {
	server := &Server{}
	handler := server.mcpImageHandler()

	message := mcpRPC(t, handler, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"node","version":"20"}}}`, "")
	if message["jsonrpc"] != "2.0" || message["id"] != float64(1) {
		t.Fatalf("envelope = %v, want jsonrpc 2.0 and id 1", message)
	}
	result, _ := message["result"].(map[string]any)
	if result["protocolVersion"] != "2025-06-18" {
		t.Fatalf("protocolVersion = %v, want the client's", result["protocolVersion"])
	}
	capabilities, _ := result["capabilities"].(map[string]any)
	if _, ok := capabilities["tools"]; !ok {
		t.Fatalf("capabilities = %v, want tools", capabilities)
	}

	// A client that names no version still gets a usable one.
	message = mcpRPC(t, handler, `{"jsonrpc":"2.0","id":"a","method":"initialize"}`, "text/event-stream")
	result, _ = message["result"].(map[string]any)
	if result["protocolVersion"] != mcpProtocolVersion {
		t.Fatalf("protocolVersion = %v, want %s", result["protocolVersion"], mcpProtocolVersion)
	}
	if message["id"] != "a" {
		t.Fatalf("id = %v, want the string the client sent", message["id"])
	}
}

func TestMCPImageToolsListPublishesGenerateImage(t *testing.T) {
	handler := (&Server{}).mcpImageHandler()
	message := mcpRPC(t, handler, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, "")
	result, _ := message["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v, want generate_image only", tools)
	}
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != "generate_image" {
		t.Fatalf("tool name = %v", tool["name"])
	}
	schema, _ := tool["inputSchema"].(map[string]any)
	required, _ := schema["required"].([]any)
	if len(required) != 1 || required[0] != "prompt" {
		t.Fatalf("required = %v, want prompt", required)
	}
}

func TestMCPImageToolCallValidatesArguments(t *testing.T) {
	handler := (&Server{}).mcpImageHandler()

	cases := []struct {
		name string
		body string
		want string
	}{
		{"unknown tool", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"edit_image","arguments":{}}}`, "unknown tool"},
		{"missing prompt", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"generate_image","arguments":{}}}`, "prompt is required"},
		{"bad n", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"generate_image","arguments":{"prompt":"x","n":9}}}`, "n must be a number"},
		{"bad n type", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"generate_image","arguments":{"prompt":"x","n":"two"}}}`, "n must be a number"},
	}
	for _, tc := range cases {
		message := mcpRPC(t, handler, tc.body, "")
		result, ok := message["result"].(map[string]any)
		if !ok {
			t.Fatalf("%s: no result in %v", tc.name, message)
		}
		if result["isError"] != true {
			t.Fatalf("%s: isError = %v, want true", tc.name, result["isError"])
		}
		content, _ := result["content"].([]any)
		first, _ := content[0].(map[string]any)
		if text, _ := first["text"].(string); !strings.Contains(text, tc.want) {
			t.Fatalf("%s: text = %q, want it to mention %q", tc.name, text, tc.want)
		}
	}
}

func TestMCPImageUnknownMethodIsAProtocolError(t *testing.T) {
	handler := (&Server{}).mcpImageHandler()
	message := mcpRPC(t, handler, `{"jsonrpc":"2.0","id":7,"method":"resources/list"}`, "")
	rpcErr, _ := message["error"].(map[string]any)
	if rpcErr["code"] != float64(-32601) {
		t.Fatalf("error = %v, want -32601", message["error"])
	}
	if message["id"] != float64(7) {
		t.Fatalf("id = %v, want 7 back", message["id"])
	}
}

func TestMCPImageNotificationsCarryNoBody(t *testing.T) {
	c, recorder := mcpTestContext(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, "")
	(&Server{}).mcpImageHandler()(c)
	// gin defers the recorder's WriteHeader until something is written; the
	// status it would send is on the writer.
	if c.Writer.Status() != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", c.Writer.Status())
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("body = %q, want none", recorder.Body.String())
	}
}

// The images pipeline answers with base64 bytes; they come back as MCP image
// content blocks, with the mime type read from the bytes themselves.
func TestMCPImageContentFromImagesResponse(t *testing.T) {
	payload := []byte(`{"created":1,"data":[{"b64_json":"iVBORw0KGgoAAAANSUhEUg"},{"url":"https://example.invalid/a.png"}]}`)
	content := mcpImageContent(payload)
	if len(content) != 2 {
		t.Fatalf("content = %v, want the two images", content)
	}
	if content[0]["type"] != "image" || content[0]["mimeType"] != "image/png" {
		t.Fatalf("first block = %v, want image/png content", content[0])
	}
	if content[0]["data"] != "iVBORw0KGgoAAAANSUhEUg" {
		t.Fatalf("first block data = %v, want the bytes as they came", content[0]["data"])
	}
	if content[1]["type"] != "resource_link" || content[1]["uri"] != "https://example.invalid/a.png" {
		t.Fatalf("second block = %v, want a resource link", content[1])
	}
	if content := mcpImageContent([]byte(`{"data":[]}`)); len(content) != 0 {
		t.Fatalf("empty payload = %v, want no content", content)
	}
}
