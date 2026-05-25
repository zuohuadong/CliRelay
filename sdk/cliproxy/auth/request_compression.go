package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const requestCompressionDisabledMetadataKey = "request_compression_disabled"

func (m *Manager) maybeCompressRequest(ctx context.Context, provider, routeModel, upstreamModel string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Request, cliproxyexecutor.Options, error) {
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	policy, reason := requestPolicyCompressionDecision(cfg, opts, routeModel, provider, upstreamModel)
	if policy == nil {
		return req, opts, nil
	}
	compression := policy.OverLimit.Compression
	targetBytes := compression.TargetRequestBytes
	if targetBytes <= 0 {
		targetBytes = 512000
	}
	if int64(len(req.Payload)) <= targetBytes {
		return req, opts, nil
	}

	compressed, errCompress := m.compressRequestWithPolicy(ctx, *policy, reason, req.Payload, opts.SourceFormat.String())
	if errCompress != nil {
		action := strings.ToLower(strings.TrimSpace(compression.UnavailableAction))
		if action == "reject" {
			return req, opts, &Error{
				Code:       "request_compression_failed",
				Message:    errCompress.Error(),
				HTTPStatus: http.StatusServiceUnavailable,
				Retryable:  true,
			}
		}
		logEntryWithRequestID(ctx).WithError(errCompress).Warnf("request compression skipped policy=%s provider=%s model=%s reason=%s", policy.Name, provider, upstreamModel, reason)
		return req, opts, nil
	}
	if len(compressed) == 0 || !json.Valid(compressed) {
		logEntryWithRequestID(ctx).Warnf("request compression skipped policy=%s provider=%s model=%s: compressor returned invalid JSON", policy.Name, provider, upstreamModel)
		return req, opts, nil
	}
	if int64(len(compressed)) > targetBytes {
		logEntryWithRequestID(ctx).Warnf("request compression skipped policy=%s provider=%s model=%s: compressed request has %d bytes, target is %d", policy.Name, provider, upstreamModel, len(compressed), targetBytes)
		return req, opts, nil
	}

	nextReq := req
	nextReq.Payload = compressed
	nextOpts := opts
	nextOpts.OriginalRequest = compressed
	nextOpts.Metadata = cloneMetadata(opts.Metadata)
	nextOpts.Metadata[cliproxyexecutor.RequestBytesMetadataKey] = len(compressed)
	nextOpts.Metadata["request_compression_policy"] = strings.TrimSpace(policy.Name)
	logEntryWithRequestID(ctx).Infof("request compression applied policy=%s provider=%s model=%s original_bytes=%d compressed_bytes=%d target_bytes=%d", policy.Name, provider, upstreamModel, len(req.Payload), len(compressed), targetBytes)
	return nextReq, nextOpts, nil
}

func (m *Manager) compressRequestWithPolicy(ctx context.Context, policy internalconfig.RequestPolicy, reason string, payload []byte, sourceFormat string) ([]byte, error) {
	compression := policy.OverLimit.Compression
	provider := strings.ToLower(strings.TrimSpace(compression.Provider))
	model := strings.TrimSpace(compression.Model)
	if provider == "" || model == "" {
		return nil, fmt.Errorf("request compression policy %s is missing compressor provider or model", policy.Name)
	}
	compressorPayload, errBuild := buildRequestCompressionPayload(model, payload, sourceFormat, compression.TargetRequestBytes, compression.Prompt)
	if errBuild != nil {
		return nil, errBuild
	}
	meta := map[string]any{
		cliproxyexecutor.RequestedModelMetadataKey: model,
		cliproxyexecutor.RequestBytesMetadataKey:   len(compressorPayload),
		requestCompressionDisabledMetadataKey:      true,
		"request_compression_source_policy":        strings.TrimSpace(policy.Name),
		"request_compression_trigger_reason":       reason,
	}
	req := cliproxyexecutor.Request{
		Model:   model,
		Payload: compressorPayload,
	}
	opts := cliproxyexecutor.Options{
		Stream:          false,
		OriginalRequest: compressorPayload,
		SourceFormat:    sdktranslator.FromString("openai-response"),
		Metadata:        meta,
	}
	resp, errExec := m.Execute(ctx, []string{provider}, req, opts)
	if errExec != nil {
		return nil, fmt.Errorf("request compression policy %s compressor %s/%s failed: %w", policy.Name, provider, model, errExec)
	}
	text := extractCompressionText(resp.Payload)
	if text == "" {
		return nil, fmt.Errorf("request compression policy %s compressor %s/%s returned empty text", policy.Name, provider, model)
	}
	compact := stripMarkdownJSON(text)
	if !json.Valid([]byte(compact)) {
		return nil, fmt.Errorf("request compression policy %s compressor %s/%s returned non-JSON text", policy.Name, provider, model)
	}
	return []byte(compact), nil
}

