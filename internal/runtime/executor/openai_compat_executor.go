package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/multimodaladapter"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	openAICompatImageHandlerType            = "openai-image"
	openAICompatVideoHandlerType            = "openai-video"
	openAICompatImagesGenerationsPath       = "/images/generations"
	openAICompatImagesEditsPath             = "/images/edits"
	openAICompatDefaultImageEndpoint        = openAICompatImagesGenerationsPath
	openAICompatVideosPath                  = "/videos"
	openAICompatVideoGenerationsPath        = "/video/generations"
	openAICompatVideosGenerationsPath       = "/videos/generations"
	openAICompatDefaultVideoEndpoint        = openAICompatVideosPath
	openAICompatMultipartMemory       int64 = 32 << 20
)

// OpenAICompatExecutor implements a stateless executor for OpenAI-compatible providers.
// It performs request/response translation and executes against the provider base URL
// using per-auth credentials (API key) and per-auth HTTP transport (proxy) from context.
type OpenAICompatExecutor struct {
	provider string
	cfg      *config.Config
}

// NewOpenAICompatExecutor creates an executor bound to a provider key (e.g., "openrouter").
func NewOpenAICompatExecutor(provider string, cfg *config.Config) *OpenAICompatExecutor {
	return &OpenAICompatExecutor{provider: provider, cfg: cfg}
}

// Identifier implements cliproxyauth.ProviderExecutor.
func (e *OpenAICompatExecutor) Identifier() string { return e.provider }

// PrepareRequest injects OpenAI-compatible credentials into the outgoing HTTP request.
func (e *OpenAICompatExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	_, apiKey := e.resolveCredentials(auth)
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	e.applyCustomHeadersAndIdentityFingerprint(req, auth, false)
	return nil
}

// HttpRequest injects OpenAI-compatible credentials into the request and executes it.
func (e *OpenAICompatExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("openai compat executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

func (e *OpenAICompatExecutor) applyMultimodalAdapter(ctx context.Context, payload []byte, model, protocol, requestedModel string) ([]byte, error) {
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

func (e *OpenAICompatExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if endpointPath := openAICompatImageEndpointPath(opts); endpointPath != "" {
		return e.executeImages(ctx, auth, req, opts, endpointPath)
	}
	if endpointPath := openAICompatVideoEndpointPath(opts); endpointPath != "" {
		return e.executeImages(ctx, auth, req, opts, endpointPath)
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return
	}

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("openai")
	endpoint := "/chat/completions"
	imagePassthrough := false
	useResponsesEndpoint := e.useResponsesEndpoint(auth, opts)
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
	default:
		if useResponsesEndpoint {
			to = sdktranslator.FromString("openai-response")
			endpoint = "/responses"
		}
	}
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	var originalTranslated []byte
	var translated []byte
	if imagePassthrough {
		translated = e.overrideModel(req.Payload, baseModel)
	} else {
		originalPayload := originalPayloadSource
		adaptedPayload, errAdapt := e.applyMultimodalAdapter(ctx, req.Payload, baseModel, from.String(), requestedModel)
		if errAdapt != nil {
			err = errAdapt
			return resp, err
		}
		originalTranslated = sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, opts.Stream)
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
	}

	requestPath := helps.PayloadRequestPath(opts)
	translated = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", translated, originalTranslated, requestedModel, requestPath, opts.Headers)
	if opts.Alt == "responses/compact" {
		if updated, errDelete := sjson.DeleteBytes(translated, "stream"); errDelete == nil {
			translated = updated
		}
		translated = sanitizeOpenAIResponsesReasoningItems(ctx, "openai compat executor", translated)
	}
	translated, err = e.normalizeBigModelTools(translated, baseURL)
	if err != nil {
		return resp, err
	}
	reporter.SetTranslatedReasoningEffort(translated, to.String())

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
	url := strings.TrimSuffix(baseURL, "/") + endpoint
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
		Body:      translated,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
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
	// Ensure we at least record the request even if upstream doesn't return usage
	reporter.EnsurePublished(ctx)
	if imagePassthrough {
		resp = cliproxyexecutor.Response{Payload: body, Headers: httpResp.Header.Clone()}
		return resp, nil
	}
	// Translate response back to source format when needed
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, opts.OriginalRequest, translated, body, &param)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

func (e *OpenAICompatExecutor) executeImages(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, endpointPath string) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return resp, err
	}

	payload, contentType, errPrepare := prepareOpenAICompatImagesPayload(req.Payload, baseModel, opts.Headers.Get("Content-Type"), false)
	if errPrepare != nil {
		err = errPrepare
		return resp, err
	}
	if contentType == "" {
		contentType = "application/json"
	}
	reporter.SetTranslatedReasoningEffort(payload, "openai")

	endpointPath = openAICompatVideoProviderEndpointPath(opts, endpointPath, payload, baseModel, auth)
	url := strings.TrimSuffix(baseURL, "/") + endpointPath
	method := openAICompatPassthroughMethod(opts, endpointPath)
	var requestBody io.Reader = bytes.NewReader(payload)
	if method == http.MethodGet {
		requestBody = nil
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, url, requestBody)
	if err != nil {
		return resp, err
	}
	if method != http.MethodGet {
		httpReq.Header.Set("Content-Type", contentType)
	}
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    method,
		Headers:   httpReq.Header.Clone(),
		Body:      payload,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	body, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		err = errRead
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), body))
		err = statusErr{code: httpResp.StatusCode, msg: string(body)}
		return resp, err
	}

	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	reporter.EnsurePublished(ctx)
	resp = cliproxyexecutor.Response{Payload: body, Headers: httpResp.Header.Clone()}
	return resp, nil
}

