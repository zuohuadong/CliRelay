package openai

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	internalvideo "github.com/router-for-me/CLIProxyAPI/v7/internal/video"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	videosPath               = "/v1/videos"
	openAIVideosPath         = "/openai/v1/videos"
	xaiVideosGenerationsAPI  = "/v1/videos/generations"
	xaiVideosEditsAPI        = "/v1/videos/edits"
	xaiVideosExtensionsAPI   = "/v1/videos/extensions"
	defaultXAIVideosModel    = "grok-imagine-video"
	defaultOpenAIVideosModel = defaultXAIVideosModel
	xaiVideos15PreviewModel  = "grok-imagine-video-1.5-preview"
	xaiVideosHandlerType     = "openai-video"
	defaultVideosSeconds     = "4"
	defaultVideosSize        = "720x1280"
	defaultVideosResolution  = "720p"
	maxXAIVideoReferences    = 7
)

const defaultVideoAuthBindingTTL = 3 * time.Hour

var videoAuthBindings = newVideoAuthBindingStore()

type xaiVideoCreateMetadata struct {
	Model         string
	UpstreamModel string
	Prompt        string
	Seconds       string
	Size          string
	CreatedAt     int64
}

type videoAuthBinding struct {
	authID          string
	model           string
	upstreamVideoID string
	expiresAt       time.Time
}

type videoAuthBindingStore struct {
	mu      sync.RWMutex
	entries map[string]videoAuthBinding
}

func newVideoAuthBindingStore() *videoAuthBindingStore {
	return &videoAuthBindingStore{
		entries: make(map[string]videoAuthBinding),
	}
}

func (s *videoAuthBindingStore) set(videoID string, authID string, ttl time.Duration) {
	s.setWithModel(videoID, authID, "", ttl)
}

func (s *videoAuthBindingStore) setWithModel(videoID string, authID string, model string, ttl time.Duration) {
	s.setWithModelAndUpstream(videoID, authID, model, "", ttl)
}

func (s *videoAuthBindingStore) setWithModelAndUpstream(videoID string, authID string, model string, upstreamVideoID string, ttl time.Duration) {
	if s == nil {
		return
	}
	videoID = strings.TrimSpace(videoID)
	authID = strings.TrimSpace(authID)
	if videoID == "" || authID == "" {
		return
	}
	if ttl <= 0 {
		ttl = defaultVideoAuthBindingTTL
	}
	now := time.Now()
	s.mu.Lock()
	s.cleanupExpiredLocked(now)
	if upstreamVideoID == "" {
		if current, exists := s.entries[videoID]; exists {
			upstreamVideoID = current.upstreamVideoID
		}
	}
	s.entries[videoID] = videoAuthBinding{
		authID:          authID,
		model:           strings.TrimSpace(model),
		upstreamVideoID: strings.TrimSpace(upstreamVideoID),
		expiresAt:       now.Add(ttl),
	}
	s.mu.Unlock()
}

func (s *videoAuthBindingStore) get(videoID string) (string, bool) {
	binding, ok := s.getBinding(videoID)
	if !ok {
		return "", false
	}
	return binding.authID, true
}

func (s *videoAuthBindingStore) getBinding(videoID string) (videoAuthBinding, bool) {
	if s == nil {
		return videoAuthBinding{}, false
	}
	videoID = strings.TrimSpace(videoID)
	if videoID == "" {
		return videoAuthBinding{}, false
	}
	now := time.Now()
	s.mu.RLock()
	entry, ok := s.entries[videoID]
	s.mu.RUnlock()
	if !ok {
		return videoAuthBinding{}, false
	}
	if now.After(entry.expiresAt) {
		s.mu.Lock()
		if current, exists := s.entries[videoID]; exists && now.After(current.expiresAt) {
			delete(s.entries, videoID)
		}
		s.mu.Unlock()
		return videoAuthBinding{}, false
	}
	return entry, true
}

func (s *videoAuthBindingStore) cleanupExpiredLocked(now time.Time) {
	for videoID, entry := range s.entries {
		if now.After(entry.expiresAt) {
			delete(s.entries, videoID)
		}
	}
}

func videosModelBase(model string) string {
	_, baseModel := imagesModelParts(model)
	return strings.ToLower(strings.TrimSpace(baseModel))
}

func isXAIVideosModel(model string) bool {
	prefix, baseModel := imagesModelParts(model)
	baseModel = strings.ToLower(strings.TrimSpace(baseModel))
	if baseModel != defaultXAIVideosModel && baseModel != xaiVideos15PreviewModel {
		return false
	}

	prefix = strings.ToLower(strings.TrimSpace(prefix))
	return prefix == "" || prefix == "xai" || prefix == "x-ai" || prefix == "grok"
}

func isOpenAICompatVideosModel(model string) bool {
	info := registry.LookupModelInfo(strings.TrimSpace(model))
	return info != nil && info.Type == registry.OpenAIVideoModelType
}

func isSupportedVideosModel(model string) bool {
	return isXAIVideosModel(model) || isOpenAICompatVideosModel(model)
}

func rejectUnsupportedVideosModel(c *gin.Context, model string) bool {
	if isSupportedVideosModel(model) {
		return false
	}

	path := strings.TrimSpace(c.Request.URL.Path)
	if path == "" {
		path = openAIVideosPath
	}
	writeVideosFailedError(c, http.StatusBadRequest, model, "invalid_request_error", fmt.Sprintf("Model %s is not supported on %s. Use %s.", model, path, defaultOpenAIVideosModel))
	return true
}

func rejectUnsupportedNativeVideosModel(c *gin.Context, model string) bool {
	if isXAIVideosModel(model) {
		return false
	}

	c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
		Error: handlers.ErrorDetail{
			Message: fmt.Sprintf("Model %s is not supported on %s, %s, or %s. Use %s.", model, xaiVideosGenerationsAPI, xaiVideosEditsAPI, xaiVideosExtensionsAPI, defaultXAIVideosModel),
			Type:    "invalid_request_error",
		},
	})
	return true
}

