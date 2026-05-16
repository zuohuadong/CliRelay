package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tiktoken-go/tokenizer"
)

type upstreamRequestLog = helps.UpstreamRequestLog

type legacyUsageReporter struct {
	inner *helps.UsageReporter
}

func newUsageReporter(ctx context.Context, provider, model string, auth *cliproxyauth.Auth) *legacyUsageReporter {
	return &legacyUsageReporter{inner: helps.NewUsageReporter(ctx, provider, model, auth)}
}

func (r *legacyUsageReporter) publish(ctx context.Context, detail usage.Detail) {
	if r != nil && r.inner != nil {
		r.inner.Publish(ctx, detail)
	}
}

func (r *legacyUsageReporter) publishWithContent(ctx context.Context, detail usage.Detail, _, _ string) {
	r.publish(ctx, detail)
}

func (r *legacyUsageReporter) publishFailure(ctx context.Context) {
	if r != nil && r.inner != nil {
		r.inner.PublishFailure(ctx)
	}
}

func (r *legacyUsageReporter) publishFailureWithContent(ctx context.Context, _, output string) {
	if r == nil || r.inner == nil {
		return
	}
	if output == "" {
		r.inner.PublishFailure(ctx)
		return
	}
	r.inner.PublishFailure(ctx, fmt.Errorf("%s", output))
}

func (r *legacyUsageReporter) trackFailure(ctx context.Context, errPtr *error) {
	if r != nil && r.inner != nil {
		r.inner.TrackFailure(ctx, errPtr)
	}
}

func (r *legacyUsageReporter) ensurePublished(ctx context.Context) {
	if r != nil && r.inner != nil {
		r.inner.EnsurePublished(ctx)
	}
}

func (r *legacyUsageReporter) setInputContent(string) {}

func (r *legacyUsageReporter) appendOutputChunk([]byte) {}

func newProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	return helps.NewProxyAwareHTTPClient(ctx, cfg, auth, timeout)
}

func payloadRequestedModel(opts cliproxyexecutor.Options, fallback string) string {
	return helps.PayloadRequestedModel(opts, fallback)
}

func applyPayloadConfigWithRoot(cfg *config.Config, model, protocol, root string, payload, original []byte, requestedModel string) []byte {
	return helps.ApplyPayloadConfigWithRoot(cfg, model, protocol, root, payload, original, requestedModel, "")
}

func recordAPIRequest(ctx context.Context, cfg *config.Config, info upstreamRequestLog) {
	helps.RecordAPIRequest(ctx, cfg, info)
}

func recordAPIResponseMetadata(ctx context.Context, cfg *config.Config, status int, headers http.Header) {
	helps.RecordAPIResponseMetadata(ctx, cfg, status, headers)
}

func recordAPIResponseError(ctx context.Context, cfg *config.Config, err error) {
	helps.RecordAPIResponseError(ctx, cfg, err)
}

func appendAPIResponseChunk(ctx context.Context, cfg *config.Config, chunk []byte) {
	helps.AppendAPIResponseChunk(ctx, cfg, chunk)
}

func readUpstreamErrorBody(_ string, r io.Reader) []byte {
	b, _ := io.ReadAll(r)
	return b
}

func readUpstreamResponseBody(_ string, r io.Reader) ([]byte, error) {
	return io.ReadAll(r)
}

func logWithRequestID(ctx context.Context) interface{ Debugf(string, ...any); Warnf(string, ...any) } {
	return helps.LogWithRequestID(ctx)
}

func summarizeErrorBody(contentType string, body []byte) string {
	return helps.SummarizeErrorBody(contentType, body)
}

func parseOpenAIUsage(data []byte) usage.Detail {
	return helps.ParseOpenAIUsage(data)
}

func parseOpenAIStreamUsage(line []byte) (usage.Detail, bool) {
	return helps.ParseOpenAIStreamUsage(line)
}

func tokenizerForModel(model string) (tokenizer.Codec, error) {
	return helps.TokenizerForModel(model)
}

func countOpenAIChatTokens(enc tokenizer.Codec, payload []byte) (int64, error) {
	return helps.CountOpenAIChatTokens(enc, payload)
}

func buildOpenAIUsageJSON(count int64) []byte {
	return helps.BuildOpenAIUsageJSON(count)
}
