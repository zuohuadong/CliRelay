package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/tiktoken-go/tokenizer"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	codexUserAgent                    = "codex-tui/0.145.0 (Mac OS 26.5.0; arm64) iTerm.app/3.6.10 (codex-tui; 0.145.0)"
	codexOriginator                   = "codex-tui"
	codexDefaultImageToolModel        = "gpt-image-2"
	codexCompactResponseHeaderTimeout = 30 * time.Second
	codexFastModeServiceTier          = "priority"
	codexEgressModeSharedProxy        = "shared_proxy"
	codexResponsesLiteHeader          = "X-OpenAI-Internal-Codex-Responses-Lite"
	codexResponsesLiteMetadata        = "client_metadata.ws_request_header_x_openai_internal_codex_responses_lite"
)

// codexHTTPStreamIdleTimeout aborts an upstream SSE stream that accepts the
// connection but sends no data for this duration. This prevents the client
// from hitting its own 5-minute idle timeout ("idle timeout waiting for SSE")
// and surfaces a clear error instead.
var codexHTTPStreamIdleTimeout = 3 * time.Minute

func startCodexHTTPStreamIdleWatch(ctx context.Context, body io.Closer) (chan struct{}, func(), *atomic.Bool) {
	idleReset := make(chan struct{}, 1)
	stop := make(chan struct{})
	done := make(chan struct{})
	timedOut := new(atomic.Bool)
	var stopOnce sync.Once

	go func() {
		defer close(done)
		timer := time.NewTimer(codexHTTPStreamIdleTimeout)
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				timedOut.Store(true)
				helps.LogWithRequestID(ctx).Warnf("codex executor: stream idle timeout after %s without upstream data, aborting read", codexHTTPStreamIdleTimeout)
				_ = body.Close()
				return
			case <-idleReset:
				resetCodexHTTPStreamIdleTimer(timer)
			case <-stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	return idleReset, func() {
		stopOnce.Do(func() {
			close(stop)
			<-done
		})
	}, timedOut
}

func resetCodexHTTPStreamIdleTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(codexHTTPStreamIdleTimeout)
}

const codexDefaultResponseHeaderTimeout = time.Duration(config.DefaultCodexResponseHeaderTimeoutSeconds) * time.Second

const (
	codexResetCreditsURL        = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"
	codexResetCreditsConsumeURL = codexResetCreditsURL + "/consume"
	codexResetCreditsTimeout    = time.Minute
)

var dataTag = []byte("data:")

const codexIncompleteStreamMessage = "stream error: stream disconnected before completion: stream closed before response.completed"

type codexIncompleteStreamError struct {
	statusErr
}

func newCodexIncompleteStreamError() codexIncompleteStreamError {
	return codexIncompleteStreamError{statusErr: statusErr{
		code: http.StatusRequestTimeout,
		msg:  codexIncompleteStreamMessage,
	}}
}

func (codexIncompleteStreamError) IsRequestScoped() bool {
	return true
}

// Streamed Codex responses may emit response.output_item.done events while leaving
// response.completed.response.output empty. Keep the stream path aligned with the
// already-patched non-stream path by reconstructing response.output from those items.
func collectCodexOutputItemDone(eventData []byte, outputItemsByIndex map[int64][]byte, outputItemsFallback *[][]byte) {
	itemResult := gjson.GetBytes(eventData, "item")
	if !itemResult.Exists() || itemResult.Type != gjson.JSON {
		return
	}
	outputIndexResult := gjson.GetBytes(eventData, "output_index")
	if outputIndexResult.Exists() {
		outputItemsByIndex[outputIndexResult.Int()] = []byte(itemResult.Raw)
		return
	}
	*outputItemsFallback = append(*outputItemsFallback, []byte(itemResult.Raw))
}

func patchCodexCompletedOutput(eventData []byte, outputItemsByIndex map[int64][]byte, outputItemsFallback [][]byte) []byte {
	outputResult := gjson.GetBytes(eventData, "response.output")
	shouldPatchOutput := (!outputResult.Exists() || !outputResult.IsArray() || len(outputResult.Array()) == 0) && (len(outputItemsByIndex) > 0 || len(outputItemsFallback) > 0)
	if !shouldPatchOutput {
		return eventData
	}

	indexes := make([]int64, 0, len(outputItemsByIndex))
	for idx := range outputItemsByIndex {
		indexes = append(indexes, idx)
	}
	sort.Slice(indexes, func(i, j int) bool {
		return indexes[i] < indexes[j]
	})

	items := make([][]byte, 0, len(outputItemsByIndex)+len(outputItemsFallback))
	for _, idx := range indexes {
		items = append(items, outputItemsByIndex[idx])
	}
	items = append(items, outputItemsFallback...)

	outputArray := []byte("[]")
	if len(items) > 0 {
		var buf bytes.Buffer
		totalLen := 2
		for _, item := range items {
			totalLen += len(item)
		}
		if len(items) > 1 {
			totalLen += len(items) - 1
		}
		buf.Grow(totalLen)
		buf.WriteByte('[')
		for i, item := range items {
			if i > 0 {
				buf.WriteByte(',')
			}
			buf.Write(item)
		}
		buf.WriteByte(']')
		outputArray = buf.Bytes()
	}

	completedDataPatched, _ := sjson.SetRawBytes(eventData, "response.output", outputArray)
	return completedDataPatched
}

func codexOutputArrayHasSemanticOutput(output gjson.Result) bool {
	if !output.IsArray() {
		return false
	}
	for _, item := range output.Array() {
		if codexOutputItemHasSemanticOutput(item) {
			return true
		}
	}
	return false
}

func codexOutputItemHasSemanticOutput(item gjson.Result) bool {
	if !item.Exists() || item.Type != gjson.JSON {
		return false
	}
	switch item.Get("type").String() {
	case "message":
		if strings.TrimSpace(item.Get("content").String()) != "" {
			return true
		}
		content := item.Get("content")
		if !content.IsArray() {
			return false
		}
		for _, part := range content.Array() {
			if strings.TrimSpace(part.Get("text").String()) != "" {
				return true
			}
		}
		return false
	case "function_call":
		return strings.TrimSpace(item.Get("call_id").String()) != "" ||
			strings.TrimSpace(item.Get("name").String()) != "" ||
			strings.TrimSpace(item.Get("arguments").String()) != ""
	case "custom_tool_call":
		return strings.TrimSpace(item.Get("call_id").String()) != "" ||
			strings.TrimSpace(item.Get("name").String()) != "" ||
			strings.TrimSpace(item.Get("input").String()) != ""
	case "reasoning":
		if strings.TrimSpace(item.Get("encrypted_content").String()) != "" {
			return true
		}
		summary := item.Get("summary")
		if !summary.IsArray() {
			return false
		}
		for _, part := range summary.Array() {
			if strings.TrimSpace(part.Get("text").String()) != "" {
				return true
			}
		}
		return false
	default:
		return strings.TrimSpace(item.Raw) != "" && strings.TrimSpace(item.Raw) != "{}"
	}
}

func codexSendStreamChunks(ctx context.Context, out chan<- cliproxyexecutor.StreamChunk, chunks [][]byte) bool {
	for i := range chunks {
		select {
		case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
		case <-ctx.Done():
			return false
		}
	}
	return true
}

func codexTerminalStreamContextLengthErr(eventData []byte) (statusErr, bool) {
	streamErr, body, ok := codexTerminalStreamErr(eventData)
	if !ok || !codexTerminalErrorIsContextLength(body) {
		return statusErr{}, false
	}
	return streamErr, true
}

func codexTerminalStreamErr(eventData []byte) (statusErr, []byte, bool) {
	body, ok := codexTerminalFailureBody(eventData)
	if !ok || !codexTerminalStreamErrShouldHandle(body) {
		return statusErr{}, nil, false
	}
	return newCodexStatusErr(codexTerminalErrorStatus(eventData, body), body), body, true
}

func codexTerminalFailureErr(eventData []byte) (statusErr, []byte, bool) {
	if streamErr, body, ok := codexTerminalStreamErr(eventData); ok {
		return streamErr, body, true
	}
	body, ok := codexTerminalFailureBody(eventData)
	if !ok {
		return statusErr{}, nil, false
	}
	return newCodexStatusErr(codexTerminalFailureStatus(body), body), body, true
}

func codexTerminalFailureStatus(body []byte) int {
	for _, path := range []string{"error.status_code", "error.status"} {
		if status := int(gjson.GetBytes(body, path).Int()); status >= 400 && status <= 599 {
			return status
		}
	}

	errorType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.type").String()))
	errorCode := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.code").String()))
	switch {
	case errorType == "invalid_request_error", errorType == "bad_request_error":
		return http.StatusBadRequest
	case errorType == "authentication_error", errorCode == "invalid_api_key", errorCode == "unauthorized":
		return http.StatusUnauthorized
	case errorType == "permission_error", errorCode == "forbidden", errorCode == "permission_denied":
		return http.StatusForbidden
	case errorType == "not_found_error", errorCode == "not_found", errorCode == "model_not_found":
		return http.StatusNotFound
	case errorType == "rate_limit_error", errorCode == "rate_limit_exceeded":
		return http.StatusTooManyRequests
	default:
		return http.StatusBadGateway
	}
}

func codexTerminalFailureBody(eventData []byte) ([]byte, bool) {
	eventType := gjson.GetBytes(eventData, "type").String()
	var body []byte
	switch eventType {
	case "error":
		body = codexTerminalErrorBody(eventData, "error")
		if len(body) == 0 {
			body = codexTerminalTopLevelErrorBody(eventData)
		}
	case "response.failed":
		body = codexTerminalErrorBody(eventData, "response.error")
		if len(body) == 0 {
			body = codexTerminalErrorBody(eventData, "error")
		}
	default:
		return nil, false
	}
	if len(body) == 0 {
		body = []byte(`{"error":{"message":"upstream stream failed without error details"}}`)
	}
	return body, true
}

func codexTerminalStreamErrShouldHandle(body []byte) bool {
	if codexTerminalErrorIsContextLength(body) {
		return true
	}
	if isCodexUsageLimitError(body) || isCodexModelCapacityError(body) {
		return true
	}
	if codexTerminalErrorStatus(nil, body) == http.StatusTooManyRequests {
		return true
	}
	code, _, ok := codexStatusErrorClassification(http.StatusBadRequest, body)
	return ok && code == "thinking_signature_invalid"
}

func codexTerminalErrorStatus(eventData []byte, body []byte) int {
	if status := int(gjson.GetBytes(eventData, "status").Int()); status > 0 {
		return status
	}

	errorType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.type").String()))
	errorCode := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.code").String()))
	errorMessage := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.message").String()))

	switch {
	case errorType == "authentication_error" || errorCode == "invalid_api_key" ||
		strings.Contains(errorMessage, "invalid or expired token") || strings.Contains(errorMessage, "refresh_token_reused"):
		return http.StatusUnauthorized
	case errorType == "rate_limit_error" || errorType == "usage_limit_reached" ||
		errorCode == "rate_limit_exceeded" || errorCode == "usage_limit_reached":
		return http.StatusTooManyRequests
	case errorType == "server_error":
		return http.StatusBadGateway
	default:
		return http.StatusBadRequest
	}
}

func codexTerminalErrorBody(eventData []byte, path string) []byte {
	errorResult := gjson.GetBytes(eventData, path)
	if !errorResult.Exists() {
		return nil
	}
	body := []byte(`{"error":{}}`)
	if errorResult.Type == gjson.JSON {
		body, _ = sjson.SetRawBytes(body, "error", []byte(errorResult.Raw))
	} else if message := strings.TrimSpace(errorResult.String()); message != "" {
		body, _ = sjson.SetBytes(body, "error.message", message)
	}
	if strings.TrimSpace(gjson.GetBytes(body, "error.message").String()) == "" {
		if message := strings.TrimSpace(gjson.GetBytes(eventData, "response.error.message").String()); message != "" {
			body, _ = sjson.SetBytes(body, "error.message", message)
		}
	}
	if strings.TrimSpace(gjson.GetBytes(body, "error.message").String()) == "" {
		if code := strings.TrimSpace(gjson.GetBytes(body, "error.code").String()); code != "" {
			body, _ = sjson.SetBytes(body, "error.message", code)
		}
	}
	if strings.TrimSpace(gjson.GetBytes(body, "error.message").String()) == "" {
		if errorType := strings.TrimSpace(gjson.GetBytes(body, "error.type").String()); errorType != "" {
			body, _ = sjson.SetBytes(body, "error.message", errorType)
		}
	}
	return body
}

func codexTerminalTopLevelErrorBody(eventData []byte) []byte {
	message := strings.TrimSpace(gjson.GetBytes(eventData, "message").String())
	code := strings.TrimSpace(gjson.GetBytes(eventData, "code").String())
	errorType := strings.TrimSpace(gjson.GetBytes(eventData, "error_type").String())
	param := strings.TrimSpace(gjson.GetBytes(eventData, "param").String())
	if message == "" && code == "" && errorType == "" && param == "" {
		return nil
	}

	body := []byte(`{"error":{}}`)
	if message != "" {
		body, _ = sjson.SetBytes(body, "error.message", message)
	}
	if code != "" {
		body, _ = sjson.SetBytes(body, "error.code", code)
	}
	if errorType != "" {
		body, _ = sjson.SetBytes(body, "error.type", errorType)
	}
	if param != "" {
		body, _ = sjson.SetBytes(body, "error.param", param)
	}
	if strings.TrimSpace(gjson.GetBytes(body, "error.message").String()) == "" {
		if code != "" {
			body, _ = sjson.SetBytes(body, "error.message", code)
		} else if errorType != "" {
			body, _ = sjson.SetBytes(body, "error.message", errorType)
		}
	}
	return body
}

