package management

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	corehandlers "github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	openaihandlers "github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const (
	imageGenerationDefaultModel       = "gpt-image-2"
	imageGenerationMaxUploadBytes     = 20 << 20
	imageGenerationTaskMaxAge         = time.Hour
	imageGenerationTaskMaxRetained    = 100
	imageGenerationTestPhaseQueued    = "queued"
	imageGenerationTestPhaseRunning   = "conversation_request"
	imageGenerationTestPhaseCompleted = "completed"
)

type imageGenerationTestRequest struct {
	Mode    string `json:"mode"`
	Model   string `json:"model"`
	Prompt  string `json:"prompt"`
	Size    string `json:"size"`
	Quality string `json:"quality"`
	N       int    `json:"n"`
}

type imageGenerationUploadedImage struct {
	Filename    string
	ContentType string
	Data        []byte
}

type imageGenerationTestTask struct {
	TaskID      string                        `json:"task_id"`
	Status      string                        `json:"status"`
	Phase       string                        `json:"phase,omitempty"`
	ElapsedMS   int64                         `json:"elapsed_ms,omitempty"`
	Result      json.RawMessage               `json:"result,omitempty"`
	Error       *imageGenerationTestTaskError `json:"error,omitempty"`
	CreatedAt   time.Time                     `json:"-"`
	StartedAt   time.Time                     `json:"-"`
	CompletedAt time.Time                     `json:"-"`
}

type imageGenerationTestTaskError struct {
	Status int `json:"status,omitempty"`
	Body   any `json:"body,omitempty"`
}

type imageGenerationChannel struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Type     string `json:"type"`
}

func (h *Handler) GetImageGenerationChannels(c *gin.Context) {
	cfg := h.currentImageGenerationConfig()
	if cfg == nil || cfg.DisableImageGeneration == config.DisableImageGenerationAll {
		c.JSON(http.StatusOK, gin.H{"items": []imageGenerationChannel{}})
		return
	}

	channels := imageGenerationChannelsFromRegistry(imageGenerationDefaultModel)
	channels = append(channels, configuredOpenAICompatImageChannels(cfg)...)
	c.JSON(http.StatusOK, gin.H{"items": dedupeImageGenerationChannels(channels)})
}

func (h *Handler) StartImageGenerationTest(c *gin.Context) {
	mode := strings.ToLower(strings.TrimSpace(c.PostForm("mode")))
	contentType := strings.ToLower(strings.TrimSpace(c.GetHeader("Content-Type")))
	if strings.HasPrefix(contentType, "multipart/form-data") {
		h.startImageEditTest(c)
		return
	}
	if mode == "edits" {
		h.startImageEditTest(c)
		return
	}
	h.startImageTextGenerationTest(c)
}

func (h *Handler) GetImageGenerationTest(c *gin.Context) {
	taskID := strings.TrimSpace(c.Param("task_id"))
	if taskID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing task_id"})
		return
	}
	task, ok := h.getImageGenerationTask(taskID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "image generation test task not found"})
		return
	}
	c.JSON(http.StatusOK, task.snapshot())
}

func (h *Handler) startImageTextGenerationTest(c *gin.Context) {
	var body imageGenerationTestRequest
	if errBind := c.ShouldBindJSON(&body); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	body.Mode = "generations"
	if errValidate := normalizeImageGenerationTestRequest(&body); errValidate != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errValidate.Error()})
		return
	}
	task := h.createImageGenerationTask()
	go h.runImageGenerationTask(task.TaskID, body, nil)
	c.JSON(http.StatusOK, task.snapshot())
}

func (h *Handler) startImageEditTest(c *gin.Context) {
	if errParse := c.Request.ParseMultipartForm(imageGenerationMaxUploadBytes); errParse != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid multipart body: %v", errParse)})
		return
	}
	body := imageGenerationTestRequest{
		Mode:    "edits",
		Model:   c.PostForm("model"),
		Prompt:  c.PostForm("prompt"),
		Size:    c.PostForm("size"),
		Quality: c.PostForm("quality"),
	}
	if n := strings.TrimSpace(c.PostForm("n")); n != "" {
		var parsed int
		if _, errScan := fmt.Sscanf(n, "%d", &parsed); errScan == nil {
			body.N = parsed
		}
	}
	if errValidate := normalizeImageGenerationTestRequest(&body); errValidate != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errValidate.Error()})
		return
	}

	images, errImages := readImageGenerationUploads(c.Request.MultipartForm)
	if errImages != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errImages.Error()})
		return
	}
	if len(images) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "at least one image is required"})
		return
	}

	task := h.createImageGenerationTask()
	go h.runImageGenerationTask(task.TaskID, body, images)
	c.JSON(http.StatusOK, task.snapshot())
}

