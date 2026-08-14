package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	codexInputItemIDLimit                 = 64
	codexMessageItemIDPrefix              = "msg"
	codexReasoningItemIDPrefix            = "rs"
	codexFunctionCallItemIDPrefix         = "fc"
	codexCustomToolCallItemIDPrefix       = "ctc"
	codexCustomToolCallOutputItemIDPrefix = "ctco"

	codexInputItemIDOccupied  uint8 = 1 << 0
	codexInputItemIDPreserved uint8 = 1 << 1
)

// SanitizeCodexInputItemIDs normalizes supported input item IDs for Codex, removes encrypted
// reasoning items whose IDs cannot be changed safely, and deterministically rewrites
// other invalid or overlong input item IDs.
func SanitizeCodexInputItemIDs(body []byte) []byte {
	input := util.GetGJSONBytesNoCopy(body, "input")
	if !input.IsArray() {
		return body
	}

	items := input.Array()
	idStates := make(map[string]uint8, len(items))
	for _, item := range items {
		if shouldDropCodexEncryptedReasoningItem(item) {
			continue
		}
		itemID := item.Get("id")
		if itemID.Type != gjson.String {
			continue
		}
		originalID := itemID.String()
		id := normalizeCodexInputItemID(item.Get("type").String(), originalID, 0)
		state := idStates[id]
		if id == originalID {
			state |= codexInputItemIDPreserved
		}
		if len([]rune(id)) <= codexInputItemIDLimit {
			state |= codexInputItemIDOccupied
		}
		if state != 0 {
			idStates[id] = state
		}
	}

	var mapped map[string]string
	var collisionMapped map[string]string
	normalizedOwners := make(map[string]string, len(items))
	resolveCollision := func(itemType, originalID, id string) string {
		collisionKey := itemType + "\x00" + originalID
		collisionID, ok := collisionMapped[collisionKey]
		if ok {
			return collisionID
		}
		for attempt := 1; ; attempt++ {
			collisionID = normalizeCodexInputItemID(itemType, originalID, attempt)
			if idStates[collisionID]&codexInputItemIDOccupied == 0 {
				break
			}
		}
		if collisionMapped == nil {
			collisionMapped = make(map[string]string)
		}
		collisionMapped[collisionKey] = collisionID
		idStates[collisionID] |= codexInputItemIDOccupied
		return collisionID
	}
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
			originalID := itemID.String()
			itemType := item.Get("type").String()
			id := normalizeCodexInputItemID(itemType, originalID, 0)
			normalizedKey := itemType + "\x00" + id
			collisionNeeded := id != originalID && idStates[id]&codexInputItemIDPreserved != 0
			if !collisionNeeded {
				if owner, exists := normalizedOwners[normalizedKey]; exists && owner != originalID {
					collisionNeeded = true
				} else {
					normalizedOwners[normalizedKey] = originalID
				}
			}
			if collisionNeeded {
				id = resolveCollision(itemType, originalID, id)
			} else {
				normalizedOwners[normalizedKey] = originalID
			}
			if len([]rune(id)) > codexInputItemIDLimit {
				mappingKey := itemType + "\x00" + id
				shortened, ok := mapped[mappingKey]
				if !ok {
					shortened = shortenCodexInputItemID(id)
					for attempt := 1; ; attempt++ {
						if idStates[shortened]&codexInputItemIDOccupied == 0 {
							break
						}
						shortened = shortenCodexInputItemIDWithAttempt(id, attempt)
					}
					if mapped == nil {
						mapped = make(map[string]string)
					}
					mapped[mappingKey] = shortened
					idStates[shortened] |= codexInputItemIDOccupied
				}
				id = shortened
			}
			if id != originalID {
				next, errSet := sjson.SetBytes([]byte(raw), "id", id)
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
	itemPrefix := codexInputItemIDPrefix(itemType)
	if itemPrefix != "" && !strings.HasPrefix(id, itemPrefix) {
		requiredPrefix = itemPrefix + "_"
		id = requiredPrefix + id
	}
	return shortenCodexInputItemIDWithRequiredPrefix(id, requiredPrefix, attempt)
}

func isValidCodexInputItemIDForType(itemType, id string) bool {
	if !isValidCodexInputItemID(id) {
		return false
	}
	requiredPrefix := codexInputItemIDPrefix(itemType)
	return requiredPrefix == "" || strings.HasPrefix(id, requiredPrefix)
}

func codexInputItemIDPrefix(itemType string) string {
	switch itemType {
	case "message":
		return codexMessageItemIDPrefix
	case "reasoning":
		return codexReasoningItemIDPrefix
	case "function_call":
		return codexFunctionCallItemIDPrefix
	case "custom_tool_call":
		return codexCustomToolCallItemIDPrefix
	case "custom_tool_call_output":
		return codexCustomToolCallOutputItemIDPrefix
	default:
		return ""
	}
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

func codexInputItemIDWithHashSuffix(id string, attempt int) string {
	return codexInputItemIDWithHashSuffixRunes(id, []rune(id), attempt)
}

func codexInputItemIDWithHashSuffixRunes(id string, runes []rune, attempt int) string {
	hashInput := id
	if attempt > 0 {
		hashInput += "\x00" + strconv.Itoa(attempt)
	}
	sum := sha256.Sum256([]byte(hashInput))
	suffix := "_" + hex.EncodeToString(sum[:8])
	prefixLength := codexInputItemIDLimit - len(suffix)
	prefix := make([]rune, 0, prefixLength)
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
