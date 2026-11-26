package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers/openai"
	"github.com/tidwall/gjson"
)

// The MCP image endpoint serves MCP clients that generate images: a JSON-RPC 2.0
// request over the Streamable HTTP transport, published at POST /mcp/image.
//
// MCP clients reach for a model-agnostic "give me an image" tool instead of an
// OpenAI images call, so the tool is the relay's own image pipeline: the same
// models (/v1/images/generations), the same quotas, the same request logs. The
// JSON-RPC envelope is all this file adds.
//
// The server keeps no session state: MCP allows that for a server whose tools
// carry none, and a stateless endpoint survives restarts and multiple nodes
// without a session store.

const (
	// mcpImageDefaultModel generates an image when the tool call names none.
	mcpImageDefaultModel = "gpt-image-2"
	// mcpImageMaxCount bounds n the way the images endpoint does.
	mcpImageMaxCount = 4
	// mcpImageKeepAlive is how often a comment holds an idle connection open
	// while the upstream generates. A model image takes tens of seconds.
	mcpImageKeepAlive = 15 * time.Second
	// mcpProtocolVersion is answered to initialize when the client names none.
	mcpProtocolVersion = "2025-06-18"
)

// mcpImageTools returns the tools this endpoint publishes. generate_image is the
// only one: an edit needs an image upload the JSON-RPC transport does not carry
// here, and /v1/images/edits serves that.
func mcpImageTools() []gin.H {
	return []gin.H{{
		"name": "generate_image",
		"description": "Generate an image from a text prompt. Returns the image itself, so the " +
			"conversation can show it.",
		"inputSchema": gin.H{
			"type": "object",
			"properties": gin.H{
				"prompt": gin.H{"type": "string", "description": "What the image should show."},
				"model": gin.H{"type": "string",
					"description": "Image model to use, for example gpt-image-2. Defaults to " + mcpImageDefaultModel + "."},
				"size": gin.H{"type": "string",
					"description": "Image size such as 1024x1024 or 1536x1024. The model's default is used when omitted."},
				"quality": gin.H{"type": "string", "description": "Model-specific quality such as low, medium or high."},
				"n": gin.H{"type": "integer", "minimum": 1, "maximum": mcpImageMaxCount,
					"description": "How many images to generate. Defaults to 1."},
			},
			"required": []string{"prompt"},
		},
	}}
}

// mcpImageHandler answers MCP JSON-RPC on POST /mcp/image.
func (s *Server) mcpImageHandler() gin.HandlerFunc {
	images := openai.NewOpenAIImagesAPIHandler(s.handlers)
	return func(c *gin.Context) {
		rawJSON, ok := handlers.ReadJSONRequestBody(c)
		if !ok {
			return
		}
		if !gjson.ValidBytes(rawJSON) {
			writeMCPRPCError(c, json.RawMessage("null"), -32700, "parse error: the body is not JSON")
			return
		}
		id := mcpRequestID(rawJSON)
		method := strings.TrimSpace(gjson.GetBytes(rawJSON, "method").String())
		if method == "" {
			writeMCPRPCError(c, id, -32600, "invalid request: method is missing")
			return
		}

		switch method {
		case "initialize":
			protocol := strings.TrimSpace(gjson.GetBytes(rawJSON, "params.protocolVersion").String())
			if protocol == "" {
				protocol = mcpProtocolVersion
			}
			c.Header("Mcp-Session-Id", uuid.NewString())
			writeMCPRPCResult(c, id, gin.H{
				"protocolVersion": protocol,
				"capabilities":    gin.H{"tools": gin.H{"listChanged": false}},
				"serverInfo": gin.H{
					"name":    "clirelay-image",
					"version": buildinfo.Version,
				},
			})
		case "notifications/initialized", "notifications/cancelled":
			// A notification carries no id and expects no body.
			c.Status(http.StatusAccepted)
		case "ping":
			writeMCPRPCResult(c, id, gin.H{})
		case "tools/list":
			writeMCPRPCResult(c, id, gin.H{"tools": mcpImageTools()})
		case "tools/call":
			writeMCPRPCResult(c, id, s.callImageTool(c, images, rawJSON))
		default:
			writeMCPRPCError(c, id, -32601, fmt.Sprintf("method %q is not supported", method))
		}
	}
}

// callImageTool runs one tools/call and returns the tool result. A failure the
// tool itself ran into is a result with isError, as MCP asks: the client shows
// it to the model instead of treating the call as a protocol fault.
func (s *Server) callImageTool(c *gin.Context, images *openai.OpenAIImagesAPIHandler, rawJSON []byte) gin.H {
	name := strings.TrimSpace(gjson.GetBytes(rawJSON, "params.name").String())
	if name != "generate_image" {
		return mcpToolError(fmt.Sprintf("unknown tool %q; this server publishes generate_image", name))
	}
	arguments := gjson.GetBytes(rawJSON, "params.arguments")
	prompt := strings.TrimSpace(arguments.Get("prompt").String())
	if prompt == "" {
		return mcpToolError("prompt is required")
	}

	model := strings.TrimSpace(arguments.Get("model").String())
	if model == "" {
		model = mcpImageDefaultModel
	}
	// The key's own restrictions are checked here: the middleware could not see a
	// model in a JSON-RPC body.
	if gate := handlers.ModelGateFromGin(c); gate != nil {
		if verdict := gate(model); verdict != nil {
			return mcpToolError(verdict.Error.Error())
		}
	}

	body := gin.H{"model": model, "prompt": prompt}
	if size := strings.TrimSpace(arguments.Get("size").String()); size != "" {
		body["size"] = size
	}
	if quality := strings.TrimSpace(arguments.Get("quality").String()); quality != "" {
		body["quality"] = quality
	}
	count := 1
	if n := arguments.Get("n"); n.Exists() {
		if n.Type != gjson.Number || n.Int() < 1 || n.Int() > mcpImageMaxCount {
			return mcpToolError(fmt.Sprintf("n must be a number between 1 and %d", mcpImageMaxCount))
		}
		count = int(n.Int())
	}
	body["n"] = count

	payload, _ := json.Marshal(body)
	// A generation can outlast an idle proxy or client: hold the connection with
	// SSE comments when the client speaks SSE, and send nothing otherwise.
	stopKeepAlive := startMCPKeepAlive(c)
	result, headers, execErr := images.RunImages(c, payload, "images/generations")
	stopKeepAlive()
	if execErr != nil {
		return mcpToolError(fmt.Sprintf("image generation failed: %s", execErr.Message))
	}

	content := mcpImageContent(result)
	if len(content) == 0 {
		return mcpToolError("the image model returned no image")
	}
	summary := fmt.Sprintf("generated %d image(s) with %s", len(content), model)
	if size := respHeaderValue(headers, "X-Image-Size"); size != "" {
		summary += " at " + size
	}
	return gin.H{
		"content": append([]gin.H{{"type": "text", "text": summary}}, content...),
		"isError": false,
	}
}