func canonicalXAIVideosModel(model string) string {
	switch videosModelBase(model) {
	case defaultXAIVideosModel:
		return defaultXAIVideosModel
	case xaiVideos15PreviewModel:
		return xaiVideos15PreviewModel
	}
	return defaultXAIVideosModel
}

func responseVideosModel(model string) string {
	return canonicalXAIVideosModel(model)
}

func readVideosCreateRequest(c *gin.Context) ([]byte, error) {
	contentType := strings.ToLower(strings.TrimSpace(c.ContentType()))
	switch contentType {
	case "multipart/form-data", "application/x-www-form-urlencoded":
		return videosCreateRequestFromForm(c)
	default:
		rawJSON, err := handlers.ReadRequestBody(c)
		if err != nil {
			return nil, err
		}
		if !json.Valid(rawJSON) {
			return nil, fmt.Errorf("body must be valid JSON")
		}
		return rawJSON, nil
	}
}

func readXAIVideosNativeRequest(c *gin.Context) ([]byte, error) {
	rawJSON, err := handlers.ReadRequestBody(c)
	if err != nil {
		return nil, err
	}
	if !json.Valid(rawJSON) {
		return nil, fmt.Errorf("body must be valid JSON")
	}
	return rawJSON, nil
}

func videosCreateRequestFromForm(c *gin.Context) ([]byte, error) {
	if strings.EqualFold(strings.TrimSpace(c.ContentType()), "multipart/form-data") {
		for _, key := range []string{"input_reference", "input_reference[file]", "image"} {
			if file, err := c.FormFile(key); err == nil && file != nil {
				return nil, fmt.Errorf("input_reference file uploads are not supported by CliRelay yet; upload the image separately and pass input_reference.image_url")
			}
		}
	}
	rawJSON := []byte(`{}`)
	for _, field := range []string{"model", "prompt", "seconds", "size", "aspect_ratio", "resolution"} {
		if value := strings.TrimSpace(c.PostForm(field)); value != "" {
			rawJSON, _ = sjson.SetBytes(rawJSON, field, value)
		}
	}
	if value := strings.TrimSpace(firstPostForm(c, "input_reference[image_url]", "input_reference.image_url", "image_url")); value != "" {
		rawJSON, _ = sjson.SetBytes(rawJSON, "input_reference.image_url", value)
	}
	if value := strings.TrimSpace(firstPostForm(c, "input_reference[file_id]", "input_reference.file_id", "file_id")); value != "" {
		rawJSON, _ = sjson.SetBytes(rawJSON, "input_reference.file_id", value)
	}
	if refs := strings.TrimSpace(c.PostForm("reference_image_urls")); refs != "" {
		for _, ref := range strings.Split(refs, ",") {
			if ref = strings.TrimSpace(ref); ref != "" {
				rawJSON, _ = sjson.SetBytes(rawJSON, "reference_image_urls.-1", ref)
			}
		}
	}
	return rawJSON, nil
}

