package management

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	corehandlers "github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	openaihandlers "github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/openai"
	"github.com/tidwall/gjson"
)

const (
	videoGenerationDefaultModel    = "agnes-video-v2.0"
	videoGenerationTaskMaxAge      = time.Hour
	videoGenerationTaskMaxRetained = 100
	videoGenerationPollInterval    = 2 * time.Second
	videoGenerationPollTimeout     = 2 * time.Minute
)

type videoGenerationTestRequest struct {
	Model   string `json:"model"`
	Prompt  string `json:"prompt"`
	Seconds string `json:"seconds"`
	Size    string `json:"size"`
}

type videoGenerationTestTaskError struct {
	Status int `json:"status,omitempty"`
	Body   any `json:"body,omitempty"`
}

type videoGenerationTestTask struct {
	TaskID      string                        `json:"task_id"`
	Status      string                        `json:"status"`
	Phase       string                        `json:"phase,omitempty"`
	ElapsedMS   int64                         `json:"elapsed_ms,omitempty"`
	Result      json.RawMessage               `json:"result,omitempty"`
	Error       *videoGenerationTestTaskError `json:"error,omitempty"`
	CreatedAt   time.Time                     `json:"-"`
	StartedAt   time.Time                     `json:"-"`
	CompletedAt time.Time                     `json:"-"`
}

type videoGenerationChannel struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Type     string `json:"type"`
}

func (h *Handler) GetVideoGenerationChannels(c *gin.Context) {
	cfg := h.currentImageGenerationConfig()
	channels := videoGenerationChannelsFromRegistry()
	channels = append(channels, configuredOpenAICompatVideoChannels(cfg)...)
	c.JSON(http.StatusOK, gin.H{"items": dedupeVideoGenerationChannels(channels)})
}

func (h *Handler) StartVideoGenerationTest(c *gin.Context) {
	var body videoGenerationTestRequest
	if errBind := c.ShouldBindJSON(&body); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if errValidate := normalizeVideoGenerationTestRequest(&body); errValidate != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errValidate.Error()})
		return
	}
	task := h.createVideoGenerationTask()
	go h.runVideoGenerationTask(task.TaskID, body)
	c.JSON(http.StatusOK, task.snapshot())
}

func (h *Handler) GetVideoGenerationTest(c *gin.Context) {
	taskID := strings.TrimSpace(c.Param("task_id"))
	if taskID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing task_id"})
		return
	}
	task, ok := h.getVideoGenerationTask(taskID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "video generation test task not found"})
		return
	}
	c.JSON(http.StatusOK, task)
}

func normalizeVideoGenerationTestRequest(body *videoGenerationTestRequest) error {
	if body == nil {
		return fmt.Errorf("invalid body")
	}
	body.Model = strings.TrimSpace(body.Model)
	body.Prompt = strings.TrimSpace(body.Prompt)
	body.Seconds = strings.TrimSpace(body.Seconds)
	body.Size = strings.TrimSpace(body.Size)
	if body.Model == "" {
		body.Model = videoGenerationDefaultModel
	}
	if body.Prompt == "" {
		return fmt.Errorf("prompt is required")
	}
	return nil
}

func (h *Handler) runVideoGenerationTask(taskID string, body videoGenerationTestRequest) {
	startedAt := time.Now()
	h.updateVideoGenerationTask(taskID, func(task *videoGenerationTestTask) {
		task.Status = "running"
		task.Phase = "create"
		task.StartedAt = startedAt
	})

	recorder, errRun := h.executeVideoGenerationTest(body)
	completedAt := time.Now()
	if errRun != nil {
		h.finishVideoGenerationTask(taskID, completedAt, nil, &videoGenerationTestTaskError{
			Status: http.StatusInternalServerError,
			Body:   gin.H{"error": gin.H{"message": errRun.Error()}},
		})
		return
	}
	responseBody := bytes.Clone(recorder.Body.Bytes())
	if recorder.Code >= http.StatusBadRequest {
		h.finishVideoGenerationTask(taskID, completedAt, nil, &videoGenerationTestTaskError{
			Status: recorder.Code,
			Body:   decodeImageGenerationBody(responseBody),
		})
		return
	}
	h.finishVideoGenerationTask(taskID, completedAt, json.RawMessage(responseBody), nil)
}

