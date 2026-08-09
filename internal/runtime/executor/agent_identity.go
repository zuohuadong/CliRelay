package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type agentIdentityCreds struct {
	runtimeID     string
	privateKeyB64 string
	taskID        string
	accountID     string
	fedRAMP       bool
}

const (
	codexBearerAuthScheme         = "Bearer"
	codexAgentAssertionAuthScheme = "AgentAssertion"
)

func agentIdentityMetadataString(auth *cliproxyauth.Auth, keys ...string) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	for _, key := range keys {
		if value, ok := auth.Metadata[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func agentIdentityCredsFromAuth(auth *cliproxyauth.Auth) agentIdentityCreds {
	return agentIdentityCreds{
		runtimeID:     agentIdentityMetadataString(auth, "agent_runtime_id"),
		privateKeyB64: agentIdentityMetadataString(auth, "agent_private_key", "private_key_pkcs8_base64", "private_key"),
		taskID:        agentIdentityMetadataString(auth, "task_id"),
		accountID:     agentIdentityMetadataString(auth, "agent_identity_account_id", "account_id", "chatgpt_account_id"),
		fedRAMP:       agentIdentityMetadataBool(auth, "chatgpt_account_is_fedramp"),
	}
}

func agentIdentityMetadataBool(auth *cliproxyauth.Auth, key string) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	value, _ := auth.Metadata[key].(bool)
	return value
}

func agentIdentityRequestCredentials(auth *cliproxyauth.Auth) (*agentIdentityCreds, error) {
	if cliproxyauth.IsStandaloneAgentIdentityAuth(auth) {
		credentials := agentIdentityCredsFromAuth(auth)
		if err := credentials.validateRequest(); err != nil {
			return nil, err
		}
		return &credentials, nil
	}
	if agentIdentityMetadataString(auth, "agent_identity_state") != string(codexauth.ManagedAgentIdentityStateReady) {
		return nil, nil
	}
	return managedAgentIdentityRequestCredentials(auth)
}

func managedAgentIdentityRequestCredentials(auth *cliproxyauth.Auth) (*agentIdentityCreds, error) {
	managed, err := codexauth.ManagedAgentIdentityCredentialsFromMetadata(auth.Metadata)
	if err != nil {
		return nil, fmt.Errorf("managed Agent Identity is invalid: %w", err)
	}
	ownerAccountID := agentIdentityMetadataString(auth, "agent_identity_account_id")
	ownerUserID := agentIdentityMetadataString(auth, "chatgpt_user_id")
	if ownerAccountID == "" || ownerUserID == "" {
		return nil, fmt.Errorf("managed Agent Identity owner binding is missing")
	}
	if managed.AccountID != ownerAccountID || managed.ChatGPTUserID != ownerUserID {
		return nil, fmt.Errorf("managed Agent Identity owner binding does not match the current OAuth identity")
	}
	if managed.AgentAccountIsFedRAMP != managed.ChatGPTAccountIsFedRAMP {
		return nil, fmt.Errorf("managed Agent Identity FedRAMP binding does not match the current OAuth identity")
	}
	credentials := agentIdentityCreds{
		runtimeID:     managed.AgentRuntimeID,
		privateKeyB64: managed.AgentPrivateKey,
		taskID:        managed.TaskID,
		accountID:     ownerAccountID,
		fedRAMP:       managed.AgentAccountIsFedRAMP,
	}
	if err = credentials.validateRequest(); err != nil {
		return nil, err
	}
	return &credentials, nil
}

func (credentials agentIdentityCreds) validateSigningMaterial() error {
	if credentials.runtimeID == "" || credentials.privateKeyB64 == "" || credentials.taskID == "" {
		return fmt.Errorf("agent identity auth: missing agent_runtime_id, agent_private_key or task_id")
	}
	return nil
}

func (credentials agentIdentityCreds) validateRequest() error {
	if err := credentials.validateSigningMaterial(); err != nil {
		return err
	}
	if credentials.accountID == "" {
		return fmt.Errorf("agent identity auth: missing account ID")
	}
	return nil
}

func requestSupportsAgentAssertion(req *http.Request) bool {
	if req == nil || req.URL == nil || req.URL.User != nil || req.Method != http.MethodPost {
		return false
	}
	scheme := strings.ToLower(strings.TrimSpace(req.URL.Scheme))
	if scheme != "https" && scheme != "wss" {
		return false
	}
	if port := req.URL.Port(); port != "" && port != "443" {
		return false
	}
	switch strings.ToLower(req.URL.Hostname()) {
	case "chatgpt.com", "chat.openai.com", "chatgpt-staging.com":
	default:
		return false
	}
	path := strings.TrimRight(req.URL.Path, "/")
	return strings.HasSuffix(path, "/responses") || strings.HasSuffix(path, "/responses/compact")
}

func sealCodexAuthenticationHeaders(req *http.Request, auth *cliproxyauth.Auth, token string) error {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Del("ChatGPT-Account-ID")
	if codexAuthHasAgentIdentity(auth) {
		req.Header.Del("X-OpenAI-Fedramp")
	}
	if !codexAuthUsesAPIKey(auth) {
		if accountID := codexAccountIDFromAuth(auth); accountID != "" {
			req.Header.Set("ChatGPT-Account-ID", accountID)
		}
	}
	return applyAgentIdentityRequestHeaders(req, auth)
}

func codexAuthHasAgentIdentity(auth *cliproxyauth.Auth) bool {
	return cliproxyauth.IsStandaloneAgentIdentityAuth(auth) ||
		agentIdentityMetadataString(auth, "agent_identity_state", "agent_runtime_id") != ""
}

func applyAgentIdentityRequestHeaders(req *http.Request, auth *cliproxyauth.Auth) error {
	if !requestSupportsAgentAssertion(req) {
		if cliproxyauth.IsStandaloneAgentIdentityAuth(auth) {
			return fmt.Errorf("agent identity auth: endpoint does not support AgentAssertion")
		}
		return nil
	}
	credentials, err := agentIdentityRequestCredentials(auth)
	if err != nil || credentials == nil {
		return err
	}
	assertion, err := buildAgentAssertion(*credentials, time.Now())
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", assertion)
	req.Header.Set("ChatGPT-Account-ID", credentials.accountID)
	if credentials.fedRAMP {
		req.Header.Set("X-OpenAI-Fedramp", "true")
	} else {
		req.Header.Del("X-OpenAI-Fedramp")
	}
	return nil
}

func applyAgentIdentityWebsocketHeaders(headers http.Header, auth *cliproxyauth.Auth, wsURL string) (http.Header, error) {
	target, err := url.Parse(wsURL)
	if err != nil {
		return nil, fmt.Errorf("agent identity auth: parse websocket URL: %w", err)
	}
	if headers == nil {
		headers = make(http.Header)
	} else {
		headers = headers.Clone()
	}
	req := &http.Request{Method: http.MethodPost, URL: target, Header: headers}
	if err = applyAgentIdentityRequestHeaders(req, auth); err != nil {
		return nil, err
	}
	return req.Header, nil
}

func agentIdentityRequestAuthScheme(auth *cliproxyauth.Auth) string {
	credentials, err := agentIdentityRequestCredentials(auth)
	if err == nil && credentials != nil {
		return codexAgentAssertionAuthScheme
	}
	return codexBearerAuthScheme
}

// RenewAgentIdentityTask registers and returns a replacement task for invalid_task_id recovery.
func (e *CodexExecutor) RenewAgentIdentityTask(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	resolvedAuth, err := e.resolveEgressAuth(ctx, auth)
	if err != nil {
		return nil, err
	}
	credentials, err := agentIdentityRequestCredentials(resolvedAuth)
	if err != nil {
		return nil, fmt.Errorf("renew Agent Identity task: credential is not ready: %w", err)
	}
	if credentials == nil {
		return nil, fmt.Errorf("renew Agent Identity task: credential is not ready")
	}
	client, err := e.outboundHTTPClient(ctx, resolvedAuth, 0, 0, false)
	if err != nil {
		return nil, err
	}
	registrar := codexauth.NewAgentIdentityClient(client)
	taskID, err := registrar.RegisterTask(ctx, codexauth.AgentIdentityKey{
		AgentRuntimeID:        credentials.runtimeID,
		PrivateKeyPKCS8Base64: credentials.privateKeyB64,
	})
	if err != nil {
		return nil, fmt.Errorf("renew Agent Identity task: %w", err)
	}
	if strings.TrimSpace(taskID) == "" {
		return nil, fmt.Errorf("renew Agent Identity task: registration returned an empty task ID")
	}
	updated := auth.Clone()
	updated.Metadata["task_id"] = strings.TrimSpace(taskID)
	return updated, nil
}

func buildAgentAssertion(credentials agentIdentityCreds, now time.Time) (string, error) {
	if err := credentials.validateSigningMaterial(); err != nil {
		return "", err
	}
	return codexauth.AgentAssertionAuthorization(codexauth.AgentIdentityKey{
		AgentRuntimeID:        credentials.runtimeID,
		PrivateKeyPKCS8Base64: credentials.privateKeyB64,
	}, credentials.taskID, now)
}
