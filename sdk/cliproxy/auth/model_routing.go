package auth

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func (m *Manager) applyConfiguredModelRoutes(providers []string, routeModel string, opts cliproxyexecutor.Options) ([]string, string, cliproxyexecutor.Options, error) {
	routeModel = strings.TrimSpace(routeModel)
	if m == nil || routeModel == "" {
		return providers, routeModel, opts, nil
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil || len(cfg.Routing.ModelRoutes) == 0 {
		return providers, routeModel, opts, nil
	}

	requestedModel := requestedModelAliasFromOptions(opts, routeModel)
	for i := range cfg.Routing.ModelRoutes {
		rule := cfg.Routing.ModelRoutes[i]
		if !modelRouteMatches(rule, requestedModel, routeModel) {
			continue
		}
		measured, ok := modelRouteMeasureValue(rule.Measure, opts.Metadata)
		if !ok {
			if rule.Measure.OnMissing == "reject" {
				return providers, routeModel, opts, &Error{
					Code:       "model_route_measure_missing",
					Message:    fmt.Sprintf("model route %q requires %s metadata", rule.Name, rule.Measure.Source),
					HTTPStatus: 400,
				}
			}
			return providers, routeModel, opts, nil
		}
		for j := range rule.Routes {
			branch := rule.Routes[j]
			if !modelRouteBranchMatches(branch, measured) {
				continue
			}
			switch branch.Action {
			case "passthrough":
				return providers, routeModel, opts, nil
			case "reject":
				return providers, routeModel, opts, &Error{
					Code:       "model_route_rejected",
					Message:    fmt.Sprintf("model route %q rejected measured input %d", rule.Name, measured),
					HTTPStatus: 400,
				}
			default:
				targetModel := strings.TrimSpace(branch.Target.Model)
				if targetModel == "" {
					return providers, routeModel, opts, nil
				}
				nextProviders := providers
				targetProvider := strings.ToLower(strings.TrimSpace(branch.Target.Provider))
				if targetProvider != "" {
					nextProviders = []string{targetProvider}
				}
				nextOpts := cloneOptionsWithMetadata(opts)
				nextOpts.Metadata[cliproxyexecutor.RoutedModelMetadataKey] = targetModel
				if targetProvider != "" {
					nextOpts.Metadata[cliproxyexecutor.RoutedProviderMetadataKey] = targetProvider
				}
				if branch.Target.PreserveRequestedModel && !hasRequestedModelMetadata(nextOpts.Metadata) {
					nextOpts.Metadata[cliproxyexecutor.RequestedModelMetadataKey] = requestedModel
				}
				return nextProviders, targetModel, nextOpts, nil
			}
		}
		return providers, routeModel, opts, nil
	}
	return providers, routeModel, opts, nil
}

func modelRouteMatches(rule internalconfig.ModelRouteRule, requestedModel, routeModel string) bool {
	if len(rule.Match.RequestedModels) == 0 {
		return false
	}
	requestedKey := canonicalRouteModel(requestedModel)
	routeKey := canonicalRouteModel(routeModel)
	for _, candidate := range rule.Match.RequestedModels {
		key := canonicalRouteModel(candidate)
		if key == "" {
			continue
		}
		if key == requestedKey || key == routeKey {
			return true
		}
	}
	return false
}

func canonicalRouteModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if parsed := thinking.ParseSuffix(model); strings.TrimSpace(parsed.ModelName) != "" {
		model = parsed.ModelName
	}
	return strings.ToLower(strings.TrimSpace(model))
}

func modelRouteMeasureValue(measure internalconfig.ModelRouteMeasure, meta map[string]any) (int64, bool) {
	switch measure.Source {
	case "request-bytes":
		return requestBytesFromMetadata(meta)
	default:
		return int64FromMetadata(meta, cliproxyexecutor.EstimatedInputTokensMetadataKey)
	}
}

func modelRouteBranchMatches(branch internalconfig.ModelRouteBranch, value int64) bool {
	if branch.MinInputTokens > 0 && value < branch.MinInputTokens {
		return false
	}
	if branch.MaxInputTokens > 0 && value > branch.MaxInputTokens {
		return false
	}
	return true
}

func cloneOptionsWithMetadata(opts cliproxyexecutor.Options) cliproxyexecutor.Options {
	if len(opts.Metadata) == 0 {
		opts.Metadata = make(map[string]any)
		return opts
	}
	meta := make(map[string]any, len(opts.Metadata)+2)
	for k, v := range opts.Metadata {
		meta[k] = v
	}
	opts.Metadata = meta
	return opts
}

func int64FromMetadata(meta map[string]any, key string) (int64, bool) {
	if len(meta) == 0 || key == "" {
		return 0, false
	}
	raw, ok := meta[key]
	if !ok || raw == nil {
		return 0, false
	}
	switch v := raw.(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case int32:
		return int64(v), true
	case float64:
		return int64(v), true
	case json.Number:
		parsed, err := v.Int64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func configuredModelOverrideContextLength(cfg *internalconfig.Config, auth *Auth, upstreamModel string) (int64, bool) {
	if cfg == nil || auth == nil || len(cfg.ModelOverrides) == 0 {
		return 0, false
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	channel := provider
	if auth.Attributes != nil {
		if resolved := OAuthModelAliasChannel(provider, auth.Attributes["auth_kind"]); resolved != "" {
			channel = resolved
		}
	}
	target := canonicalRouteModel(upstreamModel)
	for i := range cfg.ModelOverrides {
		override := cfg.ModelOverrides[i]
		if override.ContextLength <= 0 {
			continue
		}
		if override.Channel != "" && !strings.EqualFold(override.Channel, channel) {
			continue
		}
		if override.Provider != "" && !strings.EqualFold(override.Provider, provider) {
			continue
		}
		if canonicalRouteModel(override.Model) == target {
			return int64(override.ContextLength), true
		}
	}
	return 0, false
}
