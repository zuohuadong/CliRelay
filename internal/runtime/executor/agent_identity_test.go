package executor

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type parsedAgentAssertion struct {
	AgentRuntimeID string `json:"agent_runtime_id"`
	TaskID         string `json:"task_id"`
	Timestamp      string `json:"timestamp"`
	Signature      string `json:"signature"`
}

var _ cliproxyauth.AgentIdentityTaskRenewer = (*CodexAutoExecutor)(nil)

func TestBuildAgentAssertionSignsCanonicalPayload(t *testing.T) {
	auth, publicKey := agentIdentityStandaloneAuth(t)
	credentials := agentIdentityCredsFromAuth(auth)
	now := time.Date(2026, time.July, 23, 12, 34, 56, 0, time.UTC)
	header, err := buildAgentAssertion(credentials, now)
	if err != nil {
		t.Fatalf("buildAgentAssertion() error = %v", err)
	}
	assertion := parseAgentAssertion(t, header)
	if assertion.Timestamp != "2026-07-23T12:34:56Z" {
		t.Fatalf("timestamp = %q", assertion.Timestamp)
	}
	verifyAgentAssertion(t, assertion, publicKey)
}

func TestManagedAgentIdentityUsesAssertionAfterCustomHeaders(t *testing.T) {
	auth, _ := managedAgentIdentityTestAuth(t)
	auth.Attributes = map[string]string{
		"header:Authorization":      "Bearer attacker-controlled",
		"header:ChatGPT-Account-ID": "wrong-account",
		"header:X-OpenAI-Fedramp":   "false",
	}
	req := newAgentIdentityRequest(t, "/backend-api/codex/responses")
	if err := applyCodexHeadersFromSources(req, auth, "oauth-token", true, nil, nil); err != nil {
		t.Fatalf("applyCodexHeadersFromSources() error = %v", err)
	}
	parseAgentAssertion(t, req.Header.Get("Authorization"))
	if got := req.Header.Get("ChatGPT-Account-ID"); got != "account-1" {
		t.Fatalf("ChatGPT-Account-ID = %q, want account-1", got)
	}
	if got := req.Header.Get("X-OpenAI-Fedramp"); got != "true" {
		t.Fatalf("X-OpenAI-Fedramp = %q, want true", got)
	}
}

func TestManagedAgentIdentityUsesAssertionAfterModelOverrides(t *testing.T) {
	const model = "agent-identity-header-override"
	registry.GetGlobalRegistry().RegisterClient(t.Name(), "codex", []*registry.ModelInfo{{
		ID: model,
		Config: &registry.ModelConfig{OverrideHeader: map[string]string{
			"Authorization":      "Bearer attacker-controlled",
			"ChatGPT-Account-ID": "wrong-account",
			"X-OpenAI-Fedramp":   "false",
		}},
	}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(t.Name()) })

	auth, _ := managedAgentIdentityTestAuth(t)
	req := newAgentIdentityRequest(t, "/backend-api/codex/responses")
	if err := applyCodexHeadersFromSources(req, auth, "oauth-token", true, nil, nil); err != nil {
		t.Fatalf("applyCodexHeadersFromSources() error = %v", err)
	}
	if err := applyModelHeaderOverridesForRequest(req, auth, "oauth-token", model); err != nil {
		t.Fatalf("applyModelHeaderOverridesForRequest() error = %v", err)
	}
	parseAgentAssertion(t, req.Header.Get("Authorization"))
	if got := req.Header.Get("ChatGPT-Account-ID"); got != "account-1" {
		t.Fatalf("ChatGPT-Account-ID = %q, want account-1", got)
	}
	if got := req.Header.Get("X-OpenAI-Fedramp"); got != "true" {
		t.Fatalf("X-OpenAI-Fedramp = %q, want true", got)
	}
}

