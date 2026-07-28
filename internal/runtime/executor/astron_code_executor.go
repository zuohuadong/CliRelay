package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

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

const astronCodeRateLimitRetryDelay = time.Second
const maxAstronCodeRetryAfterSeconds = int64((1<<63 - 1) / int64(time.Second))

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
	syntheticCompaction := false
	useResponsesEndpoint := auth != nil && auth.Attributes != nil && auth.Attributes["response_endpoint"] == "true"
	switch opts.Alt {
	case "responses/compact":
		syntheticCompaction = true
	case "images/generations":
		endpoint = "/images/generations"
		imagePassthrough = true
	case "images/edits":
		endpoint = "/images/edits"
		imagePassthrough = true
	default:
		if useResponsesEndpoint {
			to = sdktranslator.FormatOpenAIResponse
			endpoint = "/responses"
		}
	}
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	var translated []byte
	if imagePassthrough {
		translated = e.overrideModel(req.Payload, baseModel)
	} else if syntheticCompaction {
		translated, err = buildResponsesCompactChatPayload(originalPayloadSource, baseModel)
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
		if useResponsesEndpoint {
			translated = helps.SanitizeCodexInputItemIDs(translated)
		} else {
			translated, err = e.normalizeAstronPayload(translated, baseModel)
			if err != nil {
				return resp, err
			}
		}
	}

	url := astronCodeEndpointURL(baseURL, endpoint, useResponsesEndpoint)
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
		err = newAstronCodeStatusErr(httpResp.StatusCode, b, httpResp.Header)
		return resp, err
	}
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)
	if syntheticCompaction {
		wrapped, errWrap := buildResponsesCompactResponse(baseModel, body, httpResp.Header)
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
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "streaming not supported for /responses/compact"}
	}
	if shouldHandleResponsesStreamingCompaction(req.Payload, opts) {
		return e.executeCompactionTriggerStream(ctx, auth, req, opts)
	}

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return nil, err
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	useResponsesEndpoint := auth != nil && auth.Attributes != nil && auth.Attributes["response_endpoint"] == "true"
	if useResponsesEndpoint {
		to = sdktranslator.FormatOpenAIResponse
	}
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
	requestPath := helps.PayloadRequestPath(opts)

	buildStreamPayload := func(target sdktranslator.Format, responsesEndpoint bool) ([]byte, error) {
		originalTranslated := sdktranslator.TranslateRequest(from, target, baseModel, originalPayload, true)
		translated := sdktranslator.TranslateRequest(from, target, baseModel, adaptedPayload, true)
		translated, errApply := thinking.ApplyThinking(translated, req.Model, from.String(), target.String(), e.Identifier())
		if errApply != nil {
			return nil, errApply
		}
		if shouldNormalizeKimiCompatPayload(baseModel) {
			var errNormalize error
			translated, errNormalize = normalizeKimiToolMessageLinks(translated)
			if errNormalize != nil {
				return nil, errNormalize
			}
		}
		translated = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, target.String(), from.String(), "", translated, originalTranslated, requestedModel, requestPath, opts.Headers)
		translated, _ = sjson.SetBytes(translated, "stream_options.include_usage", true)
		if responsesEndpoint {
			return helps.SanitizeCodexInputItemIDs(translated), nil
		}
		return e.normalizeAstronPayload(translated, baseModel)
	}

	translated, err := buildStreamPayload(to, useResponsesEndpoint)
	if err != nil {
		return nil, err
	}
	fallbackTo := sdktranslator.FromString("openai")
	var fallbackTranslated []byte
	if useResponsesEndpoint {
		fallbackTranslated, err = buildStreamPayload(fallbackTo, false)
		if err != nil {
			return nil, err
		}
	}

	streamEndpoint := "/chat/completions"
	if useResponsesEndpoint {
		streamEndpoint = "/responses"
	}
	url := astronCodeEndpointURL(baseURL, streamEndpoint, useResponsesEndpoint)
	fallbackURL := astronCodeEndpointURL(baseURL, "/chat/completions", false)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)

	openStreamResponse := func(requestURL string, body []byte) (*http.Response, error) {
		httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
		if errReq != nil {
			return nil, errReq
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+apiKey)
		}
		httpReq.Header.Set("User-Agent", "cli-proxy-astron-code")
		e.applyCustomHeadersAndIdentityFingerprint(httpReq, auth, false)
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("Cache-Control", "no-cache")
		helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
			URL:       requestURL,
			Method:    http.MethodPost,
			Headers:   httpReq.Header.Clone(),
			Body:      redactSensitiveJSONForLog(body),
			Provider:  e.Identifier(),
			AuthID:    authID,
			AuthLabel: authLabel,
			AuthType:  authType,
			AuthValue: authValue,
		})
		httpResp, errDo := httpClient.Do(httpReq)
		if errDo != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errDo)
			return nil, errDo
		}
		helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
		if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
			b, _ := io.ReadAll(httpResp.Body)
			helps.AppendAPIResponseChunk(ctx, e.cfg, b)
			helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("astron code executor: close response body error: %v", errClose)
			}
			return nil, newAstronCodeStatusErr(httpResp.StatusCode, b, httpResp.Header)
		}
		return httpResp, nil
	}

	httpResp, err := openStreamResponse(url, translated)
	attemptTo := to
	attemptTranslated := translated
	attemptUseResponsesEndpoint := useResponsesEndpoint
	allowEmptyStreamFallback := useResponsesEndpoint && len(fallbackTranslated) > 0
	if err != nil {
		if useResponsesEndpoint && len(fallbackTranslated) > 0 && astronShouldFallbackResponsesEndpointError(err) {
			helps.LogWithRequestID(ctx).Debugf("astron code executor: /responses endpoint unavailable for model schema; retrying via /chat/completions: %v", err)
			httpResp, err = openStreamResponse(fallbackURL, fallbackTranslated)
			if err != nil {
				return nil, err
			}
			attemptTo = fallbackTo
			attemptTranslated = fallbackTranslated
			attemptUseResponsesEndpoint = false
			allowEmptyStreamFallback = false
		} else {
			return nil, err
		}
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)

		sendStreamError := func(streamErr error) bool {
			helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
			reporter.PublishFailure(ctx, streamErr)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
			case <-ctx.Done():
			}
			return false
		}

		var scanResponse func(*http.Response, sdktranslator.Format, []byte, bool, bool) bool
		runChatFallback := func() bool {
			if len(fallbackTranslated) == 0 {
				return false
			}
			helps.LogWithRequestID(ctx).Debugf("astron code executor: /responses stream produced no semantic output; retrying via /chat/completions")
			fallbackResp, errFallback := openStreamResponse(fallbackURL, fallbackTranslated)
			if errFallback != nil {
				return sendStreamError(errFallback)
			}
			return scanResponse(fallbackResp, fallbackTo, fallbackTranslated, false, false)
		}

		scanResponse = func(resp *http.Response, attemptTo sdktranslator.Format, attemptTranslated []byte, attemptUseResponsesEndpoint bool, allowFallback bool) bool {
			if resp == nil || resp.Body == nil {
				return sendStreamError(statusErr{code: http.StatusBadGateway, msg: "astron code executor: upstream returned nil stream response"})
			}
			closed := false
			closeResp := func() {
				if closed {
					return
				}
				closed = true
				if errClose := resp.Body.Close(); errClose != nil {
					log.Errorf("astron code executor: close response body error: %v", errClose)
				}
			}
			defer closeResp()

			scanner := bufio.NewScanner(resp.Body)
			scanner.Buffer(nil, 52_428_800)
			var param any
			tcIDSeq := &astronToolCallIDSeq{}
			var pendingTranslated [][]byte
			semanticOutput := false
			emitDone := func() bool {
				chunks := sdktranslator.TranslateStream(ctx, attemptTo, from, req.Model, opts.OriginalRequest, attemptTranslated, []byte("data: [DONE]"), &param)
				if !semanticOutput && !openAICompatStreamChunksHaveSemanticOutput(chunks) {
					if allowFallback {
						closeResp()
						return runChatFallback()
					}
					streamErr := statusErr{code: http.StatusBadGateway, msg: "astron code executor: upstream returned empty stream response"}
					return sendStreamError(streamErr)
				}
				return openAICompatForwardSemanticStreamChunks(ctx, out, &pendingTranslated, &semanticOutput, chunks)
			}
			for scanner.Scan() {
				line := scanner.Bytes()
				helps.AppendAPIResponseChunk(ctx, e.cfg, line)
				if attemptUseResponsesEndpoint {
					if detail, ok := helps.ParseCodexUsage(jsonPayloadFromDataLine(line)); ok {
						reporter.Publish(ctx, detail)
					}
				} else if detail, ok := helps.ParseOpenAIStreamUsage(line); ok {
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
						if semanticOutput {
							helps.LogWithRequestID(ctx).Debugf("astron code stream ended with non-SSE payload after data: %s", helps.SummarizeErrorBody("application/json", trimmedLine))
							return emitDone()
						}
						streamErr := statusErr{code: http.StatusBadGateway, msg: string(trimmedLine)}
						return sendStreamError(streamErr)
					}
					continue
				}
				normalizedLine := trimmedLine
				if !attemptUseResponsesEndpoint {
					normalizedLine = ensureAstronToolCallIDs(bytes.Clone(trimmedLine), tcIDSeq)
				}
				chunks := sdktranslator.TranslateStream(ctx, attemptTo, from, req.Model, opts.OriginalRequest, attemptTranslated, normalizedLine, &param)
				if !openAICompatForwardSemanticStreamChunks(ctx, out, &pendingTranslated, &semanticOutput, chunks) {
					return false
				}
			}
			if errScan := scanner.Err(); errScan != nil {
				return sendStreamError(errScan)
			}
			return emitDone()
		}

		if scanResponse(httpResp, attemptTo, attemptTranslated, attemptUseResponsesEndpoint, allowEmptyStreamFallback) {
			reporter.EnsurePublished(ctx)
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func newAstronCodeStatusErr(statusCode int, body []byte, headers http.Header) statusErr {
	err := statusErr{code: statusCode, msg: string(body)}
	if statusCode != http.StatusTooManyRequests {
		return err
	}

	retryAfter := astronCodeRateLimitRetryDelay
	if raw := strings.TrimSpace(headers.Get("Retry-After")); raw != "" {
		if seconds, errParse := strconv.ParseInt(raw, 10, 64); errParse == nil && seconds > 0 && seconds <= maxAstronCodeRetryAfterSeconds {
			retryAfter = time.Duration(seconds) * time.Second
		} else if retryAt, errParse := http.ParseTime(raw); errParse == nil {
			if wait := time.Until(retryAt); wait > 0 {
				retryAfter = wait
			}
		}
	}
	err.retryAfter = &retryAfter
	return err
}

func (e *AstronCodeExecutor) executeCompactionTriggerStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	compactOpts := opts
	compactOpts.Stream = false
	compactOpts.Alt = "responses/compact"
	resp, err := e.Execute(ctx, auth, req, compactOpts)
	if err != nil {
		return nil, err
	}

	return responsesCompactionStreamResult(resp, thinking.ParseSuffix(req.Model).ModelName), nil
}