func (h *Handler) executeVideoGenerationTest(body videoGenerationTestRequest) (*httptest.ResponseRecorder, error) {
	cfg, manager, pluginHost := h.currentImageGenerationRuntime()
	if cfg == nil {
		return nil, fmt.Errorf("config is not available")
	}
	base := corehandlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
	base.SetPluginHost(pluginHost)
	handler := openaihandlers.NewOpenAIAPIHandler(base)

	req, errBuild := buildVideoGenerationJSONRequest(body)
	if errBuild != nil {
		return nil, errBuild
	}
	createRec := httptest.NewRecorder()
	createCtx, _ := gin.CreateTestContext(createRec)
	createCtx.Request = req
	handler.VideosCreate(createCtx)
	if createRec.Code >= http.StatusBadRequest {
		return createRec, nil
	}

	videoID := strings.TrimSpace(gjson.GetBytes(createRec.Body.Bytes(), "id").String())
	status := strings.ToLower(strings.TrimSpace(gjson.GetBytes(createRec.Body.Bytes(), "status").String()))
	if videoID == "" || status == "" || status == "completed" || status == "failed" || status == "succeeded" {
		return createRec, nil
	}

	deadline := time.Now().Add(videoGenerationPollTimeout)
	latest := createRec
	for time.Now().Before(deadline) {
		time.Sleep(videoGenerationPollInterval)
		pollReq := httptest.NewRequest(http.MethodGet, "/openai/v1/videos/"+videoID, nil)
		pollRec := httptest.NewRecorder()
		pollCtx, _ := gin.CreateTestContext(pollRec)
		pollCtx.Request = pollReq
		pollCtx.Params = gin.Params{{Key: "video_id", Value: videoID}}
		handler.VideosRetrieve(pollCtx)
		latest = pollRec
		if pollRec.Code >= http.StatusBadRequest {
			return pollRec, nil
		}
		pollStatus := strings.ToLower(strings.TrimSpace(gjson.GetBytes(pollRec.Body.Bytes(), "status").String()))
		if pollStatus == "completed" || pollStatus == "failed" || pollStatus == "succeeded" || pollStatus == "error" {
			return pollRec, nil
		}
	}
	return latest, nil
}

func buildVideoGenerationJSONRequest(body videoGenerationTestRequest) (*http.Request, error) {
	payload := map[string]any{
		"model":  body.Model,
		"prompt": body.Prompt,
	}
	if body.Seconds != "" {
		payload["seconds"] = body.Seconds
	}
	if body.Size != "" {
		payload["size"] = body.Size
	}
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, errMarshal
	}
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/videos", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func videoGenerationChannelsFromRegistry() []videoGenerationChannel {
	models := registry.GetGlobalRegistry().GetAvailableModels("openai")
	channels := make([]videoGenerationChannel, 0)
	seen := make(map[string]struct{})
	for _, model := range models {
		modelType, _ := model["type"].(string)
		if modelType != registry.OpenAIVideoModelType {
			continue
		}
		modelID, _ := model["id"].(string)
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			continue
		}
		providers := registry.GetGlobalRegistry().GetModelProviders(modelID)
		if len(providers) == 0 {
			if ownedBy, _ := model["owned_by"].(string); strings.TrimSpace(ownedBy) != "" {
				providers = []string{strings.TrimSpace(ownedBy)}
			}
		}
		for _, provider := range providers {
			provider = strings.TrimSpace(provider)
			if provider == "" {
				continue
			}
			key := strings.ToLower(provider + "\x00" + modelID)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			channels = append(channels, videoGenerationChannel{
				Provider: provider,
				Model:    modelID,
				Type:     "registered",
			})
		}
	}
	return channels
}

func configuredOpenAICompatVideoChannels(cfg *config.Config) []videoGenerationChannel {
	if cfg == nil {
		return nil
	}
	channels := make([]videoGenerationChannel, 0)
	appendCompat := func(provider string, entries []config.OpenAICompatibility) {
		for _, entry := range entries {
			if entry.Disabled || len(entry.APIKeyEntries) == 0 {
				continue
			}
			if !hasEnabledOpenAICompatKey(entry.APIKeyEntries) {
				continue
			}
			for _, model := range entry.Models {
				if !model.Video {
					continue
				}
				modelID := strings.TrimSpace(model.Alias)
				if modelID == "" {
					modelID = strings.TrimSpace(model.Name)
				}
				if modelID == "" {
					continue
				}
				channels = append(channels, videoGenerationChannel{
					Provider: provider,
					Model:    modelID,
					Type:     "openai-compatible",
				})
			}
		}
	}
	appendCompat("openai-compatibility", cfg.OpenAICompatibility)
	appendCompat("bigmodel-coding", cfg.BigModelCodingAPIKey)
	appendCompat("astron-code", cfg.AstronCodeAPIKey)
	appendCompat("agnes", cfg.AgnesAPIKey)
	return channels
}

