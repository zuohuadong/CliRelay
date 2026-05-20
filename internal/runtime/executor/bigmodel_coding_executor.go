package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/multimodaladapter"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/sjson"
)

// BigModelCodingExecutor is a first-class upstream for Zhipu Coding Plan.
// It keeps GLM-5.1-specific MCP and media adaptation out of the generic
// OpenAI-compatible executor.
type BigModelCodingExecutor struct {
	*OpenAICompatExecutor
}

func NewBigModelCodingExecutor(cfg *config.Config) *BigModelCodingExecutor {
	return &BigModelCodingExecutor{OpenAICompatExecutor: NewOpenAICompatExecutor("bigmodel-coding", cfg)}
}

func (e *BigModelCodingExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	endpoint := "/chat/completions"
	imagePassthrough := false
	switch opts.Alt {
	case "responses/compact":
		to = sdktranslator.FromString("openai-response")
		endpoint = "/responses/compact"
	case "images/generations":
		endpoint = "/images/generations"
		imagePassthrough = true
	case "images/edits":
		endpoint = "/images/edits"
		imagePassthrough = true
	}
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	var translated []byte
	if imagePassthrough {
		translated = e.overrideModel(req.Payload, baseModel)
	} else {
		requestedModel := helps.PayloadRequestedModel(opts, req.Model)
		adaptedPayload, errAdapt := e.applyMultimodalAdapter(ctx, req.Payload, baseModel, from.String(), requestedModel)
		if errAdapt != nil {
			err = errAdapt
			return resp, err
		}
		originalPayload := originalPayloadSource
		originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, opts.Stream)
		translated = sdktranslator.TranslateRequest(from, to, baseModel, adaptedPayload, opts.Stream)

		translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
		if err != nil {
			return resp, err
		}
		if shouldNormalizeKimiCompatPayload(baseModel) {
			translated, err = normalizeKimiToolMessageLinks(translated)
			if err != nil {
				return resp, err
			}
		}
		requestPath := helps.PayloadRequestPath(opts)
		translated = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", translated, originalTranslated, requestedModel, requestPath, opts.Headers)
		if opts.Alt == "responses/compact" {
			if updated, errDelete := sjson.DeleteBytes(translated, "stream"); errDelete == nil {
				translated = updated
			}
		}
		if opts.Alt == "" {
			translated, err = e.injectOfficialMCPTools(translated, baseModel, apiKey)
			if err != nil {
				return resp, err
			}
		}
		translated, err = e.normalizeBigModelTools(translated, baseURL)
		if err != nil {
			return resp, err
		}
	}

	url := strings.TrimSuffix(baseURL, "/") + endpoint
	requestBody := translated
	contentType := "application/json"
	if opts.Alt == "images/edits" && imageEditPayloadHasUploads(translated) {
		var multipartType string
		requestBody, multipartType, err = buildImageEditsMultipartBody(translated)
		if err != nil {
			return resp, statusErr{code: http.StatusBadRequest, msg: err.Error()}
		}
		contentType = multipartType
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(requestBody))
	if err != nil {
		return resp, err
	}
	httpReq.Header.Set("Content-Type", contentType)
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	e.applyCustomHeadersAndIdentityFingerprint(httpReq, auth, false)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      redactSensitiveJSONForLog(requestBody),
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("bigmodel coding executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	reporter.EnsurePublished(ctx)
	if imagePassthrough {
		resp = cliproxyexecutor.Response{Payload: body, Headers: httpResp.Header.Clone()}
		return resp, nil
	}
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, body, &param)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

func (e *BigModelCodingExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return nil, err
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	adaptedPayload, errAdapt := e.applyMultimodalAdapter(ctx, req.Payload, baseModel, from.String(), requestedModel)
	if errAdapt != nil {
		err = errAdapt
		return nil, err
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, true)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, adaptedPayload, true)

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}
	if shouldNormalizeKimiCompatPayload(baseModel) {
		translated, err = normalizeKimiToolMessageLinks(translated)
		if err != nil {
			return nil, err
		}
	}
	requestPath := helps.PayloadRequestPath(opts)
	translated = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", translated, originalTranslated, requestedModel, requestPath, opts.Headers)
	translated, _ = sjson.SetBytes(translated, "stream_options.include_usage", true)
	translated, err = e.injectOfficialMCPTools(translated, baseModel, apiKey)
	if err != nil {
		return nil, err
	}
	translated, err = e.normalizeBigModelTools(translated, baseURL)
	if err != nil {
		return nil, err
	}

	url := strings.TrimSuffix(baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	e.applyCustomHeadersAndIdentityFingerprint(httpReq, auth, false)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      redactSensitiveJSONForLog(translated),
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("bigmodel coding executor: close response body error: %v", errClose)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("bigmodel coding executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := helps.ParseOpenAIStreamUsage(line); ok {
				reporter.Publish(ctx, detail)
			}
			trimmedLine := bytes.TrimSpace(line)
			if len(trimmedLine) == 0 {
				continue
			}
			if !bytes.HasPrefix(trimmedLine, []byte("data:")) {
				if bytes.HasPrefix(trimmedLine, []byte(":")) || bytes.HasPrefix(trimmedLine, []byte("event:")) ||
					bytes.HasPrefix(trimmedLine, []byte("id:")) || bytes.HasPrefix(trimmedLine, []byte("retry:")) {
					continue
				}
				if bytes.HasPrefix(trimmedLine, []byte("{")) || bytes.HasPrefix(trimmedLine, []byte("[")) {
					streamErr := statusErr{code: http.StatusBadGateway, msg: string(trimmedLine)}
					helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
					reporter.PublishFailure(ctx, streamErr)
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
					case <-ctx.Done():
					}
					return
				}
				continue
			}
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, bytes.Clone(trimmedLine), &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		} else {
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, []byte("data: [DONE]"), &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		reporter.EnsurePublished(ctx)
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *BigModelCodingExecutor) applyMultimodalAdapter(ctx context.Context, payload []byte, model, protocol, requestedModel string) ([]byte, error) {
	if !e.isGLM51(model) || e.cfg == nil {
		return payload, nil
	}
	out, _, err := multimodaladapter.Apply(ctx, payload, multimodaladapter.Route{
		RequestedModel:   requestedModel,
		UpstreamProvider: e.Identifier(),
		UpstreamModel:    thinking.ParseSuffix(model).ModelName,
		Protocol:         protocol,
	}, e.cfg.MultimodalAdapters)
	if err != nil {
		return payload, err
	}
	return out, nil
}

