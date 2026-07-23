package multimodaladapter

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"golang.org/x/sync/singleflight"
)

type Route struct {
	RequestedModel   string
	UpstreamProvider string
	UpstreamModel    string
	Protocol         string
}

type Report struct {
	Applied    bool
	MediaItems int
	Extractor  string
	Injected   bool
	Stripped   bool
}

type StatusError struct {
	StatusCodeValue int
	Message         string
	Code            string
}

func (e StatusError) Error() string {
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = http.StatusText(e.StatusCode())
	}
	code := strings.TrimSpace(e.Code)
	if code == "" {
		code = "multimodal_adapter_unavailable"
	}
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
			"code":    code,
		},
	})
	if err != nil {
		return message
	}
	return string(body)
}

func (e StatusError) StatusCode() int {
	if e.StatusCodeValue > 0 {
		return e.StatusCodeValue
	}
	return http.StatusInternalServerError
}

type mediaRef struct {
	Kind string `json:"type"`
	URL  string `json:"url"`
}

type mediaCollection struct {
	refs        []mediaRef
	items       int
	unsupported bool
}

type mediaCollector struct {
	collection mediaCollection
	refs       []mediaRef
}

type visualExtractionRequest struct {
	key       string
	ref       mediaRef
	route     Route
	extractor config.MultimodalExtractorConfig
}

const (
	visualSummaryCacheTTL        = time.Hour
	visualSummaryCacheMaxEntries = 4096
)

type visualSummaryCacheEntry struct {
	summary   string
	expiresAt time.Time
}

type visualSummaryCache struct {
	mu      sync.Mutex
	entries map[string]visualSummaryCacheEntry
	flight  singleflight.Group
}

var sharedVisualSummaryCache = &visualSummaryCache{entries: make(map[string]visualSummaryCacheEntry)}

type selectedRule struct {
	action            string
	unavailableAction string
	injectAs          string
	maxMediaItems     int
	maxOutputBytes    int
	extractor         config.MultimodalExtractorConfig
	extractorName     string
}

type visualReplacement struct {
	protocol  string
	label     string
	summaries map[string]string
	bytesLeft int
	unlimited bool
}

const mcpMediaMaxBytes = int64(32 << 20)

// Apply converts media inputs into textual context after the concrete upstream route is known.
func Apply(ctx context.Context, raw []byte, route Route, cfg config.MultimodalAdaptersConfig) ([]byte, Report, error) {
	var report Report
	if !enabled(cfg) || len(raw) == 0 {
		return raw, report, nil
	}
	rule, ok := selectRule(cfg, route)
	if !ok {
		return raw, report, nil
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return raw, report, nil
	}
	collection := collectMediaRefs(payload)
	if collection.items == 0 {
		return raw, report, nil
	}
	refs := collection.refs
	if rule.maxMediaItems > 0 && len(refs) > rule.maxMediaItems {
		refs = refs[:rule.maxMediaItems]
	}
	report.MediaItems = collection.items
	report.Extractor = rule.extractorName

	switch rule.action {
	case "reject":
		return raw, report, StatusError{StatusCodeValue: http.StatusBadRequest, Message: "request contains media inputs but the selected upstream route is configured to reject media", Code: "multimodal_media_rejected"}
	case "strip":
		return stripAndInject(raw, payload, route.Protocol, rule, "Media inputs were removed because the selected upstream model does not support direct multimodal input.", &report)
	}
	if collection.unsupported {
		return unavailable(raw, payload, route.Protocol, rule, "one or more media inputs are not addressable by the configured extractor", &report)
	}

	if rule.extractor.Name == "" {
		return unavailable(raw, payload, route.Protocol, rule, "multimodal adapter matched this route but no extractor is configured", &report)
	}
	summaries, err := extractVisualContextCached(ctx, refs, route, rule.extractor)
	if err != nil {
		return unavailable(raw, payload, route.Protocol, rule, err.Error(), &report)
	}
	out, err := replaceMediaWithSummaries(payload, route.Protocol, rule.injectAs, summaries, rule.maxOutputBytes)
	if err != nil {
		return nil, report, err
	}
	report.Applied = true
	report.Stripped = true
	report.Injected = true
	return out, report, nil
}

func extractVisualContextCached(ctx context.Context, refs []mediaRef, route Route, extractor config.MultimodalExtractorConfig) (map[string]string, error) {
	summaries := make(map[string]string, len(refs))
	for _, ref := range refs {
		summary, err := extractVisualSummaryCached(ctx, ref, route, extractor)
		if err != nil {
			return nil, err
		}
		summaries[mediaRefKey(ref)] = summary
	}
	return summaries, nil
}

func extractVisualSummaryCached(ctx context.Context, ref mediaRef, route Route, extractor config.MultimodalExtractorConfig) (string, error) {
	request := visualExtractionRequest{key: visualSummaryCacheKey(ref, route, extractor), ref: ref, route: route, extractor: extractor}
	if summary, ok := sharedVisualSummaryCache.get(request.key); ok {
		return summary, nil
	}
	flight := sharedVisualSummaryCache.flight.DoChan(request.key, func() (any, error) {
		return extractAndCacheVisualSummary(context.WithoutCancel(ctx), request)
	})
	return waitVisualSummary(ctx, flight)
}

