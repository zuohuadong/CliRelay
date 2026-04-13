package management

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/api/bodyutil"
)

const publicLookupBodyLimit int64 = 8 << 10

type publicLookupRequest struct {
	APIKey string `json:"api_key"`
	Days   int    `json:"days"`
	Page   int    `json:"page"`
	Size   int    `json:"size"`
	Model  string `json:"model"`
	Status string `json:"status"`
	Part   string `json:"part"`
	Format string `json:"format"`
}

func readPublicLookupRequest(c *gin.Context) (publicLookupRequest, int, string) {
	req := publicLookupRequest{}
	if c == nil || c.Request == nil {
		return req, http.StatusInternalServerError, "request unavailable"
	}

	if c.Request.Method == http.MethodPost {
		body, err := bodyutil.ReadRequestBody(c, publicLookupBodyLimit)
		if err != nil {
			if bodyutil.IsTooLarge(err) {
				return req, http.StatusRequestEntityTooLarge, "request body too large"
			}
			return req, http.StatusBadRequest, "failed to read request body"
		}
		if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
			if err := json.Unmarshal(body, &req); err != nil {
				return req, http.StatusBadRequest, "invalid json body"
			}
		}
	}

	req.APIKey = strings.TrimSpace(req.APIKey)

	if req.Page < 1 {
		req.Page = intQueryDefault(c, "page", 1)
	}
	if req.Size < 1 {
		req.Size = intQueryDefault(c, "size", 50)
	}
	if req.Days < 1 {
		req.Days = intQueryDefault(c, "days", 7)
	}
	if strings.TrimSpace(req.Model) == "" {
		req.Model = strings.TrimSpace(c.Query("model"))
	}
	if strings.TrimSpace(req.Status) == "" {
		req.Status = strings.TrimSpace(c.Query("status"))
	}
	if strings.TrimSpace(req.Part) == "" {
		req.Part = strings.TrimSpace(c.Query("part"))
	}
	if strings.TrimSpace(req.Format) == "" {
		req.Format = strings.TrimSpace(c.Query("format"))
	}

	req.Model = strings.TrimSpace(req.Model)
	req.Status = strings.TrimSpace(req.Status)
	req.Part = normalizeLogContentPartValue(req.Part)
	req.Format = normalizeLogContentFormatValue(req.Format)

	return req, 0, ""
}
