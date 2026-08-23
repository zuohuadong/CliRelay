package executor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/client/grokbuild"
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
	isGrokClient := grokbuild.IsGrokClientContext(ctx, opts.Headers)
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
	reasoningSummaryDelivery := gjson.GetBytes(body, "stream_options.reasoning_summary_delivery")
	body, _ = sjson.DeleteBytes(body, "stream_options")
	if reasoningSummaryDelivery.Exists() {
		body, _ = sjson.SetBytes(body, "stream_options.reasoning_summary_delivery", reasoningSummaryDelivery.Value())
	}
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

	buffering := e.cfg != nil && e.cfg.Codex.StreamBootstrapBuffering

	scanner := bufio.NewScanner(streamBody)
	scanner.Buffer(nil, 52_428_800) // 50MB
	claudeInputTokens := helps.NewClaudeInputTokenState(from, to, responseFormat, originalPayload)
	var param any
	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	emittedPayload := false
	sawGrokKeepaliveEvent := false
	// sawProgressOutput marks whether the upstream stream delivered any
	// substantive output event. Lifecycle-only streams (created/in_progress)
	// can still be retried without duplicating client-visible output.
	sawProgressOutput := false
	retriedInvalidSignature := false
	idleReset, stopIdleWatch, idleTimedOut := startCodexHTTPStreamIdleWatch(ctx, streamBody)

	var bufferedChunks [][]byte
	var initialChunks [][]byte
	streamStarted := false
	immediateTerminal := false
	// bootstrapTerminalErr holds a non-overload terminal failure seen while buffering. It is
	// delivered as an in-stream chunk after the buffered handshake so downstream behaviour stays
	// identical to the unbuffered path instead of silently turning into a credential failover.
	var bootstrapTerminalErr error

	closeBootstrapBody := func() {
		stopIdleWatch()
		if errClose := streamBody.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
	}

	if buffering {
	bootstrapLoop:
		for scanner.Scan() {
			select {
			case idleReset <- struct{}{}:
			default:
			}
			line := applyCodexIdentityConfuseResponsePayload(scanner.Bytes(), identityState)
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			translatedLine := bytes.Clone(line)
			isHandshake := false
			terminalSuccess := false

			if transformed, ok := grokbuild.TransformKeepaliveSSELine(translatedLine, isGrokClient); ok {
				sawGrokKeepaliveEvent = true
				translatedLine = transformed
				isHandshake = true
			} else if bytes.HasPrefix(line, dataTag) {
				data := bytes.TrimSpace(line[5:])
				data = helps.RestoreCodexMultiAgentV2Response(data, optimizeMultiAgentV2)
				translatedLine = append([]byte("data: "), data...)
				eventType := gjson.GetBytes(data, "type").String()
				if codexStreamEventIndicatesProgress(eventType) {
					sawProgressOutput = true
				}
				if streamErr, terminalBody, ok := codexTerminalFailureErr(data); ok {
					streamErr.requestAuthScheme = streamAuthScheme
					closeBootstrapBody()
					if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, streamErr.StatusCode(), terminalBody); errClearReplay != nil {
						helps.RecordAPIResponseError(ctx, e.cfg, errClearReplay)
						reporter.PublishFailure(ctx, errClearReplay)
						return nil, errClearReplay
					}
					helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
					reporter.PublishFailure(ctx, streamErr)
					if isCodexOverloadBootstrapFailure(terminalBody) {
						// Transient capacity rejection smuggled into an HTTP 200 stream. Fail the
						// attempt before the downstream headers are committed so the conductor can
						// transparently retry on another credential, and report the status the
						// upstream refused to put on the wire.
						helps.LogWithRequestID(ctx).Debugf("codex executor: bootstrap overload rejection after %d buffered handshake events, failing over", len(bufferedChunks))
						return nil, newCodexBootstrapOverloadErr(terminalBody)
					}
					bootstrapTerminalErr = streamErr
					break bootstrapLoop
				}
				if isCodexHandshakeMetadataEvent(eventType) {
					isHandshake = true
				}
				switch eventType {
				case "response.output_item.done":
					collectCodexOutputItemDone(data, outputItemsByIndex, &outputItemsFallback)
				case "response.completed", "response.incomplete":
					terminalSuccess = true
					if detail, ok := helps.ParseCodexUsage(data); ok {
						reporter.Publish(ctx, detail)
					}
					publishCodexImageToolUsage(ctx, reporter, body, data)
					data = patchCodexCompletedOutput(data, outputItemsByIndex, outputItemsFallback)
					explicitCompleted := strings.EqualFold(strings.TrimSpace(gjson.GetBytes(data, "response.status").String()), "completed")
					emptyCompleted := !sawGrokKeepaliveEvent && !isCodexResponsesLiteRequest(body, opts.Headers) && !codexOutputArrayHasSemanticOutput(gjson.GetBytes(data, "response.output"))
					if eventType == "response.completed" && explicitCompleted && emptyCompleted {
						emptyErr := statusErr{
							code:              http.StatusBadGateway,
							msg:               "codex executor: upstream returned empty stream response",
							requestAuthScheme: streamAuthScheme,
						}
						helps.RecordAPIResponseError(ctx, e.cfg, emptyErr)
						reporter.PublishFailure(ctx, emptyErr)
						closeBootstrapBody()
						bootstrapTerminalErr = emptyErr
						break bootstrapLoop
					}
					if eventType == "response.completed" {
						cacheCodexReasoningReplayFromCompleted(replayScope, data)
					}
					translatedLine = append([]byte("data: "), data...)
				}
			} else {
				isHandshake = true
			}

			translatedLine = applyCodexIdentityExposeResponsePayload(translatedLine, identityState)
			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, originalPayload, body, translatedLine, &param, claudeInputTokens)
			if isHandshake && !terminalSuccess {
				if len(bufferedChunks) < codexBootstrapMaxBufferedEvents {
					bufferedChunks = append(bufferedChunks, chunks...)
					continue
				}
				helps.LogWithRequestID(ctx).Debugf("codex executor: bootstrap buffer limit %d reached, releasing stream without overload probing", codexBootstrapMaxBufferedEvents)
			}

			initialChunks = chunks
			streamStarted = true
			if terminalSuccess {
				immediateTerminal = true
			}
			break
		}

		if !streamStarted && bootstrapTerminalErr == nil {
			closeBootstrapBody()
			if errScan := scanner.Err(); errScan != nil {
				// A cancelled downstream request must not be recorded as an upstream failure or
				// penalise the credential; mirror the unbuffered goroutine's guard.
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				helps.RecordAPIResponseError(ctx, e.cfg, errScan)
				reporter.PublishFailure(ctx, errScan)
				return nil, errScan
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			streamErr := newCodexIncompleteStreamError()
			helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
			reporter.PublishFailure(ctx, streamErr)
			return nil, streamErr
		}
	}

	chanCapacity := len(bufferedChunks) + len(initialChunks)
	if bootstrapTerminalErr != nil {
		chanCapacity++
	}
	out := make(chan cliproxyexecutor.StreamChunk, chanCapacity)
	for _, chunk := range bufferedChunks {
		out <- cliproxyexecutor.StreamChunk{Payload: chunk}
		emittedPayload = emittedPayload || len(chunk) > 0
	}
	for _, chunk := range initialChunks {
		out <- cliproxyexecutor.StreamChunk{Payload: chunk}
		emittedPayload = emittedPayload || len(chunk) > 0
	}
	if bootstrapTerminalErr != nil {
		// Buffered handshake payloads are flushed first so the conductor observes a committed
		// stream and delivers this failure in-stream, exactly as the unbuffered path would.
		out <- cliproxyexecutor.StreamChunk{Err: bootstrapTerminalErr}
		close(out)
		return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
	}
	if immediateTerminal {
		closeBootstrapBody()
		close(out)
		return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
	}

	go func() {
		defer close(out)
		defer func() {
			if errClose := streamBody.Close(); errClose != nil {
				log.Errorf("codex executor: close response body error: %v", errClose)
			}
		}()
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

			if transformed, ok := grokbuild.TransformKeepaliveSSELine(translatedLine, isGrokClient); ok {
				sawGrokKeepaliveEvent = true
				translatedLine = transformed
			} else if bytes.HasPrefix(line, dataTag) {
				data := bytes.TrimSpace(line[5:])
				data = helps.RestoreCodexMultiAgentV2Response(data, optimizeMultiAgentV2)
				translatedLine = append([]byte("data: "), data...)
				eventType := gjson.GetBytes(data, "type").String()
				if codexStreamEventIndicatesProgress(eventType) {
					sawProgressOutput = true
				}
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
					emptyCompleted := !sawGrokKeepaliveEvent && !isCodexResponsesLiteRequest(body, opts.Headers) && !codexOutputArrayHasSemanticOutput(gjson.GetBytes(data, "response.output"))
					if eventType == "response.completed" && explicitCompleted && emptyCompleted {
						emptyErr := statusErr{
							code:              http.StatusBadGateway,
							msg:               "codex executor: upstream returned empty stream response",
							requestAuthScheme: streamAuthScheme,
						}
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
				streamErr = newCodexIncompleteStreamErrorEmitted(sawProgressOutput)
			}
			helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
			reporter.PublishFailure(ctx, streamErr)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
			case <-ctx.Done():
			}
			return
		}
		streamErr := newCodexIncompleteStreamErrorEmitted(sawProgressOutput)
		helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
		reporter.PublishFailure(ctx, streamErr)
		select {
		case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
		case <-ctx.Done():
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: streamHeaders, Chunks: out}, nil
}