func (e *OpenAICompatExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if endpointPath := openAICompatImageEndpointPath(opts); endpointPath != "" {
		return e.executeImagesStream(ctx, auth, req, opts, endpointPath)
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "streaming not supported for /responses/compact"}
	}
	if shouldHandleResponsesStreamingCompaction(req.Payload, opts) {
		return e.executeCompactionTriggerStream(ctx, auth, req, opts)
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return nil, err
	}

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("openai")
	useResponsesEndpoint := e.useResponsesEndpoint(auth, opts)
	if useResponsesEndpoint {
		to = sdktranslator.FromString("openai-response")
	}
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

	// Request usage data in the final chat-completions streaming chunk so that
	// token statistics are captured even when the upstream is an OpenAI-compatible provider.
	if !useResponsesEndpoint {
		translated, _ = sjson.SetBytes(translated, "stream_options.include_usage", true)
	}
	translated, err = e.normalizeBigModelTools(translated, baseURL)
	if err != nil {
		return nil, err
	}
	reporter.SetTranslatedReasoningEffort(translated, to.String())

	endpoint := "/chat/completions"
	if useResponsesEndpoint {
		endpoint = "/responses"
	}
	url := strings.TrimSuffix(baseURL, "/") + endpoint
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
		Body:      translated,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
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
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("openai compat executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB
		var param any
		var streamUsage helps.StreamUsageBuffer
		defer streamUsage.Publish(ctx, reporter)
		var pendingTranslated [][]byte
		semanticOutput := false
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			streamUsage.ObserveOpenAIStream(line)
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

			// OpenAI-compatible streams must use SSE data lines.
			chunks := sdktranslator.TranslateStream(ctx, to, responseFormat, req.Model, opts.OriginalRequest, translated, bytes.Clone(trimmedLine), &param)
			if !openAICompatForwardSemanticStreamChunks(ctx, out, &pendingTranslated, &semanticOutput, chunks) {
				return
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
			// In case the upstream closes the stream without a terminal [DONE] marker.
			// Feed a synthetic done marker through the translator so pending
			// response.completed events are still emitted exactly once.
			chunks := sdktranslator.TranslateStream(ctx, to, responseFormat, req.Model, opts.OriginalRequest, translated, []byte("data: [DONE]"), &param)
			if !semanticOutput && !openAICompatStreamChunksHaveSemanticOutput(chunks) {
				streamErr := statusErr{code: http.StatusBadGateway, msg: "openai compat executor: upstream returned empty stream response"}
				helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
				reporter.PublishFailure(ctx, streamErr)
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
				case <-ctx.Done():
				}
				return
			}
			if !openAICompatForwardSemanticStreamChunks(ctx, out, &pendingTranslated, &semanticOutput, chunks) {
				return
			}
		}
		// Ensure we record the request if no usage chunk was ever seen.
		streamUsage.Publish(ctx, reporter)
		reporter.EnsurePublished(ctx)
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *OpenAICompatExecutor) executeCompactionTriggerStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	compactOpts := opts
	compactOpts.Stream = false
	compactOpts.Alt = "responses/compact"
	resp, err := e.Execute(ctx, auth, req, compactOpts)
	if err != nil {
		return nil, err
	}
	return responsesCompactionStreamResult(resp, thinking.ParseSuffix(req.Model).ModelName), nil
}

