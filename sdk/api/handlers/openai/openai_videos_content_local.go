package openai

import (
	"context"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	internalvideo "github.com/router-for-me/CLIProxyAPI/v7/internal/video"
)

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