func shouldHandleResponsesStreamingCompaction(payload []byte, opts cliproxyexecutor.Options) bool {
	if responsesInputHasItemType(payload, "compaction_trigger") {
		return true
	}
	if opts.Headers != nil {
		turnMetadata := strings.TrimSpace(opts.Headers.Get("X-Codex-Turn-Metadata"))
		if strings.EqualFold(strings.TrimSpace(gjson.Get(turnMetadata, "request_kind").String()), "compaction") {
			return true
		}
	}
	return false
}

func responsesInputHasItemType(payload []byte, itemType string) bool {
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		if item.Get("type").String() == itemType {
			return true
		}
	}
	return false
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
				if id == "" || !astronToolCallHasValidJSONArguments(tcMap) || availableToolResults[id] <= 0 {
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

func astronToolCallHasValidJSONArguments(toolCall map[string]any) bool {
	if toolCall == nil {
		return false
	}
	functionValue, hasFunction := toolCall["function"]
	if !strings.EqualFold(strings.TrimSpace(fmt.Sprint(toolCall["type"])), "function") && !hasFunction {
		return true
	}
	function, ok := functionValue.(map[string]any)
	if !ok {
		return false
	}
	arguments, exists := function["arguments"]
	if !exists {
		return true
	}
	argumentsString, ok := arguments.(string)
	return ok && json.Valid([]byte(argumentsString))
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

// jsonPayloadFromDataLine extracts the JSON payload from a "data: ..." SSE line.
func jsonPayloadFromDataLine(line []byte) []byte {
	idx := bytes.IndexByte(line, ':')
	if idx < 0 {
		return nil
	}
	return bytes.TrimSpace(line[idx+1:])
}

func astronShouldFallbackResponsesEndpointError(err error) bool {
	if err == nil {
		return false
	}
	statusCode := 0
	if status, ok := err.(interface{ StatusCode() int }); ok {
		statusCode = status.StatusCode()
	}
	if statusCode != http.StatusNotFound && statusCode != http.StatusBadRequest {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no any schema route found") ||
		strings.Contains(msg, "schema route") ||
		strings.Contains(msg, "model_not_found")
}

// astronCodeEndpointURL builds the upstream request URL for Astron Code.
// iFlytek serves the Responses API under /v1 instead of the /v2 base path
// used for chat completions, so swap the version prefix when needed.
func astronCodeEndpointURL(baseURL, endpoint string, useResponses bool) string {
	base := strings.TrimSuffix(baseURL, "/")
	if useResponses {
		baseLower := strings.ToLower(base)
		endpointLower := strings.ToLower(endpoint)
		if strings.HasSuffix(baseLower, "/responses") {
			if endpointLower == "/responses" {
				return base
			}
			if strings.HasPrefix(endpointLower, "/responses/") {
				return base + strings.TrimPrefix(endpoint, "/responses")
			}
		}
		base = strings.Replace(base, "/v2", "/v1", 1)
	} else {
		base = strings.Replace(base, "/v1", "/v2", 1)
	}
	return base + endpoint
}
