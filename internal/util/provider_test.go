package util

import (
	"testing"
)

func TestGetProviderNameIncludesCherryCompatImageAlias(t *testing.T) {
	providers := GetProviderName("gptimage-2")
	if len(providers) != 1 || providers[0] != "codex" {
		t.Fatalf("providers = %#v, want [\"codex\"]", providers)
	}
}
