package claude

import (
	"strings"
	"testing"
)

func testGeminiEPrefixSignature(t *testing.T) string {
	t.Helper()
	return "e" + strings.Repeat("a", 127)
}