func codexTerminalErrorIsContextLength(body []byte) bool {
	errorCode := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.code").String()))
	message := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.message").String()))
	return errorCode == "context_length_exceeded" ||
		errorCode == "context_too_large" ||
		codexErrorTextIndicatesContextLength(message)
}

func codexErrorTextIndicatesContextLength(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	return strings.Contains(text, "context length") ||
		strings.Contains(text, "context_length_exceeded") ||
		strings.Contains(text, "context_too_large") ||
		strings.Contains(text, "context window") ||
		strings.Contains(text, "maximum context") ||
		strings.Contains(text, "ran out of room") ||
		strings.Contains(text, "too many tokens")
}

// CodexExecutor is a stateless executor for Codex (OpenAI Responses API entrypoint).
// If api_key is unavailable on auth, it falls back to legacy via ClientAdapter.
type CodexExecutor struct {
	cfg           *config.Config
	egress        egress.Resolver
	strictEgress  bool
	homeRefresh   func(context.Context, *config.Config, *cliproxyauth.Auth) (*cliproxyauth.Auth, bool, error)
	refreshTokens func(context.Context, *http.Client, string) (*codexauth.CodexTokenData, error)
}

func NewCodexExecutor(cfg *config.Config) *CodexExecutor { return &CodexExecutor{cfg: cfg} }

func NewCodexExecutorWithEgress(cfg *config.Config, resolver egress.Resolver) *CodexExecutor {
	return &CodexExecutor{cfg: cfg, egress: resolver, strictEgress: true}
}

func (e *CodexExecutor) Identifier() string { return "codex" }

func (e *CodexExecutor) refreshViaHome(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, bool, error) {
	if e != nil && e.homeRefresh != nil {
		return e.homeRefresh(ctx, e.cfg, auth)
	}
	var cfg *config.Config
	if e != nil {
		cfg = e.cfg
	}
	return helps.RefreshAuthViaHome(ctx, cfg, auth)
}

func (e *CodexExecutor) refreshCodexTokens(ctx context.Context, client *http.Client, refreshToken string) (*codexauth.CodexTokenData, error) {
	if e != nil && e.refreshTokens != nil {
		return e.refreshTokens(ctx, client, refreshToken)
	}
	return codexauth.NewCodexAuthWithHTTPClient(client).RefreshTokensWithRetry(ctx, refreshToken, 3)
}

func codexUsesSharedProxyEgress(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	egressMode, _ := auth.Metadata["egress_mode"].(string)
	return egressMode == codexEgressModeSharedProxy
}

func (e *CodexExecutor) usesStrictEgress(auth *cliproxyauth.Auth) bool {
	return e != nil && e.strictEgress && !codexUsesSharedProxyEgress(auth)
}

func (e *CodexExecutor) validateSharedProxyEgress(auth *cliproxyauth.Auth) error {
	if !codexUsesSharedProxyEgress(auth) {
		return nil
	}
	proxyURL := ""
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && e != nil && e.cfg != nil {
		proxyURL = strings.TrimSpace(e.cfg.ProxyURL)
	}
	if proxyURL == "" {
		return egress.RuntimeError(fmt.Errorf("%w: shared Codex proxy is required", egress.ErrEgressRequired))
	}
	setting, err := proxyutil.Parse(proxyURL)
	if err != nil || setting.Mode != proxyutil.ModeProxy || setting.URL == nil {
		return egress.RuntimeError(fmt.Errorf("%w: shared Codex proxy must be a valid non-direct proxy", egress.ErrEndpointInvalid))
	}
	return nil
}

func (e *CodexExecutor) outboundHTTPClient(ctx context.Context, auth *cliproxyauth.Auth, timeout, responseHeaderTimeout time.Duration, useUTLS bool) (*http.Client, error) {
	if err := e.validateSharedProxyEgress(auth); err != nil {
		return nil, err
	}
	if e.usesStrictEgress(auth) {
		proxyURL := ""
		if auth != nil {
			proxyURL = strings.TrimSpace(auth.ProxyURL)
		}
		var (
			client *http.Client
			err    error
		)
		if useUTLS {
			client, err = helps.NewStrictUtlsHTTPClient(proxyURL, timeout)
		} else {
			client, err = helps.NewStrictProxyHTTPClient(proxyURL, timeout, responseHeaderTimeout)
		}
		if err != nil {
			return nil, egress.RuntimeError(fmt.Errorf("%w: %v", egress.ErrEndpointInvalid, err))
		}
		if client == nil || client.Transport == nil {
			return nil, egress.RuntimeError(fmt.Errorf("%w: strict proxy transport is unavailable", egress.ErrEndpointInvalid))
		}
		client.Transport = strictEgressRoundTripper{base: client.Transport}
		if useUTLS {
			client = helps.WithResponseHeaderTimeout(client, responseHeaderTimeout)
		}
		return client, nil
	}
	if useUTLS {
		return helps.WithResponseHeaderTimeout(helps.NewUtlsHTTPClient(ctx, e.cfg, auth, timeout), responseHeaderTimeout), nil
	}
	return helps.NewProxyAwareHTTPClientWithResponseHeaderTimeout(ctx, e.cfg, auth, timeout, responseHeaderTimeout), nil
}

func codexResponseHeaderTimeout(cfg *config.Config) time.Duration {
	if cfg != nil {
		seconds := cfg.Codex.ResponseHeaderTimeoutSeconds
		if seconds < 0 {
			return 0
		}
		if seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
	}
	return codexDefaultResponseHeaderTimeout
}

func codexResponseHeaderTimeoutError(err error) error {
	if !helps.IsResponseHeaderTimeout(err) {
		return err
	}
	return statusErr{code: http.StatusGatewayTimeout, msg: `{"error":{"message":"upstream response header timeout","type":"server_error","code":"upstream_response_header_timeout"}}`}
}

type strictEgressRoundTripper struct {
	base http.RoundTripper
}

func (t strictEgressRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, wrapStrictEgressTransportError(err, "proxy request")
	}
	if resp != nil && resp.Body != nil {
		resp.Body = strictEgressReadCloser{ReadCloser: resp.Body}
	}
	return resp, nil
}

type strictEgressReadCloser struct {
	io.ReadCloser
}

func (r strictEgressReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	return n, wrapStrictEgressTransportError(err, "proxy response read")
}

func wrapStrictEgressTransportError(err error, operation string) error {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var runtimeErr *egress.Error
	if errors.As(err, &runtimeErr) {
		return err
	}
	var statusError interface{ StatusCode() int }
	if errors.As(err, &statusError) && statusError.StatusCode() > 0 {
		return err
	}
	operation = strings.TrimSpace(operation)
	if operation == "" {
		operation = "transport"
	}
	return egress.RuntimeError(fmt.Errorf("%w: strict egress %s failed: %v", egress.ErrEndpointDisabled, operation, err))
}

func (e *CodexExecutor) wrapStrictEgressTransportError(err error, operation string) error {
	if e == nil || !e.strictEgress {
		return err
	}
	return wrapStrictEgressTransportError(err, operation)
}

func (e *CodexExecutor) wrapStrictEgressTransportErrorForAuth(auth *cliproxyauth.Auth, err error, operation string) error {
	if !e.usesStrictEgress(auth) {
		return err
	}
	return wrapStrictEgressTransportError(err, operation)
}

func (e *CodexExecutor) resolveEgressAuth(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if codexUsesSharedProxyEgress(auth) {
		if err := e.validateSharedProxyEgress(auth); err != nil {
			return nil, err
		}
		return auth, nil
	}
	if !e.usesStrictEgress(auth) {
		return auth, nil
	}
	if e.egress == nil {
		return nil, egress.RuntimeError(fmt.Errorf("%w: Codex egress resolver is unavailable", egress.ErrEgressRequired))
	}
	if auth == nil {
		return nil, egress.RuntimeError(fmt.Errorf("%w: Codex auth is missing", egress.ErrEgressRequired))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	accountID := codexAccountIDFromAuth(auth)
	if accountID == "" {
		return nil, egress.RuntimeError(fmt.Errorf("%w: codex account_id is required", egress.ErrIdentityRequired))
	}
	resolved, err := e.egress.Resolve(ctx, accountID)
	if err != nil {
		return nil, egress.RuntimeError(err)
	}
	if strings.TrimSpace(resolved.ProxyURL) == "" {
		return nil, egress.RuntimeError(fmt.Errorf("%w: resolved endpoint has no proxy URL", egress.ErrEndpointInvalid))
	}
	cloned := auth.Clone()
	cloned.ProxyURL = strings.TrimSpace(resolved.ProxyURL)
	if cloned.Attributes == nil {
		cloned.Attributes = make(map[string]string)
	}
	cloned.Attributes["egress_id"] = resolved.Endpoint.ID
	return cloned, nil
}

// codexAccountIDFromAuth extracts the Codex account_id from auth metadata.
// It falls back to parsing the id_token JWT when account_id is missing and
// backfills the metadata map in place so downstream readers see it.
func codexAccountIDFromAuth(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	return codexauth.AccountIDFromMetadata(auth.Metadata)
}

func translateCodexRequestPair(from, to sdktranslator.Format, model string, originalPayload, payload []byte, stream bool) ([]byte, []byte) {
	if bytes.Equal(originalPayload, payload) {
		body := sdktranslator.TranslateRequest(from, to, model, payload, stream)
		return body, body
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, model, originalPayload, stream)
	body := sdktranslator.TranslateRequest(from, to, model, payload, stream)
	return originalTranslated, body
}

type codexReasoningReplayScope struct {
	modelName          string
	sessionKey         string
	requestFingerprint string
}

func (s codexReasoningReplayScope) valid() bool {
	return strings.TrimSpace(s.modelName) != "" && strings.TrimSpace(s.sessionKey) != ""
}

func applyCodexReasoningReplayCache(ctx context.Context, from sdktranslator.Format, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, body []byte) ([]byte, codexReasoningReplayScope) {
	updated, scope, _ := applyCodexReasoningReplayCacheRequired(ctx, from, req, opts, body)
	return updated, scope
}

func applyCodexReasoningReplayCacheRequired(ctx context.Context, from sdktranslator.Format, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, body []byte) ([]byte, codexReasoningReplayScope, error) {
	scope := codexReasoningReplayScopeFromRequest(ctx, from, req, opts, body)
	if !scope.valid() {
		return body, scope, nil
	}
	items, ok, errReplay := internalcache.GetCodexReasoningReplayItemsRequired(ctx, scope.modelName, scope.sessionKey)
	if errReplay != nil || !ok {
		return body, scope, errReplay
	}
	updated, ok := insertCodexReasoningReplayTurns(body, items)
	if !ok {
		return body, scope, nil
	}
	return updated, scope, nil
}

func codexReasoningReplayScopeFromRequest(ctx context.Context, from sdktranslator.Format, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, body []byte) codexReasoningReplayScope {
	if !codexReasoningReplayEnabledForSource(from) {
		return codexReasoningReplayScope{}
	}
	modelName := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if modelName == "" {
		modelName = thinking.ParseSuffix(req.Model).ModelName
	}
	inputItems := gjson.GetBytes(body, "input").Array()
	return codexReasoningReplayScope{
		modelName:          modelName,
		sessionKey:         codexReasoningReplaySessionKey(ctx, from, req, opts, body),
		requestFingerprint: codexReplayInputPrefixFingerprint(inputItems, len(inputItems)),
	}
}

func codexReasoningReplayEnabledForSource(from sdktranslator.Format) bool {
	return sourceFormatEqual(from, sdktranslator.FormatClaude)
}

func sourceFormatEqual(from, want sdktranslator.Format) bool {
	return strings.EqualFold(strings.TrimSpace(from.String()), want.String())
}

func codexClaudeCodeReplaySessionKey(ctx context.Context, payload []byte, headers http.Header) string {
	sessionKey, _ := helps.ClaudeCodeExecutionScope(ctx, payload, headers)
	return sessionKey
}

func codexReasoningReplaySessionKey(ctx context.Context, from sdktranslator.Format, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, body []byte) string {
	if ctx == nil {
		ctx = context.Background()
	}
	if sourceFormatEqual(from, sdktranslator.FormatClaude) {
		if sessionKey := codexClaudeCodeReplaySessionKey(ctx, req.Payload, opts.Headers); sessionKey != "" {
			return sessionKey
		}
	}
	if value := metadataString(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey); value != "" {
		return "execution:" + value
	}
	if value := metadataString(req.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey); value != "" {
		return "execution:" + value
	}
	if value := codexReasoningReplaySessionKeyFromPayload(body); value != "" {
		return value
	}
	if value := codexReasoningReplaySessionKeyFromPayload(req.Payload); value != "" {
		return value
	}
	if value := codexReasoningReplaySessionKeyFromHeaders(opts.Headers); value != "" {
		return value
	}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		if value := codexReasoningReplaySessionKeyFromHeaders(ginCtx.Request.Header); value != "" {
			return value
		}
	}
	if sourceFormatEqual(from, sdktranslator.FormatOpenAI) {
		if apiKey := strings.TrimSpace(helps.APIKeyFromContext(ctx)); apiKey != "" {
			return "prompt-cache:" + uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:prompt-cache:"+apiKey)).String()
		}
	}
	return ""
}

func metadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	raw, ok := metadata[key]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return ""
	}
}

func codexReasoningReplaySessionKeyFromPayload(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	if promptCacheKey := strings.TrimSpace(gjson.GetBytes(payload, "prompt_cache_key").String()); promptCacheKey != "" {
		return "prompt-cache:" + promptCacheKey
	}
	if windowID := strings.TrimSpace(gjson.GetBytes(payload, "client_metadata.x-codex-window-id").String()); windowID != "" {
		return "window:" + windowID
	}
	if turnMetadata := strings.TrimSpace(gjson.GetBytes(payload, "client_metadata.x-codex-turn-metadata").String()); turnMetadata != "" {
		return codexReasoningReplaySessionKeyFromTurnMetadata(turnMetadata)
	}
	return ""
}

func codexReasoningReplaySessionKeyFromHeaders(headers http.Header) string {
	if headers == nil {
		return ""
	}
	if turnMetadata := strings.TrimSpace(headers.Get("X-Codex-Turn-Metadata")); turnMetadata != "" {
		if key := codexReasoningReplaySessionKeyFromTurnMetadata(turnMetadata); key != "" {
			return key
		}
	}
	if windowID := strings.TrimSpace(headerValueCaseInsensitive(headers, "X-Codex-Window-Id")); windowID != "" {
		return "window:" + windowID
	}
	for _, headerName := range []string{"Session_id", "session_id", "Session-Id"} {
		if value := strings.TrimSpace(headerValueCaseInsensitive(headers, headerName)); value != "" {
			return "session-id:" + value
		}
	}
	if conversationID := strings.TrimSpace(headerValueCaseInsensitive(headers, "Conversation_id")); conversationID != "" {
		return "conversation_id:" + conversationID
	}
	return ""
}

func codexReasoningReplaySessionKeyFromTurnMetadata(turnMetadata string) string {
	if promptCacheKey := strings.TrimSpace(gjson.Get(turnMetadata, "prompt_cache_key").String()); promptCacheKey != "" {
		return "prompt-cache:" + promptCacheKey
	}
	if windowID := strings.TrimSpace(gjson.Get(turnMetadata, "window_id").String()); windowID != "" {
		return "window:" + windowID
	}
	return ""
}

func codexInputHasValidReasoningEncryptedContent(body []byte) bool {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		if strings.TrimSpace(item.Get("type").String()) != "reasoning" {
			continue
		}
		encryptedContent := item.Get("encrypted_content")
		if encryptedContent.Type != gjson.String {
			continue
		}
		if _, err := signature.InspectGPTReasoningSignature(encryptedContent.String()); err == nil {
			return true
		}
	}
	return false
}

type codexReasoningReplayTurn struct {
	marked               bool
	assistantFingerprint string
	requestFingerprint   string
	callIDs              []string
	items                [][]byte
}

func insertCodexReasoningReplayTurns(body []byte, replayItems [][]byte) ([]byte, bool) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() || len(replayItems) == 0 {
		return body, false
	}
	inputItems := input.Array()
	turns := splitCodexReasoningReplayTurns(replayItems)
	insertions := make(map[int][][]byte)
	usedAnchorIndexes := make(map[int]bool)
	fallbackAnchorEnd := len(inputItems) - 1
	inserted := false
	for turnIndex := len(turns) - 1; turnIndex >= 0; turnIndex-- {
		turn := turns[turnIndex]
		if len(turn.items) == 0 {
			continue
		}
		if !turn.marked {
			items := filterCodexReasoningReplayItemsForInput(body, turn.items)
			if len(items) == 0 {
				continue
			}
			index := codexReasoningReplayInsertIndex(inputItems, items)
			items = codexAlignReasoningReplayToolCallIDs(inputItems, items)
			insertions[index] = append(items, insertions[index]...)
			inserted = true
			continue
		}

		anchorIndex, matched := codexReasoningReplayTurnAnchorIndex(inputItems, turn, fallbackAnchorEnd, usedAnchorIndexes)
		if !matched {
			continue
		}
		usedAnchorIndexes[anchorIndex] = true
		if turn.requestFingerprint == "" {
			fallbackAnchorEnd = anchorIndex - 1
		}
		items := filterCodexReasoningReplayTurnItems(inputItems, turn.items)
		if len(items) == 0 {
			continue
		}
		items = codexAlignReasoningReplayToolCallIDs(inputItems, items)
		insertions[anchorIndex] = append(items, insertions[anchorIndex]...)
		inserted = true
	}
	if !inserted {
		return body, false
	}

	items := make([]string, 0, len(inputItems)+len(replayItems))
	for index, inputItem := range inputItems {
		for _, replayItem := range insertions[index] {
			items = append(items, string(replayItem))
		}
		items = append(items, inputItem.Raw)
	}
	for _, replayItem := range insertions[len(inputItems)] {
		items = append(items, string(replayItem))
	}
	updated, err := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(items, ",")+"]"))
	if err != nil {
		return body, false
	}
	return updated, true
}

func splitCodexReasoningReplayTurns(items [][]byte) []codexReasoningReplayTurn {
	turns := make([]codexReasoningReplayTurn, 0)
	current := codexReasoningReplayTurn{}
	appendCurrent := func() {
		if len(current.items) > 0 {
			turns = append(turns, current)
		}
	}
	for _, item := range items {
		itemResult := gjson.ParseBytes(item)
		if strings.TrimSpace(itemResult.Get("type").String()) == internalcache.CodexReasoningReplayTurnType {
			appendCurrent()
			current = codexReasoningReplayTurn{
				marked:               true,
				assistantFingerprint: strings.TrimSpace(itemResult.Get("assistant_fingerprint").String()),
				requestFingerprint:   strings.TrimSpace(itemResult.Get("request_fingerprint").String()),
			}
			if callIDs := itemResult.Get("call_ids"); callIDs.IsArray() {
				for _, callIDResult := range callIDs.Array() {
					if callID := strings.TrimSpace(callIDResult.String()); callID != "" {
						current.callIDs = append(current.callIDs, callID)
					}
				}
			}
			continue
		}
		current.items = append(current.items, item)
	}
	appendCurrent()
	return turns
}

func codexReasoningReplayTurnAnchorIndex(inputItems []gjson.Result, turn codexReasoningReplayTurn, fallbackEnd int, used map[int]bool) (int, bool) {
	searchEnd := fallbackEnd
	if turn.requestFingerprint != "" {
		searchEnd = len(inputItems) - 1
	}
	if searchEnd >= len(inputItems) {
		searchEnd = len(inputItems) - 1
	}
	matchesRequestPrefix := func(index int) bool {
		return turn.requestFingerprint == "" || codexReplayInputPrefixFingerprint(inputItems, index) == turn.requestFingerprint
	}
	if len(turn.callIDs) > 0 {
		callIDs := make(map[string]bool)
		for _, callID := range turn.callIDs {
			for _, candidate := range codexReplayComparableCallIDs(callID) {
				callIDs[candidate] = true
			}
		}
		for index := searchEnd; index >= 0; index-- {
			if used[index] || !matchesRequestPrefix(index) {
				continue
			}
			itemType := strings.TrimSpace(inputItems[index].Get("type").String())
			if itemType != "function_call" && itemType != "custom_tool_call" && itemType != "function_call_output" && itemType != "custom_tool_call_output" {
				continue
			}
			for _, candidate := range codexReplayComparableCallIDs(inputItems[index].Get("call_id").String()) {
				if callIDs[candidate] {
					return index, true
				}
			}
		}
	}
	if turn.assistantFingerprint != "" {
		for index := searchEnd; index >= 0; index-- {
			if used[index] || !matchesRequestPrefix(index) {
				continue
			}
			if codexReplayAssistantMessageFingerprint(inputItems[index]) == turn.assistantFingerprint {
				return index, true
			}
		}
	}
	if len(turn.callIDs) == 0 && turn.assistantFingerprint == "" {
		return codexReasoningReplayInsertIndex(inputItems, turn.items), true
	}
	return 0, false
}

