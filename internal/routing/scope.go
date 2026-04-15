package routing

import (
	"strings"
	"unicode"
)

const (
	// GinPathRouteContextKey stores the resolved path-route context in gin.Context.
	GinPathRouteContextKey = "cliproxy.path_route"
)

// PathRouteContext captures request-scoped channel-group routing derived from the URL path.
type PathRouteContext struct {
	RoutePath string
	Group     string
	Fallback  string
}

// NormalizeGroupName trims, lowercases, and canonicalizes channel group names.
func NormalizeGroupName(value string) string {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	trimmed = strings.Trim(trimmed, "/")
	return trimmed
}

// NormalizeNamespacePath converts route namespace inputs like "pro" or "/pro/" to "/pro".
// Only single path segments made of [A-Za-z0-9_-] are accepted.
func NormalizeNamespacePath(value string) string {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" || strings.Contains(trimmed, "/") {
		return ""
	}
	for _, r := range trimmed {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			continue
		}
		return ""
	}
	return "/" + trimmed
}

// NormalizeFallback canonicalizes fallback values. Empty defaults to "none".
func NormalizeFallback(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "none":
		return "none"
	case "default":
		return "default"
	default:
		return "none"
	}
}

// ParseNormalizedSet splits a comma-separated string into a normalized set.
func ParseNormalizedSet(raw string, normalizer func(string) string) map[string]struct{} {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	out := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		value := strings.TrimSpace(part)
		if normalizer != nil {
			value = normalizer(value)
		}
		if value == "" {
			continue
		}
		out[value] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