func buildRequestCompressionPayload(model string, original []byte, sourceFormat string, targetBytes int64, customPrompt string) ([]byte, error) {
	if targetBytes <= 0 {
		targetBytes = 512000
	}
	prompt := strings.TrimSpace(customPrompt)
	if prompt == "" {
		prompt = defaultRequestCompressionPrompt()
	}
	user := fmt.Sprintf("Source format: %s\nTarget maximum UTF-8 JSON bytes: %d\n\nOriginal request JSON:\n%s", sourceFormat, targetBytes, string(original))
	payload := []byte(`{"model":"","input":[{"role":"system","content":[{"type":"input_text","text":""}]},{"role":"user","content":[{"type":"input_text","text":""}]}],"stream":false,"temperature":0}`)
	var err error
	payload, err = setJSONBytes(payload, "model", model)
	if err != nil {
		return nil, err
	}
	payload, err = setJSONBytes(payload, "input.0.content.0.text", prompt)
	if err != nil {
		return nil, err
	}
	payload, err = setJSONBytes(payload, "input.1.content.0.text", user)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

func defaultRequestCompressionPrompt() string {
	return strings.Join([]string{
		"You compact oversized LLM proxy request JSON.",
		"Return only a valid JSON request object, with no Markdown fences and no explanation.",
		"Preserve the original model, tools, tool_choice, reasoning, metadata, stream, and other request-level fields unless they must be shortened.",
		"Shorten only conversational history or large text content. Preserve the newest user request, recent tool results, IDs, call IDs, roles, and JSON structure.",
		"Prefer replacing older omitted context with a concise summary item in the same request schema.",
		"The returned UTF-8 JSON must be at or below the requested byte target.",
	}, "\n")
}

func extractCompressionText(payload []byte) string {
	root := gjson.ParseBytes(payload)
	for _, path := range []string{
		"output_text",
		"choices.0.message.content",
		"candidates.0.content.parts.0.text",
		"response.output.0.content.0.text",
		"output.0.content.0.text",
	} {
		if v := root.Get(path); v.Exists() && strings.TrimSpace(v.String()) != "" {
			return strings.TrimSpace(v.String())
		}
	}
	var out strings.Builder
	output := root.Get("output")
	if output.IsArray() {
		output.ForEach(func(_, item gjson.Result) bool {
			content := item.Get("content")
			if content.IsArray() {
				content.ForEach(func(_, part gjson.Result) bool {
					if text := strings.TrimSpace(part.Get("text").String()); text != "" {
						if out.Len() > 0 {
							out.WriteByte('\n')
						}
						out.WriteString(text)
					}
					return true
				})
			}
			return true
		})
	}
	return strings.TrimSpace(out.String())
}

func stripMarkdownJSON(text string) string {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "```") {
		lines := strings.Split(trimmed, "\n")
		if len(lines) >= 2 {
			if strings.HasPrefix(strings.TrimSpace(lines[0]), "```") {
				lines = lines[1:]
			}
			if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
				lines = lines[:len(lines)-1]
			}
			trimmed = strings.TrimSpace(strings.Join(lines, "\n"))
		}
	}
	if start := strings.Index(trimmed, "{"); start > 0 {
		trimmed = trimmed[start:]
	}
	if end := strings.LastIndex(trimmed, "}"); end >= 0 && end+1 < len(trimmed) {
		trimmed = trimmed[:end+1]
	}
	return strings.TrimSpace(trimmed)
}

func cloneMetadata(meta map[string]any) map[string]any {
	out := make(map[string]any, len(meta)+1)
	for k, v := range meta {
		out[k] = v
	}
	return out
}

func compressionDisabledFromMetadata(meta map[string]any) bool {
	if len(meta) == 0 {
		return false
	}
	raw := meta[requestCompressionDisabledMetadataKey]
	if disabled, ok := raw.(bool); ok {
		return disabled
	}
	if text, ok := raw.(string); ok {
		return strings.EqualFold(strings.TrimSpace(text), "true")
	}
	return false
}

func setJSONBytes(payload []byte, path string, value string) ([]byte, error) {
	return sjson.SetBytes(payload, path, value)
}