func filterCodexReasoningReplayTurnItems(inputItems []gjson.Result, items [][]byte) [][]byte {
	existingReasoning := make(map[string]bool)
	existingCalls := make(map[string]bool)
	existingOutputs := make(map[string]bool)
	for _, inputItem := range inputItems {
		itemType := strings.TrimSpace(inputItem.Get("type").String())
		switch itemType {
		case "reasoning":
			if encryptedContent := strings.TrimSpace(inputItem.Get("encrypted_content").String()); encryptedContent != "" {
				existingReasoning[encryptedContent] = true
			}
		case "function_call_output", "custom_tool_call_output":
			for _, candidate := range codexReplayComparableCallIDs(inputItem.Get("call_id").String()) {
				existingOutputs[candidate] = true
			}
		}
		for _, key := range codexReplayToolCallKeys(inputItem) {
			existingCalls[key] = true
		}
	}

	filtered := make([][]byte, 0, len(items))
	for _, item := range items {
		itemResult := gjson.ParseBytes(item)
		switch strings.TrimSpace(itemResult.Get("type").String()) {
		case "reasoning":
			if existingReasoning[strings.TrimSpace(itemResult.Get("encrypted_content").String())] {
				continue
			}
		case "function_call", "custom_tool_call":
			keys := codexReplayToolCallKeys(itemResult)
			if len(keys) == 0 || codexReplayAnyToolCallKeyExists(existingCalls, keys) {
				continue
			}
			hasMatchingOutput := false
			for _, candidate := range codexReplayComparableCallIDs(itemResult.Get("call_id").String()) {
				if existingOutputs[candidate] {
					hasMatchingOutput = true
					break
				}
			}
			if !hasMatchingOutput {
				continue
			}
			for _, key := range keys {
				existingCalls[key] = true
			}
		default:
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func codexReplayAssistantMessageFingerprint(item gjson.Result) string {
	itemType := strings.TrimSpace(item.Get("type").String())
	if itemType != "" && itemType != "message" {
		return ""
	}
	if !strings.EqualFold(strings.TrimSpace(item.Get("role").String()), "assistant") {
		return ""
	}
	content := item.Get("content")
	var builder strings.Builder
	if content.Type == gjson.String {
		builder.WriteString(content.String())
	} else if content.IsArray() {
		for _, part := range content.Array() {
			switch strings.TrimSpace(part.Get("type").String()) {
			case "input_text", "output_text":
				builder.WriteString(part.Get("text").String())
			case "refusal":
				builder.WriteString("\x00refusal\x00")
				builder.WriteString(part.Get("refusal").String())
			default:
				return ""
			}
		}
	} else {
		return ""
	}
	if builder.Len() == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:])
}

func codexReplayInputPrefixFingerprint(inputItems []gjson.Result, end int) string {
	if end < 0 || end > len(inputItems) {
		return ""
	}
	hasher := sha256.New()
	for index := 0; index < end; index++ {
		_, _ = hasher.Write([]byte("\x00item\x00"))
		_, _ = hasher.Write([]byte(inputItems[index].Raw))
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func filterCodexReasoningReplayItemsForInput(body []byte, items [][]byte) [][]byte {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return nil
	}

	hasInputReasoning := codexInputHasValidReasoningEncryptedContent(body)
	existingCalls := make(map[string]bool)
	existingOutputs := make(map[string]bool)
	for _, inputItem := range input.Array() {
		itemType := strings.TrimSpace(inputItem.Get("type").String())
		if itemType == "function_call_output" || itemType == "custom_tool_call_output" {
			callID := strings.TrimSpace(inputItem.Get("call_id").String())
			if callID != "" {
				for _, candidate := range codexReplayComparableCallIDs(callID) {
					existingOutputs[candidate] = true
				}
			}
		}
		for _, key := range codexReplayToolCallKeys(inputItem) {
			existingCalls[key] = true
		}
	}

	filtered := make([][]byte, 0, len(items))
	for _, item := range items {
		itemResult := gjson.ParseBytes(item)
		switch strings.TrimSpace(itemResult.Get("type").String()) {
		case "reasoning":
			if hasInputReasoning {
				continue
			}
		case "function_call", "custom_tool_call":
			keys := codexReplayToolCallKeys(itemResult)
			if len(keys) == 0 || codexReplayAnyToolCallKeyExists(existingCalls, keys) {
				continue
			}
			// Only inject if there is a matching output in the request
			hasMatchingOutput := false
			callID := strings.TrimSpace(itemResult.Get("call_id").String())
			if callID != "" {
				for _, candidate := range codexReplayComparableCallIDs(callID) {
					if existingOutputs[candidate] {
						hasMatchingOutput = true
						break
					}
				}
			}
			if !hasMatchingOutput {
				continue
			}
			for _, key := range keys {
				existingCalls[key] = true
			}
		default:
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func insertCodexReasoningReplayItems(body []byte, replayItems [][]byte) ([]byte, bool) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() || len(replayItems) == 0 {
		return body, false
	}
	inputItems := input.Array()
	insertIndex := codexReasoningReplayInsertIndex(inputItems, replayItems)
	replayItems = codexAlignReasoningReplayToolCallIDs(inputItems, replayItems)
	items := make([]string, 0, len(inputItems)+len(replayItems))
	for i, inputItem := range inputItems {
		if i == insertIndex {
			for _, replayItem := range replayItems {
				items = append(items, string(replayItem))
			}
		}
		items = append(items, inputItem.Raw)
	}
	if insertIndex == len(inputItems) {
		for _, replayItem := range replayItems {
			items = append(items, string(replayItem))
		}
	}
	updated, err := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(items, ",")+"]"))
	if err != nil {
		return body, false
	}
	return updated, true
}

func codexReasoningReplayInsertIndex(inputItems []gjson.Result, replayItems [][]byte) int {
	replayCallIDs := make(map[string]bool)
	for _, replayItem := range replayItems {
		itemResult := gjson.ParseBytes(replayItem)
		itemType := strings.TrimSpace(itemResult.Get("type").String())
		if itemType != "function_call" && itemType != "custom_tool_call" {
			continue
		}
		for _, callID := range codexReplayComparableCallIDs(itemResult.Get("call_id").String()) {
			replayCallIDs[callID] = true
		}
	}
	if len(replayCallIDs) > 0 {
		for index, inputItem := range inputItems {
			itemType := strings.TrimSpace(inputItem.Get("type").String())
			if itemType != "function_call_output" && itemType != "custom_tool_call_output" {
				continue
			}
			callID := strings.TrimSpace(inputItem.Get("call_id").String())
			if callID == "" || replayCallIDs[callID] {
				return index
			}
		}
	}
	for index := len(inputItems) - 1; index >= 0; index-- {
		inputItem := inputItems[index]
		if role, ok := codexReplayMessageRole(inputItem); ok && role == "assistant" {
			return index
		}
	}
	for index, inputItem := range inputItems {
		if shouldInsertCodexReasoningReplayBefore(inputItem) {
			return index
		}
	}
	return len(inputItems)
}

func codexAlignReasoningReplayToolCallIDs(inputItems []gjson.Result, replayItems [][]byte) [][]byte {
	outputCallIDs := codexReplayOutputCallIDs(inputItems)
	if len(outputCallIDs) == 0 {
		return replayItems
	}

	aligned := make([][]byte, 0, len(replayItems))
	for _, replayItem := range replayItems {
		itemResult := gjson.ParseBytes(replayItem)
		itemType := strings.TrimSpace(itemResult.Get("type").String())
		if itemType != "function_call" && itemType != "custom_tool_call" {
			aligned = append(aligned, replayItem)
			continue
		}

		callID := strings.TrimSpace(itemResult.Get("call_id").String())
		outputCallID := ""
		for _, candidate := range codexReplayComparableCallIDs(callID) {
			if value := outputCallIDs[candidate]; value != "" {
				outputCallID = value
				break
			}
		}
		if outputCallID == "" || outputCallID == callID {
			aligned = append(aligned, replayItem)
			continue
		}

		updated, err := sjson.SetBytes(replayItem, "call_id", outputCallID)
		if err != nil {
			aligned = append(aligned, replayItem)
			continue
		}
		aligned = append(aligned, updated)
	}
	return aligned
}

func codexReplayOutputCallIDs(inputItems []gjson.Result) map[string]string {
	outputCallIDs := make(map[string]string)
	for _, inputItem := range inputItems {
		itemType := strings.TrimSpace(inputItem.Get("type").String())
		if itemType != "function_call_output" && itemType != "custom_tool_call_output" {
			continue
		}
		callID := strings.TrimSpace(inputItem.Get("call_id").String())
		if callID == "" {
			continue
		}
		for _, candidate := range codexReplayComparableCallIDs(callID) {
			outputCallIDs[candidate] = callID
		}
	}
	return outputCallIDs
}

func shouldInsertCodexReasoningReplayBefore(item gjson.Result) bool {
	role, ok := codexReplayMessageRole(item)
	if !ok {
		return true
	}
	switch role {
	case "developer", "system":
		return false
	default:
		return true
	}
}

func codexReplayMessageRole(item gjson.Result) (string, bool) {
	itemType := strings.TrimSpace(item.Get("type").String())
	role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
	if role == "" || (itemType != "" && itemType != "message") {
		return "", false
	}
	return role, true
}

func codexReplayToolCallKeys(item gjson.Result) []string {
	itemType := strings.TrimSpace(item.Get("type").String())
	if itemType != "function_call" && itemType != "custom_tool_call" {
		return nil
	}
	callIDs := codexReplayComparableCallIDs(item.Get("call_id").String())
	if len(callIDs) == 0 {
		return nil
	}
	keys := make([]string, 0, len(callIDs))
	for _, callID := range callIDs {
		keys = append(keys, itemType+":"+callID)
	}
	return keys
}

func codexReplayAnyToolCallKeyExists(existing map[string]bool, keys []string) bool {
	for _, key := range keys {
		if existing[key] {
			return true
		}
	}
	return false
}

func codexReplayComparableCallIDs(callID string) []string {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return nil
	}

	claudeVisibleCallID := shortenCodexReplayCallIDIfNeeded(util.SanitizeClaudeToolID(callID))
	if claudeVisibleCallID == "" || claudeVisibleCallID == callID {
		return []string{callID}
	}
	return []string{callID, claudeVisibleCallID}
}

func shortenCodexReplayCallIDIfNeeded(id string) string {
	const limit = 64
	if len(id) <= limit {
		return id
	}

	sum := sha256.Sum256([]byte(id))
	suffix := "_" + hex.EncodeToString(sum[:8])
	prefixLen := limit - len(suffix)
	if prefixLen <= 0 {
		return suffix[len(suffix)-limit:]
	}
	return id[:prefixLen] + suffix
}

func cacheCodexReasoningReplayFromCompleted(scope codexReasoningReplayScope, completedData []byte) {
	if !scope.valid() {
		return
	}
	output := gjson.GetBytes(completedData, "response.output")
	if !output.IsArray() {
		return
	}
	replayItems := make([][]byte, 0, len(output.Array()))
	callIDs := make([]string, 0)
	assistantFingerprint := ""
	for _, item := range output.Array() {
		switch strings.TrimSpace(item.Get("type").String()) {
		case "reasoning":
			replayItems = append(replayItems, []byte(item.Raw))
		case "function_call", "custom_tool_call":
			replayItems = append(replayItems, []byte(item.Raw))
			if callID := strings.TrimSpace(item.Get("call_id").String()); callID != "" {
				callIDs = append(callIDs, callID)
			}
		case "message":
			if fingerprint := codexReplayAssistantMessageFingerprint(item); fingerprint != "" {
				assistantFingerprint = fingerprint
			}
		}
	}
	if len(replayItems) == 0 {
		return
	}

	hasher := sha256.New()
	_, _ = hasher.Write([]byte(scope.requestFingerprint))
	_, _ = hasher.Write([]byte("\x00assistant\x00" + assistantFingerprint))
	for _, callID := range callIDs {
		_, _ = hasher.Write([]byte("\x00call\x00" + callID))
	}
	for _, item := range replayItems {
		_, _ = hasher.Write([]byte("\x00item\x00"))
		_, _ = hasher.Write(item)
	}
	marker := []byte(`{"type":"` + internalcache.CodexReasoningReplayTurnType + `"}`)
	marker, _ = sjson.SetBytes(marker, "id", hex.EncodeToString(hasher.Sum(nil)))
	if assistantFingerprint != "" {
		marker, _ = sjson.SetBytes(marker, "assistant_fingerprint", assistantFingerprint)
	}
	if scope.requestFingerprint != "" {
		marker, _ = sjson.SetBytes(marker, "request_fingerprint", scope.requestFingerprint)
	}
	for _, callID := range callIDs {
		marker, _ = sjson.SetBytes(marker, "call_ids.-1", callID)
	}
	items := make([][]byte, 0, len(replayItems)+1)
	items = append(items, marker)
	items = append(items, replayItems...)
	internalcache.AppendCodexReasoningReplayItemsBestEffort(context.Background(), scope.modelName, scope.sessionKey, items)
}

func clearCodexReasoningReplayOnInvalidSignature(ctx context.Context, scope codexReasoningReplayScope, statusCode int, body []byte) error {
	if !scope.valid() {
		return nil
	}
	if codexStatusErrorIsThinkingSignatureInvalid(statusCode, body) {
		return internalcache.DeleteCodexReasoningReplayItemRequired(ctx, scope.modelName, scope.sessionKey)
	}
	return nil
}

func codexStatusErrorIsThinkingSignatureInvalid(statusCode int, body []byte) bool {
	code, _, ok := codexStatusErrorClassification(statusCode, body)
	return ok && code == "thinking_signature_invalid"
}

// PrepareRequest injects Codex credentials into the outgoing HTTP request.
func (e *CodexExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	apiKey, _ := codexCreds(auth)
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	if strings.TrimSpace(apiKey) != "" {
		if err := sealCodexAuthenticationHeaders(req, auth, apiKey); err != nil {
			return fmt.Errorf("codex executor: apply Agent Identity auth: %w", err)
		}
		return nil
	}
	if err := applyAgentIdentityRequestHeaders(req, auth); err != nil {
		return fmt.Errorf("codex executor: apply Agent Identity auth: %w", err)
	}
	return nil
}

// HttpRequest injects Codex credentials into the request and executes it.
func (e *CodexExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("codex executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	var err error
	auth, err = e.resolveEgressAuth(ctx, auth)
	if err != nil {
		return nil, err
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient, err := e.outboundHTTPClient(ctx, auth, 0, 0, true)
	if err != nil {
		return nil, err
	}
	return httpClient.Do(httpReq)
}

func (e *CodexExecutor) openCodexResponse(ctx context.Context, from sdktranslator.Format, url string, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, originalPayloadSource []byte, body []byte, apiKey string, stream bool, httpClient *http.Client) (*http.Response, codexIdentityConfuseState, error) {
	var identityState codexIdentityConfuseState
	httpReq, upstreamBody, identityState, err := e.cacheHelper(ctx, from, url, auth, req, originalPayloadSource, body)
	if err != nil {
		return nil, identityState, err
	}
	if err = applyCodexHeaders(httpReq, auth, apiKey, stream, e.cfg); err != nil {
		return nil, identityState, err
	}
	applyCodexIdentityConfuseHeaders(httpReq.Header, &identityState)
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
		Body:      upstreamBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		err = codexResponseHeaderTimeoutError(err)
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, identityState, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	return httpResp, identityState, nil
}

func (e *CodexExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	auth, err = e.resolveEgressAuth(ctx, auth)
	if err != nil {
		return resp, err
	}
	if opts.Alt == "responses/compact" {
		return e.executeCompact(ctx, auth, req, opts)
	}
	if isCodexOpenAIImageRequest(opts) {
		return e.executeOpenAIImage(ctx, auth, req, opts)
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
	originalTranslated, body := translateCodexRequestPair(from, to, baseModel, originalPayload, req.Payload, false)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body, _ = sjson.SetBytes(body, "model", baseModel)
	body, _ = sjson.SetBytes(body, "stream", true)
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body, _ = sjson.DeleteBytes(body, "safety_identifier")
	body, _ = sjson.DeleteBytes(body, "stream_options")
	body, _ = sjson.DeleteBytes(body, "prepend_instructions")
	body = normalizeCodexInstructions(body)
	if e.cfg == nil || e.cfg.DisableImageGeneration == config.DisableImageGenerationOff {
		body = ensureImageGenerationTool(body, baseModel, auth, opts.Headers)
	}
	body = sanitizeOpenAIResponsesReasoningItems(ctx, "codex executor", body)
	body = normalizeCodexParallelToolCalls(body, opts.Headers)
	body, replayScope, errReplay := applyCodexReasoningReplayCacheRequired(ctx, from, req, opts, body)
	if errReplay != nil {
		return resp, errReplay
	}
	reporter.SetTranslatedReasoningEffort(body, to.String())

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	var identityState codexIdentityConfuseState
	httpReq, upstreamBody, identityState, err := e.cacheHelper(ctx, from, url, auth, req, originalPayloadSource, body, opts.Headers)
	if err != nil {
		return resp, err
	}
	if err = applyCodexHeaders(httpReq, auth, apiKey, true, e.cfg); err != nil {
		return resp, err
	}
	if err = applyModelHeaderOverridesForRequest(httpReq, auth, apiKey, baseModel); err != nil {
		return resp, err
	}
	applyCodexIdentityConfuseHeaders(httpReq.Header, &identityState)
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
		Body:      upstreamBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
	httpClient, err := e.outboundHTTPClient(ctx, auth, 0, codexResponseHeaderTimeout(e.cfg), true)
	if err != nil {
		return resp, err
	}
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		err = codexResponseHeaderTimeoutError(err)
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		b = applyCodexIdentityConfuseResponsePayload(b, identityState)
		if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, httpResp.StatusCode, b); errClearReplay != nil {
			return resp, errClearReplay
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = newCodexStatusErrForResponse(httpResp, b)
		return resp, err
	}
	data, errRead := io.ReadAll(httpResp.Body)
	upstreamData := applyCodexIdentityConfuseResponsePayload(data, identityState)
	helps.AppendAPIResponseChunk(ctx, e.cfg, upstreamData)

	lines := bytes.Split(upstreamData, []byte("\n"))
	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	for _, line := range lines {
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}

		eventData := bytes.TrimSpace(line[5:])
		eventType := gjson.GetBytes(eventData, "type").String()

		if streamErr, terminalBody, ok := codexTerminalFailureErr(eventData); ok {
			streamErr.requestAuthScheme = codexResponseRequestAuthScheme(httpResp)
			if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, streamErr.StatusCode(), terminalBody); errClearReplay != nil {
				return resp, errClearReplay
			}
			err = streamErr
			return resp, err
		}

		if eventType == "response.output_item.done" {
			itemResult := gjson.GetBytes(eventData, "item")
			if !itemResult.Exists() || itemResult.Type != gjson.JSON {
				continue
			}
			outputIndexResult := gjson.GetBytes(eventData, "output_index")
			if outputIndexResult.Exists() {
				outputItemsByIndex[outputIndexResult.Int()] = []byte(itemResult.Raw)
			} else {
				outputItemsFallback = append(outputItemsFallback, []byte(itemResult.Raw))
			}
			continue
		}

		if eventType != "response.completed" && eventType != "response.done" && eventType != "response.incomplete" {
			continue
		}

		if eventType == "response.done" {
			eventData, _ = sjson.SetRawBytes(eventData, "type", []byte(`"response.completed"`))
		}

		if detail, ok := helps.ParseCodexUsage(eventData); ok {
			reporter.Publish(ctx, detail)
		}
		publishCodexImageToolUsage(ctx, reporter, body, eventData)

		completedData := eventData
		outputResult := gjson.GetBytes(completedData, "response.output")
		shouldPatchOutput := (!outputResult.Exists() || !outputResult.IsArray() || len(outputResult.Array()) == 0) && (len(outputItemsByIndex) > 0 || len(outputItemsFallback) > 0)
		if shouldPatchOutput {
			completedDataPatched := completedData
			completedDataPatched, _ = sjson.SetRawBytes(completedDataPatched, "response.output", []byte(`[]`))

			indexes := make([]int64, 0, len(outputItemsByIndex))
			for idx := range outputItemsByIndex {
				indexes = append(indexes, idx)
			}
			sort.Slice(indexes, func(i, j int) bool {
				return indexes[i] < indexes[j]
			})
			for _, idx := range indexes {
				completedDataPatched, _ = sjson.SetRawBytes(completedDataPatched, "response.output.-1", outputItemsByIndex[idx])
			}
			for _, item := range outputItemsFallback {
				completedDataPatched, _ = sjson.SetRawBytes(completedDataPatched, "response.output.-1", item)
			}
			completedData = completedDataPatched
		}
		if eventType == "response.completed" {
			cacheCodexReasoningReplayFromCompleted(replayScope, completedData)
		}

		var param any
		clientCompletedData := applyCodexIdentityExposeResponsePayload(completedData, identityState)
		out := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, originalPayload, body, clientCompletedData, &param)
		resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
		return resp, nil
	}
	if errRead != nil {
		if errCtx := ctx.Err(); errCtx != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errCtx)
			err = errCtx
			return resp, err
		}
		if e.usesStrictEgress(auth) {
			err = e.wrapStrictEgressTransportErrorForAuth(auth, errRead, "response read")
			helps.RecordAPIResponseError(ctx, e.cfg, err)
			return resp, err
		}
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
	}
	err = newCodexIncompleteStreamError()
	return resp, err
}