func extractAndCacheVisualSummary(ctx context.Context, request visualExtractionRequest) (string, error) {
	if summary, ok := sharedVisualSummaryCache.get(request.key); ok {
		return summary, nil
	}
	summaries, err := extractVisualContext(ctx, []mediaRef{request.ref}, request.route, request.extractor)
	if err != nil {
		return "", err
	}
	summary := strings.TrimSpace(strings.Join(summaries, "\n\n"))
	if summary == "" {
		return "", fmt.Errorf("multimodal adapter returned empty visual context")
	}
	sharedVisualSummaryCache.set(request.key, summary)
	return summary, nil
}

func waitVisualSummary(ctx context.Context, flight <-chan singleflight.Result) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case flightResult := <-flight:
		if flightResult.Err != nil {
			return "", flightResult.Err
		}
		return flightResult.Val.(string), nil
	}
}

func visualSummaryCacheKey(ref mediaRef, route Route, extractor config.MultimodalExtractorConfig) string {
	payload, _ := json.Marshal(struct {
		Ref       mediaRef
		Route     Route
		Extractor config.MultimodalExtractorConfig
	}{Ref: ref, Route: route, Extractor: extractor})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func (c *visualSummaryCache) get(key string) (string, bool) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return "", false
	}
	if !now.Before(entry.expiresAt) {
		delete(c.entries, key)
		return "", false
	}
	entry.expiresAt = now.Add(visualSummaryCacheTTL)
	c.entries[key] = entry
	return entry.summary, true
}

func (c *visualSummaryCache) set(key, summary string) {
	if strings.TrimSpace(key) == "" || strings.TrimSpace(summary) == "" {
		return
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists && len(c.entries) >= visualSummaryCacheMaxEntries {
		c.evictOneLocked(now)
	}
	c.entries[key] = visualSummaryCacheEntry{summary: summary, expiresAt: now.Add(visualSummaryCacheTTL)}
}

func (c *visualSummaryCache) evictOneLocked(now time.Time) {
	oldestKey := ""
	var oldestExpiry time.Time
	for key, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, key)
			return
		}
		if oldestKey == "" || entry.expiresAt.Before(oldestExpiry) {
			oldestKey = key
			oldestExpiry = entry.expiresAt
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

func enabled(cfg config.MultimodalAdaptersConfig) bool {
	return cfg.Enabled == nil || *cfg.Enabled
}

func selectRule(cfg config.MultimodalAdaptersConfig, route Route) (selectedRule, bool) {
	extractors := map[string]config.MultimodalExtractorConfig{}
	for _, extractor := range cfg.Extractors {
		extractors[strings.ToLower(strings.TrimSpace(extractor.Name))] = extractor
	}
	for _, rule := range cfg.Rules {
		if !matchRoute(rule.Match, route) {
			continue
		}
		action := strings.ToLower(strings.TrimSpace(rule.Action))
		if action == "" {
			action = strings.ToLower(strings.TrimSpace(cfg.DefaultAction))
		}
		if action == "" {
			action = "extract"
		}
		unavailable := strings.ToLower(strings.TrimSpace(rule.UnavailableAction))
		if unavailable == "" {
			unavailable = strings.ToLower(strings.TrimSpace(cfg.UnavailableAction))
		}
		if unavailable == "" {
			unavailable = "reject"
		}
		injectAs := strings.TrimSpace(rule.InjectAs)
		if injectAs == "" {
			injectAs = strings.TrimSpace(cfg.InjectAs)
		}
		if injectAs == "" {
			injectAs = "visual_context"
		}
		maxMediaItems := rule.MaxMediaItems
		if maxMediaItems <= 0 {
			maxMediaItems = cfg.MaxMediaItems
		}
		if maxMediaItems <= 0 {
			maxMediaItems = 4
		}
		maxOutputBytes := rule.MaxOutputBytes
		if maxOutputBytes <= 0 {
			maxOutputBytes = cfg.MaxOutputBytes
		}
		if maxOutputBytes <= 0 {
			maxOutputBytes = 12000
		}
		extractorName := strings.TrimSpace(rule.Extractor)
		var extractor config.MultimodalExtractorConfig
		if extractorName != "" {
			extractor = extractors[strings.ToLower(extractorName)]
		}
		return selectedRule{
			action:            normalizeAction(action),
			unavailableAction: normalizeUnavailableAction(unavailable),
			injectAs:          injectAs,
			maxMediaItems:     maxMediaItems,
			maxOutputBytes:    maxOutputBytes,
			extractor:         extractor,
			extractorName:     extractorName,
		}, true
	}
	return selectedRule{}, false
}

func normalizeAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "reject":
		return "reject"
	case "strip":
		return "strip"
	default:
		return "extract"
	}
}

func normalizeUnavailableAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "strip":
		return "strip"
	case "pass-through":
		return "pass-through"
	default:
		return "reject"
	}
}

func matchRoute(match config.MultimodalAdapterMatch, route Route) bool {
	return listMatches(match.RequestedModels, route.RequestedModel, false) &&
		listMatches(match.UpstreamProviders, route.UpstreamProvider, true) &&
		listMatches(match.UpstreamModels, route.UpstreamModel, false) &&
		listMatches(match.Protocols, route.Protocol, true)
}

