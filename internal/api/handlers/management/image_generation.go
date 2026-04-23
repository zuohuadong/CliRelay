package management

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

const (
	imageGenerationModel = "gpt-image-2"
	imageGenerationAlt   = "images/generations"
	imageEditsAlt        = "images/edits"
	imageMaxUploads      = 5
)

func (h *Handler) PostImageGenerationTest(c *gin.Context) {
	payload, alt, err := parseImageGenerationTestPayload(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth manager unavailable"})
		return
	}

	cliCtx := context.WithValue(c.Request.Context(), util.ContextKeyGin, c)
	c.Set("apiKey", "POST /image-generation/test")
	imageCount, err := imageGenerationRequestCount(payload)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	payloads := make([][]byte, 0, imageCount)
	for i := 0; i < imageCount; i++ {
		execPayload := payload
		if imageCount > 1 {
			var setErr error
			execPayload, setErr = setImageGenerationRequestCount(payload, 1)
			if setErr != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid image generation request"})
				return
			}
		}
		resp, execErr := h.authManager.Execute(cliCtx, []string{"codex"}, coreexecutor.Request{
			Model:   "",
			Payload: execPayload,
			Format:  sdktranslator.FromString("openai"),
		}, coreexecutor.Options{
			Alt:          alt,
			SourceFormat: sdktranslator.FromString("openai"),
			Metadata: map[string]any{
				coreexecutor.SinglePickMetadataKey: true,
			},
		})
		if execErr != nil {
			status := http.StatusBadGateway
			if statusErr, ok := execErr.(coreexecutor.StatusError); ok && statusErr.StatusCode() > 0 {
				status = statusErr.StatusCode()
			}
			c.JSON(status, gin.H{"error": execErr.Error()})
			return
		}
		payloads = append(payloads, resp.Payload)
	}
	mergedPayload, err := mergeImageGenerationResponses(payloads)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	c.Data(http.StatusOK, "application/json; charset=utf-8", mergedPayload)
}

func imageGenerationRequestCount(payload []byte) (int, error) {
	var body struct {
		N int `json:"n"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return 0, fmt.Errorf("invalid image generation request")
	}
	if body.N == 0 {
		return 1, nil
	}
	if body.N < 1 || body.N > 4 {
		return 0, fmt.Errorf("n must be between 1 and 4")
	}
	return body.N, nil
}

func setImageGenerationRequestCount(payload []byte, n int) ([]byte, error) {
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, err
	}
	body["n"] = n
	return json.Marshal(body)
}

func mergeImageGenerationResponses(payloads [][]byte) ([]byte, error) {
	if len(payloads) == 0 {
		return nil, fmt.Errorf("image generation returned no responses")
	}
	if len(payloads) == 1 {
		return payloads[0], nil
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(payloads[0], &merged); err != nil {
		return nil, fmt.Errorf("parse image generation response: %w", err)
	}
	data := make([]json.RawMessage, 0, len(payloads))
	for _, payload := range payloads {
		var item struct {
			Data []json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(payload, &item); err != nil {
			return nil, fmt.Errorf("parse image generation response: %w", err)
		}
		data = append(data, item.Data...)
	}
	encodedData, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("encode image generation response: %w", err)
	}
	merged["data"] = encodedData
	return json.Marshal(merged)
}

func parseImageGenerationTestPayload(c *gin.Context) ([]byte, string, error) {
	contentType := strings.TrimSpace(c.GetHeader("Content-Type"))
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err == nil && strings.EqualFold(mediaType, "multipart/form-data") {
		return nil, "", fmt.Errorf("image edits are temporarily disabled")
	}

	var body struct {
		Model   string `json:"model"`
		Prompt  string `json:"prompt"`
		Size    string `json:"size"`
		Quality string `json:"quality"`
		N       int    `json:"n"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		return nil, "", fmt.Errorf("invalid body")
	}
	model := strings.TrimSpace(body.Model)
	if model == "" {
		model = imageGenerationModel
	}
	prompt := strings.TrimSpace(body.Prompt)
	if prompt == "" {
		return nil, "", fmt.Errorf("prompt is required")
	}
	payload := map[string]any{
		"model":  model,
		"prompt": prompt,
	}
	if size := strings.TrimSpace(body.Size); size != "" {
		payload["size"] = size
	}
	if quality := strings.TrimSpace(body.Quality); quality != "" {
		payload["quality"] = quality
	}
	if body.N > 0 {
		payload["n"] = body.N
	}
	data, _ := json.Marshal(payload)
	return data, imageGenerationAlt, nil
}

