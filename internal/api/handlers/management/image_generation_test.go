package management

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestGetImageGenerationChannelsIncludesRegisteredGPTImageModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-image-generation-codex"
	modelRegistry.RegisterClient(clientID, "codex", []*registry.ModelInfo{{ID: imageGenerationDefaultModel}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	h := &Handler{cfg: &config.Config{}}
	rec := runImageGenerationRequest(t, h, http.MethodGet, "/v0/management/image-generation/channels", nil, h.GetImageGenerationChannels)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Items []imageGenerationChannel `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Items) == 0 {
		t.Fatal("expected at least one image generation channel")
	}
	found := false
	for _, item := range payload.Items {
		if item.Provider == "codex" && item.Model == imageGenerationDefaultModel {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected codex gpt-image-2 channel, got %+v", payload.Items)
	}
}

func TestGetImageGenerationChannelsIncludesConfiguredOpenAICompatImageModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{cfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{
		{
			Name:          "images",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "sk-test"}},
			Models: []config.OpenAICompatibilityModel{
				{Name: "upstream-image", Alias: "compat-image", Image: true},
			},
		},
	}}}

	rec := runImageGenerationRequest(t, h, http.MethodGet, "/v0/management/image-generation/channels", nil, h.GetImageGenerationChannels)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Items []imageGenerationChannel `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("items len = %d, want 1: %+v", len(payload.Items), payload.Items)
	}
	if payload.Items[0].Provider != "openai-compatibility" || payload.Items[0].Model != "compat-image" {
		t.Fatalf("unexpected channel: %+v", payload.Items[0])
	}
}

func TestGetImageGenerationTestReturnsStoredTask(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{cfg: &config.Config{}, imageGenerationTasks: make(map[string]*imageGenerationTestTask)}
	task := h.createImageGenerationTask()
	h.finishImageGenerationTask(task.TaskID, task.CreatedAt, json.RawMessage(`{"created":1,"data":[]}`), nil)

	rec := runImageGenerationRequest(t, h, http.MethodGet, "/v0/management/image-generation/test/"+task.TaskID, nil, func(c *gin.Context) {
		c.Params = gin.Params{{Key: "task_id", Value: task.TaskID}}
		h.GetImageGenerationTest(c)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("response is not valid JSON: %s", rec.Body.String())
	}
	if got := rec.Body.String(); !strings.Contains(got, `"status":"succeeded"`) || !strings.Contains(got, `"result":{"created":1,"data":[]}`) {
		t.Fatalf("unexpected response: %s", got)
	}
}

func runImageGenerationRequest(t *testing.T, h *Handler, method, target string, body io.Reader, fn func(*gin.Context)) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(method, target, body)
	fn(ctx)
	return rec
}
