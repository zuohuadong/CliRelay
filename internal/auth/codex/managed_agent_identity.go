package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// CodexAuthModeChatGPT is the auth.json mode for managed ChatGPT credentials.
const CodexAuthModeChatGPT = "chatgpt"

// ManagedAgentIdentityState describes whether durable Agent Identity material is ready for use.
type ManagedAgentIdentityState string

const (
	ManagedAgentIdentityStateProvisioning ManagedAgentIdentityState = "provisioning"
	ManagedAgentIdentityStateNeedsTask    ManagedAgentIdentityState = "needs_task"
	ManagedAgentIdentityStateReady        ManagedAgentIdentityState = "ready"
	ManagedAgentIdentityStateError        ManagedAgentIdentityState = "error"
)

// ManagedAgentIdentityCredentials is the canonical flat representation stored by CLIProxyAPI.
// AccountID and ChatGPTUserID describe the current OAuth binding, while the Agent-prefixed
// fields snapshot the account and user that own the registered key. CodexAuthFile converts
// this representation to the nested schema consumed by Codex CLI.
type ManagedAgentIdentityCredentials struct {
	IDToken                 string                    `json:"id_token"`
	AccessToken             string                    `json:"access_token"`
	RefreshToken            string                    `json:"refresh_token"`
	AccountID               string                    `json:"account_id"`
	ChatGPTUserID           string                    `json:"current_chatgpt_user_id"`
	LastRefresh             string                    `json:"last_refresh"`
	AgentRuntimeID          string                    `json:"agent_runtime_id"`
	AgentPrivateKey         string                    `json:"agent_private_key"`
	AgentAccountID          string                    `json:"agent_identity_account_id"`
	AgentChatGPTUserID      string                    `json:"chatgpt_user_id"`
	AgentAccountIsFedRAMP   bool                      `json:"agent_identity_account_is_fedramp"`
	Email                   string                    `json:"email"`
	PlanType                string                    `json:"plan_type"`
	ChatGPTAccountIsFedRAMP bool                      `json:"chatgpt_account_is_fedramp"`
	TaskID                  string                    `json:"task_id,omitempty"`
	State                   ManagedAgentIdentityState `json:"agent_identity_state,omitempty"`
}

// CodexAuthFile is the managed ChatGPT auth.json schema consumed by Codex CLI.
type CodexAuthFile struct {
	AuthMode      string                   `json:"auth_mode"`
	OpenAIAPIKey  *string                  `json:"OPENAI_API_KEY"`
	Tokens        CodexAuthTokens          `json:"tokens"`
	LastRefresh   string                   `json:"last_refresh"`
	AgentIdentity CodexAgentIdentityRecord `json:"agent_identity"`
}

// CodexAuthTokens contains the OAuth token bundle in Codex CLI's nested format.
type CodexAuthTokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
}

// CodexAgentIdentityRecord is the record form of Codex CLI's agent_identity field.
type CodexAgentIdentityRecord struct {
	AgentRuntimeID          string `json:"agent_runtime_id"`
	AgentPrivateKey         string `json:"agent_private_key"`
	AccountID               string `json:"account_id"`
	ChatGPTUserID           string `json:"chatgpt_user_id"`
	Email                   string `json:"email"`
	PlanType                string `json:"plan_type"`
	ChatGPTAccountIsFedRAMP bool   `json:"chatgpt_account_is_fedramp"`
	TaskID                  string `json:"task_id,omitempty"`
}

type managedOAuthIdentity struct {
	idToken      string
	accessToken  string
	refreshToken string
	accountID    string
	userID       string
	email        string
	planType     string
	fedRAMP      bool
}

type storedAgentIdentity struct {
	lastRefresh        string
	runtimeID          string
	privateKey         string
	accountID          string
	userID             string
	fedRAMP            bool
	hasFedRAMPSnapshot bool
	taskID             string
	state              ManagedAgentIdentityState
}

type managedIdentityMetadataReader struct {
	metadata map[string]any
	err      error
}

func (reader *managedIdentityMetadataReader) text(key string) string {
	if reader.err != nil {
		return ""
	}
	value, err := metadataString(reader.metadata, key)
	reader.err = err
	return value
}

func (reader *managedIdentityMetadataReader) timestamp(key string) string {
	if reader.err != nil {
		return ""
	}
	value, err := normalizeMetadataTimestamp(reader.metadata, key)
	reader.err = err
	return value
}

func (reader *managedIdentityMetadataReader) flag(key string) (bool, bool) {
	if reader.err != nil {
		return false, false
	}
	value, present, err := metadataBool(reader.metadata, key)
	reader.err = err
	return value, present
}

