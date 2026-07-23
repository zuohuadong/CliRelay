package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const codexInputItemIDLimit = 64

// SanitizeCodexInputItemIDs removes encrypted reasoning items whose IDs cannot
// be changed safely and deterministically normalizes other invalid item IDs.
func SanitizeCodexInputItemIDs(body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}

	items := input.Array()
	occupied := make(map[string]struct{}, len(items))
	for _, item := range items {
		if shouldDropCodexEncryptedReasoningItem(item) {
			continue
		}
		itemID := item.Get("id")
		if itemID.Type != gjson.String {
			continue
		}
		id := itemID.String()
		if isValidCodexInputItemIDForType(item.Get("type").String(), id) {
			occupied[id] = struct{}{}
		}
	}

	mapped := make(map[string]string, len(items))
	rebuilt := make([]string, 0, len(items))
	changed := false
	for _, item := range items {
		if shouldDropCodexEncryptedReasoningItem(item) {
			changed = true
			continue
		}

		raw := item.Raw
		itemID := item.Get("id")
		if itemID.Type == gjson.String {
			id := itemID.String()
			itemType := item.Get("type").String()
			if !isValidCodexInputItemIDForType(itemType, id) {
				mappingKey := itemType + "\x00" + id
				shortened, ok := mapped[mappingKey]
				if !ok {
					shortened = normalizeCodexInputItemID(itemType, id, 0)
					for attempt := 1; ; attempt++ {
						if _, exists := occupied[shortened]; !exists {
							break
						}
						shortened = normalizeCodexInputItemID(itemType, id, attempt)
					}
					mapped[mappingKey] = shortened
					occupied[shortened] = struct{}{}
				}

				next, errSet := sjson.SetBytes([]byte(raw), "id", shortened)
				if errSet == nil {
					raw = string(next)
					changed = true
				}
			}
		}
		rebuilt = append(rebuilt, raw)
	}
	if !changed {
		return body
	}

	updated, errSet := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(rebuilt, ",")+"]"))
	if errSet != nil {
		return body
	}
	return updated
}

func shouldDropCodexEncryptedReasoningItem(item gjson.Result) bool {
	if item.Get("type").String() != "reasoning" {
		return false
	}
	itemID := item.Get("id")
	if itemID.Type != gjson.String || isValidCodexInputItemID(itemID.String()) {
		return false
	}
	encryptedContent := item.Get("encrypted_content")
	return encryptedContent.Type == gjson.String && encryptedContent.String() != ""
}

func shortenCodexInputItemID(id string) string {
	return shortenCodexInputItemIDWithAttempt(id, 0)
}

func normalizeCodexInputItemID(itemType, id string, attempt int) string {
	requiredPrefix := ""
	if itemType == "message" && !strings.HasPrefix(id, "msg") {
		requiredPrefix = "msg_"
		id = requiredPrefix + id
	}
	return shortenCodexInputItemIDWithRequiredPrefix(id, requiredPrefix, attempt)
}

func isValidCodexInputItemIDForType(itemType, id string) bool {
	if !isValidCodexInputItemID(id) {
		return false
	}
	return itemType != "message" || strings.HasPrefix(id, "msg")
}

func shortenCodexInputItemIDWithAttempt(id string, attempt int) string {
	return shortenCodexInputItemIDWithRequiredPrefix(id, "", attempt)
}

func shortenCodexInputItemIDWithRequiredPrefix(id, requiredPrefix string, attempt int) string {
	runes := []rune(id)
	if attempt == 0 && isValidCodexInputItemID(id) {
		return id
	}

	hashInput := id
	if attempt > 0 {
		hashInput += "\x00" + strconv.Itoa(attempt)
	}
	sum := sha256.Sum256([]byte(hashInput))
	suffix := "_" + hex.EncodeToString(sum[:8])
	prefixLength := codexInputItemIDLimit - len(suffix)
	prefix := make([]rune, 0, prefixLength)
	if requiredPrefix != "" {
		prefix = append(prefix, []rune(requiredPrefix)...)
		runes = runes[len([]rune(requiredPrefix)):]
	}
	for _, r := range runes {
		if len(prefix) == prefixLength {
			break
		}
		if isCodexInputItemIDRune(r) {
			prefix = append(prefix, r)
		} else {
			prefix = append(prefix, '_')
		}
	}
	return string(prefix) + suffix
}

func isValidCodexInputItemID(id string) bool {
	runes := []rune(id)
	if len(runes) == 0 || len(runes) > codexInputItemIDLimit {
		return false
	}
	for _, r := range runes {
		if !isCodexInputItemIDRune(r) {
			return false
		}
	}
	return true
}

func isCodexInputItemIDRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
}
