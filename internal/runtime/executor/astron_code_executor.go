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
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const astronCodeUserInputGuidanceMarker = "Astron interaction guidance:"

type AstronCodeExecutor struct {
	*OpenAICompatExecutor
}

func NewAstronCodeExecutor(cfg *config.Config) *AstronCodeExecutor {
	return &AstronCodeExecutor{OpenAICompatExecutor: NewOpenAICompatExecutor("astron-code", cfg)}
}

func (e *AstronCodeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
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
	compactPassthrough := false
	switch opts.Alt {
	case "responses/compact":
		compactPassthrough = true
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
	} else if compactPassthrough {
		translated, err = buildAstronCompactChatPayload(originalPayloadSource, baseModel)
		if err != nil {
			return resp, err
		}
	} else {
		requestedModel := helps.PayloadRequestedModel(opts, req.Model)
		adaptedPayload, errAdapt := e.applyAstronMultimodalAdapter(ctx, req.Payload, baseModel, from.String(), requestedModel)
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
		translated, err = e.normalizeAstronPayload(translated, baseModel)
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
	httpReq.Header.Set("User-Agent", "cli-proxy-astron-code")
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
			log.Errorf("astron code executor: close response body error: %v", errClose)
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
	if compactPassthrough {
		wrapped, errWrap := buildAstronCompactResponse(originalPayloadSource, baseModel, body, httpResp.Header)
		if errWrap != nil {
			err = errWrap
			return resp, err
		}
		reporter.Publish(ctx, helps.ParseOpenAIUsage(wrapped.Payload))
		reporter.EnsurePublished(ctx)
		return wrapped, nil
	}
	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	reporter.EnsurePublished(ctx)
	if imagePassthrough {
		resp = cliproxyexecutor.Response{Payload: body, Headers: httpResp.Header.Clone()}
		return resp, nil
	}
	body = ensureAstronNonStreamToolCallIDs(body)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, body, &param)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

func (e *AstronCodeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
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
	adaptedPayload, errAdapt := e.applyAstronMultimodalAdapter(ctx, req.Payload, baseModel, from.String(), requestedModel)
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
	translated, err = e.normalizeAstronPayload(translated, baseModel)
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
	httpReq.Header.Set("User-Agent", "cli-proxy-astron-code")
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
			log.Errorf("astron code executor: close response body error: %v", errClose)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("astron code executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		var param any
		tcIDSeq := &astronToolCallIDSeq{}
		sawSSEData := false
		emitDone := func() bool {
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, []byte("data: [DONE]"), &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return false
				}
			}
			return true
		}
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
					if sawSSEData {
						helps.LogWithRequestID(ctx).Debugf("astron code stream ended with non-SSE payload after data: %s", helps.SummarizeErrorBody("application/json", trimmedLine))
						if emitDone() {
							reporter.EnsurePublished(ctx)
						}
						return
					}
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
			sawSSEData = true
			normalizedLine := ensureAstronToolCallIDs(bytes.Clone(trimmedLine), tcIDSeq)
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, normalizedLine, &param)
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
			emitDone()
		}
		reporter.EnsurePublished(ctx)
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *AstronCodeExecutor) applyAstronMultimodalAdapter(ctx context.Context, payload []byte, model, protocol, requestedModel string) ([]byte, error) {
	if e.cfg == nil {
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

func (e *AstronCodeExecutor) normalizeAstronPayload(payload []byte, model string) ([]byte, error) {
	if len(payload) == 0 || !json.Valid(payload) {
		return payload, nil
	}

	finish := func(payload []byte) ([]byte, error) {
		payload = normalizeAstronToolMessageHistory(payload)
		payload = normalizeAstronWebSearchTools(payload)
		payload = normalizeAstronRequiredToolChoice(payload)
		payload = e.normalizeAstronToolParallelism(payload)
		return normalizeAstronUserInputGuidance(payload), nil
	}

	detected := false
	enabled := false
	if v := gjson.GetBytes(payload, "enable_thinking"); v.Exists() {
		detected = true
		enabled = v.Bool()
	}
	if v := gjson.GetBytes(payload, "thinking.type"); v.Exists() {
		detected = true
		if strings.EqualFold(strings.TrimSpace(v.String()), "disabled") {
			payload, _ = sjson.SetBytes(payload, "enable_thinking", false)
			payload, _ = sjson.DeleteBytes(payload, "thinking")
			payload, _ = sjson.DeleteBytes(payload, "reasoning")
			payload, _ = sjson.DeleteBytes(payload, "reasoning_effort")
			return finish(payload)
		}
		if strings.EqualFold(strings.TrimSpace(v.String()), "enabled") {
			enabled = true
		}
	}

	if reasoningEffort := gjson.GetBytes(payload, "reasoning_effort"); reasoningEffort.Exists() {
		detected = true
		level := strings.ToLower(strings.TrimSpace(reasoningEffort.String()))
		switch level {
		case "", "none":
			payload, _ = sjson.SetBytes(payload, "enable_thinking", false)
			payload, _ = sjson.DeleteBytes(payload, "thinking")
			payload, _ = sjson.DeleteBytes(payload, "reasoning")
			payload, _ = sjson.DeleteBytes(payload, "reasoning_effort")
			return finish(payload)
		default:
			enabled = true
			if budget, ok := thinking.ConvertLevelToBudget(level); ok && budget > 0 {
				payload, _ = sjson.SetBytes(payload, "thinking_budget", budget)
			}
		}
	}

	if reasoningEffort := gjson.GetBytes(payload, "reasoning.effort"); reasoningEffort.Exists() {
		detected = true
		level := strings.ToLower(strings.TrimSpace(reasoningEffort.String()))
		switch level {
		case "", "none":
			payload, _ = sjson.SetBytes(payload, "enable_thinking", false)
			payload, _ = sjson.DeleteBytes(payload, "thinking")
			payload, _ = sjson.DeleteBytes(payload, "reasoning")
			payload, _ = sjson.DeleteBytes(payload, "reasoning_effort")
			return finish(payload)
		default:
			enabled = true
			if budget, ok := thinking.ConvertLevelToBudget(level); ok && budget > 0 {
				payload, _ = sjson.SetBytes(payload, "thinking_budget", budget)
			}
		}
	}

	if thinkingType := gjson.GetBytes(payload, "thinking.type"); thinkingType.Exists() {
		detected = true
		if strings.EqualFold(strings.TrimSpace(thinkingType.String()), "enabled") {
			enabled = true
		}
		if budget := gjson.GetBytes(payload, "thinking.budget_tokens"); budget.Exists() {
			detected = true
			budgetValue := int(budget.Int())
			if budgetValue > 0 {
				enabled = true
				payload, _ = sjson.SetBytes(payload, "thinking_budget", budgetValue)
			}
		}
	}

	if !detected {
		return finish(payload)
	}

	payload, _ = sjson.SetBytes(payload, "enable_thinking", enabled)
	payload, _ = sjson.DeleteBytes(payload, "thinking")
	payload, _ = sjson.DeleteBytes(payload, "reasoning")
	payload, _ = sjson.DeleteBytes(payload, "reasoning_effort")
	return finish(payload)
}

func normalizeAstronRequiredToolChoice(payload []byte) []byte {
	toolChoice := gjson.GetBytes(payload, "tool_choice")
	if !toolChoice.Exists() || toolChoice.Type != gjson.String {
		return payload
	}
	if !strings.EqualFold(strings.TrimSpace(toolChoice.String()), "required") {
		return payload
	}
	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() || len(tools.Array()) == 0 {
		return payload
	}
	updated, err := sjson.SetBytes(payload, "tool_choice", "auto")
	if err != nil {
		return payload
	}
	return updated
}

func normalizeAstronWebSearchTools(payload []byte) []byte {
	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		return payload
	}

	tools, ok := root["tools"].([]any)
	if !ok || len(tools) == 0 {
		return payload
	}

	changed := false
	for i, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		toolType := strings.ToLower(strings.TrimSpace(fmt.Sprint(tool["type"])))
		if !isOpenAIWebSearchToolType(toolType) || toolType == "web_search" {
			continue
		}
		normalized := make(map[string]any, len(tool))
		for k, v := range tool {
			normalized[k] = v
		}
		normalized["type"] = "web_search"
		tools[i] = normalized
		changed = true
	}
	if changed {
		root["tools"] = tools
	}

	if toolChoice, ok := root["tool_choice"].(map[string]any); ok {
		choiceType := strings.ToLower(strings.TrimSpace(fmt.Sprint(toolChoice["type"])))
		if isOpenAIWebSearchToolType(choiceType) {
			root["tool_choice"] = "auto"
			changed = true
		}
	}

	if !changed {
		return payload
	}
	out, err := json.Marshal(root)
	if err != nil {
		return payload
	}
	return out
}