// ManagedAgentIdentityCredentialsFromMetadata normalizes a flat CLIProxyAPI credential.
// JWT claims are used as identity metadata only; token validity is established upstream.
func ManagedAgentIdentityCredentialsFromMetadata(metadata map[string]any) (ManagedAgentIdentityCredentials, error) {
	oauthIdentity, claims, err := managedOAuthIdentityFromMetadata(metadata)
	if err != nil {
		return ManagedAgentIdentityCredentials{}, err
	}
	storedIdentity, err := storedAgentIdentityFromMetadata(metadata)
	if err != nil {
		return ManagedAgentIdentityCredentials{}, err
	}
	if err = storedIdentity.bindOwner(metadata, oauthIdentity, claims); err != nil {
		return ManagedAgentIdentityCredentials{}, err
	}
	return managedAgentIdentityCredentials(oauthIdentity, storedIdentity), nil
}

func managedOAuthIdentityFromMetadata(metadata map[string]any) (managedOAuthIdentity, *JWTClaims, error) {
	reader := managedIdentityMetadataReader{metadata: metadata}
	identity := managedOAuthIdentity{
		idToken:      reader.text("id_token"),
		accessToken:  reader.text("access_token"),
		refreshToken: reader.text("refresh_token"),
	}
	if reader.err != nil {
		return identity, nil, reader.err
	}
	if err := validateManagedOAuthTokens(identity); err != nil {
		return identity, nil, err
	}
	claims, err := ParseJWTToken(identity.idToken)
	if err != nil {
		return identity, nil, fmt.Errorf("parse managed Agent Identity id_token: %w", err)
	}
	if err = populateManagedOAuthIdentity(&identity, metadata, claims); err != nil {
		return identity, nil, err
	}
	return identity, claims, nil
}

func validateManagedOAuthTokens(identity managedOAuthIdentity) error {
	required := []struct {
		name  string
		value string
	}{
		{name: "id_token", value: identity.idToken},
		{name: "access_token", value: identity.accessToken},
		{name: "refresh_token", value: identity.refreshToken},
	}
	for _, token := range required {
		if strings.TrimSpace(token.value) == "" {
			return fmt.Errorf("managed Agent Identity credential is missing %s", token.name)
		}
	}
	return nil
}

func populateManagedOAuthIdentity(identity *managedOAuthIdentity, metadata map[string]any, claims *JWTClaims) error {
	reader := managedIdentityMetadataReader{metadata: metadata}
	metadataAccountID := strings.TrimSpace(reader.text("account_id"))
	identity.email = reader.text("email")
	identity.planType = reader.text("plan_type")
	if reader.err != nil {
		return reader.err
	}
	identity.accountID = claims.GetAccountID()
	// An explicitly selected workspace overrides the default account in the ID token.
	if metadataAccountID != "" {
		identity.accountID = metadataAccountID
	}
	identity.userID = claims.GetUserID()
	identity.fedRAMP = claims.IsFedRAMPAccount()
	if claimEmail := claims.GetUserEmail(); claimEmail != "" {
		identity.email = claimEmail
	}
	if claimPlanType := claims.GetPlanType(); claimPlanType != "" {
		identity.planType = claimPlanType
	}
	if identity.accountID == "" {
		return errors.New("managed Agent Identity credential is missing account_id")
	}
	if identity.userID == "" {
		return errors.New("managed Agent Identity credential is missing chatgpt_user_id")
	}
	return nil
}

func storedAgentIdentityFromMetadata(metadata map[string]any) (storedAgentIdentity, error) {
	reader := managedIdentityMetadataReader{metadata: metadata}
	identity := storedAgentIdentity{
		lastRefresh: reader.timestamp("last_refresh"),
		runtimeID:   strings.TrimSpace(reader.text("agent_runtime_id")),
		privateKey:  strings.TrimSpace(reader.text("agent_private_key")),
		accountID:   strings.TrimSpace(reader.text("agent_identity_account_id")),
		userID:      strings.TrimSpace(reader.text("chatgpt_user_id")),
		taskID:      reader.text("task_id"),
		state:       ManagedAgentIdentityState(strings.TrimSpace(reader.text("agent_identity_state"))),
	}
	identity.fedRAMP, identity.hasFedRAMPSnapshot = reader.flag("agent_identity_account_is_fedramp")
	if reader.err != nil {
		return identity, reader.err
	}
	if !validManagedAgentIdentityState(identity.state) {
		return identity, fmt.Errorf("managed Agent Identity credential has unknown state %q", identity.state)
	}
	return identity, nil
}