func TestManagedAgentIdentityBearerRequestRejectsModelIdentityOverrides(t *testing.T) {
	const model = "agent-identity-bearer-header-override"
	registry.GetGlobalRegistry().RegisterClient(t.Name(), "codex", []*registry.ModelInfo{{
		ID: model,
		Config: &registry.ModelConfig{OverrideHeader: map[string]string{
			"Authorization":      "Bearer attacker-controlled",
			"ChatGPT-Account-ID": "wrong-account",
			"X-OpenAI-Fedramp":   "true",
		}},
	}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(t.Name()) })

	auth, _ := managedAgentIdentityTestAuth(t)
	req := newAgentIdentityRequest(t, "/backend-api/codex/images/generations")
	if err := applyCodexDirectImageHeaders(req, auth, "oauth-token", false, nil); err != nil {
		t.Fatalf("applyCodexDirectImageHeaders() error = %v", err)
	}
	if err := applyModelHeaderOverridesForRequest(req, auth, "oauth-token", model); err != nil {
		t.Fatalf("applyModelHeaderOverridesForRequest() error = %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer oauth-token" {
		t.Fatalf("Authorization = %q, want OAuth bearer", got)
	}
	if got := req.Header.Get("ChatGPT-Account-ID"); got != "account-1" {
		t.Fatalf("ChatGPT-Account-ID = %q, want account-1", got)
	}
	if got := req.Header.Get("X-OpenAI-Fedramp"); got != "" {
		t.Fatalf("X-OpenAI-Fedramp = %q, want empty for bearer request", got)
	}
}

func TestPlainOAuthKeepsConfiguredFedRAMPHeader(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"type": "codex", "account_id": "account-1"},
	}
	req := newAgentIdentityRequest(t, "/backend-api/codex/responses")
	req.Header.Set("X-OpenAI-Fedramp", "true")

	if err := sealCodexAuthenticationHeaders(req, auth, "oauth-token"); err != nil {
		t.Fatalf("sealCodexAuthenticationHeaders() error = %v", err)
	}
	if got := req.Header.Get("X-OpenAI-Fedramp"); got != "true" {
		t.Fatalf("X-OpenAI-Fedramp = %q, want configured OAuth value", got)
	}
}

func TestManagedAgentIdentityBindingMismatchFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "account", key: "agent_identity_account_id", value: "account-other"},
		{name: "user", key: "chatgpt_user_id", value: "user-other"},
	} {
		t.Run(test.name, func(t *testing.T) {
			auth, _ := managedAgentIdentityTestAuth(t)
			auth.Metadata[test.key] = test.value
			req := newAgentIdentityRequest(t, "/backend-api/codex/responses")
			if err := applyCodexHeadersFromSources(req, auth, "oauth-token", true, nil, nil); err == nil {
				t.Fatal("binding mismatch unexpectedly succeeded")
			}
		})
	}
}

func TestManagedAgentIdentityFedRAMPBindingMismatchFailsClosed(t *testing.T) {
	auth, _ := managedAgentIdentityTestAuth(t)
	auth.Metadata["agent_identity_account_is_fedramp"] = false
	req := newAgentIdentityRequest(t, "/backend-api/codex/responses")
	if err := applyCodexHeadersFromSources(req, auth, "oauth-token", true, nil, nil); err == nil {
		t.Fatal("FedRAMP binding mismatch unexpectedly succeeded")
	}
}

