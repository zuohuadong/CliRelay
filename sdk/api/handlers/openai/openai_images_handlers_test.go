package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

type imageCaptureExecutor struct {
	alt      string
	model    string
	payload  string
	payloads []string
	metadata map[string]any
	calls    int
}

func (e *imageCaptureExecutor) Identifier() string { return "codex" }

func (e *imageCaptureExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls++
	e.alt = opts.Alt
	e.model = req.Model
	e.payload = string(req.Payload)
	e.payloads = append(e.payloads, e.payload)
	e.metadata = opts.Metadata
	b64 := "aGVsbG8="
	if e.calls > 1 {
		b64 = "aGVsbG8" + strconv.Itoa(e.calls) + "="
	}
	return coreexecutor.Response{Payload: []byte(`{"created":1,"data":[{"b64_json":"` + b64 + `"}]}`)}, nil
}

func (e *imageCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *imageCaptureExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *imageCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *imageCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestOpenAIImagesGenerationsExecutesCodexImageAlt(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &imageCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Label:    "Team Codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"access_token": "token", "email": "team@example.com"},
	}); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIImagesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/images/generations", func(c *gin.Context) {
		c.Set("accessMetadata", map[string]string{"allowed-channels": "Team Codex"})
		h.Generations(c)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"gpt-image-2","prompt":"draw a fox"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if executor.alt != "images/generations" {
		t.Fatalf("alt = %q, want %q", executor.alt, "images/generations")
	}
	if executor.model != "" {
		t.Fatalf("model = %q, want empty route model for direct codex selection", executor.model)
	}
	if !strings.Contains(executor.payload, "draw a fox") || !strings.Contains(executor.payload, "gpt-image-2") {
		t.Fatalf("payload = %s, want prompt", executor.payload)
	}
	if executor.metadata["allowed-channels"] != "Team Codex" {
		t.Fatalf("allowed channel metadata = %#v", executor.metadata["allowed-channels"])
	}
	if executor.metadata[coreexecutor.SinglePickMetadataKey] != true {
		t.Fatalf("single-pick metadata = %#v, want true", executor.metadata[coreexecutor.SinglePickMetadataKey])
	}
	if strings.TrimSpace(resp.Body.String()) != `{"created":1,"data":[{"b64_json":"aGVsbG8="}]}` {
		t.Fatalf("body = %s", resp.Body.String())
	}
}

func TestOpenAIImagesGenerationsDefaultsModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &imageCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Label:    "Team Codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"access_token": "token", "email": "team@example.com"},
	}); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIImagesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/images/generations", h.Generations)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"prompt":"draw a fox"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if executor.model != "" {
		t.Fatalf("model = %q, want empty route model for direct codex selection", executor.model)
	}
	if !strings.Contains(executor.payload, "gpt-image-2") {
		t.Fatalf("payload = %s, want default model in payload", executor.payload)
	}
}

func TestOpenAIImagesGenerationsSplitsMultipleImagesIntoSingleImageExecutions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &imageCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Label:    "Team Codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"access_token": "token", "email": "team@example.com"},
	}); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIImagesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/images/generations", h.Generations)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"gpt-image-2","prompt":"draw a fox","n":3}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if executor.calls != 3 {
		t.Fatalf("executor calls = %d, want 3", executor.calls)
	}
	for i, payload := range executor.payloads {
		if !strings.Contains(payload, `"n":1`) {
			t.Fatalf("payload[%d] = %s, want n=1", i, payload)
		}
	}
	var body struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if len(body.Data) != 3 {
		t.Fatalf("data length = %d, want 3", len(body.Data))
	}
}

func TestOpenAIImagesEditsExecutesCodexImageEditsAlt(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &imageCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Label:    "Team Codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"access_token": "token", "email": "team@example.com"},
	}); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIImagesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/images/edits", h.Edits)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("model", "gpt-image-2")
	_ = writer.WriteField("prompt", "make it blue")
	_ = writer.WriteField("size", "1024x1792")
	_ = writer.WriteField("quality", "medium")
	_ = writer.WriteField("background", "transparent")
	_ = writer.WriteField("output_format", "webp")
	_ = writer.WriteField("input_fidelity", "high")
	_ = writer.WriteField("n", "2")
	part, err := writer.CreateFormFile("image", "icon.png")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	_, _ = part.Write([]byte("hello"))
	maskPart, err := writer.CreateFormFile("mask", "mask.png")
	if err != nil {
		t.Fatalf("CreateFormFile(mask): %v", err)
	}
	_, _ = maskPart.Write([]byte("mask-bytes"))
	_ = writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if executor.calls != 2 {
		t.Fatalf("executor calls = %d, want 2", executor.calls)
	}
	if executor.alt != "images/edits" {
		t.Fatalf("alt = %q, want %q", executor.alt, "images/edits")
	}
	if !strings.Contains(executor.payload, `"output_format":"webp"`) {
		t.Fatalf("payload = %s, want output_format", executor.payload)
	}
	if !strings.Contains(executor.payload, `"background":"transparent"`) {
		t.Fatalf("payload = %s, want background", executor.payload)
	}
	if !strings.Contains(executor.payload, `"input_fidelity":"high"`) {
		t.Fatalf("payload = %s, want input_fidelity", executor.payload)
	}
	if !strings.Contains(executor.payload, `"mask_file"`) {
		t.Fatalf("payload = %s, want mask_file", executor.payload)
	}
	if strings.Count(executor.payload, `"data_base64"`) < 2 {
		t.Fatalf("payload = %s, want base64 encoded image and mask uploads", executor.payload)
	}
}