func (e *CodexExecutor) executeCompact(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("openai-response")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated, body := translateCodexRequestPair(from, to, baseModel, originalPayload, req.Payload, false)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body, _ = sjson.SetBytes(body, "model", baseModel)
	body, _ = sjson.DeleteBytes(body, "stream")
	body = normalizeCodexInstructions(body)
	body = sanitizeOpenAIResponsesReasoningItems(ctx, "codex executor", body)
	body = normalizeCodexParallelToolCalls(body, opts.Headers)
	reporter.SetTranslatedReasoningEffort(body, to.String())

	url := strings.TrimSuffix(baseURL, "/") + "/responses/compact"
	var identityState codexIdentityConfuseState
	httpReq, upstreamBody, identityState, err := e.cacheHelper(ctx, from, url, auth, req, originalPayloadSource, body, opts.Headers)
	if err != nil {
		return resp, err
	}
	if err = applyCodexHeaders(httpReq, auth, apiKey, false, e.cfg); err != nil {
		return resp, err
	}
	if err = applyModelHeaderOverridesForRequest(httpReq, auth, apiKey, baseModel); err != nil {
		return resp, err
	}
	applyCodexIdentityConfuseHeaders(httpReq.Header, &identityState)
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
		Body:      upstreamBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
	httpClient, err := e.outboundHTTPClient(ctx, auth, 0, codexCompactResponseHeaderTimeout, false)
	if err != nil {
		return resp, err
	}
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		b = applyCodexIdentityConfuseResponsePayload(b, identityState)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = newCodexStatusErrForResponse(httpResp, b)
		return resp, err
	}
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	upstreamData := applyCodexIdentityConfuseResponsePayload(data, identityState)
	helps.AppendAPIResponseChunk(ctx, e.cfg, upstreamData)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(upstreamData))
	reporter.EnsurePublished(ctx)
	var param any
	clientData := applyCodexIdentityExposeResponsePayload(upstreamData, identityState)
	out := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, originalPayload, body, clientData, &param)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

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
	originalTranslated, body := translateCodexRequestPair(from, to, baseModel, originalPayload, req.Payload, true)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body, _ = sjson.DeleteBytes(body, "safety_identifier")
	body, _ = sjson.DeleteBytes(body, "stream_options")
	body, _ = sjson.SetBytes(body, "model", baseModel)
	body = normalizeCodexInstructions(body)
	if e.cfg == nil || e.cfg.DisableImageGeneration == config.DisableImageGenerationOff {
		body = ensureImageGenerationTool(body, baseModel, auth, opts.Headers)
	}
	body = sanitizeOpenAIResponsesReasoningItems(ctx, "codex executor", body)
	body = normalizeCodexParallelToolCalls(body, opts.Headers)
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
	httpResp, identityState, err := e.openCodexResponse(ctx, from, url, auth, req, originalPayloadSource, body, apiKey, true, httpClient)
	if err != nil {
		return nil, err
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
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
		if codexStatusErrorIsThinkingSignatureInvalid(httpResp.StatusCode, data) {
			if retryBody, okDrop := dropOpenAIResponsesReasoningItemsWithEncryptedContent(ctx, "codex executor", body, "retry after upstream rejected thinking signature"); okDrop {
				body = retryBody
				httpResp, identityState, err = e.openCodexResponse(ctx, from, url, auth, req, originalPayloadSource, body, apiKey, true, httpClient)
				if err != nil {
					return nil, err
				}
				if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
					data, readErr = io.ReadAll(httpResp.Body)
					if errClose := httpResp.Body.Close(); errClose != nil {
						log.Errorf("codex executor: close response body error after invalid signature retry: %v", errClose)
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
					helps.LogWithRequestID(ctx).Debugf("request error after invalid signature retry, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
					err = newCodexStatusErrForResponse(httpResp, data)
					return nil, err
				}
			} else {
				helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
				err = newCodexStatusErrForResponse(httpResp, data)
				return nil, err
			}
		} else {
			helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
			err = newCodexStatusErrForResponse(httpResp, data)
			return nil, err
		}
	}
	streamHeaders := httpResp.Header.Clone()
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		streamBody := httpResp.Body
		defer close(out)
		defer func() {
			if errClose := streamBody.Close(); errClose != nil {
				log.Errorf("codex executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(streamBody)
		scanner.Buffer(nil, 52_428_800) // 50MB
		var param any
		outputItemsByIndex := make(map[int64][]byte)
		var outputItemsFallback [][]byte
		emittedPayload := false
		retriedInvalidSignature := false

		// idleTimer 关闭上游连接以中止卡住的 SSE 流。
		// 上游可能接受连接但不发送任何数据，导致 scanner.Scan() 永久阻塞。
		// 超时后关闭 body 让 Scan 返回，并发出明确的 idle timeout 错误。
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
				eventType := gjson.GetBytes(data, "type").String()
				if streamErr, terminalBody, ok := codexTerminalFailureErr(data); ok {
					streamErr.requestAuthScheme = codexResponseRequestAuthScheme(httpResp)
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
						retryBody, okDrop := dropOpenAIResponsesReasoningItemsWithEncryptedContent(ctx, "codex executor", body, "retry after upstream rejected thinking signature")
						if okDrop {
							retriedInvalidSignature = true
							body = retryBody
							outputItemsByIndex = make(map[int64][]byte)
							outputItemsFallback = nil
							stopIdleWatch()
							if errClose := streamBody.Close(); errClose != nil {
								log.Errorf("codex executor: close response body error before invalid signature retry: %v", errClose)
							}
							retryResponse, retryIdentityState, retryOpenErr := e.openCodexResponse(ctx, from, url, auth, req, originalPayloadSource, body, apiKey, true, httpClient)
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
								if errClose := retryResponse.Body.Close(); errClose != nil {
									log.Errorf("codex executor: close response body error after invalid signature retry: %v", errClose)
								}
								if readErr != nil {
									helps.RecordAPIResponseError(ctx, e.cfg, readErr)
									reporter.PublishFailure(ctx, readErr)
									select {
									case out <- cliproxyexecutor.StreamChunk{Err: readErr}:
									case <-ctx.Done():
									}
									return
								}
								retryData = applyCodexIdentityConfuseResponsePayload(retryData, retryIdentityState)
								helps.AppendAPIResponseChunk(ctx, e.cfg, retryData)
								helps.LogWithRequestID(ctx).Debugf("request error after invalid signature retry, error status: %d, error message: %s", retryResponse.StatusCode, helps.SummarizeErrorBody(retryResponse.Header.Get("Content-Type"), retryData))
								retryErr := newCodexStatusErrForResponse(retryResponse, retryData)
								helps.RecordAPIResponseError(ctx, e.cfg, retryErr)
								reporter.PublishFailure(ctx, retryErr)
								select {
								case out <- cliproxyexecutor.StreamChunk{Err: retryErr}:
								case <-ctx.Done():
								}
								return
							}
							streamBody = retryResponse.Body
							identityState = retryIdentityState
							scanner = bufio.NewScanner(streamBody)
							scanner.Buffer(nil, 52_428_800) // 50MB
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
					if gjson.GetBytes(data, "type").String() == "response.done" {
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
			chunks := sdktranslator.TranslateStream(ctx, to, responseFormat, req.Model, originalPayload, body, translatedLine, &param)
			if !codexSendStreamChunks(ctx, out, chunks) {
				return
			}
			if len(chunks) > 0 {
				emittedPayload = true
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
		} else {
			var streamErr error = newCodexIncompleteStreamError()
			helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
			reporter.PublishFailure(ctx, streamErr)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
			case <-ctx.Done():
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: streamHeaders, Chunks: out}, nil
}

func (e *CodexExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("codex")
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	body, err := thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	body, _ = sjson.SetBytes(body, "model", baseModel)
	body, _ = sjson.DeleteBytes(body, "previous_response_id")
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body, _ = sjson.DeleteBytes(body, "safety_identifier")
	body, _ = sjson.DeleteBytes(body, "stream_options")
	body, _ = sjson.SetBytes(body, "stream", false)
	body = normalizeCodexInstructions(body)

	enc, err := tokenizerForCodexModel(baseModel)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("codex executor: tokenizer init failed: %w", err)
	}

	count, err := countCodexInputTokens(enc, body)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("codex executor: token counting failed: %w", err)
	}

	usageJSON := fmt.Sprintf(`{"response":{"usage":{"input_tokens":%d,"output_tokens":0,"total_tokens":%d}}}`, count, count)
	translated := sdktranslator.TranslateTokenCount(ctx, to, responseFormat, count, []byte(usageJSON))
	return cliproxyexecutor.Response{Payload: translated}, nil
}

func tokenizerForCodexModel(model string) (tokenizer.Codec, error) {
	sanitized := strings.ToLower(strings.TrimSpace(model))
	switch {
	case sanitized == "":
		return tokenizer.Get(tokenizer.Cl100kBase)
	case strings.HasPrefix(sanitized, "gpt-5"):
		return tokenizer.ForModel(tokenizer.GPT5)
	case strings.HasPrefix(sanitized, "gpt-4.1"):
		return tokenizer.ForModel(tokenizer.GPT41)
	case strings.HasPrefix(sanitized, "gpt-4o"):
		return tokenizer.ForModel(tokenizer.GPT4o)
	case strings.HasPrefix(sanitized, "gpt-4"):
		return tokenizer.ForModel(tokenizer.GPT4)
	case strings.HasPrefix(sanitized, "gpt-3.5"), strings.HasPrefix(sanitized, "gpt-3"):
		return tokenizer.ForModel(tokenizer.GPT35Turbo)
	default:
		return tokenizer.Get(tokenizer.Cl100kBase)
	}
}

func countCodexInputTokens(enc tokenizer.Codec, body []byte) (int64, error) {
	if enc == nil {
		return 0, fmt.Errorf("encoder is nil")
	}
	if len(body) == 0 {
		return 0, nil
	}

	root := gjson.ParseBytes(body)
	var segments []string

	if inst := strings.TrimSpace(root.Get("instructions").String()); inst != "" {
		segments = append(segments, inst)
	}

	inputItems := root.Get("input")
	if inputItems.IsArray() {
		arr := inputItems.Array()
		for i := range arr {
			item := arr[i]
			switch item.Get("type").String() {
			case "message":
				content := item.Get("content")
				if content.IsArray() {
					parts := content.Array()
					for j := range parts {
						part := parts[j]
						if text := strings.TrimSpace(part.Get("text").String()); text != "" {
							segments = append(segments, text)
						}
					}
				}
			case "function_call":
				if name := strings.TrimSpace(item.Get("name").String()); name != "" {
					segments = append(segments, name)
				}
				if args := strings.TrimSpace(item.Get("arguments").String()); args != "" {
					segments = append(segments, args)
				}
			case "function_call_output":
				if out := strings.TrimSpace(item.Get("output").String()); out != "" {
					segments = append(segments, out)
				}
			default:
				if text := strings.TrimSpace(item.Get("text").String()); text != "" {
					segments = append(segments, text)
				}
			}
		}
	}

	tools := root.Get("tools")
	if tools.IsArray() {
		tarr := tools.Array()
		for i := range tarr {
			tool := tarr[i]
			if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
				segments = append(segments, name)
			}
			if desc := strings.TrimSpace(tool.Get("description").String()); desc != "" {
				segments = append(segments, desc)
			}
			if params := tool.Get("parameters"); params.Exists() {
				val := params.Raw
				if params.Type == gjson.String {
					val = params.String()
				}
				if trimmed := strings.TrimSpace(val); trimmed != "" {
					segments = append(segments, trimmed)
				}
			}
		}
	}

	textFormat := root.Get("text.format")
	if textFormat.Exists() {
		if name := strings.TrimSpace(textFormat.Get("name").String()); name != "" {
			segments = append(segments, name)
		}
		if schema := textFormat.Get("schema"); schema.Exists() {
			val := schema.Raw
			if schema.Type == gjson.String {
				val = schema.String()
			}
			if trimmed := strings.TrimSpace(val); trimmed != "" {
				segments = append(segments, trimmed)
			}
		}
	}

	text := strings.Join(segments, "\n")
	if text == "" {
		return 0, nil
	}

	count, err := enc.Count(text)
	if err != nil {
		return 0, err
	}
	return int64(count), nil
}

func (e *CodexExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("codex executor: refresh called")
	var err error
	auth, err = e.resolveEgressAuth(ctx, auth)
	if err != nil {
		return nil, err
	}
	if !e.usesStrictEgress(auth) {
		if refreshed, handled, err := e.refreshViaHome(ctx, auth); handled {
			return refreshed, err
		}
	}
	if auth == nil {
		return nil, statusErr{code: 500, msg: "codex executor: auth is nil"}
	}
	var refreshToken string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["refresh_token"].(string); ok && v != "" {
			refreshToken = v
		}
	}
	if refreshToken == "" {
		return auth, nil
	}
	boundAccountID := codexAccountIDFromAuth(auth)
	httpClient, err := e.outboundHTTPClient(ctx, auth, 0, 0, false)
	if err != nil {
		return nil, err
	}
	td, err := e.refreshCodexTokens(ctx, httpClient, refreshToken)
	if err != nil {
		return nil, err
	}
	refreshedAccountID := strings.TrimSpace(td.AccountID)
	if e.usesStrictEgress(auth) && boundAccountID != "" && refreshedAccountID != "" && refreshedAccountID != boundAccountID {
		return nil, egress.RuntimeError(fmt.Errorf("%w: refreshed Codex account_id does not match the bound identity", egress.ErrIdentityMismatch))
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["id_token"] = td.IDToken
	// Re-extract plan_type from the refreshed ID token so plan upgrades/downgrades
	// are reflected without restarting or waiting for a full file re-synthesis.
	if td.IDToken != "" {
		if claims, errParse := codexauth.ParseJWTToken(td.IDToken); errParse == nil && claims != nil {
			if pt := strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType); pt != "" {
				if auth.Attributes == nil {
					auth.Attributes = make(map[string]string)
				}
				auth.Attributes["plan_type"] = pt
			}
		}
	}
	auth.Metadata["access_token"] = td.AccessToken
	if td.RefreshToken != "" {
		auth.Metadata["refresh_token"] = td.RefreshToken
	}
	if td.AccountID != "" {
		auth.Metadata["account_id"] = td.AccountID
	}
	auth.Metadata["email"] = td.Email
	// Use unified key in files
	auth.Metadata["expired"] = td.Expire
	auth.Metadata["type"] = "codex"
	now := time.Now().Format(time.RFC3339)
	auth.Metadata["last_refresh"] = now
	return auth, nil
}

// ProbeQuotaRecovery performs a lightweight quota check for Codex auths by calling
// the ChatGPT usage endpoint. It implements the cliproxyauth.QuotaRecoveryProber interface.
func (e *CodexExecutor) ProbeQuotaRecovery(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.QuotaProbeResult, error) {
	var err error
	auth, err = e.resolveEgressAuth(ctx, auth)
	if err != nil {
		return nil, err
	}
	if auth == nil {
		return nil, fmt.Errorf("codex executor: auth is nil")
	}
	accountID := codexAccountIDFromAuth(auth)
	if accountID == "" {
		return nil, fmt.Errorf("codex executor: missing account_id")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/backend-api/wham/usage", nil)
	if err != nil {
		return nil, err
	}
	apiKey, _ := codexCreds(auth)
	if err = applyCodexHeaders(req, auth, apiKey, false, e.cfg); err != nil {
		return nil, err
	}

	httpClient, err := e.outboundHTTPClient(ctx, auth, 0, 0, false)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close quota probe body error: %v", errClose)
		}
	}()

	body, err := readUpstreamResponseBody(e.Identifier(), resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, newCodexStatusErrForResponse(resp, body)
	}
	return parseCodexQuotaProbe(body), nil
}