func TestManagedAgentIdentityNeedsTaskKeepsBearer(t *testing.T) {
	auth, _ := managedAgentIdentityTestAuth(t)
	auth.Metadata["agent_identity_state"] = string(codex.ManagedAgentIdentityStateNeedsTask)
	req := newAgentIdentityRequest(t, "/backend-api/codex/responses")
	if err := applyCodexHeadersFromSources(req, auth, "oauth-token", true, nil, nil); err != nil {
		t.Fatalf("applyCodexHeadersFromSources() error = %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer oauth-token" {
		t.Fatalf("Authorization = %q, want OAuth bearer", got)
	}
}

func TestManagedAgentIdentityOAuthOnlyEndpointKeepsBearer(t *testing.T) {
	auth, _ := managedAgentIdentityTestAuth(t)
	req := newAgentIdentityRequest(t, "/backend-api/codex/alpha/search")
	if err := applyCodexHeadersFromSources(req, auth, "oauth-token", false, nil, nil); err != nil {
		t.Fatalf("applyCodexHeadersFromSources() error = %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer oauth-token" {
		t.Fatalf("Authorization = %q, want OAuth bearer", got)
	}
}

func TestStandaloneAgentIdentityRejectsUnsupportedEndpoint(t *testing.T) {
	auth, _ := agentIdentityStandaloneAuth(t)
	req := newAgentIdentityRequest(t, "/backend-api/wham/usage")
	if err := NewCodexExecutor(nil).PrepareRequest(req, auth); err == nil {
		t.Fatal("standalone Agent Identity unexpectedly authenticated unsupported endpoint")
	}
}

func TestApplyAgentIdentityWebsocketHeadersMintsAssertionPerCall(t *testing.T) {
	auth, publicKey := agentIdentityStandaloneAuth(t)
	var authorization []string
	wsURL := "wss://chatgpt.com/backend-api/codex/responses"
	for _, taskID := range []string{"task-1", "task-2"} {
		auth.Metadata["task_id"] = taskID
		headers, err := applyAgentIdentityWebsocketHeaders(http.Header{"Authorization": []string{"Bearer wrong"}}, auth, wsURL)
		if err != nil {
			t.Fatalf("headers for task %s: %v", taskID, err)
		}
		authorization = append(authorization, headers.Get("Authorization"))
	}
	if len(authorization) != 2 {
		t.Fatalf("header count = %d, want 2", len(authorization))
	}
	for index, header := range authorization {
		assertion := parseAgentAssertion(t, header)
		if assertion.TaskID != []string{"task-1", "task-2"}[index] {
			t.Fatalf("dial %d task_id = %q", index, assertion.TaskID)
		}
		verifyAgentAssertion(t, assertion, publicKey)
	}
}

func TestAgentIdentityRejectsNonChatGPTAssertionTarget(t *testing.T) {
	auth, _ := agentIdentityStandaloneAuth(t)
	for _, target := range []string{
		"https://example.test/backend-api/codex/responses",
		"http://chatgpt.com/backend-api/codex/responses",
		"https://user@chatgpt.com/backend-api/codex/responses",
		"https://chatgpt.com:8443/backend-api/codex/responses",
		"https://evil.chatgpt.com/backend-api/codex/responses",
	} {
		req, err := http.NewRequest(http.MethodPost, target, nil)
		if err != nil {
			t.Fatalf("http.NewRequest(%q) error = %v", target, err)
		}
		if err = applyAgentIdentityRequestHeaders(req, auth); err == nil {
			t.Fatalf("target %q unexpectedly accepted AgentAssertion", target)
		}
	}
}

func TestCodexWebsocketRouteKeyChangesWithTask(t *testing.T) {
	auth, _ := agentIdentityStandaloneAuth(t)
	first := codexWebsocketRouteKey(auth)
	auth.Metadata["task_id"] = "task-rotated"
	if second := codexWebsocketRouteKey(auth); second == first {
		t.Fatal("route key did not change after task rotation")
	}
}

func TestCodexInvalidTaskErrorKeepsExactCode(t *testing.T) {
	err := newCodexStatusErr(http.StatusUnauthorized, []byte(`{"error":{"code":"invalid_task_id","message":"expired task"}}`))
	if err.ErrorCode() != "invalid_task_id" {
		t.Fatalf("ErrorCode() = %q", err.ErrorCode())
	}
	if err.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("StatusCode() = %d", err.StatusCode())
	}
}

func TestCodexStatusErrorCapturesRequestAuthScheme(t *testing.T) {
	for _, authorization := range []string{"Bearer oauth-token", "AgentAssertion signed-envelope"} {
		req := newAgentIdentityRequest(t, "/backend-api/codex/responses")
		req.Header.Set("Authorization", authorization)
		resp := &http.Response{StatusCode: http.StatusUnauthorized, Request: req}
		err := newCodexStatusErrForResponse(resp, []byte(`{"error":{"message":"unauthorized"}}`))
		want := strings.SplitN(authorization, " ", 2)[0]
		if got := err.RequestAuthScheme(); got != want {
			t.Fatalf("RequestAuthScheme() = %q, want %q", got, want)
		}
	}
}

func managedAgentIdentityTestAuth(t *testing.T) (*cliproxyauth.Auth, ed25519.PublicKey) {
	t.Helper()
	auth, publicKey := agentIdentityStandaloneAuth(t)
	auth.Metadata["type"] = "codex"
	auth.Metadata["id_token"] = agentIdentityIDToken(t, "account-1", "user-1", true)
	auth.Metadata["access_token"] = "oauth-token"
	auth.Metadata["refresh_token"] = "refresh-token"
	auth.Metadata["agent_identity_account_id"] = "account-1"
	auth.Metadata["chatgpt_user_id"] = "user-1"
	auth.Metadata["agent_identity_state"] = string(codex.ManagedAgentIdentityStateReady)
	return auth, publicKey
}

func agentIdentityStandaloneAuth(t *testing.T) (*cliproxyauth.Auth, ed25519.PublicKey) {
	t.Helper()
	material, err := codex.GenerateAgentKeyMaterial()
	if err != nil {
		t.Fatalf("GenerateAgentKeyMaterial() error = %v", err)
	}
	privateKey, err := codex.ParseAgentIdentityPrivateKey(material.PrivateKeyPKCS8Base64)
	if err != nil {
		t.Fatalf("ParseAgentIdentityPrivateKey() error = %v", err)
	}
	return &cliproxyauth.Auth{Provider: "codex", Metadata: map[string]any{
		"type":                       cliproxyauth.AuthKindAgentIdentity,
		"agent_runtime_id":           "runtime-1",
		"agent_private_key":          material.PrivateKeyPKCS8Base64,
		"task_id":                    "task-1",
		"account_id":                 "account-1",
		"chatgpt_account_is_fedramp": true,
	}}, privateKey.Public().(ed25519.PublicKey)
}

func newAgentIdentityRequest(t *testing.T, path string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com"+path, nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	return req
}

func parseAgentAssertion(t *testing.T, header string) parsedAgentAssertion {
	t.Helper()
	if !strings.HasPrefix(header, "AgentAssertion ") {
		t.Fatalf("Authorization = %q, want AgentAssertion", header)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(header, "AgentAssertion "))
	if err != nil {
		t.Fatalf("decode assertion envelope: %v", err)
	}
	var assertion parsedAgentAssertion
	if err = json.Unmarshal(raw, &assertion); err != nil {
		t.Fatalf("decode assertion JSON: %v", err)
	}
	return assertion
}

func verifyAgentAssertion(t *testing.T, assertion parsedAgentAssertion, publicKey ed25519.PublicKey) {
	t.Helper()
	signature, err := base64.StdEncoding.DecodeString(assertion.Signature)
	if err != nil {
		t.Fatalf("decode assertion signature: %v", err)
	}
	payload := assertion.AgentRuntimeID + ":" + assertion.TaskID + ":" + assertion.Timestamp
	if !ed25519.Verify(publicKey, []byte(payload), signature) {
		t.Fatal("AgentAssertion signature verification failed")
	}
}

func agentIdentityIDToken(t *testing.T, accountID string, userID string, fedRAMP bool) string {
	t.Helper()
	header, _ := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	payload, err := json.Marshal(map[string]any{
		"email": "owner@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id":         accountID,
			"chatgpt_user_id":            userID,
			"chatgpt_plan_type":          "pro",
			"chatgpt_account_is_fedramp": fedRAMP,
		},
	})
	if err != nil {
		t.Fatalf("marshal JWT payload: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}