// mcpImageContent turns an OpenAI images response body into MCP image content
// blocks, one per image. An upstream that answers with a link instead of bytes
// (some dall-e deployments do) is returned as a resource link the client can
// fetch, since MCP image content carries the bytes themselves.
func mcpImageContent(payload []byte) []gin.H {
	items := gjson.GetBytes(payload, "data")
	if !items.IsArray() {
		return nil
	}
	content := make([]gin.H, 0, len(items.Array()))
	for _, item := range items.Array() {
		if data := strings.TrimSpace(item.Get("b64_json").String()); data != "" {
			mimeType := strings.TrimSpace(item.Get("mime_type").String())
			if mimeType == "" {
				mimeType = detectImageMimeType(data)
			}
			content = append(content, gin.H{"type": "image", "data": data, "mimeType": mimeType})
			continue
		}
		if url := strings.TrimSpace(item.Get("url").String()); url != "" {
			content = append(content, gin.H{
				"type":     "resource_link",
				"uri":      url,
				"name":     "generated image",
				"mimeType": "image/png",
			})
		}
	}
	return content
}

// detectImageMimeType names the format from the first base64 bytes. Every image
// the relay generates today is a PNG; a mislabel would only cost the client a
// decode failure, so the default stays image/png.
func detectImageMimeType(base64Data string) string {
	header := base64Data
	if len(header) > 24 {
		header = header[:24]
	}
	switch {
	case strings.HasPrefix(header, "iVBORw0KGgo"):
		return "image/png"
	case strings.HasPrefix(header, "/9j/"):
		return "image/jpeg"
	case strings.HasPrefix(header, "R0lGOD"):
		return "image/gif"
	case strings.HasPrefix(header, "UklGR"):
		return "image/webp"
	default:
		return "image/png"
	}
}

// startMCPKeepAlive holds the connection open while a tool call runs, with the
// comment form SSE defines for it: it writes nothing when the client did not ask
// for a stream, nor once the response has started.
func startMCPKeepAlive(c *gin.Context) func() {
	if !mcpWantsEventStream(c) {
		return func() {}
	}
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(mcpImageKeepAlive)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-c.Request.Context().Done():
				return
			case <-ticker.C:
				if _, err := c.Writer.Write([]byte(": keep-alive\n\n")); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}()
	return func() { close(done) }
}

// mcpWantsEventStream reports whether the client accepts the SSE form of the
// transport. MCP clients send both in Accept; a plain JSON client sends only the
// JSON type.
func mcpWantsEventStream(c *gin.Context) bool {
	accept := strings.ToLower(c.GetHeader("Accept"))
	return strings.Contains(accept, "text/event-stream")
}

// mcpRequestID keeps the id exactly as the client sent it: a string, a number or
// null. It goes back verbatim, as the transport requires.
func mcpRequestID(rawJSON []byte) json.RawMessage {
	id := gjson.GetBytes(rawJSON, "id")
	if !id.Exists() {
		return json.RawMessage("null")
	}
	return json.RawMessage(id.Raw)
}

func mcpToolError(message string) gin.H {
	return gin.H{"content": []gin.H{{"type": "text", "text": message}}, "isError": true}
}

func writeMCPRPCResult(c *gin.Context, id json.RawMessage, result any) {
	writeMCPMessage(c, gin.H{"jsonrpc": "2.0", "id": id, "result": result})
}

func writeMCPRPCError(c *gin.Context, id json.RawMessage, code int, message string) {
	writeMCPMessage(c, gin.H{"jsonrpc": "2.0", "id": id, "error": gin.H{"code": code, "message": message}})
}

// writeMCPMessage answers in the transport form the client asked for: one SSE
// event, or the JSON body itself. Both carry the same JSON-RPC message.
func writeMCPMessage(c *gin.Context, message gin.H) {
	encoded, err := json.Marshal(message)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"jsonrpc": "2.0", "id": message["id"],
			"error": gin.H{"code": -32603, "message": "internal error"}})
		return
	}
	if !mcpWantsEventStream(c) {
		c.Data(http.StatusOK, "application/json; charset=utf-8", encoded)
		return
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	var event bytes.Buffer
	event.WriteString("event: message\ndata: ")
	event.Write(encoded)
	event.WriteString("\n\n")
	if _, writeErr := c.Writer.Write(event.Bytes()); writeErr != nil {
		return
	}
	if flusher, ok := c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func respHeaderValue(headers http.Header, name string) string {
	if headers == nil {
		return ""
	}
	return strings.TrimSpace(headers.Get(name))
}