func listMatches(values []string, candidate string, lower bool) bool {
	if len(values) == 0 {
		return true
	}
	candidate = strings.TrimSpace(candidate)
	if lower {
		candidate = strings.ToLower(candidate)
	}
	for _, value := range values {
		v := strings.TrimSpace(value)
		if lower {
			v = strings.ToLower(v)
		}
		if strings.EqualFold(v, candidate) {
			return true
		}
	}
	return false
}

func unavailable(raw []byte, payload any, protocol string, rule selectedRule, message string, report *Report) ([]byte, Report, error) {
	switch rule.unavailableAction {
	case "pass-through":
		return raw, *report, nil
	case "strip":
		return stripAndInject(raw, payload, protocol, rule, "Media inputs were removed because visual extraction is unavailable: "+message, report)
	default:
		return raw, *report, StatusError{StatusCodeValue: http.StatusBadRequest, Message: message, Code: "multimodal_extractor_unavailable"}
	}
}

func stripAndInject(raw []byte, payload any, protocol string, rule selectedRule, text string, report *Report) ([]byte, Report, error) {
	_ = raw
	stripped := stripMedia(payload)
	out, err := injectVisualContext(stripped, protocol, rule.injectAs, text)
	if err != nil {
		return nil, *report, err
	}
	report.Applied = true
	report.Stripped = true
	report.Injected = true
	return out, *report, nil
}

func replaceMediaWithSummaries(payload any, protocol, label string, summaries map[string]string, maxBytes int) ([]byte, error) {
	replacement := visualReplacement{
		protocol:  effectiveMediaProtocol(payload, protocol),
		label:     strings.TrimSpace(label),
		summaries: summaries,
		bytesLeft: maxBytes,
		unlimited: maxBytes <= 0,
	}
	replaced, _ := replacement.replace(payload)
	return json.Marshal(replaced)
}

func effectiveMediaProtocol(payload any, protocol string) string {
	root, _ := payload.(map[string]any)
	if root["contents"] != nil {
		return "gemini"
	}
	return strings.ToLower(strings.TrimSpace(protocol))
}

func (r *visualReplacement) replace(jsonNode any) (any, bool) {
	switch typed := jsonNode.(type) {
	case map[string]any:
		return r.replaceMap(typed)
	case []any:
		return r.replaceSlice(typed), true
	default:
		return jsonNode, true
	}
}

func (r *visualReplacement) replaceMap(fields map[string]any) (any, bool) {
	if ref, ok := mediaRefFromMap(fields); ok {
		return r.textBlock(ref)
	}
	out := make(map[string]any, len(fields))
	for _, key := range sortedKeys(fields) {
		replaced, keep := r.replace(fields[key])
		if keep {
			out[key] = replaced
		}
	}
	return out, true
}

func (r *visualReplacement) replaceSlice(jsonNodes []any) []any {
	out := make([]any, 0, len(jsonNodes))
	for _, jsonNode := range jsonNodes {
		replaced, keep := r.replace(jsonNode)
		if keep {
			out = append(out, replaced)
		}
	}
	return out
}

func (r *visualReplacement) textBlock(ref mediaRef) (any, bool) {
	summary := strings.TrimSpace(r.summaries[mediaRefKey(ref)])
	if summary == "" || (!r.unlimited && r.bytesLeft <= 0) {
		return nil, false
	}
	if !r.unlimited {
		summary = boundedVisualSummary(summary, r.bytesLeft)
		r.bytesLeft -= len(summary)
	}
	return mediaTextBlock(r.protocol, r.label, summary), true
}

func boundedVisualSummary(summary string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	limited := limitStringBytes(summary, maxBytes)
	if len(limited) <= maxBytes {
		return limited
	}
	return safePrefix(summary, maxBytes)
}

func mediaTextBlock(protocol, label, summary string) map[string]any {
	if label == "" {
		label = "visual_context"
	}
	text := "[" + label + "]\n" + summary
	switch protocol {
	case "openai-response":
		return map[string]any{"type": "input_text", "text": text}
	case "gemini":
		return map[string]any{"text": text}
	default:
		return map[string]any{"type": "text", "text": text}
	}
}

func sortedKeys(fields map[string]any) []string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func collectMediaRefs(jsonRoot any) mediaCollection {
	collector := mediaCollector{}
	collector.collect(jsonRoot)
	collector.collection.refs = dedupeRefs(collector.refs)
	return collector.collection
}

func (c *mediaCollector) collect(jsonNode any) {
	switch typed := jsonNode.(type) {
	case map[string]any:
		c.collectObject(typed)
	case []any:
		for _, child := range typed {
			c.collect(child)
		}
	}
}

func (c *mediaCollector) collectObject(fields map[string]any) {
	if kind := mediaKindFromMap(fields); kind != "" {
		c.append(kind, mediaURLForObject(kind, fields))
		return
	}
	for key, child := range fields {
		if c.collectField(key, child) {
			continue
		}
		c.collect(child)
	}
}

func (c *mediaCollector) collectField(key string, mediaValue any) bool {
	lowerKey := strings.ToLower(strings.TrimSpace(key))
	kind := mediaKeyKind(lowerKey)
	if kind == "" && lowerKey != "inline_data" && lowerKey != "inlinedata" {
		return false
	}
	if kind == "" {
		kind = "image"
	}
	c.append(kind, mediaURLForKey(kind, mediaValue, lowerKey))
	return true
}