func (e *OpenAICompatExecutor) executeImagesStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, endpointPath string) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return nil, err
	}

	payload, contentType, errPrepare := prepareOpenAICompatImagesPayload(req.Payload, baseModel, opts.Headers.Get("Content-Type"), true)
	if errPrepare != nil {
		err = errPrepare
		return nil, err
	}
	if contentType == "" {
		contentType = "application/json"
	}
	reporter.SetTranslatedReasoningEffort(payload, "openai")

	url := strings.TrimSuffix(baseURL, "/") + endpointPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", contentType)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
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
		Body:      payload,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		body, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			return nil, errRead
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, body)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), body))
		return nil, statusErr{code: httpResp.StatusCode, msg: string(body)}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("openai compat executor: close response body error: %v", errClose)
			}
			reporter.EnsurePublished(ctx)
		}()
		buffer := make([]byte, 32*1024)
		for {
			n, errRead := httpResp.Body.Read(buffer)
			if n > 0 {
				chunk := bytes.Clone(buffer[:n])
				helps.AppendAPIResponseChunk(ctx, e.cfg, chunk)
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
				case <-ctx.Done():
					return
				}
			}
			if errRead != nil {
				if errRead != io.EOF {
					helps.RecordAPIResponseError(ctx, e.cfg, errRead)
					reporter.PublishFailure(ctx, errRead)
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: errRead}:
					case <-ctx.Done():
					}
				}
				return
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *OpenAICompatExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	modelForCounting := baseModel

	translated, err := thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	enc, err := helps.TokenizerForModel(modelForCounting)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat executor: tokenizer init failed: %w", err)
	}

	count, err := helps.CountOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat executor: token counting failed: %w", err)
	}

	usageJSON := helps.BuildOpenAIUsageJSON(count)
	translatedUsage := sdktranslator.TranslateTokenCount(ctx, to, responseFormat, count, usageJSON)
	return cliproxyexecutor.Response{Payload: translatedUsage}, nil
}

func shouldNormalizeKimiCompatPayload(model string) bool {
	model = strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(model).ModelName))
	return strings.HasPrefix(model, "kimi-") ||
		strings.Contains(model, "/kimi-") ||
		strings.Contains(model, "moonshot")
}

func (e *OpenAICompatExecutor) normalizeBigModelTools(payload []byte, baseURL string) ([]byte, error) {
	if !isBigModelCompatProvider(e.Identifier(), baseURL) || !gjson.GetBytes(payload, "tools").Exists() {
		return payload, nil
	}
	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		return nil, fmt.Errorf("normalize bigmodel tools: invalid payload: %w", err)
	}
	tools, ok := root["tools"].([]any)
	if !ok {
		return payload, nil
	}
	changed := false
	for i, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		toolType := strings.ToLower(strings.TrimSpace(fmt.Sprint(tool["type"])))
		switch {
		case isOpenAIWebSearchToolType(toolType):
			tools[i] = normalizeBigModelWebSearchTool(tool)
			changed = true
		case toolType == "mcp":
			tools[i] = normalizeBigModelMCPTool(tool)
			changed = true
		}
	}
	if changed {
		root["tools"] = tools
	}
	if toolChoice, ok := root["tool_choice"].(map[string]any); ok {
		choiceType := strings.ToLower(strings.TrimSpace(fmt.Sprint(toolChoice["type"])))
		switch {
		case isOpenAIWebSearchToolType(choiceType), choiceType == "mcp":
			delete(root, "tool_choice")
			changed = true
		}
	}
	if !changed {
		return payload, nil
	}
	out, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("normalize bigmodel tools: encode payload: %w", err)
	}
	return out, nil
}

func isBigModelCompatProvider(provider, baseURL string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	baseURL = strings.ToLower(strings.TrimSpace(baseURL))
	return strings.Contains(provider, "bigmodel") ||
		strings.Contains(provider, "zhipu") ||
		strings.Contains(baseURL, "open.bigmodel.cn")
}

func isOpenAIWebSearchToolType(toolType string) bool {
	return toolType == "web_search" || strings.HasPrefix(toolType, "web_search_")
}