func firstPostForm(c *gin.Context, keys ...string) string {
	for _, key := range keys {
		if value := c.PostForm(key); strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (h *OpenAIAPIHandler) videoAuthBindingTTL() time.Duration {
	if h != nil && h.BaseAPIHandler != nil && h.Cfg != nil {
		raw := strings.TrimSpace(h.Cfg.VideoResultAuthCacheTTL)
		if raw != "" {
			if ttl, err := time.ParseDuration(raw); err == nil && ttl > 0 {
				return ttl
			}
		}
	}
	return defaultVideoAuthBindingTTL
}

func videoIDFromPayload(payload []byte) string {
	videoID := strings.TrimSpace(gjson.GetBytes(payload, "request_id").String())
	if videoID == "" {
		videoID = strings.TrimSpace(gjson.GetBytes(payload, "id").String())
	}
	return videoID
}

func videoBindingIDsFromPayload(payload []byte) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, 4)
	for _, path := range []string{"request_id", "id", "task_id", "video_id"} {
		value := strings.TrimSpace(gjson.GetBytes(payload, path).String())
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func upstreamVideoIDFromPayload(payload []byte) string {
	if videoID := strings.TrimSpace(gjson.GetBytes(payload, "video_id").String()); videoID != "" {
		return videoID
	}
	return videoIDFromPayload(payload)
}

func (h *OpenAIAPIHandler) bindVideoAuthIDFromPayload(payload []byte, authID string) {
	h.bindVideoAuthIDAndModelFromPayload(payload, authID, strings.TrimSpace(gjson.GetBytes(payload, "model").String()))
}

func (h *OpenAIAPIHandler) bindVideoAuthIDAndModelFromPayload(payload []byte, authID string, model string) {
	videoIDs := videoBindingIDsFromPayload(payload)
	if len(videoIDs) == 0 {
		return
	}
	upstreamVideoID := upstreamVideoIDFromPayload(payload)
	canonicalModel := canonicalVideoBindingModel(model)
	for _, videoID := range videoIDs {
		videoAuthBindings.setWithModelAndUpstream(videoID, authID, canonicalModel, upstreamVideoID, h.videoAuthBindingTTL())
	}
}

func (h *OpenAIAPIHandler) bindVideoAuthID(videoID string, authID string, model string) {
	videoAuthBindings.setWithModel(videoID, authID, canonicalVideoBindingModel(model), h.videoAuthBindingTTL())
}

func (h *OpenAIAPIHandler) contextWithVideoAuthBinding(ctx context.Context, videoID string) context.Context {
	if job, ok := h.durableVideoJob(ctx, videoID); ok && strings.TrimSpace(job.AuthID) != "" {
		return handlers.WithPinnedAuthID(ctx, job.AuthID)
	}
	if authID, ok := videoAuthBindings.get(videoID); ok {
		return handlers.WithPinnedAuthID(ctx, authID)
	}
	return ctx
}

func (h *OpenAIAPIHandler) modelWithVideoAuthBinding(ctx context.Context, videoID string, fallbackModel string) string {
	if job, ok := h.durableVideoJob(ctx, videoID); ok {
		if model := strings.TrimSpace(job.Model); model != "" {
			return model
		}
	}
	if binding, ok := videoAuthBindings.getBinding(videoID); ok {
		if model := strings.TrimSpace(binding.model); model != "" {
			return model
		}
	}
	return fallbackModel
}

func (h *OpenAIAPIHandler) payloadWithVideoAuthBinding(ctx context.Context, rawJSON []byte, videoID string) []byte {
	if job, ok := h.durableVideoJob(ctx, videoID); ok {
		upstreamVideoID := strings.TrimSpace(job.UpstreamID)
		if upstreamVideoID != "" && upstreamVideoID != videoID {
			rawJSON, _ = sjson.SetBytes(rawJSON, "request_id", upstreamVideoID)
			rawJSON, _ = sjson.SetBytes(rawJSON, "video_id", upstreamVideoID)
		}
		return rawJSON
	}
	binding, ok := videoAuthBindings.getBinding(videoID)
	if !ok {
		return rawJSON
	}
	if !isOpenAICompatVideosModel(binding.model) {
		return rawJSON
	}
	upstreamVideoID := strings.TrimSpace(binding.upstreamVideoID)
	if upstreamVideoID == "" || upstreamVideoID == videoID {
		return rawJSON
	}
	rawJSON, _ = sjson.SetBytes(rawJSON, "video_id", upstreamVideoID)
	return rawJSON
}

func (h *OpenAIAPIHandler) durableVideoJob(ctx context.Context, videoID string) (internalvideo.Job, bool) {
	if h == nil || h.videoService == nil || strings.TrimSpace(videoID) == "" {
		return internalvideo.Job{}, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	job, err := h.videoService.GetJob(ctx, videoID)
	return job, err == nil
}

func shouldUseLegacyXAIVideoRetrieve(videoID string) bool {
	videoID = strings.TrimSpace(videoID)
	if videoID == "" {
		return false
	}
	if binding, ok := videoAuthBindings.getBinding(videoID); ok {
		upstreamVideoID := strings.TrimSpace(binding.upstreamVideoID)
		return isXAIVideosModel(binding.model) && (upstreamVideoID == "" || upstreamVideoID == videoID)
	}
	// Public task IDs are always generated in the video_ namespace. Preserve
	// the historical xAI GET route for pre-existing upstream IDs outside it.
	return !strings.HasPrefix(videoID, "video_")
}

func isCanonicalPublicVideoRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return false
	}
	path := strings.TrimRight(c.Request.URL.Path, "/")
	return path == videosPath || strings.HasPrefix(path, videosPath+"/")
}

func writePublicVideoRoutingError(c *gin.Context, status int, message string) {
	c.JSON(status, handlers.ErrorResponse{
		Error: handlers.ErrorDetail{
			Message: message,
			Type:    "video_routing_error",
		},
	})
}

func (h *OpenAIAPIHandler) requirePublicVideoService(c *gin.Context) bool {
	if !isCanonicalPublicVideoRequest(c) {
		return true
	}
	if h == nil || h.videoService == nil {
		writePublicVideoRoutingError(c, http.StatusServiceUnavailable, "Public video task persistence is unavailable")
		return false
	}
	if err := h.videoService.Ready(c.Request.Context()); err != nil {
		writePublicVideoRoutingError(c, http.StatusServiceUnavailable, "Public video task persistence is unavailable")
		return false
	}
	return true
}

func (h *OpenAIAPIHandler) requirePublicVideoJob(c *gin.Context, videoID string) (internalvideo.Job, bool) {
	if !isCanonicalPublicVideoRequest(c) {
		return internalvideo.Job{}, false
	}
	if !h.requirePublicVideoService(c) {
		return internalvideo.Job{}, false
	}
	job, err := h.videoService.GetJob(c.Request.Context(), videoID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writePublicVideoRoutingError(c, http.StatusNotFound, "Video task was not found")
		} else {
			writePublicVideoRoutingError(c, http.StatusServiceUnavailable, "Public video task persistence is unavailable")
		}
		return internalvideo.Job{}, false
	}
	if strings.TrimSpace(job.UpstreamID) == "" || strings.TrimSpace(job.AuthID) == "" || strings.TrimSpace(job.Model) == "" {
		writePublicVideoRoutingError(c, http.StatusServiceUnavailable, "Video task routing record is incomplete")
		return internalvideo.Job{}, false
	}
	return job, true
}

func canonicalVideoBindingModel(model string) string {
	if isOpenAICompatVideosModel(model) {
		return strings.TrimSpace(model)
	}
	return canonicalXAIVideosModel(model)
}

func (h *OpenAIAPIHandler) persistPublicVideoCreate(ctx context.Context, payload []byte, authID string, model string) ([]byte, error) {
	if h == nil || h.videoService == nil {
		h.bindVideoAuthIDAndModelFromPayload(payload, authID, model)
		return payload, nil
	}
	upstreamID := upstreamVideoIDFromPayload(payload)
	if upstreamID == "" {
		return nil, fmt.Errorf("video create response did not include an upstream task id")
	}
	provider := ""
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return nil, fmt.Errorf("video create did not select an upstream credential")
	}
	if h.AuthManager != nil {
		if selected, ok := h.AuthManager.GetByID(authID); ok && selected != nil {
			provider = strings.TrimSpace(selected.Provider)
		}
	}
	if provider == "" {
		return nil, fmt.Errorf("video create selected credential %q without a provider", authID)
	}
	status := openAIVideoStatus(gjson.GetBytes(payload, "status").String())
	if status == "" {
		status = "queued"
	}
	job, err := h.videoService.CreateJob(ctx, internalvideo.Job{
		UpstreamID: upstreamID,
		Provider:   provider,
		AuthID:     authID,
		Model:      canonicalVideoBindingModel(model),
		Status:     status,
		Progress:   int(gjson.GetBytes(payload, "progress").Int()),
		ResultURL:  videoURLFromPayload(payload),
	})
	if err != nil {
		return nil, err
	}
	videoAuthBindings.setWithModelAndUpstream(job.ID, authID, job.Model, upstreamID, h.videoAuthBindingTTL())
	payload, _ = sjson.SetBytes(payload, "id", job.ID)
	payload, _ = sjson.SetBytes(payload, "object", "video")
	payload, _ = sjson.DeleteBytes(payload, "request_id")
	payload, _ = sjson.DeleteBytes(payload, "task_id")
	payload, _ = sjson.DeleteBytes(payload, "video_id")
	return payload, nil
}