func (c *mediaCollector) append(kind, mediaURL string) {
	c.collection.items++
	if strings.TrimSpace(mediaURL) == "" {
		c.collection.unsupported = true
		return
	}
	c.refs = append(c.refs, mediaRef{Kind: kind, URL: mediaURL})
}

func mediaKindFromMap(m map[string]any) string {
	if rawType, ok := m["type"].(string); ok {
		switch strings.ToLower(strings.TrimSpace(rawType)) {
		case "input_image", "image", "image_url":
			return "image"
		case "input_file", "file":
			return "file"
		case "input_video", "video":
			return "video"
		case "input_audio", "audio":
			return "audio"
		}
	}
	if _, ok := m["inline_data"]; ok {
		return "image"
	}
	if _, ok := m["inlineData"]; ok {
		return "image"
	}
	if _, ok := m["image_url"]; ok {
		return "image"
	}
	if _, ok := m["file_url"]; ok {
		return "file"
	}
	if _, ok := m["video_url"]; ok {
		return "video"
	}
	if _, ok := m["audio_url"]; ok {
		return "audio"
	}
	if _, ok := m["file_data"]; ok {
		return "file"
	}
	if _, ok := m["fileData"]; ok {
		return "file"
	}
	return ""
}

func mediaRefFromMap(fields map[string]any) (mediaRef, bool) {
	kind := mediaKindFromMap(fields)
	if kind == "" {
		return mediaRef{}, false
	}
	return mediaRef{Kind: kind, URL: mediaURLForObject(kind, fields)}, true
}

func mediaRefKey(ref mediaRef) string {
	return ref.Kind + "\x00" + ref.URL
}

func mediaKeyKind(key string) string {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "image_url":
		return "image"
	case "file_url", "file_data", "filedata":
		return "file"
	case "video_url":
		return "video"
	case "audio_url":
		return "audio"
	default:
		return ""
	}
}

func mediaURLForObject(kind string, fields map[string]any) string {
	for _, key := range []string{"image_url", "file_url", "video_url", "audio_url", "file_data", "fileData", "url", "uri"} {
		if ref := mediaURLFromValue(fields[key]); ref != "" {
			return ref
		}
	}
	for _, key := range []string{"source", "inline_data", "inlineData"} {
		if source, ok := fields[key].(map[string]any); ok {
			if ref := mediaURLForKey(kind, source, key); ref != "" {
				return ref
			}
		}
	}
	return ""
}

func mediaURLForKey(kind string, mediaValue any, key string) string {
	if key == "inline_data" || key == "inlinedata" || key == "inlineData" {
		return inlineDataURL(mediaValue)
	}
	if source, ok := mediaValue.(map[string]any); ok {
		if sourceType, _ := source["type"].(string); strings.EqualFold(strings.TrimSpace(sourceType), "base64") {
			return inlineDataURL(source)
		}
		return mediaURLForObject(kind, source)
	}
	return mediaURLFromValue(mediaValue)
}

func mediaURLFromValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		for _, key := range []string{"url", "uri", "path", "file_url", "image_url"} {
			if v, ok := typed[key].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

func inlineDataURL(mediaValue any) string {
	fields, ok := mediaValue.(map[string]any)
	if !ok {
		return ""
	}
	encoded, _ := fields["data"].(string)
	mime, _ := fields["mimeType"].(string)
	if mime == "" {
		mime, _ = fields["mime_type"].(string)
	}
	if mime == "" {
		mime, _ = fields["media_type"].(string)
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(mime)), "image/") || strings.TrimSpace(encoded) == "" {
		return ""
	}
	if _, err := base64.StdEncoding.DecodeString(encoded); err != nil {
		return ""
	}
	return "data:" + strings.TrimSpace(mime) + ";base64," + encoded
}

func dedupeRefs(refs []mediaRef) []mediaRef {
	out := make([]mediaRef, 0, len(refs))
	seen := map[string]struct{}{}
	for _, ref := range refs {
		key := ref.Kind + "\x00" + ref.URL
		if strings.TrimSpace(ref.URL) == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, ref)
	}
	return out
}

func stripMedia(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		if mediaKindFromMap(typed) != "" {
			return nil
		}
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			if isMediaKey(key) {
				continue
			}
			stripped := stripMedia(child)
			if stripped != nil {
				out[key] = stripped
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, child := range typed {
			stripped := stripMedia(child)
			if stripped != nil {
				out = append(out, stripped)
			}
		}
		return out
	default:
		return value
	}
}

func isMediaKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "image_url", "file_url", "video_url", "audio_url", "inline_data", "inlinedata", "file_data", "filedata":
		return true
	default:
		return false
	}
}

func injectVisualContext(payload any, protocol, injectAs, contextText string) ([]byte, error) {
	root, ok := payload.(map[string]any)
	if !ok {
		return json.Marshal(payload)
	}
	text := strings.TrimSpace(contextText)
	if text == "" {
		return json.Marshal(payload)
	}
	label := strings.TrimSpace(injectAs)
	if label == "" {
		label = "visual_context"
	}
	if strings.EqualFold(protocol, "openai-response") {
		item := map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": "[" + label + "]\n" + text},
			},
		}
		items, _ := root["input"].([]any)
		root["input"] = append([]any{item}, items...)
		return json.Marshal(root)
	}
	if strings.EqualFold(protocol, "gemini") || root["contents"] != nil {
		contextEntry := map[string]any{"role": "user", "parts": []any{map[string]any{"text": "[" + label + "]\n" + text}}}
		items, _ := root["contents"].([]any)
		root["contents"] = append([]any{contextEntry}, items...)
		return json.Marshal(root)
	}
	if strings.EqualFold(protocol, "claude") {
		contextEntry := map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "[" + label + "]\n" + text}}}
		items, _ := root["messages"].([]any)
		root["messages"] = append([]any{contextEntry}, items...)
		return json.Marshal(root)
	}
	contextEntry := map[string]any{"role": "user", "content": "[" + label + "]\n" + text}
	items, _ := root["messages"].([]any)
	root["messages"] = append([]any{contextEntry}, items...)
	return json.Marshal(root)
}