func normalizeAstronToolMessageHistory(payload []byte) []byte {
	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		return payload
	}
	messages, ok := root["messages"].([]any)
	if !ok || len(messages) == 0 {
		return payload
	}

	availableToolResults := make(map[string]int)
	for _, item := range messages {
		msg, ok := item.(map[string]any)
		if !ok || !strings.EqualFold(strings.TrimSpace(fmt.Sprint(msg["role"])), "tool") {
			continue
		}
		id := strings.TrimSpace(fmt.Sprint(msg["tool_call_id"]))
		if id == "" {
			id = strings.TrimSpace(fmt.Sprint(msg["call_id"]))
		}
		if id != "" {
			availableToolResults[id]++
		}
	}

	changed := false
	pendingToolCalls := make(map[string]int)
	kept := make([]any, 0, len(messages))
	for _, item := range messages {
		msg, ok := item.(map[string]any)
		if !ok {
			kept = append(kept, item)
			continue
		}

		role := strings.ToLower(strings.TrimSpace(fmt.Sprint(msg["role"])))
		switch role {
		case "assistant":
			toolCalls, ok := msg["tool_calls"].([]any)
			if !ok || len(toolCalls) == 0 {
				kept = append(kept, msg)
				continue
			}
			filtered := make([]any, 0, len(toolCalls))
			for _, tc := range toolCalls {
				tcMap, ok := tc.(map[string]any)
				if !ok {
					continue
				}
				id := strings.TrimSpace(fmt.Sprint(tcMap["id"]))
				if id == "" || availableToolResults[id] <= 0 {
					changed = true
					continue
				}
				filtered = append(filtered, tcMap)
				pendingToolCalls[id]++
				availableToolResults[id]--
			}
			if len(filtered) == 0 {
				delete(msg, "tool_calls")
				changed = true
				if astronMessageHasContent(msg) {
					kept = append(kept, msg)
				}
				continue
			}
			if len(filtered) != len(toolCalls) {
				msg["tool_calls"] = filtered
				changed = true
			}
			kept = append(kept, msg)
		case "tool":
			id := strings.TrimSpace(fmt.Sprint(msg["tool_call_id"]))
			if id == "" {
				id = strings.TrimSpace(fmt.Sprint(msg["call_id"]))
				if id != "" {
					msg["tool_call_id"] = id
					changed = true
				}
			}
			if id != "" && availableToolResults[id] > 0 {
				availableToolResults[id]--
			}
			if id == "" || pendingToolCalls[id] <= 0 {
				changed = true
				continue
			}
			pendingToolCalls[id]--
			kept = append(kept, msg)
		default:
			kept = append(kept, msg)
		}
	}

	if !changed {
		return payload
	}
	root["messages"] = kept
	out, err := json.Marshal(root)
	if err != nil {
		return payload
	}
	return out
}