// CodexRateLimitResetCredit is one earned, account-scoped usage reset credit.
type CodexRateLimitResetCredit struct {
	ID          string  `json:"id"`
	ResetType   string  `json:"reset_type"`
	Status      string  `json:"status"`
	GrantedAt   string  `json:"granted_at"`
	ExpiresAt   *string `json:"expires_at"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
}

// CodexRateLimitResetCredits is the account's reset-credit inventory.
type CodexRateLimitResetCredits struct {
	Credits        []CodexRateLimitResetCredit `json:"credits"`
	AvailableCount int64                       `json:"available_count"`
}

// CodexRateLimitResetResult reports the outcome of one idempotent redemption.
type CodexRateLimitResetResult struct {
	Code         string `json:"code"`
	WindowsReset int64  `json:"windows_reset"`
}

type codexRateLimitResetRequest struct {
	RedeemRequestID string `json:"redeem_request_id"`
	CreditID        string `json:"credit_id"`
}

// ListRateLimitResetCredits returns the reset credits currently attached to a Codex account.
func (e *CodexExecutor) ListRateLimitResetCredits(ctx context.Context, auth *cliproxyauth.Auth) (*CodexRateLimitResetCredits, error) {
	body, err := e.codexResetCreditsRequest(ctx, auth, http.MethodGet, codexResetCreditsURL, nil)
	if err != nil {
		return nil, err
	}
	return decodeCodexRateLimitResetCredits(body)
}

// ConsumeRateLimitResetCredit redeems one selected credit using an idempotent request ID.
func (e *CodexExecutor) ConsumeRateLimitResetCredit(ctx context.Context, auth *cliproxyauth.Auth, creditID, redeemRequestID string) (*CodexRateLimitResetResult, error) {
	payload, err := json.Marshal(codexRateLimitResetRequest{
		RedeemRequestID: redeemRequestID,
		CreditID:        creditID,
	})
	if err != nil {
		return nil, fmt.Errorf("codex reset credits: encode consume request: %w", err)
	}
	body, err := e.codexResetCreditsRequest(ctx, auth, http.MethodPost, codexResetCreditsConsumeURL, payload)
	if err != nil {
		return nil, err
	}
	return decodeCodexRateLimitResetResult(body)
}

func decodeCodexRateLimitResetCredits(body []byte) (*CodexRateLimitResetCredits, error) {
	var response struct {
		Credits        *[]CodexRateLimitResetCredit `json:"credits"`
		AvailableCount *int64                       `json:"available_count"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("codex reset credits: decode list response: %w", err)
	}
	if response.Credits == nil || response.AvailableCount == nil || *response.AvailableCount < 0 {
		return nil, errors.New("codex reset credits: invalid list response")
	}
	for _, credit := range *response.Credits {
		if !validCodexRateLimitResetCredit(credit) {
			return nil, errors.New("codex reset credits: invalid credit entry")
		}
	}
	return &CodexRateLimitResetCredits{Credits: *response.Credits, AvailableCount: *response.AvailableCount}, nil
}

func validCodexRateLimitResetCredit(credit CodexRateLimitResetCredit) bool {
	return strings.TrimSpace(credit.ID) != "" &&
		strings.TrimSpace(credit.ResetType) != "" &&
		strings.TrimSpace(credit.Status) != "" &&
		strings.TrimSpace(credit.GrantedAt) != ""
}

