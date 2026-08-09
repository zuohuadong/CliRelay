package auth

import (
	"errors"
	"net/http"
	"strings"

	internalegress "github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
)

func shouldStopMixedProviderFallback(provider, routeModel string, err error) bool {
	if err == nil {
		return false
	}
	if isTerminalEgressError(err) {
		return true
	}
	if !strings.EqualFold(strings.TrimSpace(provider), "astron-code") {
		return false
	}
	model := strings.TrimSpace(routeModel)
	if parsed := thinking.ParseSuffix(model); parsed.ModelName != "" {
		model = strings.TrimSpace(parsed.ModelName)
	}
	if !strings.EqualFold(model, "gpt-5.3-codex") && !strings.EqualFold(model, "glm-5.1") {
		return false
	}
	if isModelSupportError(err) || isRequestInvalidError(err) {
		return false
	}
	var authErr *Error
	if errors.As(err, &authErr) && authErr.Retryable {
		return false
	}
	switch statusCodeFromError(err) {
	case http.StatusRequestTimeout, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func isTerminalEgressError(err error) bool {
	if err == nil {
		return false
	}
	var target *internalegress.Error
	return errors.As(err, &target) && target != nil && strings.HasPrefix(strings.TrimSpace(target.Code), "egress_")
}

func isCredentialLocalEgressFallbackError(err error) bool {
	if err == nil {
		return false
	}
	var target *internalegress.Error
	if !errors.As(err, &target) || target == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(target.Code)) {
	case "egress_disabled", "egress_unbound", "egress_identity_required":
		return true
	default:
		return false
	}
}
