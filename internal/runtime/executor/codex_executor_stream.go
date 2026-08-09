package executor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func (e *CodexExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	auth, err = e.resolveEgressAuth(ctx, auth)
	if err != nil {
		return nil, err
	}
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "streaming not supported for /responses/compact"}
	}
	if isCodexOpenAIImageRequest(opts) {
		return e.executeOpenAIImageStream(ctx, auth, req, opts)
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("codex")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated, body := translateCodexRequestPair(from, to, baseModel, originalPayload, req.Payload, true, helps.APIKeyModelIsCompat(req))

	body, err = helps.ApplyRequestThinking(body, req, opts, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body, _ = sjson.DeleteBytes(body, "generate")
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body, _ = sjson.DeleteBytes(body, "safety_identifier")
	body, _ = sjson.DeleteBytes(body, "stream_options")
	body = helps.SetStringIfDifferent(body, "model", baseModel)
	body = normalizeCodexInstructions(body)
	if e.cfg == nil || e.cfg.DisableImageGeneration == config.DisableImageGenerationOff {
		body = ensureImageGenerationTool(body, baseModel, auth, opts.Headers)
	}
	body = sanitizeOpenAIResponsesReasoningItems(ctx, "codex executor", body)
	body = normalizeCodexParallelToolCalls(body, opts.Headers)
	body, optimizeMultiAgentV2 := helps.OptimizeCodexMultiAgentV2RequestForAuth(ctx, opts.Headers, body, e.cfg, auth, baseModel)
	body, replayScope, errReplay := applyCodexReasoningReplayCacheRequired(ctx, from, req, opts, body)
	if errReplay != nil {
		return nil, errReplay
	}
	reporter.SetTranslatedReasoningEffort(body, to.String())

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	httpClient, err := e.outboundHTTPClient(ctx, auth, 0, codexResponseHeaderTimeout(e.cfg), true)
	if err != nil {
		return nil, err
	}
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, identityState, err := e.openCodexResponse(ctx, from, url, auth, req, originalPayloadSource, body, apiKey, true, httpClient, opts.Headers)
	if err != nil {
		return nil, err
	}
	retriedHTTPInvalidSignature := false
	for httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, readErr := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
		if readErr != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, readErr)
			return nil, readErr
		}
		data = applyCodexIdentityConfuseResponsePayload(data, identityState)
		if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, httpResp.StatusCode, data); errClearReplay != nil {
			return nil, errClearReplay
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		if !retriedHTTPInvalidSignature && codexStatusErrorIsThinkingSignatureInvalid(httpResp.StatusCode, data) {
			if retryBody, okDrop := dropOpenAIResponsesReasoningItemsWithEncryptedContent(ctx, "codex executor", body, "retry after upstream rejected thinking signature"); okDrop {
				retriedHTTPInvalidSignature = true
				body = retryBody
				httpResp, identityState, err = e.openCodexResponse(ctx, from, url, auth, req, originalPayloadSource, body, apiKey, true, httpClient, opts.Headers)
				if err != nil {
					return nil, err
				}
				continue
			}
		}
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = newCodexStatusErrForResponse(httpResp, data)
		return nil, err
	}
	streamHeaders := httpResp.Header.Clone()
	streamBody := httpResp.Body
	streamAuthScheme := codexResponseRequestAuthScheme(httpResp)
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := streamBody.Close(); errClose != nil {
				log.Errorf("codex executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(streamBody)
		scanner.Buffer(nil, 52_428_800) // 50MB
		claudeInputTokens := helps.NewClaudeInputTokenState(from, to, responseFormat, originalPayload)
		var param any
		outputItemsByIndex := make(map[int64][]byte)
		var outputItemsFallback [][]byte
		emittedPayload := false
		retriedInvalidSignature := false
		idleReset, stopIdleWatch, idleTimedOut := startCodexHTTPStreamIdleWatch(ctx, streamBody)
		defer func() { stopIdleWatch() }()
		for scanner.Scan() {
			select {
			case idleReset <- struct{}{}:
			default:
			}
			line := applyCodexIdentityConfuseResponsePayload(scanner.Bytes(), identityState)
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if bytes.HasPrefix(bytes.TrimSpace(line), []byte("event:")) {
				continue
			}
			translatedLine := bytes.Clone(line)
			terminalSuccess := false

			if bytes.HasPrefix(line, dataTag) {
				data := bytes.TrimSpace(line[5:])
				data = helps.RestoreCodexMultiAgentV2Response(data, optimizeMultiAgentV2)
				translatedLine = append([]byte("data: "), data...)
				eventType := gjson.GetBytes(data, "type").String()
				if streamErr, terminalBody, ok := codexTerminalFailureErr(data); ok {
					streamErr.requestAuthScheme = streamAuthScheme
					if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, streamErr.StatusCode(), terminalBody); errClearReplay != nil {
						helps.RecordAPIResponseError(ctx, e.cfg, errClearReplay)
						reporter.PublishFailure(ctx, errClearReplay)
						select {
						case out <- cliproxyexecutor.StreamChunk{Err: errClearReplay}:
						case <-ctx.Done():
						}
						return
					}
					if !emittedPayload && !retriedInvalidSignature && codexStatusErrorIsThinkingSignatureInvalid(streamErr.StatusCode(), terminalBody) {
						if retryBody, okDrop := dropOpenAIResponsesReasoningItemsWithEncryptedContent(ctx, "codex executor", body, "retry after upstream rejected thinking signature"); okDrop {
							retriedInvalidSignature = true
							body = retryBody
							outputItemsByIndex = make(map[int64][]byte)
							outputItemsFallback = nil
							stopIdleWatch()
							if errClose := streamBody.Close(); errClose != nil {
								log.Errorf("codex executor: close response body error before invalid signature retry: %v", errClose)
							}
							retryResponse, retryIdentityState, retryOpenErr := e.openCodexResponse(ctx, from, url, auth, req, originalPayloadSource, body, apiKey, true, httpClient, opts.Headers)
							if retryOpenErr != nil {
								helps.RecordAPIResponseError(ctx, e.cfg, retryOpenErr)
								reporter.PublishFailure(ctx, retryOpenErr)
								select {
								case out <- cliproxyexecutor.StreamChunk{Err: retryOpenErr}:
								case <-ctx.Done():
								}
								return
							}
							if retryResponse.StatusCode < 200 || retryResponse.StatusCode >= 300 {
								retryData, readErr := io.ReadAll(retryResponse.Body)
								_ = retryResponse.Body.Close()
								if readErr != nil {
									select {
									case out <- cliproxyexecutor.StreamChunk{Err: readErr}:
									case <-ctx.Done():
									}
									return
								}
								retryErr := newCodexStatusErrForResponse(retryResponse, retryData)
								select {
								case out <- cliproxyexecutor.StreamChunk{Err: retryErr}:
								case <-ctx.Done():
								}
								return
							}
							streamBody = retryResponse.Body
							streamAuthScheme = codexResponseRequestAuthScheme(retryResponse)
							identityState = retryIdentityState
							scanner = bufio.NewScanner(streamBody)
							scanner.Buffer(nil, 52_428_800)
							idleReset, stopIdleWatch, idleTimedOut = startCodexHTTPStreamIdleWatch(ctx, streamBody)
							continue
						}
					}
					helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
					reporter.PublishFailure(ctx, streamErr)
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
					case <-ctx.Done():
					}
					return
				}
				switch eventType {
				case "response.output_item.done":
					collectCodexOutputItemDone(data, outputItemsByIndex, &outputItemsFallback)
				case "response.completed", "response.done", "response.incomplete":
					terminalSuccess = true
					if eventType == "response.done" {
						data, _ = sjson.SetRawBytes(data, "type", []byte(`"response.completed"`))
					}
					if detail, ok := helps.ParseCodexUsage(data); ok {
						reporter.Publish(ctx, detail)
					}
					publishCodexImageToolUsage(ctx, reporter, body, data)
					data = patchCodexCompletedOutput(data, outputItemsByIndex, outputItemsFallback)
					explicitCompleted := strings.EqualFold(strings.TrimSpace(gjson.GetBytes(data, "response.status").String()), "completed")
					emptyCompleted := !isCodexResponsesLiteRequest(body, opts.Headers) && !codexOutputArrayHasSemanticOutput(gjson.GetBytes(data, "response.output"))
					if eventType == "response.completed" && explicitCompleted && emptyCompleted {
						emptyErr := statusErr{code: http.StatusBadGateway, msg: "codex executor: upstream returned empty stream response"}
						helps.RecordAPIResponseError(ctx, e.cfg, emptyErr)
						reporter.PublishFailure(ctx, emptyErr)
						select {
						case out <- cliproxyexecutor.StreamChunk{Err: emptyErr}:
						case <-ctx.Done():
						}
						return
					}
					if eventType == "response.completed" {
						cacheCodexReasoningReplayFromCompleted(replayScope, data)
					}
					translatedLine = append([]byte("data: "), data...)
				}
			}

			translatedLine = applyCodexIdentityExposeResponsePayload(translatedLine, identityState)
			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, originalPayload, body, translatedLine, &param, claudeInputTokens)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
					emittedPayload = true
				case <-ctx.Done():
					return
				}
			}
			if terminalSuccess {
				return
			}
		}
		if idleTimedOut.Load() {
			var idleErr error = statusErr{code: http.StatusRequestTimeout, msg: `{"error":{"message":"upstream stream idle timeout","type":"server_error","code":"stream_idle_timeout"}}`}
			if e.usesStrictEgress(auth) && !emittedPayload {
				idleErr = wrapStrictEgressTransportError(errors.New("upstream stream idle timeout"), "stream idle timeout")
			}
			helps.RecordAPIResponseError(ctx, e.cfg, idleErr)
			reporter.PublishFailure(ctx, idleErr)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: idleErr}:
			case <-ctx.Done():
			}
			return
		}
		if errScan := scanner.Err(); errScan != nil {
			if ctx.Err() != nil {
				return
			}
			var streamErr error
			if e.usesStrictEgress(auth) {
				streamErr = e.wrapStrictEgressTransportErrorForAuth(auth, errScan, "stream read")
			} else {
				streamErr = newCodexIncompleteStreamError()
			}
			helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
			reporter.PublishFailure(ctx, streamErr)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
			case <-ctx.Done():
			}
			return
		}
		streamErr := newCodexIncompleteStreamError()
		helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
		reporter.PublishFailure(ctx, streamErr)
		select {
		case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
		case <-ctx.Done():
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: streamHeaders, Chunks: out}, nil
}
