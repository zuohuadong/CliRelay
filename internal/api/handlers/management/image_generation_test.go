package management

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
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type managementImageExecutor struct {
	alt      string
	model    string
	payload  string
	payloads []string
	metadata map[string]any
	calls    int
}

func (e *managementImageExecutor) Identifier() string { return "codex" }

func (e *managementImageExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls++
	e.alt = opts.Alt
	e.model = req.Model
	e.payload = string(req.Payload)
	e.payloads = append(e.payloads, e.payload)
	e.metadata = opts.Metadata
	b64 := "dGVzdA=="
	if e.calls > 1 {
		b64 = "dGVzdA" + strconv.Itoa(e.calls) + "="
	}
	return coreexecutor.Response{Payload: []byte(`{"created":1,"data":[{"b64_json":"` + b64 + `"}]}`)}, nil
}

func (e *managementImageExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *managementImageExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *managementImageExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *managementImageExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestPostImageGenerationTestExecutesCodexImageAlt(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &managementImageExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"access_token": "token"},
	}); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	h := &Handler{authManager: manager}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/image-generation/test", strings.NewReader(`{"prompt":"test prompt"}`))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req

	h.PostImageGenerationTest(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if executor.alt != "images/generations" {
		t.Fatalf("alt = %q, want images/generations", executor.alt)
	}
	if executor.model != "" {
		t.Fatalf("model = %q, want empty route model for direct codex selection", executor.model)
	}
	if !strings.Contains(executor.payload, "test prompt") || !strings.Contains(executor.payload, "gpt-image-2") {
		t.Fatalf("payload = %s, want prompt and model", executor.payload)
	}
	if !strings.Contains(executor.payload, `"size":"1024x1024"`) && strings.Contains(executor.payload, "size") {
		t.Fatalf("payload = %s, should only include explicit size", executor.payload)
	}
	if executor.metadata[coreexecutor.SinglePickMetadataKey] != true {
		t.Fatalf("single-pick metadata = %#v, want true", executor.metadata[coreexecutor.SinglePickMetadataKey])
	}
}

func TestPostImageGenerationTestForwardsGenerationOptions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &managementImageExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"access_token": "token"},
	}); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	h := &Handler{authManager: manager}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/image-generation/test", strings.NewReader(`{
		"prompt":"test prompt",
		"size":"1024x1792",
		"quality":"high",
		"n":2
	}`))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req

	h.PostImageGenerationTest(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if executor.calls != 2 {
		t.Fatalf("executor calls = %d, want 2", executor.calls)
	}
	if executor.alt != "images/generations" {
		t.Fatalf("alt = %q, want images/generations", executor.alt)
	}
	for i, payload := range executor.payloads {
		for _, want := range []string{`"size":"1024x1792"`, `"quality":"high"`, `"n":1`} {
			if !strings.Contains(payload, want) {
				t.Fatalf("payload[%d] = %s, want %s", i, payload, want)
			}
		}
	}
	var body struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if len(body.Data) != 2 {
		t.Fatalf("data length = %d, want 2", len(body.Data))
	}
}

func TestPostImageGenerationTestRejectsMultipartImageEditsWhileDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &managementImageExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"access_token": "token"},
	}); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("model", "gpt-image-2")
	_ = writer.WriteField("prompt", "make it blue")
	_ = writer.WriteField("size", "1792x1024")
	_ = writer.WriteField("quality", "low")
	_ = writer.WriteField("n", "2")
	part, err := writer.CreateFormFile("image", "icon.png")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	_, _ = part.Write([]byte("hello"))
	_ = writer.Close()

	h := &Handler{authManager: manager}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/image-generation/test", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c.Request = req

	h.PostImageGenerationTest(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if executor.calls != 0 {
		t.Fatalf("executor calls = %d, want 0", executor.calls)
	}
	if !strings.Contains(rec.Body.String(), "image edits are temporarily disabled") {
		t.Fatalf("body = %s, want disabled message", rec.Body.String())
	}
}

func TestListImageGenerationChannelsUsesCurrentChannelLabels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	_, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-oauth-1",
		Provider: "codex",
		Label:    "设计号 A",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"type": "codex", "email": "a@example.com"},
	})
	if err != nil {
		t.Fatalf("Register first auth: %v", err)
	}
	_, err = manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-oauth-2",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex", "label": "设计号 B", "email": "b@example.com"},
		Status:   coreauth.StatusActive,
	})
	if err != nil {
		t.Fatalf("Register second auth: %v", err)
	}
	_, err = manager.Register(context.Background(), &coreauth.Auth{
		ID:       "gemini-oauth-1",
		Provider: "gemini-cli",
		Label:    "Gemini",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"type": "gemini-cli"},
	})
	if err != nil {
		t.Fatalf("Register third auth: %v", err)
	}

	h := &Handler{authManager: manager}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/image-generation/channels", nil)

	h.ListImageGenerationChannels(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "设计号 A") || !strings.Contains(body, "设计号 B") {
		t.Fatalf("body = %s, want codex channel labels", body)
	}
	if strings.Contains(body, "Gemini") {
		t.Fatalf("body = %s, should not include non-codex channel", body)
	}
}