func validManagedAgentIdentityState(state ManagedAgentIdentityState) bool {
	switch state {
	case "", ManagedAgentIdentityStateProvisioning, ManagedAgentIdentityStateNeedsTask, ManagedAgentIdentityStateReady, ManagedAgentIdentityStateError:
		return true
	default:
		return false
	}
}

func (identity *storedAgentIdentity) bindOwner(metadata map[string]any, current managedOAuthIdentity, claims *JWTClaims) error {
	hasMaterial := identity.runtimeID != "" || identity.privateKey != "" || strings.TrimSpace(identity.taskID) != ""
	if !hasMaterial {
		identity.accountID = current.accountID
		identity.userID = current.userID
		identity.fedRAMP = current.fedRAMP
		return nil
	}
	// Legacy records without owner snapshots belong to the selected account that
	// already carried their registration material.
	if identity.accountID == "" {
		identity.accountID = current.accountID
	}
	if identity.userID == "" {
		identity.userID = current.userID
	}
	if identity.hasFedRAMPSnapshot {
		return nil
	}
	legacyFedRAMP, present, err := metadataBool(metadata, "chatgpt_account_is_fedramp")
	if err != nil {
		return err
	}
	if present {
		identity.fedRAMP = legacyFedRAMP
	} else {
		identity.fedRAMP = claims.IsFedRAMPAccount()
	}
	return nil
}

func managedAgentIdentityCredentials(current managedOAuthIdentity, stored storedAgentIdentity) ManagedAgentIdentityCredentials {
	return ManagedAgentIdentityCredentials{
		IDToken: current.idToken, AccessToken: current.accessToken, RefreshToken: current.refreshToken,
		AccountID: current.accountID, ChatGPTUserID: current.userID, LastRefresh: stored.lastRefresh,
		AgentRuntimeID: stored.runtimeID, AgentPrivateKey: stored.privateKey, AgentAccountID: stored.accountID,
		AgentChatGPTUserID: stored.userID, AgentAccountIsFedRAMP: stored.fedRAMP,
		Email: strings.TrimSpace(current.email), PlanType: normalizeAgentIdentityPlanType(current.planType),
		ChatGPTAccountIsFedRAMP: current.fedRAMP, TaskID: stored.taskID, State: stored.state,
	}
}

func normalizeAgentIdentityPlanType(planType string) string {
	switch strings.ToLower(strings.TrimSpace(planType)) {
	case "":
		return "unknown"
	case "free":
		return "free"
	case "go":
		return "go"
	case "plus":
		return "plus"
	case "pro":
		return "pro"
	case "prolite":
		return "prolite"
	case "team":
		return "team"
	case "self_serve_business_usage_based":
		return "self_serve_business_usage_based"
	case "business":
		return "business"
	case "enterprise_cbp_usage_based":
		return "enterprise_cbp_usage_based"
	case "hc":
		return "enterprise"
	case "enterprise":
		return "enterprise"
	case "education":
		return "edu"
	case "edu":
		return "edu"
	default:
		return "unknown"
	}
}

// CodexAuthFile validates the managed credential and returns the Codex CLI-compatible auth.json form.
func (credentials ManagedAgentIdentityCredentials) CodexAuthFile() (CodexAuthFile, error) {
	if err := credentials.validateForExport(); err != nil {
		return CodexAuthFile{}, err
	}

	lastRefresh, err := time.Parse(time.RFC3339, strings.TrimSpace(credentials.LastRefresh))
	if err != nil {
		return CodexAuthFile{}, errors.New("last_refresh must be a valid RFC3339 timestamp")
	}

	return CodexAuthFile{
		AuthMode:     CodexAuthModeChatGPT,
		OpenAIAPIKey: nil,
		Tokens: CodexAuthTokens{
			IDToken:      credentials.IDToken,
			AccessToken:  credentials.AccessToken,
			RefreshToken: credentials.RefreshToken,
			AccountID:    strings.TrimSpace(credentials.AccountID),
		},
		LastRefresh: lastRefresh.UTC().Format(time.RFC3339Nano),
		AgentIdentity: CodexAgentIdentityRecord{
			AgentRuntimeID:          strings.TrimSpace(credentials.AgentRuntimeID),
			AgentPrivateKey:         strings.TrimSpace(credentials.AgentPrivateKey),
			AccountID:               strings.TrimSpace(credentials.AgentAccountID),
			ChatGPTUserID:           strings.TrimSpace(credentials.AgentChatGPTUserID),
			Email:                   strings.TrimSpace(credentials.Email),
			PlanType:                normalizeAgentIdentityPlanType(credentials.PlanType),
			ChatGPTAccountIsFedRAMP: credentials.AgentAccountIsFedRAMP,
			TaskID:                  credentials.TaskID,
		},
	}, nil
}

