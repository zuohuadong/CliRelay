package videotool

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestClientVideoFlow(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/openai/v1/videos":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode create body: %v", err)
			}
			if body["model"] != DefaultModel || body["prompt"] != "make a video" {
				t.Fatalf("unexpected create body: %#v", body)
			}
			_, _ = w.Write([]byte(`{"id":"video_123","object":"video","status":"queued"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/openai/v1/videos/video_123":
			_, _ = w.Write([]byte(`{"id":"video_123","object":"video","status":"completed","video_url":"https://example.com/video.mp4"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/openai/v1/videos/video_123/content":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("video-bytes"))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(Options{BaseURL: server.URL, APIKey: "test-key"})
	created, err := client.CreateVideo(context.Background(), CreateVideoRequest{Prompt: "make a video"})
	if err != nil {
		t.Fatalf("CreateVideo error: %v", err)
	}
	if got := VideoID(created); got != "video_123" {
		t.Fatalf("VideoID = %q", got)
	}
	status, err := client.GetVideo(context.Background(), "video_123")
	if err != nil {
		t.Fatalf("GetVideo error: %v", err)
	}
	if got := status["status"]; got != "completed" {
		t.Fatalf("status = %v", got)
	}
	outputPath := filepath.Join(t.TempDir(), "out.mp4")
	download, err := client.DownloadVideo(context.Background(), "video_123", outputPath)
	if err != nil {
		t.Fatalf("DownloadVideo error: %v", err)
	}
	if download.Bytes != int64(len("video-bytes")) || download.ContentType != "video/mp4" {
		t.Fatalf("download = %+v", download)
	}
	raw, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(raw) != "video-bytes" {
		t.Fatalf("output = %q", string(raw))
	}
	want := []string{
		"POST /openai/v1/videos",
		"GET /openai/v1/videos/video_123",
		"GET /openai/v1/videos/video_123/content",
	}
	if strings.Join(paths, "\n") != strings.Join(want, "\n") {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
}

func TestClientListVideoModelsFiltersVideoIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"agnes-2.0-flash"},{"id":"agnes-video-v2.0"},{"id":"custom-video"}]}`))
	}))
	defer server.Close()

	models, err := NewClient(Options{BaseURL: server.URL}).ListVideoModels(context.Background())
	if err != nil {
		t.Fatalf("ListVideoModels error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models len = %d, want 2: %#v", len(models), models)
	}
}

func TestMCPServerToolsList(t *testing.T) {
	request := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var in bytes.Buffer
	_, _ = in.WriteString("Content-Length: " + strconv.Itoa(len(request)) + "\r\n\r\n")
	_, _ = in.Write(request)
	var out bytes.Buffer

	err := (&MCPServer{
		Client: NewClient(Options{BaseURL: "http://127.0.0.1:1"}),
		In:     &in,
		Out:    &out,
	}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if !strings.Contains(out.String(), "clirelay_video_create") {
		t.Fatalf("tools/list response missing video tool: %s", out.String())
	}
}
