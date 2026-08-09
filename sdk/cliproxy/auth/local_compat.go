package auth

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func noExecutableUpstreamModelError(ctx context.Context, cfg *internalconfig.Config, auth *Auth, opts cliproxyexecutor.Options, provider, routeModel string, candidates []string) *Error {
	provider = strings.TrimSpace(provider)
	routeModel = strings.TrimSpace(routeModel)
	candidates = dedupeStrings(candidates)
	requestBytes, hasRequestBytes := requestBytesFromMetadata(opts.Metadata)
	contextDiagnostics := candidateContextLengthDiagnostics(cfg, auth, candidates)
	logEntryWithRequestID(ctx).Debugf("no executable upstream model available provider=%s model=%s request_bytes=%d candidates=%v candidate_context_lengths=%v", provider, routeModel, requestBytes, candidates, contextDiagnostics)

	message := fmt.Sprintf("no executable upstream model available (provider=%s, model=%s", provider, routeModel)
	if hasRequestBytes && requestBytes > 0 {
		message += ", request_bytes=" + strconv.FormatInt(requestBytes, 10)
	}
	if len(candidates) > 0 {
		message += ", candidates=" + strings.Join(candidates, ",")
	}
	if len(contextDiagnostics) > 0 {
		message += ", candidate_context_lengths=" + strings.Join(contextDiagnostics, ",")
	}
	message += ")"
	return &Error{Code: "upstream_model_unavailable", Message: message, HTTPStatus: http.StatusServiceUnavailable}
}

func candidateContextLengthDiagnostics(cfg *internalconfig.Config, auth *Auth, candidates []string) []string {
	if cfg == nil || auth == nil || len(candidates) == 0 {
		return nil
	}
	diagnostics := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		contextLength, ok := configuredOpenAICompatModelContextLength(cfg, auth, candidate)
		if ok && contextLength > 0 {
			diagnostics = append(diagnostics, fmt.Sprintf("%s:%d", candidate, contextLength))
		}
	}
	return diagnostics
}

func requestFitsConfiguredModelContext(cfg *internalconfig.Config, auth *Auth, opts cliproxyexecutor.Options, upstreamModel string) bool {
	inputTokens, ok := int64FromMetadata(opts.Metadata, cliproxyexecutor.EstimatedInputTokensMetadataKey)
	if !ok || inputTokens <= 0 {
		return true
	}
	contextLength, ok := configuredOpenAICompatModelContextLength(cfg, auth, upstreamModel)
	if !ok || contextLength <= 0 || inputTokens <= contextLength {
		return true
	}
	return requestCompressionPolicyMatchesAuth(requestCompressionRouteCheck{
		config: cfg, auth: auth, options: opts,
		routeModel: requestedModelAliasFromOptions(opts, upstreamModel), upstreamModel: upstreamModel,
	})
}

func authHasConfiguredContextCapacity(m *Manager, cfg *internalconfig.Config, auth *Auth, opts cliproxyexecutor.Options, routeModel string) bool {
	inputTokens, ok := int64FromMetadata(opts.Metadata, cliproxyexecutor.EstimatedInputTokensMetadataKey)
	if !ok || inputTokens <= 0 {
		return true
	}
	candidates := m.executionModelCandidatesForCapacityCheck(auth, routeModel)
	if len(candidates) == 0 {
		return true
	}
	requestedModel := requestedModelAliasFromOptions(opts, routeModel)
	hasConfiguredContext := false
	for _, upstreamModel := range candidates {
		contextLength, ok := configuredOpenAICompatModelContextLength(cfg, auth, upstreamModel)
		if !ok || contextLength <= 0 {
			return true
		}
		hasConfiguredContext = true
		if inputTokens <= contextLength || requestCompressionPolicyMatchesAuth(requestCompressionRouteCheck{config: cfg, auth: auth, options: opts, routeModel: requestedModel, upstreamModel: upstreamModel}) {
			return true
		}
	}
	return !hasConfiguredContext
}

func withoutPinnedAuth(opts cliproxyexecutor.Options) cliproxyexecutor.Options {
	if pinnedAuthIDFromMetadata(opts.Metadata) == "" || responseAffinityActive(opts) {
		return opts
	}
	metadata := make(map[string]any, len(opts.Metadata)-1)
	for key, value := range opts.Metadata {
		if key != cliproxyexecutor.PinnedAuthMetadataKey {
			metadata[key] = value
		}
	}
	opts.Metadata = metadata
	return opts
}

type requestCompressionRouteCheck struct {
	config        *internalconfig.Config
	auth          *Auth
	options       cliproxyexecutor.Options
	routeModel    string
	upstreamModel string
}

func requestCompressionPolicyMatchesAuth(check requestCompressionRouteCheck) bool {
	if check.config == nil || check.auth == nil {
		return false
	}
	provider := strings.ToLower(strings.TrimSpace(check.auth.Provider))
	if check.auth.Attributes != nil {
		if providerKey := strings.ToLower(strings.TrimSpace(check.auth.Attributes["provider_key"])); providerKey != "" {
			provider = providerKey
		}
	}
	policy, _ := requestPolicyCompressionDecision(check.config, check.options, check.routeModel, provider, check.upstreamModel)
	return policy != nil
}

func configuredOpenAICompatModelContextLength(cfg *internalconfig.Config, auth *Auth, upstreamModel string) (int64, bool) {
	if cfg == nil || auth == nil {
		return 0, false
	}
	if contextLength, ok := configuredModelOverrideContextLength(cfg, auth, upstreamModel); ok {
		return contextLength, true
	}
	providerKey := ""
	compatName := ""
	if auth.Attributes != nil {
		providerKey = strings.TrimSpace(auth.Attributes["provider_key"])
		compatName = strings.TrimSpace(auth.Attributes["compat_name"])
	}
	entry := resolveOpenAICompatConfig(cfg, providerKey, compatName, auth.Provider)
	if entry == nil || len(entry.Models) == 0 {
		return 0, false
	}
	target := canonicalPolicyModel(upstreamModel)
	for i := range entry.Models {
		model := entry.Models[i]
		if canonicalPolicyModel(model.Name) != target && canonicalPolicyModel(model.Alias) != target {
			continue
		}
		contextLength := model.ContextLength
		if contextLength <= 0 {
			contextLength = model.MaxContextLength
		}
		if contextLength <= 0 {
			return 0, false
		}
		return int64(contextLength), true
	}
	return 0, false
}

func authAllowedByChannels(auth *Auth, allowed map[string]struct{}) bool {
	if len(allowed) == 0 {
		return true
	}
	if auth == nil {
		return false
	}
	for _, identifier := range auth.ChannelIdentifiers() {
		if _, ok := allowed[strings.ToLower(strings.TrimSpace(identifier))]; ok {
			return true
		}
	}
	return false
}