func normalizeImageGenerationTestRequest(body *imageGenerationTestRequest) error {
	if body == nil {
		return fmt.Errorf("invalid body")
	}
	body.Model = strings.TrimSpace(body.Model)
	if body.Model == "" {
		body.Model = imageGenerationDefaultModel
	}
	body.Prompt = strings.TrimSpace(body.Prompt)
	if body.Prompt == "" {
		return fmt.Errorf("prompt is required")
	}
	body.Size = strings.TrimSpace(body.Size)
	body.Quality = strings.TrimSpace(body.Quality)
	if body.N < 0 {
		body.N = 0
	}
	return nil
}

func readImageGenerationUploads(form *multipart.Form) ([]imageGenerationUploadedImage, error) {
	if form == nil || len(form.File) == 0 {
		return nil, nil
	}
	fileHeaders := form.File["image"]
	if len(fileHeaders) == 0 {
		return nil, nil
	}
	images := make([]imageGenerationUploadedImage, 0, len(fileHeaders))
	for _, fileHeader := range fileHeaders {
		if fileHeader == nil {
			continue
		}
		if fileHeader.Size > imageGenerationMaxUploadBytes {
			return nil, fmt.Errorf("image %s is too large", fileHeader.Filename)
		}
		file, errOpen := fileHeader.Open()
		if errOpen != nil {
			return nil, fmt.Errorf("failed to open image %s", fileHeader.Filename)
		}
		data, errRead := io.ReadAll(io.LimitReader(file, imageGenerationMaxUploadBytes+1))
		errClose := file.Close()
		if errRead != nil {
			return nil, fmt.Errorf("failed to read image %s", fileHeader.Filename)
		}
		if errClose != nil {
			return nil, fmt.Errorf("failed to close image %s", fileHeader.Filename)
		}
		if len(data) > imageGenerationMaxUploadBytes {
			return nil, fmt.Errorf("image %s is too large", fileHeader.Filename)
		}
		contentType := fileHeader.Header.Get("Content-Type")
		if strings.TrimSpace(contentType) == "" {
			contentType = http.DetectContentType(data)
		}
		images = append(images, imageGenerationUploadedImage{
			Filename:    fileHeader.Filename,
			ContentType: contentType,
			Data:        data,
		})
	}
	return images, nil
}

func (h *Handler) runImageGenerationTask(taskID string, body imageGenerationTestRequest, images []imageGenerationUploadedImage) {
	h.updateImageGenerationTask(taskID, func(task *imageGenerationTestTask) {
		task.Status = "running"
		task.Phase = imageGenerationTestPhaseRunning
		task.StartedAt = time.Now()
	})

	recorder, errRun := h.executeImageGenerationTest(body, images)
	completedAt := time.Now()
	if errRun != nil {
		h.finishImageGenerationTask(taskID, completedAt, nil, &imageGenerationTestTaskError{
			Status: http.StatusInternalServerError,
			Body:   gin.H{"error": gin.H{"message": errRun.Error()}},
		})
		return
	}

	statusCode := recorder.Code
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	responseBody := bytes.TrimSpace(recorder.Body.Bytes())
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		h.finishImageGenerationTask(taskID, completedAt, nil, &imageGenerationTestTaskError{
			Status: statusCode,
			Body:   decodeImageGenerationBody(responseBody),
		})
		return
	}
	if !json.Valid(responseBody) {
		h.finishImageGenerationTask(taskID, completedAt, nil, &imageGenerationTestTaskError{
			Status: statusCode,
			Body:   gin.H{"error": gin.H{"message": "image generation returned invalid JSON"}},
		})
		return
	}
	h.finishImageGenerationTask(taskID, completedAt, json.RawMessage(bytes.Clone(responseBody)), nil)
}