func astronMessageHasContent(msg map[string]any) bool {
	content, ok := msg["content"]
	if !ok || content == nil {
		return false
	}
	switch v := content.(type) {
	case string:
		return strings.TrimSpace(v) != ""
	case []any:
		return len(v) > 0
	default:
		return true
	}
}

func (e *AstronCodeExecutor) normalizeAstronToolParallelism(payload []byte) []byte {
	tools := gjson.GetBytes(payload, "tools")
	if tools.IsArray() && len(tools.Array()) > 0 {
		payload, _ = sjson.SetBytes(payload, "parallel_tool_calls", true)
		if gjson.GetBytes(payload, "stream").Bool() {
			payload, _ = sjson.SetBytes(payload, "tool_stream", true)
		}
	}
	return payload
}

func normalizeAstronUserInputGuidance(payload []byte) []byte {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() || len(tools.Array()) == 0 {
		return payload
	}
	if strings.Contains(string(payload), astronCodeUserInputGuidanceMarker) {
		return payload
	}

	guidance := astronCodeUserInputGuidanceMarker + " When you need the user to choose or confirm, call the request_user_input tool if it is available in the tools list. If request_user_input is not available, do not fake a selection UI in normal text; either make a reasonable assumption or ask one concise plain-text question."
	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		return payload
	}
	messages, ok := root["messages"].([]any)
	if !ok {
		return payload
	}
	if len(messages) > 0 {
		if first, ok := messages[0].(map[string]any); ok && strings.EqualFold(strings.TrimSpace(fmt.Sprint(first["role"])), "system") {
			if content, ok := first["content"].(string); ok {
				first["content"] = strings.TrimSpace(content) + "\n\n" + guidance
				out, errMarshal := json.Marshal(root)
				if errMarshal != nil {
					return payload
				}
				return out
			}
		}
	}
	systemMessage := map[string]any{
		"role":    "system",
		"content": guidance,
	}
	root["messages"] = append([]any{systemMessage}, messages...)
	out, err := json.Marshal(root)
	if err != nil {
		return payload
	}
	return out
}