func normalizeBigModelWebSearchTool(tool map[string]any) map[string]any {
	webSearch := objectValue(tool["web_search"])
	if webSearch == nil {
		webSearch = make(map[string]any)
	}
	if _, ok := webSearch["enable"]; !ok {
		webSearch["enable"] = true
	}
	if isEmptyJSONString(webSearch["search_engine"]) {
		webSearch["search_engine"] = "search_pro"
	}
	if _, ok := webSearch["content_size"]; !ok {
		if size := bigModelContentSize(fmt.Sprint(tool["search_context_size"])); size != "" {
			webSearch["content_size"] = size
		}
	}
	return map[string]any{
		"type":       "web_search",
		"web_search": webSearch,
	}
}

func normalizeBigModelMCPTool(tool map[string]any) map[string]any {
	mcp := objectValue(tool["mcp"])
	if mcp == nil {
		mcp = make(map[string]any)
	}
	for _, field := range []string{"server_label", "server_url", "transport_type", "allowed_tools", "headers"} {
		if _, ok := mcp[field]; !ok {
			if value, exists := tool[field]; exists {
				mcp[field] = value
			}
		}
	}
	if isEmptyJSONString(mcp["transport_type"]) {
		mcp["transport_type"] = "streamable-http"
	}
	return map[string]any{
		"type": "mcp",
		"mcp":  mcp,
	}
}

func objectValue(value any) map[string]any {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return obj
}

func isEmptyJSONString(value any) bool {
	if value == nil {
		return true
	}
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) == ""
}

func bigModelContentSize(searchContextSize string) string {
	switch strings.ToLower(strings.TrimSpace(searchContextSize)) {
	case "high":
		return "high"
	case "low", "medium":
		return "medium"
	default:
		return ""
	}
}

func imageEditPayloadHasUploads(payload []byte) bool {
	return gjson.GetBytes(payload, "image_files").Exists() || gjson.GetBytes(payload, "mask_file").Exists()
}

func buildImageEditsMultipartBody(payload []byte) ([]byte, string, error) {
	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		return nil, "", fmt.Errorf("invalid image edits payload: %w", err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range root {
		switch key {
		case "image_files", "mask_file":
			continue
		}
		if value == nil {
			continue
		}
		fieldValue, err := stringifyMultipartField(value)
		if err != nil {
			return nil, "", fmt.Errorf("encode image edits field %s: %w", key, err)
		}
		if fieldValue == "" {
			continue
		}
		if err := writer.WriteField(key, fieldValue); err != nil {
			return nil, "", fmt.Errorf("write image edits field %s: %w", key, err)
		}
	}
	if err := writeImageEditFiles(writer, "image", root["image_files"]); err != nil {
		return nil, "", err
	}
	if err := writeImageEditFile(writer, "mask", root["mask_file"]); err != nil {
		return nil, "", err
	}
	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("finalize image edits multipart body: %w", err)
	}
	return body.Bytes(), writer.FormDataContentType(), nil
}

func stringifyMultipartField(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case bool:
		return fmt.Sprint(typed), nil
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	case json.Number:
		return typed.String(), nil
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}

func writeImageEditFiles(writer *multipart.Writer, fieldName string, value any) error {
	if value == nil {
		return nil
	}
	files, ok := value.([]any)
	if !ok {
		return fmt.Errorf("image edits %s must be an array", fieldName)
	}
	if len(files) == 0 {
		return fmt.Errorf("image edits %s is required", fieldName)
	}
	for _, file := range files {
		if err := writeImageEditFile(writer, fieldName, file); err != nil {
			return err
		}
	}
	return nil
}

func writeImageEditFile(writer *multipart.Writer, fieldName string, value any) error {
	if value == nil {
		return nil
	}
	file, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("image edits %s file must be an object", fieldName)
	}
	fileName := strings.TrimSpace(fmt.Sprint(file["file_name"]))
	if fileName == "" || fileName == "<nil>" {
		fileName = fieldName + ".png"
	}
	contentType := strings.TrimSpace(fmt.Sprint(file["content_type"]))
	if contentType == "" || contentType == "<nil>" {
		contentType = "application/octet-stream"
	}
	dataBase64 := strings.TrimSpace(fmt.Sprint(file["data_base64"]))
	if dataBase64 == "" || dataBase64 == "<nil>" {
		return fmt.Errorf("image edits %s file is missing data_base64", fieldName)
	}
	data, err := decodeImageEditBase64(dataBase64)
	if err != nil {
		return fmt.Errorf("decode image edits %s file: %w", fieldName, err)
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, escapeMultipartQuote(fieldName), escapeMultipartQuote(fileName)))
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return fmt.Errorf("create image edits %s part: %w", fieldName, err)
	}
	if _, err := part.Write(data); err != nil {
		return fmt.Errorf("write image edits %s file: %w", fieldName, err)
	}
	return nil
}

