package videotool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

type MCPServer struct {
	Client *Client
	In     io.Reader
	Out    io.Writer
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func (s *MCPServer) Run(ctx context.Context) error {
	if s == nil || s.Client == nil {
		return fmt.Errorf("MCP server requires a video client")
	}
	in := s.In
	if in == nil {
		return fmt.Errorf("MCP server input is nil")
	}
	if s.Out == nil {
		return fmt.Errorf("MCP server output is nil")
	}
	reader := bufio.NewReader(in)
	for {
		raw, err := readMCPFrame(reader)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		var req rpcRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			continue
		}
		if len(req.ID) == 0 {
			continue
		}
		result, rpcErr := s.handle(ctx, req)
		resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rpcErr}
		if rpcErr != nil {
			resp.Result = nil
		}
		if err := writeMCPFrame(s.Out, resp); err != nil {
			return err
		}
	}
}

func (s *MCPServer) handle(ctx context.Context, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    "clirelay-tools",
				"version": "0.1.0",
			},
		}, nil
	case "tools/list":
		return map[string]any{"tools": videoTools()}, nil
	case "tools/call":
		var params toolCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid tool call params"}
		}
		return s.callTool(ctx, params)
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found"}
	}
}

func (s *MCPServer) callTool(ctx context.Context, params toolCallParams) (any, *rpcError) {
	args := params.Arguments
	if args == nil {
		args = map[string]any{}
	}
	switch params.Name {
	case "clirelay_video_models":
		models, err := s.Client.ListVideoModels(ctx)
		return toolJSON(models, err)
	case "clirelay_video_create":
		req := CreateVideoRequest{
			Model:       stringArg(args, "model"),
			Prompt:      stringArg(args, "prompt"),
			Seconds:     intArg(args, "seconds"),
			Size:        stringArg(args, "size"),
			AspectRatio: stringArg(args, "aspect_ratio"),
			Resolution:  stringArg(args, "resolution"),
		}
		if req.Model == "" {
			req.Model = s.Client.DefaultModel()
		}
		out, err := s.Client.CreateVideo(ctx, req)
		if err != nil {
			return toolJSON(nil, err)
		}
		if boolArg(args, "wait") {
			videoID := VideoID(out)
			timeout := time.Duration(intArg(args, "timeout_seconds")) * time.Second
			if timeout <= 0 {
				timeout = 10 * time.Minute
			}
			waitCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			interval := time.Duration(intArg(args, "poll_interval_seconds")) * time.Second
			if interval <= 0 {
				interval = 5 * time.Second
			}
			out, err = s.Client.WaitVideo(waitCtx, videoID, interval)
			if err != nil {
				return toolJSON(out, err)
			}
		}
		return toolJSON(out, nil)
	case "clirelay_video_status":
		out, err := s.Client.GetVideo(ctx, stringArg(args, "video_id"))
		return toolJSON(out, err)
	case "clirelay_video_download":
		out, err := s.Client.DownloadVideo(ctx, stringArg(args, "video_id"), stringArg(args, "output_path"))
		return toolJSON(out, err)
	default:
		return nil, &rpcError{Code: -32602, Message: "unknown tool: " + params.Name}
	}
}

func videoTools() []map[string]any {
	return []map[string]any{
		{
			"name":        "clirelay_video_models",
			"description": "List video-capable models from the configured CliRelay server.",
			"inputSchema": objectSchema(nil, nil),
		},
		{
			"name":        "clirelay_video_create",
			"description": "Create a video generation task through CliRelay /openai/v1/videos.",
			"inputSchema": objectSchema(map[string]any{
				"prompt":                stringSchema("Video prompt."),
				"model":                 stringSchema("Video model. Defaults to agnes-video-v2.0."),
				"seconds":               numberSchema("Video duration in seconds."),
				"size":                  stringSchema("Video size such as 720x1280."),
				"aspect_ratio":          stringSchema("Aspect ratio such as 9:16 or 16:9."),
				"resolution":            stringSchema("Resolution such as 720p."),
				"wait":                  boolSchema("Poll until the video reaches a terminal state."),
				"poll_interval_seconds": numberSchema("Polling interval when wait is true."),
				"timeout_seconds":       numberSchema("Maximum wait time when wait is true."),
			}, []string{"prompt"}),
		},
		{
			"name":        "clirelay_video_status",
			"description": "Get the current status and video_url for a video task.",
			"inputSchema": objectSchema(map[string]any{
				"video_id": stringSchema("Video id returned by create."),
			}, []string{"video_id"}),
		},
		{
			"name":        "clirelay_video_download",
			"description": "Download a completed video to a local file path.",
			"inputSchema": objectSchema(map[string]any{
				"video_id":    stringSchema("Video id returned by create."),
				"output_path": stringSchema("Local output path. Defaults to <video_id>.mp4."),
			}, []string{"video_id"}),
		},
	}
}

func toolJSON(value any, err error) (any, *rpcError) {
	if err != nil {
		return nil, &rpcError{Code: -32000, Message: err.Error()}
	}
	raw, _ := json.MarshalIndent(value, "", "  ")
	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": string(raw)},
		},
	}, nil
}

func readMCPFrame(reader *bufio.Reader) ([]byte, error) {
	contentLength := -1
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, err
			}
			contentLength = n
		}
	}
	if contentLength < 0 {
		return nil, fmt.Errorf("missing Content-Length")
	}
	raw := make([]byte, contentLength)
	_, err := io.ReadFull(reader, raw)
	return raw, err
}

func writeMCPFrame(out io.Writer, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "Content-Length: %d\r\n\r\n", len(raw)); err != nil {
		return err
	}
	_, err = out.Write(raw)
	return err
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	out := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func stringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func numberSchema(description string) map[string]any {
	return map[string]any{"type": "number", "description": description}
}

func boolSchema(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

func stringArg(args map[string]any, key string) string {
	return strings.TrimSpace(stringValue(args[key]))
}

func intArg(args map[string]any, key string) int {
	switch value := args[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(value))
		return parsed
	default:
		return 0
	}
}

func boolArg(args map[string]any, key string) bool {
	switch value := args[key].(type) {
	case bool:
		return value
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(value))
		return parsed
	default:
		return false
	}
}