// MarshalCodexAuthFile returns an indented auth.json document with a trailing newline.
func (credentials ManagedAgentIdentityCredentials) MarshalCodexAuthFile() ([]byte, error) {
	authFile, err := credentials.CodexAuthFile()
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(authFile, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal Codex auth file: %w", err)
	}
	return append(data, '\n'), nil
}

func (credentials ManagedAgentIdentityCredentials) validateForExport() error {
	if err := credentials.validateExportFields(); err != nil {
		return err
	}
	if err := credentials.validateExportState(); err != nil {
		return err
	}
	return credentials.validateExportBinding()
}

func (credentials ManagedAgentIdentityCredentials) validateExportFields() error {
	required := []struct {
		name  string
		value string
	}{
		{name: "id_token", value: credentials.IDToken},
		{name: "access_token", value: credentials.AccessToken},
		{name: "refresh_token", value: credentials.RefreshToken},
		{name: "account_id", value: credentials.AccountID},
		{name: "last_refresh", value: credentials.LastRefresh},
		{name: "agent_runtime_id", value: credentials.AgentRuntimeID},
		{name: "agent_private_key", value: credentials.AgentPrivateKey},
		{name: "current token chatgpt_user_id", value: credentials.ChatGPTUserID},
		{name: "agent_identity_account_id", value: credentials.AgentAccountID},
		{name: "Agent Identity chatgpt_user_id", value: credentials.AgentChatGPTUserID},
		{name: "plan_type", value: credentials.PlanType},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("managed Agent Identity credential is missing %s", field.name)
		}
	}
	return nil
}

func (credentials ManagedAgentIdentityCredentials) validateExportState() error {
	switch credentials.State {
	case "", ManagedAgentIdentityStateNeedsTask:
		return nil
	case ManagedAgentIdentityStateReady:
		if strings.TrimSpace(credentials.TaskID) == "" {
			return errors.New("managed Agent Identity credential is ready but missing task_id")
		}
		return nil
	case ManagedAgentIdentityStateProvisioning, ManagedAgentIdentityStateError:
		return fmt.Errorf("managed Agent Identity credential is not exportable in state %q", credentials.State)
	default:
		return fmt.Errorf("managed Agent Identity credential has unknown state %q", credentials.State)
	}
}

func (credentials ManagedAgentIdentityCredentials) validateExportBinding() error {
	if _, err := ParseAgentIdentityPrivateKey(credentials.AgentPrivateKey); err != nil {
		return fmt.Errorf("managed Agent Identity credential has invalid agent_private_key: %w", err)
	}
	if strings.TrimSpace(credentials.AgentAccountID) != strings.TrimSpace(credentials.AccountID) {
		return errors.New("managed Agent Identity account binding does not match the selected account")
	}
	if strings.TrimSpace(credentials.AgentChatGPTUserID) != strings.TrimSpace(credentials.ChatGPTUserID) {
		return errors.New("managed Agent Identity user binding does not match the current token")
	}
	if credentials.AgentAccountIsFedRAMP != credentials.ChatGPTAccountIsFedRAMP {
		return errors.New("managed Agent Identity FedRAMP binding does not match the current token")
	}
	return nil
}

func metadataString(metadata map[string]any, key string) (string, error) {
	value, ok := metadata[key]
	if !ok || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("managed Agent Identity field %s must be a string", key)
	}
	return text, nil
}

func metadataBool(metadata map[string]any, key string) (bool, bool, error) {
	value, ok := metadata[key]
	if !ok || value == nil {
		return false, false, nil
	}
	flag, ok := value.(bool)
	if !ok {
		return false, false, fmt.Errorf("managed Agent Identity field %s must be a boolean", key)
	}
	return flag, true, nil
}

func normalizeMetadataTimestamp(metadata map[string]any, key string) (string, error) {
	value, ok := metadata[key]
	if !ok || value == nil {
		return "", nil
	}
	var timestamp time.Time
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return "", nil
		}
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(typed))
		if err != nil {
			return "", fmt.Errorf("managed Agent Identity field %s must be an RFC3339 timestamp", key)
		}
		timestamp = parsed
	case time.Time:
		if typed.IsZero() {
			return "", nil
		}
		timestamp = typed
	default:
		return "", fmt.Errorf("managed Agent Identity field %s must be a string or time.Time", key)
	}
	return timestamp.UTC().Format(time.RFC3339Nano), nil
}