func decodeImageEditBase64(value string) ([]byte, error) {
	if comma := strings.Index(value, ","); comma >= 0 {
		prefix := strings.ToLower(strings.TrimSpace(value[:comma]))
		if !strings.HasPrefix(prefix, "data:") || !strings.Contains(prefix, ";base64") {
			return nil, fmt.Errorf("invalid data URI prefix in base64 content")
		}
		value = value[comma+1:]
	}
	return base64.StdEncoding.DecodeString(value)
}

func escapeMultipartQuote(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}

func (e *OpenAICompatExecutor) applyCustomHeadersAndIdentityFingerprint(req *http.Request, auth *cliproxyauth.Auth, websocket bool) {
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	e.applyIdentityFingerprint(req, auth, websocket)
}

func (e *OpenAICompatExecutor) applyIdentityFingerprint(req *http.Request, auth *cliproxyauth.Auth, websocket bool) {
	if req == nil {
		return
	}
	fingerprint := ""
	if auth != nil && auth.Attributes != nil {
		fingerprint = strings.TrimSpace(strings.ToLower(auth.Attributes["identity_fingerprint"]))
	}
	if fingerprint == "" {
		if compat := e.resolveCompatConfig(auth); compat != nil {
			fingerprint = strings.TrimSpace(strings.ToLower(compat.IdentityFingerprint))
		}
	}
	switch fingerprint {
	case "codex":
		if fp, ok := codexIdentityFingerprint(e.cfg); ok {
			applyCodexIdentityFingerprintHeaders(req.Header, fp, websocket)
			if strings.TrimSpace(fp.Originator) != "" {
				req.Header.Set("Originator", fp.Originator)
			}
		}
	}
}

func (e *OpenAICompatExecutor) useResponsesEndpoint(auth *cliproxyauth.Auth, opts cliproxyexecutor.Options) bool {
	if opts.Alt != "" {
		return false
	}
	sourceFormat := opts.SourceFormat.String()
	switch sourceFormat {
	case "openai", "openai-response":
		// These are the two schemas covered by the OpenAI Responses bridge.
	default:
		return false
	}
	if auth != nil && auth.Attributes != nil {
		if strings.EqualFold(strings.TrimSpace(auth.Attributes["response_endpoint"]), "true") {
			return true
		}
		if sourceFormat == "openai-response" && strings.EqualFold(strings.TrimSpace(auth.Attributes["identity_fingerprint"]), "codex") {
			return true
		}
	}
	compat := e.resolveCompatConfig(auth)
	if compat == nil {
		return false
	}
	if compat.ResponseEndpoint {
		return true
	}
	return sourceFormat == "openai-response" && strings.EqualFold(strings.TrimSpace(compat.IdentityFingerprint), "codex")
}

