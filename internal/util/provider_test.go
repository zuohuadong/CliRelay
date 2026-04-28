package util

import (
	"testing"
)

func TestGetProviderNameDoesNotIncludeRemovedCherryCompatImageAlias(t *testing.T) {
	providers := GetProviderName("gptimage-2")
	if len(providers) != 0 {
		t.Fatalf("providers = %#v, want no provider for removed alias", providers)
	}
}
