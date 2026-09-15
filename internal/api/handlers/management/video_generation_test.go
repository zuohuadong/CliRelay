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

func TestGetVideoGenerationChannelsIncludesRegisteredVideoModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-video-generation-agnes"
	modelRegistry.RegisterClient(clientID, "agnes", []*registry.ModelInfo{
		{ID: videoGenerationDefaultModel, Type: registry.OpenAIVideoModelType},
	})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	h := &Handler{cfg: &config.Config{}}
	rec := runVideoGenerationRequest(t, h, http.MethodGet, "/v0/management/video-generation/channels", nil, h.GetVideoGenerationChannels)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Items []videoGenerationChannel `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Items) == 0 {
		t.Fatal("expected at least one video generation channel")
	}
	found := false
	for _, item := range payload.Items {
		if item.Provider == "agnes" && item.Model == videoGenerationDefaultModel {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected agnes %s channel, got %+v", videoGenerationDefaultModel, payload.Items)
	}
}

func TestGetVideoGenerationChannelsIncludesConfiguredOpenAICompatVideoModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{cfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{
		{
			Name:          "videos",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "sk-test"}},
			Models: []config.OpenAICompatibilityModel{
				{Name: "upstream-video", Alias: "compat-video", Video: true},
			},
		},
	}}}

	rec := runVideoGenerationRequest(t, h, http.MethodGet, "/v0/management/video-generation/channels", nil, h.GetVideoGenerationChannels)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Items []videoGenerationChannel `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("items len = %d, want 1: %+v", len(payload.Items), payload.Items)
	}
	if payload.Items[0].Provider != "openai-compatibility" || payload.Items[0].Model != "compat-video" {
		t.Fatalf("unexpected channel: %+v", payload.Items[0])
	}
}

func TestGetVideoGenerationTestReturnsStoredTask(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{cfg: &config.Config{}, videoGenerationTasks: make(map[string]*videoGenerationTestTask)}
	task := h.createVideoGenerationTask()
	h.finishVideoGenerationTask(task.TaskID, task.CreatedAt, json.RawMessage(`{"id":"vid_1","status":"completed"}`), nil)

	rec := runVideoGenerationRequest(t, h, http.MethodGet, "/v0/management/video-generation/test/"+task.TaskID, nil, func(c *gin.Context) {
		c.Params = gin.Params{{Key: "task_id", Value: task.TaskID}}
		h.GetVideoGenerationTest(c)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("response is not valid JSON: %s", rec.Body.String())
	}
	if got := rec.Body.String(); !strings.Contains(got, `"status":"succeeded"`) || !strings.Contains(got, `"result":{"id":"vid_1","status":"completed"}`) {
		t.Fatalf("unexpected response: %s", got)
	}
}

func runVideoGenerationRequest(t *testing.T, h *Handler, method, target string, body io.Reader, fn func(*gin.Context)) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(method, target, body)
	fn(ctx)
	return rec
}