// Refresh is a no-op for API-key based compatibility providers.
func (e *OpenAICompatExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("openai compat executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	return auth, nil
}

func openAICompatImageEndpointPath(opts cliproxyexecutor.Options) string {
	if opts.SourceFormat.String() != openAICompatImageHandlerType {
		return ""
	}
	path := helps.PayloadRequestPath(opts)
	if strings.HasSuffix(path, "/images/edits") {
		return openAICompatImagesEditsPath
	}
	if strings.HasSuffix(path, "/images/generations") {
		return openAICompatImagesGenerationsPath
	}
	return openAICompatDefaultImageEndpoint
}

func openAICompatVideoEndpointPath(opts cliproxyexecutor.Options) string {
	if opts.SourceFormat.String() != openAICompatVideoHandlerType {
		return ""
	}
	path := normalizedOpenAICompatEndpointPath(helps.PayloadRequestPath(opts))
	if strings.HasSuffix(path, "/video/generations") {
		return openAICompatVideoGenerationsPath
	}
	if strings.HasSuffix(path, "/videos/generations") {
		return openAICompatVideosGenerationsPath
	}
	if strings.Contains(path, "/videos/") {
		endpoint := strings.TrimPrefix(path, "/v1")
		endpoint = strings.TrimSuffix(endpoint, "/content")
		return endpoint
	}
	return openAICompatDefaultVideoEndpoint
}

func openAICompatPassthroughMethod(opts cliproxyexecutor.Options, endpointPath string) string {
	if opts.SourceFormat.String() == openAICompatVideoHandlerType && (strings.Contains(endpointPath, "/videos/") || strings.Contains(endpointPath, "/agnesapi")) {
		return http.MethodGet
	}
	return http.MethodPost
}

func openAICompatVideoProviderEndpointPath(opts cliproxyexecutor.Options, endpointPath string, payload []byte, model string, auth *cliproxyauth.Auth) string {
	if opts.SourceFormat.String() != openAICompatVideoHandlerType {
		return endpointPath
	}
	if !strings.Contains(endpointPath, "/videos/") {
		return endpointPath
	}
	if strings.HasSuffix(endpointPath, "/videos/generations") {
		return endpointPath
	}
	videoID := strings.TrimSpace(gjson.GetBytes(payload, "video_id").String())
	if isAgnesOpenAICompatVideo(model, auth) {
		if videoID == "" {
			return endpointPath
		}
		return "/agnesapi?video_id=" + url.QueryEscape(videoID)
	}
	if videoID == "" {
		videoID = strings.TrimSpace(gjson.GetBytes(payload, "request_id").String())
	}
	if videoID == "" {
		return endpointPath
	}
	return "/videos/" + url.PathEscape(videoID)
}

func isAgnesOpenAICompatVideo(model string, auth *cliproxyauth.Auth) bool {
	model = strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(model).ModelName))
	if strings.Contains(model, "agnes-video") {
		return true
	}
	if auth == nil {
		return false
	}
	label := strings.ToLower(strings.TrimSpace(auth.Label))
	if label == "agnes" || strings.Contains(label, "agnes") {
		return true
	}
	for _, key := range []string{"compat_name", "provider_key"} {
		value := strings.ToLower(strings.TrimSpace(auth.Attributes[key]))
		if value == "agnes" || value == "agnes-ai" {
			return true
		}
	}
	return false
}

func normalizedOpenAICompatEndpointPath(path string) string {
	path = strings.TrimSpace(path)
	for _, prefix := range []string{"/openai/v1", "/v1"} {
		if strings.HasPrefix(path, prefix) {
			path = strings.TrimPrefix(path, prefix)
			if path == "" {
				return "/"
			}
			return path
		}
	}
	return path
}

func prepareOpenAICompatImagesPayload(payload []byte, model string, contentType string, stream bool) ([]byte, string, error) {
	model = strings.TrimSpace(model)
	contentType = strings.TrimSpace(contentType)
	if json.Valid(payload) {
		if model != "" {
			payload, _ = sjson.SetBytes(payload, "model", model)
		}
		if stream {
			payload, _ = sjson.SetBytes(payload, "stream", true)
		} else {
			payload, _ = sjson.DeleteBytes(payload, "stream")
		}
		return payload, "application/json", nil
	}

	mediaType, params, errParse := mime.ParseMediaType(contentType)
	if errParse != nil || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(mediaType)), "multipart/") {
		return payload, contentType, nil
	}
	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return nil, "", fmt.Errorf("multipart boundary is missing")
	}
	return rewriteOpenAICompatImagesMultipartPayload(payload, model, boundary, stream)
}