type astronToolCallIDSeq struct {
	counter int64
	ids     map[string]string
}

func (s *astronToolCallIDSeq) next() string {
	s.counter++
	return fmt.Sprintf("call_astron_%d", s.counter)
}

func (s *astronToolCallIDSeq) idFor(choiceIndex, toolIndex int, preferred string) string {
	if s == nil {
		return ""
	}
	if s.ids == nil {
		s.ids = make(map[string]string)
	}
	key := fmt.Sprintf("%d:%d", choiceIndex, toolIndex)
	preferred = strings.TrimSpace(preferred)
	if preferred != "" {
		s.ids[key] = preferred
		return preferred
	}
	if existing := strings.TrimSpace(s.ids[key]); existing != "" {
		return existing
	}
	generated := s.next()
	s.ids[key] = generated
	return generated
}

func (s *astronToolCallIDSeq) existingID(choiceIndex, toolIndex int) string {
	if s == nil || s.ids == nil {
		return ""
	}
	return strings.TrimSpace(s.ids[fmt.Sprintf("%d:%d", choiceIndex, toolIndex)])
}

func ensureAstronToolCallIDs(line []byte, seq *astronToolCallIDSeq) []byte {
	if !bytes.HasPrefix(line, []byte("data:")) {
		return line
	}
	jsonPart := bytes.TrimPrefix(line, []byte("data:"))
	jsonPart = bytes.TrimSpace(jsonPart)
	if len(jsonPart) == 0 || jsonPart[0] != '{' {
		return line
	}
	choices := gjson.GetBytes(jsonPart, "choices")
	if !choices.Exists() || !choices.IsArray() {
		return line
	}
	modified := false
	var root map[string]any
	if err := json.Unmarshal(jsonPart, &root); err != nil {
		return line
	}
	choicesArr, ok := root["choices"].([]any)
	if !ok {
		return line
	}
	for choiceOffset, choice := range choicesArr {
		choiceMap, ok := choice.(map[string]any)
		if !ok {
			continue
		}
		choiceIndex := astronJSONIndex(choiceMap["index"], choiceOffset)
		delta, ok := choiceMap["delta"].(map[string]any)
		if !ok {
			continue
		}
		tcs, ok := delta["tool_calls"].([]any)
		if !ok {
			continue
		}
		filtered := make([]any, 0, len(tcs))
		for toolOffset, tc := range tcs {
			tcMap, ok := tc.(map[string]any)
			if !ok {
				filtered = append(filtered, tc)
				continue
			}
			hasNonEmptyName := false
			hasArguments := false
			// 上游可能返回 function.name 为空的 tool_call，直接丢弃避免客户端报错。
			// 流式传输中，参数增量帧通常没有 "name" 字段，因此只处理显式空 name。
			if fnMap, ok := tcMap["function"].(map[string]any); ok {
				if nameVal, nameExists := fnMap["name"]; nameExists {
					if astronJSONString(nameVal) == "" {
						modified = true
						continue
					}
					hasNonEmptyName = true
				}
				hasArguments = astronJSONString(fnMap["arguments"]) != ""
			}
			toolIndex := astronJSONIndex(tcMap["index"], toolOffset)
			if id := astronJSONString(tcMap["id"]); id == "" {
				if hasNonEmptyName || seq.existingID(choiceIndex, toolIndex) == "" && !hasArguments {
					tcMap["id"] = seq.idFor(choiceIndex, toolIndex, "")
					modified = true
				}
			} else {
				if hasNonEmptyName || seq.existingID(choiceIndex, toolIndex) == "" {
					seq.idFor(choiceIndex, toolIndex, id)
				} else if hasArguments {
					delete(tcMap, "id")
					modified = true
				}
			}
			filtered = append(filtered, tcMap)
		}
		if len(filtered) != len(tcs) {
			delta["tool_calls"] = filtered
		}
	}
	if !modified {
		return line
	}
	out, err := json.Marshal(root)
	if err != nil {
		return line
	}
	return append([]byte("data: "), out...)
}