func (e *BigModelCodingExecutor) injectOfficialMCPTools(payload []byte, model, apiKey string) ([]byte, error) {
	if !e.isGLM51(model) || strings.TrimSpace(apiKey) == "" {
		return payload, nil
	}
	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		return nil, fmt.Errorf("inject bigmodel coding MCP tools: invalid payload: %w", err)
	}
	tools, _ := root["tools"].([]any)
	added := false
	for _, spec := range []struct {
		label string
		url   string
	}{
		{label: "web-search-prime", url: "https://open.bigmodel.cn/api/mcp/web_search_prime/mcp"},
		{label: "web-reader", url: "https://open.bigmodel.cn/api/mcp/web_reader/mcp"},
	} {
		if hasMCPServerTool(tools, spec.label, spec.url) {
			continue
		}
		tools = append(tools, map[string]any{
			"type":           "mcp",
			"server_label":   spec.label,
			"server_url":     spec.url,
			"transport_type": "streamable-http",
			"headers": map[string]any{
				"Authorization": "Bearer " + strings.TrimSpace(apiKey),
			},
		})
		added = true
	}
	if !added {
		return payload, nil
	}
	root["tools"] = tools
	out, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("inject bigmodel coding MCP tools: encode payload: %w", err)
	}
	return out, nil
}

func (e *BigModelCodingExecutor) isGLM51(model string) bool {
	model = strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(model).ModelName))
	return model == "glm-5.1"
}

func hasMCPServerTool(tools []any, label, serverURL string) bool {
	label = strings.ToLower(strings.TrimSpace(label))
	serverURL = strings.TrimSpace(serverURL)
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if strings.ToLower(strings.TrimSpace(fmt.Sprint(tool["type"]))) != "mcp" {
			continue
		}
		mcp := objectValue(tool["mcp"])
		if mcp == nil {
			mcp = tool
		}
		gotLabel := strings.ToLower(strings.TrimSpace(fmt.Sprint(mcp["server_label"])))
		gotURL := strings.TrimSpace(fmt.Sprint(mcp["server_url"]))
		if gotLabel == label || gotURL == serverURL {
			return true
		}
	}
	return false
}

func redactSensitiveJSONForLog(body []byte) []byte {
	if len(body) == 0 || !json.Valid(body) {
		return body
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return body
	}
	redactSensitiveJSONValue(value, "")
	out, err := json.Marshal(value)
	if err != nil {
		return body
	}
	return out
}

func redactSensitiveJSONValue(value any, parentKey string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if text, ok := child.(string); ok && isSensitiveJSONKey(key, parentKey) {
				typed[key] = maskSensitiveJSONValue(key, text)
				continue
			}
			redactSensitiveJSONValue(child, key)
		}
	case []any:
		for _, child := range typed {
			redactSensitiveJSONValue(child, parentKey)
		}
	}
}

func isSensitiveJSONKey(key, parentKey string) bool {
	lowerKey := strings.ToLower(strings.TrimSpace(key))
	lowerParent := strings.ToLower(strings.TrimSpace(parentKey))
	if lowerKey == "authorization" || strings.Contains(lowerKey, "api-key") ||
		strings.Contains(lowerKey, "apikey") || strings.Contains(lowerKey, "api_key") ||
		strings.Contains(lowerKey, "token") || strings.Contains(lowerKey, "secret") {
		return true
	}
	return lowerParent == "headers" && strings.Contains(lowerKey, "authorization")
}

func maskSensitiveJSONValue(key, value string) string {
	if strings.Contains(strings.ToLower(strings.TrimSpace(key)), "authorization") {
		return util.MaskAuthorizationHeader(value)
	}
	return util.HideAPIKey(value)
}