func (h *OpenAIAPIHandler) updatePublicVideoResult(c *gin.Context, publicID string, payload []byte) []byte {
	if h == nil || h.videoService == nil {
		return payload
	}
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	job, ok := h.durableVideoJob(ctx, publicID)
	if !ok {
		return payload
	}
	status := openAIVideoStatus(gjson.GetBytes(payload, "status").String())
	if status == "" {
		status = job.Status
	}
	resultURL := videoURLFromPayload(payload)
	updated, err := h.videoService.UpdateResult(ctx, publicID, internalvideo.ResultUpdate{
		Status:    status,
		Progress:  int(gjson.GetBytes(payload, "progress").Int()),
		ResultURL: resultURL,
	})
	if err != nil {
		log.WithError(err).Warn("update durable video job")
		updated = job
	}
	if status == "completed" && resultURL != "" && h.videoService.ObjectStorageEnabled() && strings.TrimSpace(updated.ObjectKey) == "" {
		if stored, errStore := h.videoService.StoreCompleted(ctx, publicID, h.videoContentHTTPClient(c), resultURL); errStore != nil {
			log.WithError(errStore).Warn("persist completed video object")
		} else {
			updated = stored
		}
	}
	payload, _ = sjson.SetBytes(payload, "id", publicID)
	payload, _ = sjson.SetBytes(payload, "content_url", publicVideoContentURL(c, publicID))
	if strings.TrimSpace(updated.ObjectKey) != "" {
		if signedURL, errSigned := h.videoService.SignedURL(updated); errSigned == nil {
			payload, _ = sjson.SetBytes(payload, "video_url", signedURL)
		}
	}
	return payload
}

func publicVideoContentURL(c *gin.Context, videoID string) string {
	if c == nil || c.Request == nil {
		return "/v1/videos/" + url.PathEscape(videoID) + "/content"
	}
	proto := strings.TrimSpace(c.GetHeader("X-Forwarded-Proto"))
	if proto == "" {
		proto = "http"
		if c.Request.TLS != nil {
			proto = "https"
		}
	}
	host := strings.TrimSpace(c.GetHeader("X-Forwarded-Host"))
	if host == "" {
		host = strings.TrimSpace(c.Request.Host)
	}
	if host == "" {
		return "/v1/videos/" + url.PathEscape(videoID) + "/content"
	}
	return proto + "://" + host + "/v1/videos/" + url.PathEscape(videoID) + "/content"
}

// VideoContentURL returns a short-lived object URL when the completed result
// has been persisted, otherwise it returns the authenticated public API route.
func (h *OpenAIAPIHandler) VideoContentURL(c *gin.Context, videoID string) string {
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	if job, ok := h.durableVideoJob(ctx, videoID); ok && strings.TrimSpace(job.ObjectKey) != "" {
		if signedURL, err := h.videoService.SignedURL(job); err == nil {
			return signedURL
		}
	}
	return publicVideoContentURL(c, videoID)
}

func buildXAIVideosCreateRequest(rawJSON []byte, model string) ([]byte, xaiVideoCreateMetadata, error) {
	prompt := strings.TrimSpace(gjson.GetBytes(rawJSON, "prompt").String())
	if prompt == "" {
		return nil, xaiVideoCreateMetadata{}, fmt.Errorf("prompt is required")
	}

	seconds, duration, err := normalizeXAIVideosSeconds(gjson.GetBytes(rawJSON, "seconds").String())
	if err != nil {
		return nil, xaiVideoCreateMetadata{}, err
	}

	size, aspectRatio, resolution, err := xaiVideosSizeOptions(gjson.GetBytes(rawJSON, "size").String())
	if err != nil {
		return nil, xaiVideoCreateMetadata{}, err
	}
	if value := xaiVideosAspectRatio(gjson.GetBytes(rawJSON, "aspect_ratio").String(), ""); value != "" {
		aspectRatio = value
	}
	if value := xaiVideosResolution(gjson.GetBytes(rawJSON, "resolution").String(), ""); value != "" {
		resolution = value
	}

	imageURL, err := xaiVideosInputImageURL(rawJSON)
	if err != nil {
		return nil, xaiVideoCreateMetadata{}, err
	}
	referenceImages := collectXAIVideoReferenceImages(rawJSON)
	if len(referenceImages) > maxXAIVideoReferences {
		return nil, xaiVideoCreateMetadata{}, fmt.Errorf("reference_images supports at most %d images on xAI", maxXAIVideoReferences)
	}
	if imageURL != "" && len(referenceImages) > 0 {
		return nil, xaiVideoCreateMetadata{}, fmt.Errorf("image and reference_images cannot be combined on xAI")
	}
	if len(referenceImages) > 0 && duration > 10 {
		duration = 10
		seconds = "10"
	}

	videoModel := canonicalXAIVideosModel(model)
	req := []byte(`{}`)
	req, _ = sjson.SetBytes(req, "model", videoModel)
	req, _ = sjson.SetBytes(req, "prompt", prompt)
	req, _ = sjson.SetRawBytes(req, "duration", []byte(strconv.FormatInt(duration, 10)))
	req, _ = sjson.SetBytes(req, "aspect_ratio", aspectRatio)
	req, _ = sjson.SetBytes(req, "resolution", resolution)
	if imageURL != "" {
		req, _ = sjson.SetBytes(req, "image.url", imageURL)
	}
	for _, image := range referenceImages {
		req, _ = sjson.SetBytes(req, "reference_images.-1.url", image)
	}

	meta := xaiVideoCreateMetadata{
		Model:         responseVideosModel(model),
		UpstreamModel: videoModel,
		Prompt:        prompt,
		Seconds:       seconds,
		Size:          size,
		CreatedAt:     time.Now().Unix(),
	}
	return req, meta, nil
}