func ensureAstronNonStreamToolCallIDs(body []byte) []byte {
	if len(body) == 0 || body[0] != '{' {
		return body
	}
	choices := gjson.GetBytes(body, "choices")
	if !choices.Exists() || !choices.IsArray() {
		return body
	}
	modified := false
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return body
	}
	choicesArr, ok := root["choices"].([]any)
	if !ok {
		return body
	}
	for choiceOffset, choice := range choicesArr {
		choiceMap, ok := choice.(map[string]any)
		if !ok {
			continue
		}
		choiceIndex := astronJSONIndex(choiceMap["index"], choiceOffset)
		msg, ok := choiceMap["message"].(map[string]any)
		if !ok {
			continue
		}
		tcs, ok := msg["tool_calls"].([]any)
		if !ok {
			continue
		}
		filtered := make([]any, 0, len(tcs))
		for toolOffset, tc := range tcs {
			tcMap, ok := tc.(map[string]any)
			if !ok {
				filtered = append(filtered, tc)
				continue
			}
			// 上游可能返回 function.name 为空的 tool_call，直接丢弃避免客户端报错
			// 仅在 "name" 键存在且值为空字符串时才丢弃，若不含 "name" 键则保留
			if fnMap, ok := tcMap["function"].(map[string]any); ok {
				if nameVal, nameExists := fnMap["name"]; nameExists && astronJSONString(nameVal) == "" {
					modified = true
					continue
				}
			}
			toolIndex := astronJSONIndex(tcMap["index"], toolOffset)
			if id := astronJSONString(tcMap["id"]); id == "" {
				tcMap["id"] = fmt.Sprintf("call_astron_%d_%d", choiceIndex, toolIndex)
				modified = true
			}
			filtered = append(filtered, tcMap)
		}
		if len(filtered) != len(tcs) {
			msg["tool_calls"] = filtered
		}
	}
	if !modified {
		return body
	}
	out, err := json.Marshal(root)
	if err != nil {
		return body
	}
	return out
}

func astronJSONString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	default:
		return ""
	}
}

func astronJSONIndex(v any, fallback int) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return int(n)
		}
	}
	return fallback
}