func extractVisualContext(ctx context.Context, refs []mediaRef, route Route, extractor config.MultimodalExtractorConfig) ([]string, error) {
	switch strings.ToLower(strings.TrimSpace(extractor.Type)) {
	case "http":
		return callHTTPExtractor(ctx, refs, route, extractor)
	case "zai-vision-http":
		return callZAIVisionHTTPExtractor(ctx, refs, extractor)
	case "mcp":
		return callMCPExtractor(ctx, refs, extractor)
	default:
		return nil, fmt.Errorf("multimodal adapter extractor %q has unsupported type %q", extractor.Name, extractor.Type)
	}
}

func callHTTPExtractor(ctx context.Context, refs []mediaRef, route Route, extractor config.MultimodalExtractorConfig) ([]string, error) {
	if strings.TrimSpace(extractor.Endpoint) == "" {
		return nil, fmt.Errorf("multimodal adapter extractor %q endpoint is not configured", extractor.Name)
	}
	timeout := time.Duration(extractor.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, err := json.Marshal(map[string]any{
		"media":             refs,
		"prompt":            extractor.Prompt,
		"requested_model":   route.RequestedModel,
		"upstream_provider": route.UpstreamProvider,
		"upstream_model":    route.UpstreamModel,
		"protocol":          route.Protocol,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, os.ExpandEnv(extractor.Endpoint), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range extractor.Headers {
		if strings.TrimSpace(key) != "" {
			req.Header.Set(key, os.ExpandEnv(value))
		}
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("multimodal adapter extractor %q request failed: %w", extractor.Name, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("multimodal adapter extractor %q returned status %d: %s", extractor.Name, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	text := extractHTTPText(data)
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("multimodal adapter extractor %q returned no text", extractor.Name)
	}
	return []string{text}, nil
}

func callZAIVisionHTTPExtractor(ctx context.Context, refs []mediaRef, extractor config.MultimodalExtractorConfig) ([]string, error) {
	if strings.TrimSpace(extractor.Endpoint) == "" {
		return nil, fmt.Errorf("multimodal adapter extractor %q endpoint is not configured", extractor.Name)
	}
	imageRefs := make([]mediaRef, 0, len(refs))
	for _, ref := range refs {
		if ref.Kind == "image" && strings.TrimSpace(ref.URL) != "" {
			imageRefs = append(imageRefs, ref)
		}
	}
	if len(imageRefs) == 0 {
		return nil, fmt.Errorf("multimodal adapter extractor %q requires at least one image input", extractor.Name)
	}
	timeout := time.Duration(extractor.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	prompt := strings.TrimSpace(extractor.Prompt)
	if prompt == "" {
		prompt = "Describe this visual input for a coding assistant. Extract visible text, UI elements, errors, filenames, paths, code snippets, charts, and anything needed to answer the user's request. Be concise and factual."
	}
	content := []map[string]any{{"type": "text", "text": prompt}}
	for _, ref := range imageRefs {
		content = append(content, map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": strings.TrimSpace(ref.URL),
			},
		})
	}
	model := strings.TrimSpace(extractor.ToolName)
	if model == "" {
		model = strings.TrimSpace(extractor.Env["model"])
	}
	if model == "" {
		model = "glm-5.1"
	}
	maxTokens := 512
	if rawMaxTokens := strings.TrimSpace(extractor.Env["max_tokens"]); rawMaxTokens != "" {
		if parsed, err := strconv.Atoi(rawMaxTokens); err == nil && parsed > 0 {
			maxTokens = parsed
		}
	}
	requestBody := map[string]any{
		"model": model,
		"messages": []map[string]any{
			{
				"role":    "user",
				"content": content,
			},
		},
		"max_tokens": maxTokens,
	}
	if !strings.EqualFold(strings.TrimSpace(extractor.Env["disable_thinking"]), "false") {
		requestBody["thinking"] = map[string]any{"type": "disabled"}
	}
	body, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(os.ExpandEnv(extractor.Endpoint), "/")
	if !strings.HasSuffix(endpoint, "/chat/completions") {
		endpoint += "/chat/completions"
	}
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range extractor.Headers {
		if strings.TrimSpace(key) != "" {
			req.Header.Set(key, os.ExpandEnv(value))
		}
	}
	applyExtractorIdentityFingerprint(req.Header, extractor)
	if req.Header.Get("Authorization") == "" {
		if apiKey := strings.TrimSpace(os.ExpandEnv(extractor.Env["api_key"])); apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("multimodal adapter extractor %q request failed: %w", extractor.Name, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("multimodal adapter extractor %q returned status %d: %s", extractor.Name, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	text := extractHTTPText(data)
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("multimodal adapter extractor %q returned no text", extractor.Name)
	}
	return []string{text}, nil
}

func applyExtractorIdentityFingerprint(headers http.Header, extractor config.MultimodalExtractorConfig) {
	if headers == nil || !strings.EqualFold(strings.TrimSpace(extractor.Env["identity_fingerprint"]), "codex") {
		return
	}
	if strings.TrimSpace(headers.Get("Session_id")) == "" {
		headers.Set("Session_id", uuid.NewString())
	}
}

func extractHTTPText(data []byte) string {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return strings.TrimSpace(string(data))
	}
	var texts []string
	var walk func(any)
	walk = func(v any) {
		switch typed := v.(type) {
		case map[string]any:
			for _, key := range []string{"text", "summary", "description", "result", "content"} {
				if s, ok := typed[key].(string); ok && strings.TrimSpace(s) != "" {
					texts = append(texts, strings.TrimSpace(s))
				}
			}
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		case string:
			if strings.TrimSpace(typed) != "" {
				texts = append(texts, strings.TrimSpace(typed))
			}
		}
	}
	walk(value)
	if len(texts) == 0 {
		return ""
	}
	return texts[0]
}

func callMCPExtractor(ctx context.Context, refs []mediaRef, extractor config.MultimodalExtractorConfig) ([]string, error) {
	timeout := time.Duration(extractor.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, err := startMCP(callCtx, extractor)
	if err != nil {
		return nil, err
	}
	defer client.close()
	tools, err := client.listTools(callCtx)
	if err != nil {
		return nil, err
	}
	tool := selectTool(tools, extractor.ToolName)
	if tool.Name == "" {
		return nil, fmt.Errorf("multimodal adapter extractor %q has no MCP tool available", extractor.Name)
	}
	out := make([]string, 0, len(refs))
	for i, ref := range refs {
		prepared, cleanup, errPrepare := prepareMCPMediaRef(callCtx, ref)
		if errPrepare != nil {
			return nil, errPrepare
		}
		if cleanup != nil {
			defer cleanup()
		}
		args := buildToolArgs(tool, prepared, extractor.Prompt)
		text, errCall := client.callTool(callCtx, tool.Name, args)
		if errCall != nil {
			return nil, errCall
		}
		if strings.TrimSpace(text) != "" {
			out = append(out, fmt.Sprintf("Visual input %d (%s: %s):\n%s", i+1, ref.Kind, describeMediaRef(ref), strings.TrimSpace(text)))
		}
	}
	return out, nil
}

func prepareMCPMediaRef(ctx context.Context, ref mediaRef) (mediaRef, func(), error) {
	if isDataURLRef(ref.URL) {
		return prepareMCPDataURLRef(ref)
	}
	if !isRemoteHTTPRef(ref.URL) {
		return ref, nil, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref.URL, nil)
	if err != nil {
		return ref, nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ref, nil, fmt.Errorf("multimodal adapter MCP media download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ref, nil, fmt.Errorf("multimodal adapter MCP media download returned status %d", resp.StatusCode)
	}
	file, err := os.CreateTemp("", "clirelay-mcp-media-*"+mcpMediaExtension(ref.URL, resp.Header.Get("Content-Type")))
	if err != nil {
		return ref, nil, err
	}
	written, copyErr := io.Copy(file, io.LimitReader(resp.Body, mcpMediaMaxBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(file.Name())
		return ref, nil, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(file.Name())
		return ref, nil, closeErr
	}
	if written > mcpMediaMaxBytes {
		_ = os.Remove(file.Name())
		return ref, nil, fmt.Errorf("multimodal adapter MCP media download exceeds %d bytes", mcpMediaMaxBytes)
	}
	prepared := ref
	prepared.URL = file.Name()
	return prepared, func() { _ = os.Remove(file.Name()) }, nil
}

func prepareMCPDataURLRef(ref mediaRef) (mediaRef, func(), error) {
	mediaType, payload, err := parseBase64DataURL(ref.URL)
	if err != nil {
		return ref, nil, err
	}
	if base64.StdEncoding.DecodedLen(len(payload)) > int(mcpMediaMaxBytes) {
		return ref, nil, fmt.Errorf("multimodal adapter MCP media data URL exceeds %d bytes", mcpMediaMaxBytes)
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(payload)
	}
	if err != nil {
		return ref, nil, fmt.Errorf("multimodal adapter MCP media data URL base64 decode failed: %w", err)
	}
	if int64(len(data)) > mcpMediaMaxBytes {
		return ref, nil, fmt.Errorf("multimodal adapter MCP media data URL exceeds %d bytes", mcpMediaMaxBytes)
	}
	file, err := os.CreateTemp("", "clirelay-mcp-media-*"+mcpMediaExtension("", mediaType))
	if err != nil {
		return ref, nil, err
	}
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return ref, nil, err
	}
	if err = file.Close(); err != nil {
		_ = os.Remove(file.Name())
		return ref, nil, err
	}
	prepared := ref
	prepared.URL = file.Name()
	return prepared, func() { _ = os.Remove(file.Name()) }, nil
}

func parseBase64DataURL(value string) (string, string, error) {
	value = strings.TrimSpace(value)
	header, payload, ok := strings.Cut(value, ",")
	if !ok || !strings.HasPrefix(strings.ToLower(header), "data:") {
		return "", "", fmt.Errorf("multimodal adapter MCP media data URL is invalid")
	}
	if !strings.Contains(strings.ToLower(header), ";base64") {
		return "", "", fmt.Errorf("multimodal adapter MCP media data URL is not base64 encoded")
	}
	mediaType := strings.TrimSpace(strings.TrimPrefix(header, "data:"))
	if semi := strings.Index(mediaType, ";"); semi >= 0 {
		mediaType = mediaType[:semi]
	}
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	payload = strings.TrimSpace(payload)
	if unescaped, err := url.PathUnescape(payload); err == nil {
		payload = unescaped
	}
	if payload == "" {
		return "", "", fmt.Errorf("multimodal adapter MCP media data URL has empty payload")
	}
	return mediaType, payload, nil
}

func isRemoteHTTPRef(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")
}

func isDataURLRef(value string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "data:")
}

func describeMediaRef(ref mediaRef) string {
	if isDataURLRef(ref.URL) {
		mediaType, payload, err := parseBase64DataURL(ref.URL)
		if err == nil {
			return fmt.Sprintf("inline %s data URL (%d base64 bytes)", mediaType, len(payload))
		}
		return "inline data URL"
	}
	return ref.URL
}

func mcpMediaExtension(rawURL, contentType string) string {
	switch {
	case strings.Contains(contentType, "png"):
		return ".png"
	case strings.Contains(contentType, "jpeg"), strings.Contains(contentType, "jpg"):
		return ".jpg"
	case strings.Contains(contentType, "gif"):
		return ".gif"
	case strings.Contains(contentType, "webp"):
		return ".webp"
	}
	parsed, err := url.Parse(rawURL)
	if err == nil {
		ext := strings.ToLower(filepath.Ext(parsed.Path))
		switch ext {
		case ".png", ".jpg", ".jpeg", ".gif", ".webp":
			return ext
		}
	}
	return ".bin"
}

type mcpClient struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	reader    *bufio.Reader
	nextID    int
	transport string
}

type mcpTool struct {
	Name       string
	InputKeys  []string
	Required   map[string]struct{}
	SchemaType string
}

func startMCP(ctx context.Context, extractor config.MultimodalExtractorConfig) (*mcpClient, error) {
	if strings.TrimSpace(extractor.Command) == "" {
		return nil, fmt.Errorf("multimodal adapter extractor %q command is not configured", extractor.Name)
	}
	cmd := exec.CommandContext(ctx, extractor.Command, extractor.Args...)
	cmd.Env = os.Environ()
	for key, value := range extractor.Env {
		key = strings.TrimSpace(key)
		if key != "" {
			cmd.Env = append(cmd.Env, key+"="+os.ExpandEnv(value))
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	client := &mcpClient{cmd: cmd, stdin: stdin, reader: bufio.NewReader(stdout), nextID: 1, transport: mcpTransport(extractor)}
	if _, err = client.request(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "clirelay", "version": "dev"},
	}); err != nil {
		client.close()
		if strings.TrimSpace(stderr.String()) != "" {
			return nil, fmt.Errorf("multimodal adapter initialize: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("multimodal adapter initialize: %w", err)
	}
	_ = client.notify(ctx, "notifications/initialized", map[string]any{})
	return client, nil
}

func mcpTransport(extractor config.MultimodalExtractorConfig) string {
	for _, key := range []string{"MCP_TRANSPORT", "mcp_transport", "transport"} {
		if value := strings.ToLower(strings.TrimSpace(extractor.Env[key])); value != "" {
			switch value {
			case "jsonl", "json-lines", "line", "line-json":
				return "jsonl"
			}
		}
	}
	return "content-length"
}

func (c *mcpClient) listTools(ctx context.Context) ([]mcpTool, error) {
	result, err := c.request(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	rawTools, _ := result["tools"].([]any)
	tools := make([]mcpTool, 0, len(rawTools))
	for _, raw := range rawTools {
		toolMap, _ := raw.(map[string]any)
		name, _ := toolMap["name"].(string)
		if strings.TrimSpace(name) == "" {
			continue
		}
		tool := mcpTool{Name: strings.TrimSpace(name), Required: map[string]struct{}{}}
		if schema, _ := toolMap["inputSchema"].(map[string]any); schema != nil {
			if typ, _ := schema["type"].(string); typ != "" {
				tool.SchemaType = typ
			}
			if props, _ := schema["properties"].(map[string]any); props != nil {
				for key := range props {
					tool.InputKeys = append(tool.InputKeys, key)
				}
				sort.Strings(tool.InputKeys)
			}
			if required, _ := schema["required"].([]any); required != nil {
				for _, item := range required {
					if key, _ := item.(string); key != "" {
						tool.Required[key] = struct{}{}
					}
				}
			}
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

func selectTool(tools []mcpTool, configured string) mcpTool {
	configured = strings.TrimSpace(configured)
	for _, tool := range tools {
		if configured != "" && strings.EqualFold(tool.Name, configured) {
			return tool
		}
	}
	if configured != "" {
		return mcpTool{}
	}
	for _, tool := range tools {
		lower := strings.ToLower(tool.Name)
		if strings.Contains(lower, "vision") || strings.Contains(lower, "image") || strings.Contains(lower, "visual") || strings.Contains(lower, "ocr") {
			return tool
		}
	}
	if len(tools) > 0 {
		return tools[0]
	}
	return mcpTool{}
}

func buildToolArgs(tool mcpTool, ref mediaRef, prompt string) map[string]any {
	args := map[string]any{}
	keys := append([]string(nil), tool.InputKeys...)
	if len(keys) == 0 {
		keys = []string{"image_url", "file_url", "prompt"}
	}
	if key := chooseMediaArg(keys, ref.Kind, ref.URL); key != "" {
		args[key] = ref.URL
	}
	if key := choosePromptArg(keys); key != "" {
		args[key] = prompt
	}
	for key := range tool.Required {
		if _, ok := args[key]; !ok {
			args[key] = prompt
		}
	}
	return args
}

func chooseMediaArg(keys []string, kind, ref string) string {
	var preferred []string
	if kind == "image" {
		preferred = []string{"image_url", "image", "url", "path", "image_path", "file", "file_path"}
	} else if kind == "file" {
		preferred = []string{"file_url", "file", "url", "path", "file_path"}
	} else {
		preferred = []string{"video_url", "video", "url", "path", "video_path", "file", "file_path"}
	}
	if strings.HasPrefix(ref, "file://") || strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, ".") {
		preferred = append([]string{"path", kind + "_path", "file_path"}, preferred...)
	}
	for _, want := range preferred {
		for _, key := range keys {
			if strings.EqualFold(key, want) {
				return key
			}
		}
	}
	for _, key := range keys {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "url") || strings.Contains(lower, "path") || strings.Contains(lower, kind) || strings.Contains(lower, "file") {
			return key
		}
	}
	return ""
}

func choosePromptArg(keys []string) string {
	for _, want := range []string{"prompt", "query", "instruction", "instructions", "question"} {
		for _, key := range keys {
			if strings.EqualFold(key, want) {
				return key
			}
		}
	}
	return ""
}

func (c *mcpClient) callTool(ctx context.Context, name string, args map[string]any) (string, error) {
	result, err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", err
	}
	if isError, _ := result["isError"].(bool); isError {
		return "", fmt.Errorf("multimodal adapter MCP tool %s returned error: %s", name, extractMCPContentText(result))
	}
	return extractMCPContentText(result), nil
}

func extractMCPContentText(result map[string]any) string {
	content, _ := result["content"].([]any)
	var parts []string
	for _, item := range content {
		m, _ := item.(map[string]any)
		if text, _ := m["text"].(string); strings.TrimSpace(text) != "" {
			parts = append(parts, strings.TrimSpace(text))
		}
	}
	return strings.Join(parts, "\n")
}

func (c *mcpClient) request(ctx context.Context, method string, params any) (map[string]any, error) {
	id := c.nextID
	c.nextID++
	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		msg, err := c.read(ctx)
		if err != nil {
			return nil, err
		}
		if intFromAny(msg["id"]) != id {
			continue
		}
		if rawErr, ok := msg["error"].(map[string]any); ok {
			return nil, fmt.Errorf("MCP %s error: %v", method, rawErr["message"])
		}
		result, _ := msg["result"].(map[string]any)
		return result, nil
	}
}

func (c *mcpClient) notify(ctx context.Context, method string, params any) error {
	_ = ctx
	return c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *mcpClient) write(msg map[string]any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if strings.EqualFold(c.transport, "jsonl") {
		_, err = fmt.Fprintf(c.stdin, "%s\n", data)
		return err
	}
	_, err = fmt.Fprintf(c.stdin, "Content-Length: %d\r\n\r\n%s", len(data), data)
	return err
}

func (c *mcpClient) read(ctx context.Context) (map[string]any, error) {
	type result struct {
		msg map[string]any
		err error
	}
	ch := make(chan result, 1)
	go func() {
		msg, err := c.readOne()
		ch <- result{msg: msg, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case got := <-ch:
		return got.msg, got.err
	}
}

func (c *mcpClient) readOne() (map[string]any, error) {
	if strings.EqualFold(c.transport, "jsonl") {
		return c.readOneJSONL()
	}
	contentLength := -1
	for {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(parts[0]), "content-length") {
			n, err := strconv.Atoi(strings.TrimSpace(parts[1]))
			if err == nil {
				contentLength = n
			}
		}
	}
	if contentLength < 0 {
		return nil, fmt.Errorf("MCP response missing Content-Length")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(c.reader, body); err != nil {
		return nil, err
	}
	var msg map[string]any
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, err
	}
	return msg, nil
}

func (c *mcpClient) readOneJSONL() (map[string]any, error) {
	for {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		return msg, nil
	}
}

func (c *mcpClient) close() {
	if c == nil {
		return
	}
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_, _ = c.cmd.Process.Wait()
	}
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case json.Number:
		i, _ := typed.Int64()
		return int(i)
	default:
		return 0
	}
}

func limitStringBytes(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	const marker = "\n...[truncated visual context]...\n"
	edge := (maxBytes - len(marker)) / 2
	if edge <= 0 {
		return marker
	}
	return safePrefix(value, edge) + marker + safeSuffix(value, edge)
}

func safePrefix(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	end := 0
	for i := range value {
		if i > maxBytes {
			break
		}
		end = i
	}
	return value[:end]
}

func safeSuffix(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	start := len(value)
	for i := range value {
		if len(value)-i <= maxBytes {
			start = i
			break
		}
	}
	return value[start:]
}