func normalizeXAIVideosSeconds(raw string) (string, int64, error) {
	seconds := strings.TrimSpace(raw)
	if seconds == "" {
		seconds = defaultVideosSeconds
	}
	duration, err := strconv.ParseInt(seconds, 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("seconds must be an integer")
	}
	if duration < 1 {
		duration = 1
	}
	if duration > 15 {
		duration = 15
	}
	return strconv.FormatInt(duration, 10), duration, nil
}

func xaiVideosSizeOptions(raw string) (size string, aspectRatio string, resolution string, err error) {
	size = strings.TrimSpace(raw)
	if size == "" {
		size = defaultVideosSize
	}
	switch size {
	case "720x1280", "1024x1792":
		return size, "9:16", defaultVideosResolution, nil
	case "1280x720", "1792x1024":
		return size, "16:9", defaultVideosResolution, nil
	default:
		return "", "", "", fmt.Errorf("size must be one of 720x1280, 1280x720, 1024x1792, or 1792x1024")
	}
}

func xaiVideosAspectRatio(raw string, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1:1", "square":
		return "1:1"
	case "16:9", "landscape":
		return "16:9"
	case "9:16", "portrait":
		return "9:16"
	case "4:3":
		return "4:3"
	case "3:4":
		return "3:4"
	case "3:2":
		return "3:2"
	case "2:3":
		return "2:3"
	default:
		return fallback
	}
}

func xaiVideosResolution(raw string, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "480p":
		return "480p"
	case "720p":
		return "720p"
	default:
		return fallback
	}
}

func xaiVideosInputImageURL(rawJSON []byte) (string, error) {
	inputRef := gjson.GetBytes(rawJSON, "input_reference")
	if inputRef.Exists() {
		imageURL := strings.TrimSpace(inputRef.Get("image_url").String())
		fileID := strings.TrimSpace(inputRef.Get("file_id").String())
		if imageURL != "" && fileID != "" {
			return "", fmt.Errorf("input_reference must provide exactly one of image_url or file_id")
		}
		if fileID != "" {
			return "", fmt.Errorf("input_reference.file_id is not supported for xAI video generation; use input_reference.image_url")
		}
		if imageURL != "" {
			return imageURL, nil
		}
	}

	image := gjson.GetBytes(rawJSON, "image")
	if image.Exists() {
		if image.Type == gjson.String {
			return strings.TrimSpace(image.String()), nil
		}
		if value := strings.TrimSpace(image.Get("url").String()); value != "" {
			return value, nil
		}
		if value := strings.TrimSpace(image.Get("image_url.url").String()); value != "" {
			return value, nil
		}
	}

	return strings.TrimSpace(gjson.GetBytes(rawJSON, "image_url").String()), nil
}