func decodeCodexRateLimitResetResult(body []byte) (*CodexRateLimitResetResult, error) {
	var response struct {
		Code         *string `json:"code"`
		WindowsReset *int64  `json:"windows_reset"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("codex reset credits: decode consume response: %w", err)
	}
	if response.Code == nil || response.WindowsReset == nil || *response.WindowsReset < 0 {
		return nil, errors.New("codex reset credits: invalid consume response")
	}
	code := strings.ToLower(strings.TrimSpace(*response.Code))
	if code != "reset" && code != "nothing_to_reset" && code != "no_credit" && code != "already_redeemed" {
		return nil, errors.New("codex reset credits: unknown consume outcome")
	}
	return &CodexRateLimitResetResult{Code: code, WindowsReset: *response.WindowsReset}, nil
}

func (e *CodexExecutor) codexResetCreditsRequest(ctx context.Context, auth *cliproxyauth.Auth, method, targetURL string, payload []byte) ([]byte, error) {
	resolvedAuth, err := e.resolveEgressAuth(ctx, auth)
	if err != nil {
		return nil, err
	}
	if resolvedAuth == nil || !strings.EqualFold(strings.TrimSpace(resolvedAuth.Provider), "codex") {
		return nil, statusErr{code: http.StatusBadRequest, msg: "codex reset credits require Codex OAuth auth"}
	}
	if resolvedAuth.AuthKind() != cliproxyauth.AuthKindOAuth {
		return nil, statusErr{code: http.StatusBadRequest, msg: "codex reset credits require Codex OAuth auth"}
	}
	token, _ := resolvedAuth.Metadata["access_token"].(string)
	if strings.TrimSpace(token) == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "codex reset credits require an access token"}
	}
	accountID := codexAccountIDFromAuth(resolvedAuth)
	if accountID == "" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "codex reset credits require a ChatGPT account ID"}
	}
	req, err := http.NewRequestWithContext(ctx, method, targetURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	if err = applyCodexHeaders(req, resolvedAuth, token, false, e.cfg); err != nil {
		return nil, err
	}
	applyCodexResetCreditSecurityHeaders(req, token, accountID)
	client, err := e.outboundHTTPClient(ctx, resolvedAuth, codexResetCreditsTimeout, 0, false)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, e.wrapStrictEgressTransportErrorForAuth(resolvedAuth, err, "reset credits request")
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Errorf("codex executor: close reset-credit body error: %v", closeErr)
		}
	}()
	body, err := readUpstreamResponseBody(e.Identifier(), resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, newCodexStatusErrForResponse(resp, body)
	}
	return body, nil
}

func applyCodexResetCreditSecurityHeaders(req *http.Request, token, accountID string) {
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("Chatgpt-Account-Id", strings.TrimSpace(accountID))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Host = req.URL.Host
	req.Header.Del("Host")
}

func parseCodexQuotaProbe(body []byte) *cliproxyauth.QuotaProbeResult {
	if len(body) == 0 {
		return nil
	}

	rateLimit := gjson.GetBytes(body, "rate_limit")
	if !rateLimit.Exists() {
		return nil
	}

	allowed := rateLimit.Get("allowed")
	limitReached := rateLimit.Get("limit_reached")
	if limitReached.Exists() && limitReached.Bool() {
		return &cliproxyauth.QuotaProbeResult{
			Recovered:     false,
			NextRecoverAt: codexQuotaProbeNextRecoverAt(rateLimit, false),
		}
	}

	hasWindowUsage := false
	hasExhaustedWindow := false
	nextRecoverAt := time.Time{}
	for _, path := range []string{"primary_window", "secondary_window"} {
		window := rateLimit.Get(path)
		if !window.Exists() {
			continue
		}
		usedPercent := window.Get("used_percent")
		windowExhausted := false
		if usedPercent.Exists() {
			hasWindowUsage = true
			windowExhausted = usedPercent.Float() >= 100
			if windowExhausted {
				hasExhaustedWindow = true
			}
		}
		if !windowExhausted {
			continue
		}
		if resetAt := codexQuotaWindowResetAt(window, time.Now()); !resetAt.IsZero() {
			if nextRecoverAt.IsZero() || resetAt.Before(nextRecoverAt) {
				nextRecoverAt = resetAt
			}
		}
	}

	if !hasExhaustedWindow {
		if allowed.Exists() {
			return &cliproxyauth.QuotaProbeResult{
				Recovered:     allowed.Bool(),
				NextRecoverAt: codexQuotaProbeNextRecoverAt(rateLimit, false),
			}
		}
		if hasWindowUsage {
			return &cliproxyauth.QuotaProbeResult{Recovered: true}
		}
	}

	return &cliproxyauth.QuotaProbeResult{
		Recovered:     false,
		NextRecoverAt: nextRecoverAt,
	}
}

func codexQuotaProbeNextRecoverAt(rateLimit gjson.Result, exhaustedOnly bool) time.Time {
	nextRecoverAt := time.Time{}
	for _, path := range []string{"primary_window", "secondary_window"} {
		window := rateLimit.Get(path)
		if !window.Exists() {
			continue
		}
		if exhaustedOnly {
			usedPercent := window.Get("used_percent")
			if usedPercent.Exists() && usedPercent.Float() < 100 {
				continue
			}
		}
		if resetAt := codexQuotaWindowResetAt(window, time.Now()); !resetAt.IsZero() {
			if nextRecoverAt.IsZero() || resetAt.Before(nextRecoverAt) {
				nextRecoverAt = resetAt
			}
		}
	}
	return nextRecoverAt
}

func codexQuotaWindowResetAt(window gjson.Result, now time.Time) time.Time {
	if !window.Exists() {
		return time.Time{}
	}
	if resetAt := window.Get("reset_at").Int(); resetAt > 0 {
		resetAtTime := time.Unix(resetAt, 0)
		if resetAtTime.After(now) {
			return resetAtTime
		}
	}
	if afterSeconds := window.Get("reset_after_seconds").Int(); afterSeconds > 0 {
		return now.Add(time.Duration(afterSeconds) * time.Second)
	}
	return time.Time{}
}

type codexIdentityConfuseState struct {
	enabled                bool
	authID                 string
	originalPromptCacheKey string
	promptCacheKey         string
	turnIDs                []codexIdentityReplacement
}

type codexIdentityReplacement struct {
	original string
	confused string
}

func (e *CodexExecutor) cacheHelper(ctx context.Context, from sdktranslator.Format, url string, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, userPayload []byte, rawJSON []byte, headerSets ...http.Header) (*http.Request, []byte, codexIdentityConfuseState, error) {
	var headers http.Header
	if len(headerSets) > 0 {
		headers = headerSets[0]
	}
	var cache helps.CodexCache
	if sourceFormatEqual(from, sdktranslator.FormatClaude) {
		modelName := strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String())
		if modelName == "" {
			modelName = thinking.ParseSuffix(req.Model).ModelName
		}
		cached, ok, errCache := helps.ClaudeCodePromptCache(ctx, modelName, req.Payload, headers)
		if errCache != nil {
			return nil, nil, codexIdentityConfuseState{}, errCache
		}
		if ok {
			cache = cached
		}
	} else if sourceFormatEqual(from, sdktranslator.FormatOpenAIResponse) {
		promptCacheKey := gjson.GetBytes(req.Payload, "prompt_cache_key")
		if promptCacheKey.Exists() {
			cache.ID = promptCacheKey.String()
		}
	} else if sourceFormatEqual(from, sdktranslator.FormatOpenAI) {
		if apiKey := strings.TrimSpace(helps.APIKeyFromContext(ctx)); apiKey != "" {
			cache.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:prompt-cache:"+apiKey)).String()
		}
	}

	if cache.ID != "" {
		rawJSON, _ = sjson.SetBytes(rawJSON, "prompt_cache_key", cache.ID)
	}
	rawJSON = applyCodexFastModeServiceTier(auth, rawJSON)
	rawJSON = helps.SanitizeCodexInputItemIDs(rawJSON)
	var identityState codexIdentityConfuseState
	rawJSON, identityState = applyCodexIdentityConfuseBody(e.cfg, auth, userPayload, rawJSON)
	if identityState.promptCacheKey != "" {
		cache.ID = identityState.promptCacheKey
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(rawJSON))
	if err != nil {
		return nil, nil, codexIdentityConfuseState{}, err
	}
	if strings.TrimSpace(cache.ID) != "" {
		setCodexSessionHeaderCasePreserved(httpReq.Header, "Session_id", cache.ID)
	}
	return httpReq, rawJSON, identityState, nil
}

func applyCodexFastModeServiceTier(auth *cliproxyauth.Auth, rawJSON []byte) []byte {
	if len(rawJSON) == 0 || auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return rawJSON
	}

	normalized := rawJSON
	if gjson.GetBytes(normalized, "service_tier").Exists() {
		if updated, errDelete := sjson.DeleteBytes(normalized, "service_tier"); errDelete == nil {
			normalized = updated
		}
	}
	if !codexFastModeEnabled(auth) {
		return normalized
	}
	updated, errSet := sjson.SetBytes(normalized, "service_tier", codexFastModeServiceTier)
	if errSet != nil {
		return normalized
	}
	return updated
}

func codexFastModeEnabled(auth *cliproxyauth.Auth) bool {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return false
	}
	if auth.Attributes != nil {
		if parsed, ok := parseBoolString(auth.Attributes["codex_fast_mode"]); ok {
			return parsed
		}
	}
	if auth.Metadata == nil {
		return false
	}
	switch raw := auth.Metadata["codex_fast_mode"].(type) {
	case bool:
		return raw
	case string:
		parsed, ok := parseBoolString(raw)
		return ok && parsed
	default:
		return false
	}
}

func parseBoolString(value string) (bool, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false, false
	}
	parsed, errParse := strconv.ParseBool(trimmed)
	if errParse != nil {
		return false, false
	}
	return parsed, true
}

func applyCodexIdentityConfuseBody(cfg *config.Config, auth *cliproxyauth.Auth, userPayload []byte, rawJSON []byte) ([]byte, codexIdentityConfuseState) {
	if !codexIdentityConfuseEnabled(cfg) || auth == nil || strings.TrimSpace(auth.ID) == "" || len(rawJSON) == 0 {
		return rawJSON, codexIdentityConfuseState{}
	}

	state := codexIdentityConfuseState{enabled: true, authID: strings.TrimSpace(auth.ID)}
	if promptCacheKey := strings.TrimSpace(gjson.GetBytes(userPayload, "prompt_cache_key").String()); promptCacheKey != "" {
		state.originalPromptCacheKey = promptCacheKey
		state.promptCacheKey = codexIdentityConfuseUUID(auth.ID, "prompt-cache", promptCacheKey)
		rawJSON, _ = sjson.SetBytes(rawJSON, "prompt_cache_key", state.promptCacheKey)
	}
	if installationID := strings.TrimSpace(gjson.GetBytes(userPayload, "client_metadata.x-codex-installation-id").String()); installationID != "" {
		rawJSON, _ = sjson.SetBytes(rawJSON, "client_metadata.x-codex-installation-id", codexIdentityConfuseUUID(auth.ID, "installation", installationID))
	}
	if turnMetadata := strings.TrimSpace(gjson.GetBytes(rawJSON, "client_metadata.x-codex-turn-metadata").String()); turnMetadata != "" {
		rawJSON, _ = sjson.SetBytes(rawJSON, "client_metadata.x-codex-turn-metadata", applyCodexTurnMetadataIdentityConfuse(turnMetadata, &state))
	}
	if state.promptCacheKey != "" {
		if windowID := strings.TrimSpace(gjson.GetBytes(rawJSON, "client_metadata.x-codex-window-id").String()); windowID != "" {
			rawJSON, _ = sjson.SetBytes(rawJSON, "client_metadata.x-codex-window-id", state.promptCacheKey+":0")
		}
	}

	return rawJSON, state
}

func applyCodexIdentityConfuseHeaders(headers http.Header, state *codexIdentityConfuseState) {
	if headers == nil {
		return
	}
	if state == nil || !state.enabled {
		deleteDeprecatedCodexConversationHeader(headers)
		return
	}

	if rawTurnMetadata := strings.TrimSpace(headers.Get("X-Codex-Turn-Metadata")); rawTurnMetadata != "" {
		headers.Set("X-Codex-Turn-Metadata", applyCodexTurnMetadataIdentityConfuse(rawTurnMetadata, state))
	}
	if state.promptCacheKey == "" {
		return
	}

	setCodexSessionHeaderCasePreserved(headers, "Session_id", state.promptCacheKey)
	if headerValueCaseInsensitive(headers, "Conversation_id") != "" {
		setHeaderCasePreserved(headers, "Conversation_id", state.promptCacheKey)
	}
	headers.Set("X-Client-Request-Id", state.promptCacheKey)
	headers.Set("Thread-Id", state.promptCacheKey)
	headers.Set("X-Codex-Window-Id", state.promptCacheKey+":0")
}

func applyCodexTurnMetadataIdentityConfuse(rawTurnMetadata string, state *codexIdentityConfuseState) string {
	updatedTurnMetadata := rawTurnMetadata
	if state == nil || !state.enabled {
		return updatedTurnMetadata
	}
	if state.promptCacheKey != "" && gjson.Get(rawTurnMetadata, "prompt_cache_key").Exists() {
		updatedTurnMetadata, _ = sjson.Set(updatedTurnMetadata, "prompt_cache_key", state.promptCacheKey)
	} else if state.promptCacheKey != "" && state.originalPromptCacheKey != "" {
		updatedTurnMetadata = strings.ReplaceAll(updatedTurnMetadata, state.originalPromptCacheKey, state.promptCacheKey)
	}
	if turnID := strings.TrimSpace(gjson.Get(rawTurnMetadata, "turn_id").String()); turnID != "" {
		updatedTurnMetadata, _ = sjson.Set(updatedTurnMetadata, "turn_id", state.confuseTurnID(turnID))
	}
	if state.promptCacheKey != "" && gjson.Get(rawTurnMetadata, "window_id").Exists() {
		updatedTurnMetadata, _ = sjson.Set(updatedTurnMetadata, "window_id", state.promptCacheKey+":0")
	}
	return updatedTurnMetadata
}

func applyCodexIdentityConfuseResponsePayload(payload []byte, state codexIdentityConfuseState) []byte {
	payload = replaceCodexIdentityResponsePayload(payload, state.originalPromptCacheKey, state.promptCacheKey)
	for _, turnID := range state.turnIDs {
		payload = replaceCodexIdentityResponsePayload(payload, turnID.original, turnID.confused)
	}
	return payload
}

func applyCodexIdentityExposeResponsePayload(payload []byte, state codexIdentityConfuseState) []byte {
	payload = replaceCodexIdentityResponsePayload(payload, state.promptCacheKey, state.originalPromptCacheKey)
	for _, turnID := range state.turnIDs {
		payload = replaceCodexIdentityResponsePayload(payload, turnID.confused, turnID.original)
	}
	return payload
}

func (state *codexIdentityConfuseState) confuseTurnID(turnID string) string {
	turnID = strings.TrimSpace(turnID)
	if state == nil || !state.enabled || strings.TrimSpace(state.authID) == "" || turnID == "" {
		return turnID
	}
	for _, replacement := range state.turnIDs {
		if replacement.original == turnID || replacement.confused == turnID {
			return replacement.confused
		}
	}
	confusedTurnID := codexIdentityConfuseUUID(state.authID, "turn", turnID)
	state.turnIDs = append(state.turnIDs, codexIdentityReplacement{original: turnID, confused: confusedTurnID})
	return confusedTurnID
}

func replaceCodexIdentityResponsePayload(payload []byte, from string, to string) []byte {
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)
	if len(payload) == 0 || from == "" || to == "" || from == to || !bytes.Contains(payload, []byte(from)) {
		return payload
	}
	return bytes.ReplaceAll(payload, []byte(from), []byte(to))
}

func codexIdentityConfuseEnabled(cfg *config.Config) bool {
	if cfg == nil || !cfg.Codex.IdentityConfuse {
		return false
	}
	strategy := strings.ToLower(strings.TrimSpace(cfg.Routing.Strategy))
	return cfg.Routing.SessionAffinity || strategy == "fill-first" || strategy == "fillfirst" || strategy == "ff"
}

func codexIdentityConfuseUUID(authID string, kind string, value string) string {
	name := strings.Join([]string{"cli-proxy-api", "codex", "identity-confuse", kind, strings.TrimSpace(authID), strings.TrimSpace(value)}, ":")
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)).String()
}

func applyCodexHeaders(r *http.Request, auth *cliproxyauth.Auth, token string, stream bool, cfg *config.Config) error {
	var ginHeaders http.Header
	if ginCtx, ok := r.Context().Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		ginHeaders = ginCtx.Request.Header
	}
	return applyCodexHeadersFromSources(r, auth, token, stream, cfg, ginHeaders)
}

// applyModelHeaderOverrides forces models.json config.override_header onto upstream headers.
func applyModelHeaderOverrides(headers http.Header, modelName string) {
	if headers == nil {
		return
	}
	overrides := registry.ModelOverrideHeaders(modelName)
	if len(overrides) == 0 {
		return
	}
	for key, value := range overrides {
		headers.Set(key, value)
	}
	if strings.Contains(headers.Get("User-Agent"), "Mac OS") && codexSessionHeaderValue(headers) == "" {
		headers.Set("Session_id", uuid.NewString())
	}
}

func applyModelHeaderOverridesForRequest(req *http.Request, auth *cliproxyauth.Auth, token, modelName string) error {
	applyModelHeaderOverrides(req.Header, modelName)
	return sealCodexAuthenticationHeaders(req, auth, token)
}

// applyCodexDirectImageHeaders sets Codex upstream headers for direct /images/* calls.
// Downstream client User-Agent values are not forwarded to reduce Cloudflare 1010 blocks.
func applyCodexDirectImageHeaders(r *http.Request, auth *cliproxyauth.Auth, token string, stream bool, cfg *config.Config) error {
	var ginHeaders http.Header
	if ginCtx, ok := r.Context().Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		ginHeaders = ginCtx.Request.Header.Clone()
		ginHeaders.Del("User-Agent")
	}
	return applyCodexHeadersFromSources(r, auth, token, stream, cfg, ginHeaders)
}

func applyCodexHeadersFromSources(r *http.Request, auth *cliproxyauth.Auth, token string, stream bool, cfg *config.Config, ginHeaders http.Header) error {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)

	if ginHeaders != nil && ginHeaders.Get("X-Codex-Beta-Features") != "" {
		r.Header.Set("X-Codex-Beta-Features", ginHeaders.Get("X-Codex-Beta-Features"))
	}
	misc.EnsureHeader(r.Header, ginHeaders, "Version", "")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Codex-Turn-Metadata", "")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Client-Request-Id", "")
	fp, fingerprintEnabled := codexIdentityFingerprint(cfg)
	cfgUserAgent, _ := codexHeaderDefaults(cfg, auth)
	if fingerprintEnabled {
		applyCodexIdentityFingerprintHeaders(r.Header, fp, false)
	} else {
		ensureHeaderWithConfigPrecedence(r.Header, ginHeaders, "User-Agent", cfgUserAgent, codexUserAgent)
	}

	if stream {
		r.Header.Set("Accept", "text/event-stream")
	} else {
		r.Header.Set("Accept", "application/json")
	}
	r.Header.Set("Connection", "Keep-Alive")

	isAPIKey := false
	if auth != nil && auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["api_key"]); v != "" {
			isAPIKey = true
		}
	}
	if originator := strings.TrimSpace(ginHeaders.Get("Originator")); originator != "" {
		r.Header.Set("Originator", originator)
	} else if !isAPIKey {
		if fingerprintEnabled {
			r.Header.Set("Originator", fp.Originator)
		} else {
			r.Header.Set("Originator", codexOriginator)
		}
	}
	if !isAPIKey && auth != nil {
		if accountID := codexAccountIDFromAuth(auth); accountID != "" {
			r.Header.Set("Chatgpt-Account-Id", accountID)
		}
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(r, attrs)
	if fingerprintEnabled {
		applyCodexIdentityFingerprintHeaders(r.Header, fp, false)
		if strings.TrimSpace(ginHeaders.Get("Originator")) == "" && !isAPIKey {
			r.Header.Set("Originator", fp.Originator)
		}
	}
	deleteDeprecatedCodexConversationHeader(r.Header)
	if err := sealCodexAuthenticationHeaders(r, auth, token); err != nil {
		return fmt.Errorf("codex executor: apply Agent Identity auth: %w", err)
	}
	return nil
}

func newCodexStatusErr(statusCode int, body []byte) statusErr {
	errCode := statusCode
	if isCodexModelCapacityError(body) || isCodexUsageLimitError(body) {
		errCode = http.StatusTooManyRequests
	}
	body = classifyCodexStatusError(errCode, body)
	err := statusErr{code: errCode, msg: string(body), errorCode: strings.TrimSpace(gjson.GetBytes(body, "error.code").String())}
	if retryAfter := parseCodexRetryAfter(errCode, body, time.Now()); retryAfter != nil {
		err.retryAfter = retryAfter
	}
	return err
}

func newCodexStatusErrForResponse(resp *http.Response, body []byte) statusErr {
	err := newCodexStatusErr(resp.StatusCode, body)
	err.requestAuthScheme = codexResponseRequestAuthScheme(resp)
	return err
}

func codexResponseRequestAuthScheme(resp *http.Response) string {
	if resp == nil || resp.Request == nil {
		return ""
	}
	return codexAuthorizationScheme(resp.Request.Header.Get("Authorization"))
}

func codexAuthorizationScheme(authorization string) string {
	scheme, _, _ := strings.Cut(strings.TrimSpace(authorization), " ")
	return scheme
}

func classifyCodexStatusError(statusCode int, body []byte) []byte {
	code, errType, ok := codexStatusErrorClassification(statusCode, body)
	if !ok {
		return body
	}
	message := gjson.GetBytes(body, "error.message").String()
	if message == "" {
		message = gjson.GetBytes(body, "message").String()
	}
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	if message == "" {
		message = http.StatusText(statusCode)
	}
	out := []byte(`{"error":{}}`)
	out, _ = sjson.SetBytes(out, "error.message", message)
	out, _ = sjson.SetBytes(out, "error.type", errType)
	out, _ = sjson.SetBytes(out, "error.code", code)
	return out
}

func codexStatusErrorClassification(statusCode int, body []byte) (code string, errType string, ok bool) {
	errorMessage := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.message").String()))
	if errorMessage == "" {
		errorMessage = strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "message").String()))
	}
	lower := strings.ToLower(strings.TrimSpace(string(body)))
	upstreamCode := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.code").String()))
	upstreamType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.type").String()))
	isInvalidRequest := upstreamType == "" || upstreamType == "invalid_request_error"

	switch {
	case upstreamCode == "invalid_task_id":
		return "invalid_task_id", "authentication_error", true
	case statusCode == http.StatusRequestEntityTooLarge ||
		upstreamCode == "context_length_exceeded" ||
		upstreamCode == "context_too_large" ||
		isInvalidRequest && (codexErrorTextIndicatesContextLength(errorMessage) || codexErrorTextIndicatesContextLength(lower)):
		return "context_too_large", "invalid_request_error", true
	case upstreamCode == "thinking_signature_invalid" ||
		upstreamCode == "invalid_encrypted_content" ||
		strings.Contains(lower, "invalid signature in thinking block") ||
		strings.Contains(lower, "invalid_encrypted_content") ||
		strings.Contains(lower, "encrypted content") &&
			(strings.Contains(lower, "could not be verified") ||
				strings.Contains(lower, "could not be decrypted") ||
				strings.Contains(lower, "could not be parsed")):
		return "thinking_signature_invalid", "invalid_request_error", true
	case upstreamCode == "previous_response_not_found" || strings.Contains(lower, "previous_response_not_found") || strings.Contains(lower, "previous_response_id") && strings.Contains(lower, "not found"):
		return "previous_response_not_found", "invalid_request_error", true
	case statusCode == http.StatusUnauthorized || upstreamType == "authentication_error" || upstreamCode == "invalid_api_key" || strings.Contains(lower, "invalid or expired token") || strings.Contains(lower, "refresh_token_reused"):
		return "auth_unavailable", "authentication_error", true
	default:
		return "", "", false
	}
}

func normalizeCodexInstructions(body []byte) []byte {
	instructions := gjson.GetBytes(body, "instructions")
	if !instructions.Exists() || instructions.Type == gjson.Null {
		body, _ = sjson.SetBytes(body, "instructions", "")
	}
	return body
}

var imageGenToolJSON = []byte(`{"type":"image_generation","output_format":"png"}`)
var imageGenToolArrayJSON = []byte(`[{"type":"image_generation","output_format":"png"}]`)

func isCodexFreePlanAuth(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Attributes["plan_type"]), "free")
}

func isImageGenerationFunctionTool(tool gjson.Result) bool {
	switch tool.Get("type").String() {
	case "function":
		return tool.Get("name").String() == "image_gen.imagegen"
	case "namespace":
		if tool.Get("name").String() != "image_gen" {
			return false
		}
		tools := tool.Get("tools")
		if !tools.IsArray() {
			return false
		}
		for _, nestedTool := range tools.Array() {
			if nestedTool.Get("type").String() == "function" && nestedTool.Get("name").String() == "imagegen" {
				return true
			}
		}
	}
	return false
}

func isCodexResponsesLiteRequest(body []byte, headers http.Header) bool {
	if strings.EqualFold(strings.TrimSpace(headers.Get(codexResponsesLiteHeader)), "true") {
		return true
	}
	// Codex Desktop mirrors websocket-only request headers into client_metadata.
	value := gjson.GetBytes(body, codexResponsesLiteMetadata)
	if !value.Exists() {
		return false
	}
	return value.Type == gjson.True || value.Type == gjson.String && strings.EqualFold(strings.TrimSpace(value.String()), "true")
}

func ensureImageGenerationTool(body []byte, baseModel string, auth *cliproxyauth.Auth, headers http.Header) []byte {
	if isCodexResponsesLiteRequest(body, headers) {
		return body
	}
	if strings.HasSuffix(baseModel, "spark") {
		return body
	}
	if isCodexFreePlanAuth(auth) {
		return body
	}

	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() {
		body, _ = sjson.SetRawBytes(body, "tools", imageGenToolArrayJSON)
		return body
	}
	for _, t := range tools.Array() {
		if t.Get("type").String() == "image_generation" || isImageGenerationFunctionTool(t) {
			return body
		}
	}
	body, _ = sjson.SetRawBytes(body, "tools.-1", imageGenToolJSON)
	return body
}

func normalizeCodexParallelToolCalls(body []byte, headers http.Header) []byte {
	if isCodexResponsesLiteRequest(body, headers) {
		body, _ = sjson.SetBytes(body, "parallel_tool_calls", false)
		return body
	}
	return normalizeCodexParallelToolCallsForTools(body)
}

func normalizeCodexParallelToolCallsForTools(body []byte) []byte {
	if !gjson.GetBytes(body, "parallel_tool_calls").Exists() {
		return body
	}

	tools := gjson.GetBytes(body, "tools")
	hasTools := tools.Exists() && tools.IsArray() && len(tools.Array()) > 0
	if hasTools {
		return body
	}

	body, _ = sjson.DeleteBytes(body, "parallel_tool_calls")
	return body
}

func publishCodexImageToolUsage(ctx context.Context, reporter *helps.UsageReporter, body []byte, completedData []byte) {
	detail, ok := helps.ParseCodexImageToolUsage(completedData)
	if !ok {
		return
	}
	reporter.EnsurePublished(ctx)
	reporter.PublishAdditionalModel(ctx, codexImageGenerationToolModel(body), detail)
}

func codexImageGenerationToolModel(body []byte) string {
	tools := gjson.GetBytes(body, "tools")
	if tools.IsArray() {
		for _, tool := range tools.Array() {
			if tool.Get("type").String() != "image_generation" {
				continue
			}
			if model := strings.TrimSpace(tool.Get("model").String()); model != "" {
				return model
			}
			break
		}
	}
	return codexDefaultImageToolModel
}

func isCodexModelCapacityError(errorBody []byte) bool {
	if len(errorBody) == 0 {
		return false
	}
	candidates := []string{
		gjson.GetBytes(errorBody, "error.message").String(),
		gjson.GetBytes(errorBody, "message").String(),
		string(errorBody),
	}
	for _, candidate := range candidates {
		lower := strings.ToLower(strings.TrimSpace(candidate))
		if lower == "" {
			continue
		}
		if strings.Contains(lower, "selected model is at capacity") ||
			strings.Contains(lower, "model is at capacity. please try a different model") {
			return true
		}
	}
	return false
}

// isCodexUsageLimitError reports whether the error body represents a Codex
// quota/plan-limit exhaustion (error.type == "usage_limit_reached"). This is the
// signal Codex emits when a credential's usage quota is depleted, and it carries
// reset timing (resets_at/resets_in_seconds) parsed by parseCodexRetryAfter.
// Transient per-minute rate limits (rate_limit_error/rate_limit_exceeded) are
// intentionally excluded, as they should be retried rather than cooled down.
func isCodexUsageLimitError(errorBody []byte) bool {
	if len(errorBody) == 0 {
		return false
	}
	candidates := []string{
		gjson.GetBytes(errorBody, "error.type").String(),
		gjson.GetBytes(errorBody, "type").String(),
	}
	for _, candidate := range candidates {
		if strings.EqualFold(strings.TrimSpace(candidate), "usage_limit_reached") {
			return true
		}
	}
	return false
}

func parseCodexRetryAfter(statusCode int, errorBody []byte, now time.Time) *time.Duration {
	if statusCode != http.StatusTooManyRequests || len(errorBody) == 0 {
		return nil
	}
	if strings.TrimSpace(gjson.GetBytes(errorBody, "error.type").String()) != "usage_limit_reached" {
		return nil
	}
	if resetsAt := gjson.GetBytes(errorBody, "error.resets_at").Int(); resetsAt > 0 {
		resetAtTime := time.Unix(resetsAt, 0)
		if resetAtTime.After(now) {
			retryAfter := resetAtTime.Sub(now)
			return &retryAfter
		}
	}
	if resetsInSeconds := gjson.GetBytes(errorBody, "error.resets_in_seconds").Int(); resetsInSeconds > 0 {
		retryAfter := time.Duration(resetsInSeconds) * time.Second
		return &retryAfter
	}
	return nil
}

func codexCreds(a *cliproxyauth.Auth) (apiKey, baseURL string) {
	if a == nil {
		return "", ""
	}
	if a.Attributes != nil {
		apiKey = a.Attributes["api_key"]
		baseURL = a.Attributes["base_url"]
	}
	if apiKey == "" && a.Metadata != nil {
		if v, ok := a.Metadata["access_token"].(string); ok {
			apiKey = v
		}
	}
	return
}

func (e *CodexExecutor) resolveCodexConfig(auth *cliproxyauth.Auth) *config.CodexKey {
	if auth == nil || e.cfg == nil {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range e.cfg.CodexKey {
		entry := &e.cfg.CodexKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" && attrBase != "" {
			if strings.EqualFold(cfgKey, attrKey) && strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	if attrKey != "" {
		for i := range e.cfg.CodexKey {
			entry := &e.cfg.CodexKey[i]
			if strings.EqualFold(strings.TrimSpace(entry.APIKey), attrKey) {
				return entry
			}
		}
	}
	return nil
}
