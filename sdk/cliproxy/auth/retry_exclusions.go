package auth

import "strings"

const retryExclusionsMetadataKey = "cliproxy.retry_exclusions"

type retryExclusions struct {
	authIDs []string
}

// ExcludeAuthsForRetry carries per-request stream failures into a retry without
// mutating the original options shared by the abandoned stream.
func ExcludeAuthsForRetry(meta map[string]any, authIDs []string) map[string]any {
	out := make(map[string]any, len(meta)+1)
	for key, value := range meta {
		out[key] = value
	}
	seen := make(map[string]struct{}, len(authIDs))
	ids := make([]string, 0, len(authIDs))
	for _, id := range authIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	out[retryExclusionsMetadataKey] = retryExclusions{authIDs: ids}
	return out
}

func retryExclusionsFromMetadata(meta map[string]any) (retryExclusions, bool) {
	value, ok := meta[retryExclusionsMetadataKey]
	if !ok {
		return retryExclusions{}, false
	}
	exclusions, ok := value.(retryExclusions)
	return exclusions, ok
}