func collectXAIVideoReferenceImages(rawJSON []byte) []string {
	out := make([]string, 0)
	appendRef := func(value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	collectArray := func(result gjson.Result) {
		if !result.IsArray() {
			return
		}
		result.ForEach(func(_, item gjson.Result) bool {
			if item.Type == gjson.String {
				appendRef(item.String())
				return true
			}
			if value := item.Get("url").String(); value != "" {
				appendRef(value)
				return true
			}
			if value := item.Get("image_url.url").String(); value != "" {
				appendRef(value)
			}
			return true
		})
	}
	collectArray(gjson.GetBytes(rawJSON, "reference_images"))
	collectArray(gjson.GetBytes(rawJSON, "reference_image_urls"))
	return out
}

func buildVideosCreateAPIResponseFromXAI(payload []byte, meta xaiVideoCreateMetadata) ([]byte, error) {
	requestID := strings.TrimSpace(gjson.GetBytes(payload, "request_id").String())
	if requestID == "" {
		requestID = strings.TrimSpace(gjson.GetBytes(payload, "id").String())
	}
	if requestID == "" {
		return nil, fmt.Errorf("xAI video response did not include request_id")
	}

	out := []byte(`{"object":"video","progress":0,"status":"queued"}`)
	out, _ = sjson.SetBytes(out, "id", requestID)
	out, _ = sjson.SetBytes(out, "model", meta.Model)
	out, _ = sjson.SetBytes(out, "prompt", meta.Prompt)
	out, _ = sjson.SetBytes(out, "seconds", meta.Seconds)
	out, _ = sjson.SetBytes(out, "size", meta.Size)
	out, _ = sjson.SetBytes(out, "created_at", meta.CreatedAt)
	if status := openAIVideoStatus(gjson.GetBytes(payload, "status").String()); status != "" {
		out, _ = sjson.SetBytes(out, "status", status)
	}
	if progress := gjson.GetBytes(payload, "progress"); progress.Exists() {
		out, _ = sjson.SetRawBytes(out, "progress", []byte(progress.Raw))
	}
	return out, nil
}

func buildVideosFailedAPIResponse(model string, code string, message string) []byte {
	model = strings.TrimSpace(model)
	if model == "" {
		model = defaultOpenAIVideosModel
	}
	code = strings.TrimSpace(code)
	if code == "" {
		code = "invalid_request_error"
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = "Video generation failed"
	}

	out := []byte(`{"object":"video","status":"failed","progress":0}`)
	out, _ = sjson.SetBytes(out, "id", "video_"+strings.ReplaceAll(uuid.NewString(), "-", ""))
	out, _ = sjson.SetBytes(out, "model", model)
	out, _ = sjson.SetBytes(out, "error.code", code)
	out, _ = sjson.SetBytes(out, "error.message", message)
	return out
}

func writeVideosFailedError(c *gin.Context, status int, model string, code string, message string) {
	if status <= 0 {
		status = http.StatusBadRequest
	}
	c.Data(status, "application/json", buildVideosFailedAPIResponse(model, code, message))
}

func buildVideosRetrieveAPIResponseFromXAI(videoID string, payload []byte, fallbackModel string) ([]byte, error) {
	out := []byte(`{"object":"video"}`)
	out, _ = sjson.SetBytes(out, "id", videoID)
	model := strings.TrimSpace(gjson.GetBytes(payload, "model").String())
	if model == "" {
		model = responseVideosModel(fallbackModel)
	}
	out, _ = sjson.SetBytes(out, "model", model)

	for _, field := range []string{"created_at", "completed_at", "expires_at", "prompt", "remixed_from_video_id", "size"} {
		if value := gjson.GetBytes(payload, field); value.Exists() {
			out, _ = sjson.SetRawBytes(out, field, []byte(value.Raw))
		}
	}

	if status := openAIVideoStatus(gjson.GetBytes(payload, "status").String()); status != "" {
		out, _ = sjson.SetBytes(out, "status", status)
	}
	if progress := gjson.GetBytes(payload, "progress"); progress.Exists() {
		out, _ = sjson.SetRawBytes(out, "progress", []byte(progress.Raw))
	}
	if seconds := gjson.GetBytes(payload, "seconds"); seconds.Exists() {
		out, _ = sjson.SetRawBytes(out, "seconds", []byte(seconds.Raw))
	} else if duration := gjson.GetBytes(payload, "video.duration"); duration.Exists() {
		out, _ = sjson.SetBytes(out, "seconds", duration.String())
	}
	if videoURL := videoURLFromPayload(payload); videoURL != "" {
		out, _ = sjson.SetBytes(out, "video_url", videoURL)
	}
	out = setOpenAIVideoErrorFromXAI(out, payload)
	return out, nil
}

func setOpenAIVideoErrorFromXAI(out []byte, payload []byte) []byte {
	if errPayload := gjson.GetBytes(payload, "error"); errPayload.Exists() {
		out = markOpenAIVideoFailed(out)
		if errPayload.Type == gjson.JSON && json.Valid([]byte(errPayload.Raw)) {
			message := strings.TrimSpace(errPayload.Get("message").String())
			if message != "" {
				code := strings.TrimSpace(gjson.GetBytes(payload, "code").String())
				if code == "" {
					code = strings.TrimSpace(errPayload.Get("code").String())
				}
				if code == "" {
					code = "video_generation_failed"
				}
				out, _ = sjson.SetBytes(out, "error.code", code)
				out, _ = sjson.SetBytes(out, "error.message", message)
			}
			return out
		}
		message := strings.TrimSpace(errPayload.String())
		if message != "" {
			code := strings.TrimSpace(gjson.GetBytes(payload, "code").String())
			if code == "" {
				code = "video_generation_failed"
			}
			out, _ = sjson.SetBytes(out, "error.code", code)
			out, _ = sjson.SetBytes(out, "error.message", message)
		}
		return out
	}

	code := strings.TrimSpace(gjson.GetBytes(payload, "code").String())
	if code != "" {
		out = markOpenAIVideoFailed(out)
		out, _ = sjson.SetBytes(out, "error.code", code)
		out, _ = sjson.SetBytes(out, "error.message", code)
	}
	return out
}

func markOpenAIVideoFailed(out []byte) []byte {
	if !gjson.GetBytes(out, "status").Exists() {
		out, _ = sjson.SetBytes(out, "status", "failed")
	}
	if !gjson.GetBytes(out, "progress").Exists() {
		out, _ = sjson.SetRawBytes(out, "progress", []byte("0"))
	}
	return out
}

func xaiVideoContentURLFromPayload(payload []byte) (string, error) {
	rawURL := videoURLFromPayload(payload)
	if rawURL == "" {
		return "", fmt.Errorf("video response did not include video.url or url")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("video response included invalid video URL")
	}
	return rawURL, nil
}

func videoURLFromPayload(payload []byte) string {
	for _, path := range []string{"video.url", "video_url", "url", "remixed_from_video_id"} {
		rawURL := strings.TrimSpace(gjson.GetBytes(payload, path).String())
		if rawURL == "" {
			continue
		}
		parsed, err := url.Parse(rawURL)
		if err == nil && parsed != nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" {
			return rawURL
		}
	}
	return ""
}

func openAIVideoStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "queued", "pending":
		return "queued"
	case "in_progress", "processing", "running":
		return "in_progress"
	case "completed", "done", "succeeded", "success":
		return "completed"
	case "failed", "error", "expired", "cancelled", "canceled":
		return "failed"
	default:
		return ""
	}
}

func (h *OpenAIAPIHandler) VideosCreate(c *gin.Context) {
	rawJSON, err := readVideosCreateRequest(c)
	if err != nil {
		writeVideosFailedError(c, http.StatusBadRequest, defaultOpenAIVideosModel, "invalid_request_error", fmt.Sprintf("Invalid request: %v", err))
		return
	}
	if !h.requirePublicVideoService(c) {
		return
	}

	videoModel := strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String())
	if videoModel == "" {
		videoModel = defaultXAIVideosModel
	}
	if rejectUnsupportedVideosModel(c, videoModel) {
		return
	}

	if isOpenAICompatVideosModel(videoModel) {
		h.collectOpenAICompatVideosCreate(c, rawJSON, videoModel)
		return
	}

	xaiReq, meta, err := buildXAIVideosCreateRequest(rawJSON, videoModel)
	if err != nil {
		writeVideosFailedError(c, http.StatusBadRequest, videoModel, "invalid_request_error", fmt.Sprintf("Invalid request: %v", err))
		return
	}

	h.collectXAIVideosCreate(c, xaiReq, meta)
}

func (h *OpenAIAPIHandler) XAIVideosGenerations(c *gin.Context) {
	h.handleXAIVideosNativePost(c)
}

func (h *OpenAIAPIHandler) XAIVideosEdits(c *gin.Context) {
	h.handleXAIVideosNativePost(c)
}

func (h *OpenAIAPIHandler) XAIVideosExtensions(c *gin.Context) {
	h.handleXAIVideosNativePost(c)
}

func (h *OpenAIAPIHandler) handleXAIVideosNativePost(c *gin.Context) {
	rawJSON, err := readXAIVideosNativeRequest(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	videoModel := strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String())
	if videoModel == "" {
		videoModel = defaultXAIVideosModel
	}
	if rejectUnsupportedNativeVideosModel(c, videoModel) {
		return
	}

	h.collectXAIVideosNative(c, rawJSON, videoModel, true)
}

