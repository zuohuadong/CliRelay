package videotool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultBaseURL = "http://127.0.0.1:8317"
	DefaultModel   = "agnes-video-v2.0"
)

type Options struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

type Client struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
}

type CreateVideoRequest struct {
	Model          string         `json:"model,omitempty"`
	Prompt         string         `json:"prompt,omitempty"`
	Seconds        int            `json:"seconds,omitempty"`
	Size           string         `json:"size,omitempty"`
	AspectRatio    string         `json:"aspect_ratio,omitempty"`
	Resolution     string         `json:"resolution,omitempty"`
	InputReference map[string]any `json:"input_reference,omitempty"`
	Extra          map[string]any `json:"-"`
}

type DownloadResult struct {
	Path        string `json:"path"`
	ContentType string `json:"content_type,omitempty"`
	Bytes       int64  `json:"bytes"`
}

func OptionsFromEnv() Options {
	return Options{
		BaseURL: strings.TrimSpace(os.Getenv("CLIRELAY_BASE_URL")),
		APIKey:  strings.TrimSpace(os.Getenv("CLIRELAY_API_KEY")),
		Model:   strings.TrimSpace(os.Getenv("CLIRELAY_VIDEO_MODEL")),
	}
}

func NewClient(opts Options) *Client {
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	model := strings.TrimSpace(opts.Model)
	if model == "" {
		model = DefaultModel
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Minute}
	}
	return &Client{
		baseURL:    baseURL,
		apiKey:     strings.TrimSpace(opts.APIKey),
		model:      model,
		httpClient: httpClient,
	}
}

func (c *Client) DefaultModel() string {
	if c == nil || c.model == "" {
		return DefaultModel
	}
	return c.model
}

func (c *Client) VideoContentURL(videoID string) string {
	if c == nil {
		return ""
	}
	videoID = strings.TrimSpace(videoID)
	if videoID == "" {
		return ""
	}
	return c.baseURL + "/openai/v1/videos/" + url.PathEscape(videoID) + "/content"
}

func (c *Client) ListVideoModels(ctx context.Context) ([]map[string]any, error) {
	payload, err := c.doJSON(ctx, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return nil, err
	}
	items, _ := payload["data"].([]any)
	models := make([]map[string]any, 0, len(items))
	for _, item := range items {
		model, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := strings.ToLower(strings.TrimSpace(stringValue(model["id"])))
		if id == "" {
			continue
		}
		if strings.Contains(id, "video") || id == strings.ToLower(c.DefaultModel()) {
			models = append(models, model)
		}
	}
	return models, nil
}

func (c *Client) CreateVideo(ctx context.Context, req CreateVideoRequest) (map[string]any, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = c.DefaultModel()
	}
	payload := map[string]any{
		"model":  model,
		"prompt": strings.TrimSpace(req.Prompt),
	}
	if req.Seconds > 0 {
		payload["seconds"] = req.Seconds
	}
	setString(payload, "size", req.Size)
	setString(payload, "aspect_ratio", req.AspectRatio)
	setString(payload, "resolution", req.Resolution)
	if len(req.InputReference) > 0 {
		payload["input_reference"] = req.InputReference
	}
	for key, value := range req.Extra {
		key = strings.TrimSpace(key)
		if key != "" {
			payload[key] = value
		}
	}
	return c.doJSON(ctx, http.MethodPost, "/openai/v1/videos", payload)
}

func (c *Client) GetVideo(ctx context.Context, videoID string) (map[string]any, error) {
	videoID = strings.TrimSpace(videoID)
	if videoID == "" {
		return nil, fmt.Errorf("video_id is required")
	}
	return c.doJSON(ctx, http.MethodGet, "/openai/v1/videos/"+url.PathEscape(videoID), nil)
}

func (c *Client) WaitVideo(ctx context.Context, videoID string, interval time.Duration) (map[string]any, error) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			payload, err := c.GetVideo(ctx, videoID)
			if err != nil {
				return nil, err
			}
			switch strings.ToLower(stringValue(payload["status"])) {
			case "completed", "succeeded", "success", "done":
				return payload, nil
			case "failed", "error", "cancelled", "canceled", "expired":
				return payload, fmt.Errorf("video generation failed: %s", stringValue(payload["error"]))
			}
			timer.Reset(interval)
		}
	}
}

func (c *Client) DownloadVideo(ctx context.Context, videoID, outputPath string) (DownloadResult, error) {
	videoID = strings.TrimSpace(videoID)
	if videoID == "" {
		return DownloadResult{}, fmt.Errorf("video_id is required")
	}
	outputPath = strings.TrimSpace(outputPath)
	if outputPath == "" {
		outputPath = videoID + ".mp4"
	}
	endpoint := "/openai/v1/videos/" + url.PathEscape(videoID) + "/content"
	req, err := c.newRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return DownloadResult{}, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return DownloadResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
		return DownloadResult{}, fmt.Errorf("download failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil && filepath.Dir(outputPath) != "." {
		return DownloadResult{}, err
	}
	file, err := os.Create(outputPath)
	if err != nil {
		return DownloadResult{}, err
	}
	written, copyErr := io.Copy(file, resp.Body)
	closeErr := file.Close()
	if copyErr != nil {
		return DownloadResult{}, copyErr
	}
	if closeErr != nil {
		return DownloadResult{}, closeErr
	}
	return DownloadResult{
		Path:        outputPath,
		ContentType: cleanContentType(resp.Header.Get("Content-Type")),
		Bytes:       written,
	}, nil
}

func VideoID(payload map[string]any) string {
	for _, key := range []string{"id", "request_id", "video_id"} {
		if value := strings.TrimSpace(stringValue(payload[key])); value != "" {
			return value
		}
	}
	return ""
}

func (c *Client) doJSON(ctx context.Context, method, path string, payload any) (map[string]any, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s failed: %s: %s", method, path, resp.Status, strings.TrimSpace(string(raw)))
	}
	var out map[string]any
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	if c == nil {
		return nil, fmt.Errorf("client is nil")
	}
	endpoint := strings.TrimRight(c.baseURL, "/") + "/" + strings.TrimLeft(path, "/")
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "clirelay-video/0.1")
	return req, nil
}

func setString(payload map[string]any, key, value string) {
	if value = strings.TrimSpace(value); value != "" {
		payload[key] = value
	}
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case nil:
		return ""
	default:
		raw, _ := json.Marshal(typed)
		return string(raw)
	}
}

func cleanContentType(value string) string {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err == nil && mediaType != "" {
		return mediaType
	}
	return strings.TrimSpace(value)
}