func dedupeVideoGenerationChannels(channels []videoGenerationChannel) []videoGenerationChannel {
	seen := make(map[string]struct{}, len(channels))
	out := make([]videoGenerationChannel, 0, len(channels))
	for _, channel := range channels {
		channel.Provider = strings.TrimSpace(channel.Provider)
		channel.Model = strings.TrimSpace(channel.Model)
		channel.Type = strings.TrimSpace(channel.Type)
		if channel.Provider == "" || channel.Model == "" {
			continue
		}
		key := strings.ToLower(channel.Provider + "\x00" + channel.Model + "\x00" + channel.Type)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, channel)
	}
	return out
}

func (h *Handler) createVideoGenerationTask() *videoGenerationTestTask {
	task := &videoGenerationTestTask{
		TaskID:    newVideoGenerationTaskID(),
		Status:    "queued",
		Phase:     "queued",
		CreatedAt: time.Now(),
	}
	h.videoTasksMu.Lock()
	defer h.videoTasksMu.Unlock()
	if h.videoGenerationTasks == nil {
		h.videoGenerationTasks = make(map[string]*videoGenerationTestTask)
	}
	h.purgeVideoGenerationTasksLocked(time.Now())
	h.videoGenerationTasks[task.TaskID] = task
	return task
}

func (h *Handler) getVideoGenerationTask(taskID string) (*videoGenerationTestTask, bool) {
	if h == nil {
		return nil, false
	}
	h.videoTasksMu.Lock()
	defer h.videoTasksMu.Unlock()
	task, ok := h.videoGenerationTasks[taskID]
	if !ok || task == nil {
		return nil, false
	}
	return task.snapshot(), true
}

func (h *Handler) updateVideoGenerationTask(taskID string, update func(*videoGenerationTestTask)) {
	if h == nil || update == nil {
		return
	}
	h.videoTasksMu.Lock()
	defer h.videoTasksMu.Unlock()
	if task := h.videoGenerationTasks[taskID]; task != nil {
		update(task)
	}
}

func (h *Handler) finishVideoGenerationTask(taskID string, completedAt time.Time, result json.RawMessage, taskErr *videoGenerationTestTaskError) {
	h.updateVideoGenerationTask(taskID, func(task *videoGenerationTestTask) {
		task.CompletedAt = completedAt
		if !task.StartedAt.IsZero() {
			task.ElapsedMS = completedAt.Sub(task.StartedAt).Milliseconds()
		}
		if taskErr != nil {
			task.Status = "failed"
			task.Error = taskErr
			return
		}
		task.Status = "succeeded"
		task.Phase = "completed"
		task.Result = result
	})
}

func (task *videoGenerationTestTask) snapshot() *videoGenerationTestTask {
	if task == nil {
		return nil
	}
	clone := *task
	if len(task.Result) > 0 {
		clone.Result = bytes.Clone(task.Result)
	}
	return &clone
}

func (h *Handler) purgeVideoGenerationTasksLocked(now time.Time) {
	if h == nil || len(h.videoGenerationTasks) == 0 {
		return
	}
	for id, task := range h.videoGenerationTasks {
		if task == nil || now.Sub(task.CreatedAt) > videoGenerationTaskMaxAge {
			delete(h.videoGenerationTasks, id)
		}
	}
	if len(h.videoGenerationTasks) <= videoGenerationTaskMaxRetained {
		return
	}
	for id, task := range h.videoGenerationTasks {
		if len(h.videoGenerationTasks) <= videoGenerationTaskMaxRetained {
			return
		}
		if task == nil || task.Status == "succeeded" || task.Status == "failed" {
			delete(h.videoGenerationTasks, id)
		}
	}
}

func newVideoGenerationTaskID() string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err == nil {
		return "vid_" + hex.EncodeToString(buf[:])
	}
	return fmt.Sprintf("vid_%d", time.Now().UnixNano())
}