func cloneOpenAICompatMIMEHeader(src textproto.MIMEHeader) textproto.MIMEHeader {
	dst := make(textproto.MIMEHeader, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func rewriteOpenAICompatImagesMultipartPayload(payload []byte, model string, boundary string, stream bool) ([]byte, string, error) {
	reader := multipart.NewReader(bytes.NewReader(payload), boundary)
	form, errRead := reader.ReadForm(openAICompatMultipartMemory)
	if errRead != nil {
		return nil, "", fmt.Errorf("read multipart form failed: %w", errRead)
	}
	defer func() {
		if errRemove := form.RemoveAll(); errRemove != nil {
			log.Errorf("openai compat executor: remove multipart form files error: %v", errRemove)
		}
	}()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if model != "" {
		if errWrite := writer.WriteField("model", model); errWrite != nil {
			return nil, "", fmt.Errorf("write model field failed: %w", errWrite)
		}
	}
	if stream {
		if errWrite := writer.WriteField("stream", "true"); errWrite != nil {
			return nil, "", fmt.Errorf("write stream field failed: %w", errWrite)
		}
	}
	for key, values := range form.Value {
		if key == "model" || key == "stream" {
			continue
		}
		for _, value := range values {
			if errWrite := writer.WriteField(key, value); errWrite != nil {
				return nil, "", fmt.Errorf("write form field %s failed: %w", key, errWrite)
			}
		}
	}
	for key, files := range form.File {
		for _, fileHeader := range files {
			if fileHeader == nil {
				continue
			}
			header := cloneOpenAICompatMIMEHeader(fileHeader.Header)
			header.Set("Content-Disposition", multipart.FileContentDisposition(key, fileHeader.Filename))
			if header.Get("Content-Type") == "" {
				header.Set("Content-Type", "application/octet-stream")
			}
			part, errCreate := writer.CreatePart(header)
			if errCreate != nil {
				return nil, "", fmt.Errorf("create file field %s failed: %w", key, errCreate)
			}
			src, errOpen := fileHeader.Open()
			if errOpen != nil {
				return nil, "", fmt.Errorf("open upload file failed: %w", errOpen)
			}
			_, errCopy := io.Copy(part, src)
			if errClose := src.Close(); errClose != nil {
				log.Errorf("openai compat executor: close upload file error: %v", errClose)
				if errCopy == nil {
					errCopy = errClose
				}
			}
			if errCopy != nil {
				return nil, "", fmt.Errorf("copy upload file failed: %w", errCopy)
			}
		}
	}
	if errClose := writer.Close(); errClose != nil {
		return nil, "", fmt.Errorf("close multipart writer failed: %w", errClose)
	}
	return body.Bytes(), writer.FormDataContentType(), nil
}

func (e *OpenAICompatExecutor) resolveCredentials(auth *cliproxyauth.Auth) (baseURL, apiKey string) {
	if auth == nil {
		return "", ""
	}
	if auth.Attributes != nil {
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
		apiKey = strings.TrimSpace(auth.Attributes["api_key"])
	}
	return
}

func (e *OpenAICompatExecutor) resolveCompatConfig(auth *cliproxyauth.Auth) *config.OpenAICompatibility {
	if auth == nil || e.cfg == nil {
		return nil
	}
	candidates := make([]string, 0, 3)
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["compat_name"]); v != "" {
			candidates = append(candidates, v)
		}
		if v := strings.TrimSpace(auth.Attributes["provider_key"]); v != "" {
			candidates = append(candidates, v)
		}
	}
	if v := strings.TrimSpace(auth.Provider); v != "" {
		candidates = append(candidates, v)
	}
	for _, candidate := range candidates {
		if strings.EqualFold(strings.TrimSpace(candidate), config.DefaultAgnesProviderName) ||
			strings.EqualFold(strings.TrimSpace(candidate), "agnes-ai") {
			for i := range e.cfg.AgnesAPIKey {
				compat := &e.cfg.AgnesAPIKey[i]
				if !compat.Disabled {
					return compat
				}
			}
		}
	}
	for i := range e.cfg.OpenAICompatibility {
		compat := &e.cfg.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		for _, candidate := range candidates {
			if candidate != "" && strings.EqualFold(strings.TrimSpace(candidate), compat.Name) {
				return compat
			}
		}
	}
	return nil
}

func (e *OpenAICompatExecutor) overrideModel(payload []byte, model string) []byte {
	if len(payload) == 0 || model == "" {
		return payload
	}
	payload, _ = sjson.SetBytes(payload, "model", model)
	return payload
}

func openAICompatForwardSemanticStreamChunks(ctx context.Context, out chan<- cliproxyexecutor.StreamChunk, pending *[][]byte, semanticOutput *bool, chunks [][]byte) bool {
	if len(chunks) == 0 {
		return true
	}
	if semanticOutput != nil && !*semanticOutput {
		if openAICompatStreamChunksHaveSemanticOutput(chunks) {
			*semanticOutput = true
			if pending != nil && len(*pending) > 0 {
				if !openAICompatSendStreamChunks(ctx, out, *pending) {
					return false
				}
				*pending = nil
			}
		} else {
			if pending != nil {
				*pending = append(*pending, chunks...)
			}
			return true
		}
	}
	return openAICompatSendStreamChunks(ctx, out, chunks)
}