func (h *OpenAIAPIHandler) XAIVideosRetrieve(c *gin.Context) {
	requestID := strings.TrimSpace(c.Param("request_id"))
	if requestID == "" {
		requestID = strings.TrimSpace(c.Param("video_id"))
	}
	if requestID == "" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Invalid request: request_id is required",
				Type:    "invalid_request_error",
			},
		})
		return
	}

	payload := []byte(`{}`)
	payload, _ = sjson.SetBytes(payload, "request_id", requestID)
	h.collectXAIVideosNative(c, payload, defaultXAIVideosModel, false)
}

func (h *OpenAIAPIHandler) VideosRetrieve(c *gin.Context) {
	videoID := strings.TrimSpace(c.Param("video_id"))
	if videoID == "" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Invalid request: video_id is required",
				Type:    "invalid_request_error",
			},
		})
		return
	}
	if isCanonicalPublicVideoRequest(c) && shouldUseLegacyXAIVideoRetrieve(videoID) {
		h.XAIVideosRetrieve(c)
		return
	}
	var publicJob *internalvideo.Job
	if isCanonicalPublicVideoRequest(c) {
		job, ok := h.requirePublicVideoJob(c, videoID)
		if !ok {
			return
		}
		publicJob = &job
		videoAuthBindings.setWithModelAndUpstream(job.ID, job.AuthID, job.Model, job.UpstreamID, h.videoAuthBindingTTL())
	}

	payload := []byte(`{}`)
	payload, _ = sjson.SetBytes(payload, "request_id", videoID)

	c.Header("Content-Type", "application/json")
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	selectedAuthID := ""
	executionModel := defaultXAIVideosModel
	if publicJob != nil {
		cliCtx = handlers.WithPinnedAuthID(cliCtx, publicJob.AuthID)
		executionModel = publicJob.Model
		payload, _ = sjson.SetBytes(payload, "request_id", publicJob.UpstreamID)
		payload, _ = sjson.SetBytes(payload, "video_id", publicJob.UpstreamID)
	} else {
		cliCtx = h.contextWithVideoAuthBinding(cliCtx, videoID)
		executionModel = h.modelWithVideoAuthBinding(cliCtx, videoID, defaultXAIVideosModel)
		payload = h.payloadWithVideoAuthBinding(cliCtx, payload, videoID)
	}
	cliCtx = handlers.WithSelectedAuthIDCallback(cliCtx, func(authID string) {
		selectedAuthID = authID
	})
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, xaiVideosHandlerType, executionModel, payload, "")
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		if errMsg.Error != nil {
			cliCancel(errMsg.Error)
		} else {
			cliCancel(nil)
		}
		return
	}

	out, err := buildVideosRetrieveAPIResponseFromXAI(videoID, resp, executionModel)
	if err != nil {
		errMsg := &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err}
		h.WriteErrorResponse(c, errMsg)
		cliCancel(err)
		return
	}

	h.bindVideoAuthID(videoID, selectedAuthID, executionModel)
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	out = h.updatePublicVideoResult(c, videoID, out)
	_, _ = c.Writer.Write(out)
	cliCancel(nil)
}

func (h *OpenAIAPIHandler) VideosContent(c *gin.Context) {
	videoID := strings.TrimSpace(c.Param("video_id"))
	if videoID == "" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Invalid request: video_id is required",
				Type:    "invalid_request_error",
			},
		})
		return
	}
	variant := strings.TrimSpace(c.Query("variant"))
	if variant == "" {
		variant = "video"
	}
	if variant != "video" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: variant %q is not available for xAI video downloads", variant),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	var publicJob *internalvideo.Job
	if isCanonicalPublicVideoRequest(c) {
		job, ok := h.requirePublicVideoJob(c, videoID)
		if !ok {
			return
		}
		publicJob = &job
		videoAuthBindings.setWithModelAndUpstream(job.ID, job.AuthID, job.Model, job.UpstreamID, h.videoAuthBindingTTL())
	}
	if publicJob != nil {
		if strings.TrimSpace(publicJob.ObjectKey) != "" {
			if signedURL, errSigned := h.videoService.SignedURL(*publicJob); errSigned == nil {
				c.Redirect(http.StatusTemporaryRedirect, signedURL)
				return
			}
		}
	} else {
		if job, ok := h.durableVideoJob(c.Request.Context(), videoID); ok && strings.TrimSpace(job.ObjectKey) != "" {
			if signedURL, errSigned := h.videoService.SignedURL(job); errSigned == nil {
				c.Redirect(http.StatusTemporaryRedirect, signedURL)
				return
			}
		}
	}

	payload := []byte(`{}`)
	payload, _ = sjson.SetBytes(payload, "request_id", videoID)

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	selectedAuthID := ""
	executionModel := defaultXAIVideosModel
	if publicJob != nil {
		cliCtx = handlers.WithPinnedAuthID(cliCtx, publicJob.AuthID)
		executionModel = publicJob.Model
		payload, _ = sjson.SetBytes(payload, "request_id", publicJob.UpstreamID)
		payload, _ = sjson.SetBytes(payload, "video_id", publicJob.UpstreamID)
	} else {
		cliCtx = h.contextWithVideoAuthBinding(cliCtx, videoID)
		executionModel = h.modelWithVideoAuthBinding(cliCtx, videoID, defaultXAIVideosModel)
		payload = h.payloadWithVideoAuthBinding(cliCtx, payload, videoID)
	}
	cliCtx = handlers.WithSelectedAuthIDCallback(cliCtx, func(authID string) {
		selectedAuthID = authID
	})
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, _, errMsg := h.ExecuteWithAuthManager(cliCtx, xaiVideosHandlerType, executionModel, payload, "")
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		if errMsg.Error != nil {
			cliCancel(errMsg.Error)
		} else {
			cliCancel(nil)
		}
		return
	}

	h.bindVideoAuthID(videoID, selectedAuthID, executionModel)
	contentURL, err := xaiVideoContentURLFromPayload(resp)
	if err != nil {
		errMsg := &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err}
		h.WriteErrorResponse(c, errMsg)
		cliCancel(err)
		return
	}

	if errDownload := h.writeVideoContentFromURL(c, contentURL); errDownload != nil {
		cliCancel(errDownload)
		return
	}
	cliCancel(nil)
}

