package registry

import "regexp"

// contextWindowMarkerRe matches trailing context window markers like [128K], [1M], [200K], etc.
// A space before the bracket is required so that "model[128K]" (no space) is not treated as a marker.
var contextWindowMarkerRe = regexp.MustCompile(`\s+\[\d+[KMG]?\]\s*$`)

// StripContextWindowMarker removes trailing context window markers from a model name.
// For example: "claude-sonnet-4-20250514 [128K]" -> "claude-sonnet-4-20250514"
func StripContextWindowMarker(modelID string) string {
	return contextWindowMarkerRe.ReplaceAllString(modelID, "")
}

// NormalizeModelID strips context window markers and trims whitespace from a model ID.
func NormalizeModelID(modelID string) string {
	return StripContextWindowMarker(modelID)
}