func (h *Handler) executeImageGenerationTest(body imageGenerationTestRequest, images []imageGenerationUploadedImage) (*httptest.ResponseRecorder, error) {
	cfg, manager, pluginHost := h.currentImageGenerationRuntime()
	if cfg == nil {
		return nil, fmt.Errorf("config is not available")
	}
	if cfg.DisableImageGeneration == config.DisableImageGenerationAll {
		return nil, fmt.Errorf("image generation is disabled")
	}
	base := corehandlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
	base.SetPluginHost(pluginHost)
	handler := openaihandlers.NewOpenAIAPIHandler(base)

	var req *http.Request
	var errBuild error
	if len(images) > 0 || strings.EqualFold(body.Mode, "edits") {
		req, errBuild = buildImageGenerationEditRequest(body, images)
	} else {
		req, errBuild = buildImageGenerationJSONRequest(body)
	}
	if errBuild != nil {
		return nil, errBuild
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = req
	if len(images) > 0 || strings.EqualFold(body.Mode, "edits") {
		handler.ImagesEdits(ctx)
	} else {
		handler.ImagesGenerations(ctx)
	}
	return recorder, nil
}

func buildImageGenerationJSONRequest(body imageGenerationTestRequest) (*http.Request, error) {
	payload := map[string]any{
		"model":  body.Model,
		"prompt": body.Prompt,
	}
	if body.Size != "" {
		payload["size"] = body.Size
	}
	if body.Quality != "" {
		payload["quality"] = body.Quality
	}
	if body.N > 0 {
		payload["n"] = body.N
	}
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, errMarshal
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func buildImageGenerationEditRequest(body imageGenerationTestRequest, images []imageGenerationUploadedImage) (*http.Request, error) {
	buf := &bytes.Buffer{}
	writer := multipart.NewWriter(buf)
	if err := writer.WriteField("model", body.Model); err != nil {
		return nil, err
	}
	if err := writer.WriteField("prompt", body.Prompt); err != nil {
		return nil, err
	}
	if body.Size != "" {
		if err := writer.WriteField("size", body.Size); err != nil {
			return nil, err
		}
	}
	if body.Quality != "" {
		if err := writer.WriteField("quality", body.Quality); err != nil {
			return nil, err
		}
	}
	if body.N > 0 {
		if err := writer.WriteField("n", fmt.Sprintf("%d", body.N)); err != nil {
			return nil, err
		}
	}
	for _, image := range images {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="image"; filename="%s"`, escapeImageGenerationMultipartFilename(image.Filename)))
		if strings.TrimSpace(image.ContentType) != "" {
			header.Set("Content-Type", image.ContentType)
		}
		part, errPart := writer.CreatePart(header)
		if errPart != nil {
			return nil, errPart
		}
		if _, errWrite := part.Write(image.Data); errWrite != nil {
			return nil, errWrite
		}
	}
	if errClose := writer.Close(); errClose != nil {
		return nil, errClose
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req, nil
}

func escapeImageGenerationMultipartFilename(filename string) string {
	filename = strings.ReplaceAll(filename, "\\", "\\\\")
	filename = strings.ReplaceAll(filename, `"`, `\"`)
	return filename
}

func decodeImageGenerationBody(raw []byte) any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err == nil {
		return decoded
	}
	return gin.H{"error": gin.H{"message": string(raw)}}
}

func (h *Handler) currentImageGenerationConfig() *config.Config {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg
}

func (h *Handler) currentImageGenerationRuntime() (*config.Config, *coreauth.Manager, corehandlers.PluginInterceptorHost) {
	if h == nil {
		return nil, nil, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg, h.authManager, h.pluginHost
}

func imageGenerationChannelsFromRegistry(model string) []imageGenerationChannel {
	providers := registry.GetGlobalRegistry().GetModelProviders(model)
	channels := make([]imageGenerationChannel, 0, len(providers))
	for _, provider := range providers {
		provider = strings.TrimSpace(provider)
		if provider == "" {
			continue
		}
		channels = append(channels, imageGenerationChannel{Provider: provider, Model: model, Type: "registered"})
	}
	return channels
}

func configuredOpenAICompatImageChannels(cfg *config.Config) []imageGenerationChannel {
	if cfg == nil {
		return nil
	}
	channels := make([]imageGenerationChannel, 0)
	appendCompat := func(provider string, entries []config.OpenAICompatibility) {
		for _, entry := range entries {
			if entry.Disabled || len(entry.APIKeyEntries) == 0 {
				continue
			}
			if !hasEnabledOpenAICompatKey(entry.APIKeyEntries) {
				continue
			}
			for _, model := range entry.Models {
				modelID := strings.TrimSpace(model.Alias)
				if modelID == "" {
					modelID = strings.TrimSpace(model.Name)
				}
				if modelID == "" {
					continue
				}
				if model.Image || strings.EqualFold(modelID, imageGenerationDefaultModel) {
					channels = append(channels, imageGenerationChannel{Provider: provider, Model: modelID, Type: "openai-compatible"})
				}
			}
		}
	}
	appendCompat("openai-compatibility", cfg.OpenAICompatibility)
	appendCompat("bigmodel-coding", cfg.BigModelCodingAPIKey)
	appendCompat("astron-code", cfg.AstronCodeAPIKey)
	return channels
}

func hasEnabledOpenAICompatKey(entries []config.OpenAICompatibilityAPIKey) bool {
	for _, entry := range entries {
		if !entry.Disabled && strings.TrimSpace(entry.APIKey) != "" {
			return true
		}
	}
	return false
}

func dedupeImageGenerationChannels(channels []imageGenerationChannel) []imageGenerationChannel {
	seen := make(map[string]struct{}, len(channels))
	out := make([]imageGenerationChannel, 0, len(channels))
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

func (h *Handler) createImageGenerationTask() *imageGenerationTestTask {
	task := &imageGenerationTestTask{
		TaskID:    newImageGenerationTaskID(),
		Status:    "queued",
		Phase:     imageGenerationTestPhaseQueued,
		CreatedAt: time.Now(),
	}
	h.imageTasksMu.Lock()
	defer h.imageTasksMu.Unlock()
	if h.imageGenerationTasks == nil {
		h.imageGenerationTasks = make(map[string]*imageGenerationTestTask)
	}
	h.purgeImageGenerationTasksLocked(time.Now())
	h.imageGenerationTasks[task.TaskID] = task
	return task
}

func (h *Handler) getImageGenerationTask(taskID string) (*imageGenerationTestTask, bool) {
	if h == nil {
		return nil, false
	}
	h.imageTasksMu.Lock()
	defer h.imageTasksMu.Unlock()
	task, ok := h.imageGenerationTasks[taskID]
	if !ok || task == nil {
		return nil, false
	}
	return task.snapshot(), true
}

func (h *Handler) updateImageGenerationTask(taskID string, update func(*imageGenerationTestTask)) {
	if h == nil || update == nil {
		return
	}
	h.imageTasksMu.Lock()
	defer h.imageTasksMu.Unlock()
	if task := h.imageGenerationTasks[taskID]; task != nil {
		update(task)
	}
}

func (h *Handler) finishImageGenerationTask(taskID string, completedAt time.Time, result json.RawMessage, taskErr *imageGenerationTestTaskError) {
	h.updateImageGenerationTask(taskID, func(task *imageGenerationTestTask) {
		task.CompletedAt = completedAt
		task.ElapsedMS = completedAt.Sub(task.StartedAt).Milliseconds()
		if taskErr != nil {
			task.Status = "failed"
			task.Error = taskErr
			return
		}
		task.Status = "succeeded"
		task.Phase = imageGenerationTestPhaseCompleted
		task.Result = result
	})
}

func (task *imageGenerationTestTask) snapshot() *imageGenerationTestTask {
	if task == nil {
		return nil
	}
	clone := *task
	if len(task.Result) > 0 {
		clone.Result = bytes.Clone(task.Result)
	}
	return &clone
}

func (h *Handler) purgeImageGenerationTasksLocked(now time.Time) {
	if h == nil || len(h.imageGenerationTasks) == 0 {
		return
	}
	for id, task := range h.imageGenerationTasks {
		if task == nil || now.Sub(task.CreatedAt) > imageGenerationTaskMaxAge {
			delete(h.imageGenerationTasks, id)
		}
	}
	if len(h.imageGenerationTasks) <= imageGenerationTaskMaxRetained {
		return
	}
	for id, task := range h.imageGenerationTasks {
		if len(h.imageGenerationTasks) <= imageGenerationTaskMaxRetained {
			return
		}
		if task == nil || task.Status == "succeeded" || task.Status == "failed" {
			delete(h.imageGenerationTasks, id)
		}
	}
}

func newImageGenerationTaskID() string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err == nil {
		return "img_" + hex.EncodeToString(buf[:])
	}
	return fmt.Sprintf("img_%d", time.Now().UnixNano())
}