func (h *OpenAIAPIHandler) writeVideoContentFromURL(c *gin.Context, contentURL string) error {
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, contentURL, nil)
	if err != nil {
		errMsg := &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err}
		h.WriteErrorResponse(c, errMsg)
		return err
	}

	httpClient := h.videoContentHTTPClient(c)
	resp, err := httpClient.Do(req)
	if err != nil {
		errMsg := &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err}
		h.WriteErrorResponse(c, errMsg)
		return err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("video content body close error: %v", errClose)
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		errDownloadStatus := fmt.Errorf("video content download failed: %s", strings.TrimSpace(string(body)))
		if strings.TrimSpace(string(body)) == "" {
			errDownloadStatus = fmt.Errorf("video content download failed: %s", resp.Status)
		}
		errMsg := &interfaces.ErrorMessage{StatusCode: resp.StatusCode, Error: errDownloadStatus}
		h.WriteErrorResponse(c, errMsg)
		return errDownloadStatus
	}

	copyVideoContentHeaders(c.Writer.Header(), resp.Header)
	if c.Writer.Header().Get("Content-Type") == "" {
		c.Writer.Header().Set("Content-Type", "application/octet-stream")
	}
	c.Status(resp.StatusCode)
	_, err = io.Copy(c.Writer, resp.Body)
	return err
}

func (h *OpenAIAPIHandler) videoContentHTTPClient(c *gin.Context) *http.Client {
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	var cfg *config.Config
	if h != nil && h.BaseAPIHandler != nil && h.Cfg != nil {
		cfg = &config.Config{SDKConfig: *h.Cfg}
	}
	return helps.NewProxyAwareHTTPClient(ctx, cfg, h.videoContentDownloadAuth(c), 0)
}

func (h *OpenAIAPIHandler) videoContentDownloadAuth(c *gin.Context) *coreauth.Auth {
	if h == nil || h.BaseAPIHandler == nil || h.AuthManager == nil || c == nil {
		return nil
	}
	videoID := strings.TrimSpace(c.Param("video_id"))
	if videoID == "" {
		return nil
	}
	authID := ""
	if job, ok := h.durableVideoJob(c.Request.Context(), videoID); ok {
		authID = strings.TrimSpace(job.AuthID)
	}
	if authID == "" {
		var ok bool
		authID, ok = videoAuthBindings.get(videoID)
		if !ok {
			return nil
		}
	}
	auth, ok := h.AuthManager.GetByID(authID)
	if !ok {
		return nil
	}
	return auth
}

func copyVideoContentHeaders(dst http.Header, src http.Header) {
	for _, key := range []string{"Content-Type", "Content-Length", "Content-Disposition", "Cache-Control", "ETag", "Last-Modified"} {
		if value := src.Get(key); value != "" {
			dst.Set(key, value)
		}
	}
}

func (h *OpenAIAPIHandler) collectXAIVideosNative(c *gin.Context, rawJSON []byte, model string, bindCreatedVideoAuth bool) {
	c.Header("Content-Type", "application/json")

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	selectedAuthID := ""
	videoID := videoIDFromPayload(rawJSON)
	executionModel := model
	if !bindCreatedVideoAuth {
		cliCtx = h.contextWithVideoAuthBinding(cliCtx, videoID)
		executionModel = h.modelWithVideoAuthBinding(cliCtx, videoID, model)
	}
	cliCtx = handlers.WithSelectedAuthIDCallback(cliCtx, func(authID string) {
		selectedAuthID = authID
	})
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, xaiVideosHandlerType, executionModel, rawJSON, "")
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		if errMsg.Error != nil {
			cliCancel(errMsg.Error)
		} else {
			cliCancel(nil)
		}
		return
	}

	if bindCreatedVideoAuth {
		h.bindVideoAuthIDAndModelFromPayload(resp, selectedAuthID, executionModel)
	} else {
		h.bindVideoAuthID(videoID, selectedAuthID, executionModel)
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
	cliCancel(nil)
}

func (h *OpenAIAPIHandler) collectOpenAICompatVideosCreate(c *gin.Context, rawJSON []byte, model string) {
	c.Header("Content-Type", "application/json")

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	selectedAuthID := ""
	cliCtx = handlers.WithSelectedAuthIDCallback(cliCtx, func(authID string) {
		selectedAuthID = authID
	})
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, xaiVideosHandlerType, model, rawJSON, "")
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		if errMsg.Error != nil {
			cliCancel(errMsg.Error)
		} else {
			cliCancel(nil)
		}
		return
	}

	resp, errPersist := h.persistPublicVideoCreate(cliCtx, resp, selectedAuthID, model)
	if errPersist != nil {
		errMsg := &interfaces.ErrorMessage{StatusCode: http.StatusInternalServerError, Error: errPersist}
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errPersist)
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
	cliCancel(nil)
}

func (h *OpenAIAPIHandler) collectXAIVideosCreate(c *gin.Context, xaiReq []byte, meta xaiVideoCreateMetadata) {
	c.Header("Content-Type", "application/json")

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	selectedAuthID := ""
	cliCtx = handlers.WithSelectedAuthIDCallback(cliCtx, func(authID string) {
		selectedAuthID = authID
	})
	upstreamModel := strings.TrimSpace(meta.UpstreamModel)
	if upstreamModel == "" {
		upstreamModel = meta.Model
	}
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, xaiVideosHandlerType, upstreamModel, xaiReq, "")
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		if errMsg.Error != nil {
			cliCancel(errMsg.Error)
		} else {
			cliCancel(nil)
		}
		return
	}

	out, err := buildVideosCreateAPIResponseFromXAI(resp, meta)
	if err != nil {
		errMsg := &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err}
		h.WriteErrorResponse(c, errMsg)
		cliCancel(err)
		return
	}

	out, err = h.persistPublicVideoCreate(cliCtx, out, selectedAuthID, upstreamModel)
	if err != nil {
		errMsg := &interfaces.ErrorMessage{StatusCode: http.StatusInternalServerError, Error: err}
		h.WriteErrorResponse(c, errMsg)
		cliCancel(err)
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(out)
	cliCancel(nil)
}
