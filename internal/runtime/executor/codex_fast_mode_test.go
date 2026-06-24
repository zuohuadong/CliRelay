package executor

import (
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestApplyCodexFastModeServiceTierDefaultsOff(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":"hi"}`)
	auth := &cliproxyauth.Auth{Provider: "codex"}

	got := applyCodexFastModeServiceTier(auth, body)

	if gjson.GetBytes(got, "service_tier").Exists() {
		t.Fatalf("service_tier should be omitted by default, got %s", got)
	}
}

func TestApplyCodexFastModeServiceTierStripsClientTierWhenDisabled(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":"hi","service_tier":"priority"}`)
	auth := &cliproxyauth.Auth{Provider: "codex"}

	got := applyCodexFastModeServiceTier(auth, body)

	if gjson.GetBytes(got, "service_tier").Exists() {
		t.Fatalf("client service_tier should be ignored when fast mode is disabled, got %s", got)
	}
}

func TestApplyCodexFastModeServiceTierUsesPriorityWhenEnabled(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":"hi"}`)
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"codex_fast_mode": "true",
		},
	}

	got := applyCodexFastModeServiceTier(auth, body)

	if tier := gjson.GetBytes(got, "service_tier").String(); tier != "priority" {
		t.Fatalf("service_tier = %q, want priority; body=%s", tier, got)
	}
}

func TestApplyCodexFastModeServiceTierOverridesClientTierWhenEnabled(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":"hi","service_tier":"default"}`)
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"codex_fast_mode": "true",
		},
	}

	got := applyCodexFastModeServiceTier(auth, body)

	if tier := gjson.GetBytes(got, "service_tier").String(); tier != "priority" {
		t.Fatalf("service_tier = %q, want priority; body=%s", tier, got)
	}
}

func TestApplyCodexFastModeServiceTierIgnoresNonCodexAuth(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":"hi"}`)
	auth := &cliproxyauth.Auth{
		Provider: "claude",
		Attributes: map[string]string{
			"codex_fast_mode": "true",
		},
	}

	got := applyCodexFastModeServiceTier(auth, body)

	if gjson.GetBytes(got, "service_tier").Exists() {
		t.Fatalf("service_tier should be omitted for non-codex auth, got %s", got)
	}
}