func openAICompatSendStreamChunks(ctx context.Context, out chan<- cliproxyexecutor.StreamChunk, chunks [][]byte) bool {
	for i := range chunks {
		select {
		case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
		case <-ctx.Done():
			return false
		}
	}
	return true
}

func openAICompatStreamChunksHaveSemanticOutput(chunks [][]byte) bool {
	for _, chunk := range chunks {
		if openAICompatStreamChunkHasSemanticOutput(chunk) {
			return true
		}
	}
	return false
}

func openAICompatStreamChunkHasSemanticOutput(chunk []byte) bool {
	for _, payload := range openAICompatSSEDataPayloads(chunk) {
		if openAICompatStreamPayloadHasSemanticOutput(payload) {
			return true
		}
	}
	return false
}

func openAICompatSSEDataPayloads(chunk []byte) [][]byte {
	chunk = bytes.TrimSpace(chunk)
	if len(chunk) == 0 {
		return nil
	}
	var payloads [][]byte
	frames := bytes.Split(chunk, []byte("\n\n"))
	for _, frame := range frames {
		var payload bytes.Buffer
		for _, line := range bytes.Split(frame, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			data := bytes.TrimSpace(line[len("data:"):])
			if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
				continue
			}
			if payload.Len() > 0 {
				payload.WriteByte('\n')
			}
			payload.Write(data)
		}
		if payload.Len() > 0 {
			payloads = append(payloads, append([]byte(nil), payload.Bytes()...))
		}
	}
	if len(payloads) == 0 && json.Valid(chunk) {
		payloads = append(payloads, append([]byte(nil), chunk...))
	}
	return payloads
}

func openAICompatStreamPayloadHasSemanticOutput(payload []byte) bool {
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return false
	}
	payloadType := gjson.GetBytes(payload, "type").String()
	if strings.HasPrefix(payloadType, "response.image_generation_call.") ||
		strings.HasPrefix(payloadType, "image_generation.") {
		return true
	}
	switch payloadType {
	case "response.output_item.added",
		"response.output_item.done",
		"response.content_part.added",
		"response.content_part.done",
		"response.output_text.delta",
		"response.output_text.done",
		"response.function_call_arguments.delta",
		"response.function_call_arguments.done",
		"response.reasoning_summary_part.added",
		"response.reasoning_summary_part.done",
		"response.reasoning_summary_text.delta",
		"response.reasoning_summary_text.done":
		return true
	case "response.completed":
		output := gjson.GetBytes(payload, "response.output")
		return output.IsArray() && len(output.Array()) > 0
	}
	return openAICompatChatCompletionPayloadHasSemanticOutput(payload)
}

func openAICompatChatCompletionPayloadHasSemanticOutput(payload []byte) bool {
	choices := gjson.GetBytes(payload, "choices")
	if !choices.IsArray() {
		return false
	}
	semantic := false
	choices.ForEach(func(_, choice gjson.Result) bool {
		if strings.TrimSpace(choice.Get("text").String()) != "" {
			semantic = true
			return false
		}
		message := choice.Get("message")
		if strings.TrimSpace(message.Get("content").String()) != "" {
			semantic = true
			return false
		}
		delta := choice.Get("delta")
		if strings.TrimSpace(delta.Get("content").String()) != "" ||
			strings.TrimSpace(delta.Get("reasoning_content").String()) != "" {
			semantic = true
			return false
		}
		if functionCall := delta.Get("function_call"); functionCall.Exists() {
			if strings.TrimSpace(functionCall.Get("name").String()) != "" ||
				strings.TrimSpace(functionCall.Get("arguments").String()) != "" {
				semantic = true
				return false
			}
		}
		if toolCalls := delta.Get("tool_calls"); toolCalls.IsArray() {
			toolCalls.ForEach(func(_, toolCall gjson.Result) bool {
				if strings.TrimSpace(toolCall.Get("id").String()) != "" ||
					strings.TrimSpace(toolCall.Get("function.name").String()) != "" ||
					strings.TrimSpace(toolCall.Get("function.arguments").String()) != "" {
					semantic = true
					return false
				}
				return true
			})
			if semantic {
				return false
			}
		}
		return true
	})
	return semantic
}

type statusErr struct {
	code       int
	msg        string
	retryAfter *time.Duration
}

func (e statusErr) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return fmt.Sprintf("status %d", e.code)
}
func (e statusErr) StatusCode() int            { return e.code }
func (e statusErr) RetryAfter() *time.Duration { return e.retryAfter }