func parseImageGenerationMultipartPayload(c *gin.Context) ([]byte, string, error) {
	if err := c.Request.ParseMultipartForm(20 << 20); err != nil {
		return nil, "", fmt.Errorf("invalid body")
	}
	form := c.Request.MultipartForm
	if form == nil {
		return nil, "", fmt.Errorf("invalid body")
	}
	model := firstImageGenerationFormValue(form.Value, "model", imageGenerationModel)
	prompt := firstImageGenerationFormValue(form.Value, "prompt", "")
	if strings.TrimSpace(prompt) == "" {
		return nil, "", fmt.Errorf("prompt is required")
	}
	payload := map[string]any{
		"model":  model,
		"prompt": prompt,
	}
	if size := firstImageGenerationFormValue(form.Value, "size", ""); size != "" {
		payload["size"] = size
	}
	if quality := firstImageGenerationFormValue(form.Value, "quality", ""); quality != "" {
		payload["quality"] = quality
	}
	if value := firstImageGenerationFormValue(form.Value, "n", ""); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return nil, "", fmt.Errorf("n must be a positive integer")
		}
		payload["n"] = n
	}
	files := form.File["image"]
	for key, value := range form.File {
		if strings.HasPrefix(key, "image[") {
			files = append(files, value...)
		}
	}
	if len(files) == 0 {
		return nil, "", fmt.Errorf("image file is required")
	}
	if len(files) > imageMaxUploads {
		return nil, "", fmt.Errorf("image edit supports at most %d images", imageMaxUploads)
	}
	uploads := make([]map[string]any, 0, len(files))
	for _, fileHeader := range files {
		if fileHeader == nil {
			continue
		}
		file, err := fileHeader.Open()
		if err != nil {
			return nil, "", fmt.Errorf("read image file: %w", err)
		}
		data, err := io.ReadAll(io.LimitReader(file, (20<<20)+1))
		_ = file.Close()
		if err != nil {
			return nil, "", fmt.Errorf("read image file: %w", err)
		}
		if len(data) == 0 {
			return nil, "", fmt.Errorf("image file is empty")
		}
		if len(data) > 20<<20 {
			return nil, "", fmt.Errorf("image file is too large")
		}
		uploads = append(uploads, map[string]any{
			"file_name":    fileHeader.Filename,
			"content_type": strings.TrimSpace(fileHeader.Header.Get("Content-Type")),
			"data_base64":  base64.StdEncoding.EncodeToString(data),
		})
	}
	payload["image_files"] = uploads
	data, _ := json.Marshal(payload)
	return data, imageEditsAlt, nil
}

func firstImageGenerationFormValue(values map[string][]string, key, fallback string) string {
	for _, value := range values[key] {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return fallback
}

func (h *Handler) ListImageGenerationChannels(c *gin.Context) {
	channels := make([]string, 0)
	seen := make(map[string]struct{})
	if h != nil && h.authManager != nil {
		for _, auth := range h.authManager.List() {
			if auth == nil || auth.Disabled {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
				continue
			}
			accountType, _ := auth.AccountInfo()
			if !strings.EqualFold(strings.TrimSpace(accountType), "oauth") {
				continue
			}
			if auth.Status == coreauth.StatusDisabled {
				continue
			}
			name := strings.TrimSpace(auth.ChannelName())
			if name == "" {
				continue
			}
			key := strings.ToLower(name)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			channels = append(channels, name)
		}
	}
	sort.Strings(channels)
	c.JSON(http.StatusOK, gin.H{
		"model":    imageGenerationModel,
		"channels": channels,
	})
}
