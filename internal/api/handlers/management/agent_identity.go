package management

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

const codexAgentIdentityHarnessID = "codex-cli"

var (
	errAgentIdentityAuthNotFound = errors.New("auth credential not found")
	errAgentIdentityAuthConflict = errors.New("auth index identifies multiple credentials")
	errAgentIdentityRequestBody  = errors.New("invalid request body")
	errAgentIdentityAuthRequired = errors.New("auth_index is required")
)

type agentIdentityRegistrar interface {
	RegisterAgent(context.Context, codex.AgentRegistration) (string, error)
	RegisterTask(context.Context, codex.AgentIdentityKey) (string, error)
}

type agentIdentityRegistrarFactory func(*coreauth.Auth) agentIdentityRegistrar

type provisionAgentIdentityRequest struct {
	AuthIndex    string `json:"auth_index"`
	RegisterTask *bool  `json:"register_task"`
	Force        bool   `json:"force"`
}

type exportAgentIdentityRequest struct {
	AuthIndex string `json:"auth_index"`
}

type provisionAgentIdentityResult struct {
	Status           codex.ManagedAgentIdentityState
	AuthIndex        string
	HasTask          bool
	Reused           bool
	ReplacedExisting bool
}

type provisionAgentIdentityOptions struct {
	registerTask bool
	force        bool
}

type provisionAgentIdentityInput struct {
	transaction *coreauth.MetadataTransaction
	auth        *coreauth.Auth
	credentials codex.ManagedAgentIdentityCredentials
	options     provisionAgentIdentityOptions
}

type agentIdentityMaterialState struct {
	hasRuntime             bool
	hasPrivateKey          bool
	hasTask                bool
	hasAny                 bool
	bindingSnapshotMissing bool
}

// ProvisionAgentIdentity creates and persists a managed Codex Agent Identity.
func (h *Handler) ProvisionAgentIdentity(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	request, err := decodeProvisionAgentIdentityRequest(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	result, err := h.executeAgentIdentityProvision(c.Request.Context(), request)
	if err != nil {
		status, payload := agentIdentityProvisionErrorResponse(err, request.AuthIndex)
		c.JSON(status, payload)
		return
	}

	c.JSON(http.StatusOK, agentIdentityProvisionPayload(result))
}

func decodeProvisionAgentIdentityRequest(c *gin.Context) (provisionAgentIdentityRequest, error) {
	var request provisionAgentIdentityRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		return request, errAgentIdentityRequestBody
	}
	request.AuthIndex = strings.TrimSpace(request.AuthIndex)
	if request.AuthIndex == "" {
		return request, errAgentIdentityAuthRequired
	}
	return request, nil
}

func (h *Handler) executeAgentIdentityProvision(ctx context.Context, request provisionAgentIdentityRequest) (provisionAgentIdentityResult, error) {
	var result provisionAgentIdentityResult
	var persisted bool
	finalAuth, err := h.authManager.WithMetadataTransactionByIndex(ctx, request.AuthIndex, func(transaction *coreauth.MetadataTransaction) error {
		var errProvision error
		result, errProvision = h.provisionAgentIdentityTransaction(ctx, transaction, request)
		persisted = transaction.Persisted()
		return errProvision
	})
	return result, h.runAgentIdentityPostPersistHook(ctx, finalAuth, persisted, err)
}

func (h *Handler) provisionAgentIdentityTransaction(ctx context.Context, transaction *coreauth.MetadataTransaction, request provisionAgentIdentityRequest) (provisionAgentIdentityResult, error) {
	auth := transaction.Auth()
	if err := validateProvisionAgentIdentityAuth(auth); err != nil {
		return provisionAgentIdentityResult{}, err
	}
	credentials, err := codex.ManagedAgentIdentityCredentialsFromMetadata(auth.Metadata)
	if err != nil {
		return provisionAgentIdentityResult{}, &agentIdentityCredentialError{cause: err}
	}
	return h.provisionManagedAgentIdentity(ctx, provisionAgentIdentityInput{
		transaction: transaction,
		auth:        auth,
		credentials: credentials,
		options: provisionAgentIdentityOptions{
			registerTask: request.RegisterTask == nil || *request.RegisterTask,
			force:        request.Force,
		},
	})
}

func validateProvisionAgentIdentityAuth(auth *coreauth.Auth) error {
	if auth == nil {
		return errAgentIdentityAuthNotFound
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return &agentIdentityStateError{message: "auth credential is not a Codex credential"}
	}
	if coreauth.IsPluginVirtualAuth(auth) || isRuntimeOnlyAuth(auth) {
		return &agentIdentityStateError{message: "auth credential cannot be modified directly"}
	}
	return nil
}

func (h *Handler) runAgentIdentityPostPersistHook(ctx context.Context, auth *coreauth.Auth, persisted bool, provisionErr error) error {
	if !persisted || auth == nil || h.postAuthPersistHook == nil {
		return provisionErr
	}
	if err := h.postAuthPersistHook(ctx, auth.Clone()); err != nil && provisionErr == nil {
		return fmt.Errorf("post-auth persist hook failed: %w", err)
	}
	return provisionErr
}

func agentIdentityProvisionErrorResponse(err error, authIndex string) (int, gin.H) {
	switch {
	case errors.Is(err, coreauth.ErrAuthStoreUnavailable):
		return http.StatusServiceUnavailable, gin.H{"error": "durable auth store unavailable"}
	case errors.Is(err, coreauth.ErrAuthIndexNotFound), errors.Is(err, errAgentIdentityAuthNotFound):
		return http.StatusNotFound, gin.H{"error": "auth credential not found"}
	case errors.Is(err, coreauth.ErrAuthIndexAmbiguous), errors.Is(err, errAgentIdentityAuthConflict):
		return http.StatusConflict, gin.H{"error": "auth_index is not unique"}
	}
	var upstreamError *agentIdentityUpstreamError
	if errors.As(err, &upstreamError) {
		return http.StatusBadGateway, agentIdentityUpstreamErrorPayload(upstreamError, authIndex)
	}
	var conflictError *agentIdentityStateError
	if errors.As(err, &conflictError) {
		return http.StatusConflict, gin.H{"error": conflictError.Error()}
	}
	var credentialError *agentIdentityCredentialError
	if errors.As(err, &credentialError) {
		return http.StatusUnprocessableEntity, gin.H{"error": "invalid Codex credential"}
	}
	return http.StatusInternalServerError, gin.H{"error": "failed to persist Agent Identity credential"}
}

func agentIdentityUpstreamErrorPayload(upstreamError *agentIdentityUpstreamError, authIndex string) gin.H {
	payload := gin.H{
		"error":              upstreamError.message,
		"status":             upstreamError.state,
		"auth_index":         authIndex,
		"has_tokens":         true,
		"has_agent_identity": upstreamError.hasIdentity,
		"has_task":           upstreamError.hasTask,
	}
	if upstreamError.preservedExisting {
		payload["preserved_existing"] = true
	}
	return payload
}

func agentIdentityProvisionPayload(result provisionAgentIdentityResult) gin.H {
	payload := gin.H{
		"status":             result.Status,
		"auth_index":         result.AuthIndex,
		"has_tokens":         true,
		"has_agent_identity": true,
		"has_task":           result.HasTask,
		"reused":             result.Reused,
	}
	if result.ReplacedExisting {
		payload["replaced_existing"] = true
		payload["previous_identity_revoked"] = false
		payload["rotation_scope"] = "local_credential_only"
	}
	return payload
}

// ExportAgentIdentityAuth returns a Codex CLI-compatible auth.json representation.
func (h *Handler) ExportAgentIdentityAuth(c *gin.Context) {
	if !requireAdminManagementCredential(c) {
		return
	}
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	if c.Request != nil && strings.TrimSpace(c.Request.URL.RawQuery) != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query parameters are not allowed for Agent Identity export"})
		return
	}
	request, err := decodeExportAgentIdentityRequest(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	data, err := h.exportAgentIdentityData(request.AuthIndex)
	if err != nil {
		status, payload := agentIdentityExportErrorResponse(err)
		c.JSON(status, payload)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="auth.json"`)
	c.Header("Cache-Control", "no-store, private")
	c.Header("Pragma", "no-cache")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, "application/json", data)
}

func decodeExportAgentIdentityRequest(c *gin.Context) (exportAgentIdentityRequest, error) {
	var request exportAgentIdentityRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		return request, errAgentIdentityRequestBody
	}
	request.AuthIndex = strings.TrimSpace(request.AuthIndex)
	if request.AuthIndex == "" {
		return request, errAgentIdentityAuthRequired
	}
	return request, nil
}

func (h *Handler) exportAgentIdentityData(authIndex string) ([]byte, error) {
	auth, err := h.agentIdentityAuthByIndex(authIndex)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return nil, &agentIdentityStateError{message: "auth credential is not a Codex credential"}
	}
	credentials, err := codex.ManagedAgentIdentityCredentialsFromMetadata(auth.Metadata)
	if err != nil {
		return nil, &agentIdentityCredentialError{cause: err}
	}
	credentials.TaskID = ""
	credentials.State = codex.ManagedAgentIdentityStateNeedsTask
	data, err := credentials.MarshalCodexAuthFile()
	if err != nil {
		return nil, &agentIdentityStateError{message: "managed Agent Identity is not ready for export"}
	}
	return data, nil
}

func agentIdentityExportErrorResponse(err error) (int, gin.H) {
	switch {
	case errors.Is(err, errAgentIdentityAuthNotFound):
		return http.StatusNotFound, gin.H{"error": "auth credential not found"}
	case errors.Is(err, errAgentIdentityAuthConflict):
		return http.StatusConflict, gin.H{"error": "auth_index is not unique"}
	}
	var stateError *agentIdentityStateError
	if errors.As(err, &stateError) {
		return http.StatusConflict, gin.H{"error": stateError.Error()}
	}
	var credentialError *agentIdentityCredentialError
	if errors.As(err, &credentialError) {
		return http.StatusUnprocessableEntity, gin.H{"error": "invalid Codex credential"}
	}
	return http.StatusInternalServerError, gin.H{"error": "failed to resolve auth credential"}
}

func (h *Handler) provisionManagedAgentIdentity(ctx context.Context, input provisionAgentIdentityInput) (provisionAgentIdentityResult, error) {
	result := provisionAgentIdentityResult{AuthIndex: input.auth.EnsureIndex()}
	material := inspectAgentIdentityMaterial(input.auth, input.credentials)
	previousState := input.credentials.State
	if err := validateAgentIdentityMaterial(input.credentials, material, input.options.force); err != nil {
		return result, err
	}
	if !input.options.force && material.hasRuntime && material.hasPrivateKey {
		return h.reuseManagedAgentIdentity(ctx, input, material)
	}
	credentials, keyMaterial, err := prepareAgentIdentityRegistration(input.credentials, material, input.options.force)
	if err != nil {
		return result, err
	}
	input.credentials = credentials
	result, err = h.registerManagedAgentIdentity(ctx, input, keyMaterial)
	if input.options.force && material.hasAny {
		if err == nil {
			result.ReplacedExisting = true
		} else {
			markPreservedAgentIdentity(err, material, previousState)
		}
	}
	return result, err
}

func markPreservedAgentIdentity(err error, material agentIdentityMaterialState, state codex.ManagedAgentIdentityState) {
	var upstreamError *agentIdentityUpstreamError
	if !errors.As(err, &upstreamError) {
		return
	}
	upstreamError.hasIdentity = material.hasRuntime && material.hasPrivateKey
	upstreamError.hasTask = material.hasTask
	upstreamError.preservedExisting = true
	upstreamError.state = state
	if upstreamError.state == "" {
		upstreamError.state = codex.ManagedAgentIdentityStateNeedsTask
		if material.hasTask {
			upstreamError.state = codex.ManagedAgentIdentityStateReady
		}
	}
}

func inspectAgentIdentityMaterial(auth *coreauth.Auth, credentials codex.ManagedAgentIdentityCredentials) agentIdentityMaterialState {
	material := agentIdentityMaterialState{
		hasRuntime:    strings.TrimSpace(credentials.AgentRuntimeID) != "",
		hasPrivateKey: strings.TrimSpace(credentials.AgentPrivateKey) != "",
		hasTask:       strings.TrimSpace(credentials.TaskID) != "",
	}
	material.hasAny = material.hasRuntime || material.hasPrivateKey || material.hasTask
	material.bindingSnapshotMissing = material.hasAny && agentIdentityBindingSnapshotMissing(auth.Metadata)
	return material
}

func validateAgentIdentityMaterial(credentials codex.ManagedAgentIdentityCredentials, material agentIdentityMaterialState, force bool) error {
	if force {
		return nil
	}
	accountChanged := strings.TrimSpace(credentials.AgentAccountID) != strings.TrimSpace(credentials.AccountID)
	userChanged := strings.TrimSpace(credentials.AgentChatGPTUserID) != strings.TrimSpace(credentials.ChatGPTUserID)
	fedRAMPChanged := credentials.AgentAccountIsFedRAMP != credentials.ChatGPTAccountIsFedRAMP
	if material.hasAny && (accountChanged || userChanged || fedRAMPChanged) {
		return &agentIdentityStateError{message: "managed Agent Identity belongs to a different ChatGPT account; re-provision with force=true"}
	}
	if material.hasRuntime && !material.hasPrivateKey {
		return &agentIdentityStateError{message: "managed Agent Identity is missing its private key; re-provision with force=true"}
	}
	if material.hasTask && !material.hasRuntime && !material.hasPrivateKey {
		return &agentIdentityStateError{message: "managed Agent Identity has a task without registration material; re-provision with force=true"}
	}
	return nil
}

func (h *Handler) reuseManagedAgentIdentity(ctx context.Context, input provisionAgentIdentityInput, material agentIdentityMaterialState) (provisionAgentIdentityResult, error) {
	if _, err := codex.ParseAgentIdentityPrivateKey(input.credentials.AgentPrivateKey); err != nil {
		return provisionAgentIdentityResult{AuthIndex: input.auth.EnsureIndex()}, &agentIdentityStateError{message: "managed Agent Identity private key is invalid; re-provision with force=true"}
	}
	desiredState := codex.ManagedAgentIdentityStateNeedsTask
	if material.hasTask {
		desiredState = codex.ManagedAgentIdentityStateReady
	}
	needsPersist := strings.TrimSpace(input.credentials.LastRefresh) == "" || input.credentials.State != desiredState || material.bindingSnapshotMissing
	input.credentials.State = desiredState
	if strings.TrimSpace(input.credentials.LastRefresh) == "" {
		input.credentials.LastRefresh = time.Now().UTC().Format(time.RFC3339)
	}
	result := provisionAgentIdentityResult{Status: desiredState, AuthIndex: input.auth.EnsureIndex(), HasTask: material.hasTask, Reused: true}
	if material.hasTask || !input.options.registerTask {
		return persistAgentIdentityIfNeeded(input.transaction, input.credentials, result, needsPersist)
	}
	credentials, result, err := h.registerAgentIdentityTask(ctx, input.auth, input.credentials, true)
	return persistAgentIdentityProvision(input.transaction, credentials, result, err)
}

func prepareAgentIdentityRegistration(credentials codex.ManagedAgentIdentityCredentials, material agentIdentityMaterialState, force bool) (codex.ManagedAgentIdentityCredentials, codex.AgentKeyMaterial, error) {
	if !force && material.hasPrivateKey {
		publicKey, err := codex.PublicKeySSHFromPrivateKey(credentials.AgentPrivateKey)
		if err != nil {
			return credentials, codex.AgentKeyMaterial{}, &agentIdentityStateError{message: "managed Agent Identity private key is invalid; re-provision with force=true"}
		}
		return credentials, codex.AgentKeyMaterial{PrivateKeyPKCS8Base64: credentials.AgentPrivateKey, PublicKeySSH: publicKey}, nil
	}
	keyMaterial, err := codex.GenerateAgentKeyMaterial()
	if err != nil {
		return credentials, codex.AgentKeyMaterial{}, err
	}
	credentials.AgentPrivateKey = keyMaterial.PrivateKeyPKCS8Base64
	credentials.AgentRuntimeID = ""
	credentials.TaskID = ""
	credentials.AgentAccountID = strings.TrimSpace(credentials.AccountID)
	credentials.AgentChatGPTUserID = strings.TrimSpace(credentials.ChatGPTUserID)
	credentials.AgentAccountIsFedRAMP = credentials.ChatGPTAccountIsFedRAMP
	credentials.State = codex.ManagedAgentIdentityStateProvisioning
	return credentials, keyMaterial, nil
}

func (h *Handler) registerManagedAgentIdentity(ctx context.Context, input provisionAgentIdentityInput, keyMaterial codex.AgentKeyMaterial) (provisionAgentIdentityResult, error) {
	result := provisionAgentIdentityResult{AuthIndex: input.auth.EnsureIndex()}
	runtimeID, err := h.registerAgentRuntime(ctx, input.auth, input.credentials, keyMaterial)
	if err != nil {
		input.credentials.State = codex.ManagedAgentIdentityStateError
		upstreamErr := &agentIdentityUpstreamError{message: "Agent Identity registration failed", state: codex.ManagedAgentIdentityStateError, cause: err}
		if input.options.force {
			return result, upstreamErr
		}
		return persistAgentIdentityProvision(input.transaction, input.credentials, result, upstreamErr)
	}
	input.credentials.AgentRuntimeID = runtimeID
	input.credentials.AgentPrivateKey = keyMaterial.PrivateKeyPKCS8Base64
	input.credentials.TaskID = ""
	input.credentials.State = codex.ManagedAgentIdentityStateNeedsTask
	if strings.TrimSpace(input.credentials.LastRefresh) == "" {
		input.credentials.LastRefresh = time.Now().UTC().Format(time.RFC3339)
	}
	if !input.options.registerTask {
		result.Status = codex.ManagedAgentIdentityStateNeedsTask
		return persistAgentIdentityProvision(input.transaction, input.credentials, result, nil)
	}
	credentials, result, err := h.registerAgentIdentityTask(ctx, input.auth, input.credentials, false)
	return persistAgentIdentityProvision(input.transaction, credentials, result, err)
}

func (h *Handler) registerAgentRuntime(ctx context.Context, auth *coreauth.Auth, credentials codex.ManagedAgentIdentityCredentials, keyMaterial codex.AgentKeyMaterial) (string, error) {
	registrar, err := h.agentIdentityRegistrarFor(ctx, auth)
	if err != nil {
		return "", err
	}
	runtimeID, err := registrar.RegisterAgent(ctx, codex.AgentRegistration{
		AccessToken:      credentials.AccessToken,
		IsFedRAMPAccount: credentials.AgentAccountIsFedRAMP,
		KeyMaterial:      keyMaterial,
		BillOfMaterials: codex.AgentBillOfMaterials{
			AgentVersion:    agentIdentityVersion(),
			AgentHarnessID:  codexAgentIdentityHarnessID,
			RunningLocation: "custom-" + runtime.GOOS,
		},
	})
	if err == nil && strings.TrimSpace(runtimeID) == "" {
		err = errors.New("Agent Identity registration returned an empty runtime ID")
	}
	return strings.TrimSpace(runtimeID), err
}

func agentIdentityBindingSnapshotMissing(metadata map[string]any) bool {
	accountID, _ := metadata["agent_identity_account_id"].(string)
	userID, _ := metadata["chatgpt_user_id"].(string)
	_, hasFedRAMP := metadata["agent_identity_account_is_fedramp"]
	return strings.TrimSpace(accountID) == "" || strings.TrimSpace(userID) == "" || !hasFedRAMP
}

func (h *Handler) registerAgentIdentityTask(ctx context.Context, auth *coreauth.Auth, credentials codex.ManagedAgentIdentityCredentials, reused bool) (codex.ManagedAgentIdentityCredentials, provisionAgentIdentityResult, error) {
	result := provisionAgentIdentityResult{
		Status:    codex.ManagedAgentIdentityStateNeedsTask,
		AuthIndex: auth.EnsureIndex(),
		Reused:    reused,
	}
	credentials.TaskID = ""
	registrar, err := h.agentIdentityRegistrarFor(ctx, auth)
	var taskID string
	if err == nil {
		taskID, err = registrar.RegisterTask(ctx, codex.AgentIdentityKey{
			AgentRuntimeID:        credentials.AgentRuntimeID,
			PrivateKeyPKCS8Base64: credentials.AgentPrivateKey,
		})
	}
	if err == nil && strings.TrimSpace(taskID) == "" {
		err = errors.New("task registration returned an empty task ID")
	}
	if err != nil {
		credentials.State = codex.ManagedAgentIdentityStateNeedsTask
		return credentials, result, &agentIdentityUpstreamError{
			message:     "Agent Identity task registration failed",
			state:       codex.ManagedAgentIdentityStateNeedsTask,
			hasIdentity: true,
			cause:       err,
		}
	}

	credentials.TaskID = strings.TrimSpace(taskID)
	credentials.State = codex.ManagedAgentIdentityStateReady
	result.Status = codex.ManagedAgentIdentityStateReady
	result.HasTask = true
	return credentials, result, nil
}

func persistAgentIdentityIfNeeded(transaction *coreauth.MetadataTransaction, credentials codex.ManagedAgentIdentityCredentials, result provisionAgentIdentityResult, needed bool) (provisionAgentIdentityResult, error) {
	if !needed {
		return result, nil
	}
	return persistAgentIdentityProvision(transaction, credentials, result, nil)
}

func persistAgentIdentityProvision(transaction *coreauth.MetadataTransaction, credentials codex.ManagedAgentIdentityCredentials, result provisionAgentIdentityResult, provisionErr error) (provisionAgentIdentityResult, error) {
	if _, err := mergeAgentIdentityMetadata(transaction, credentials); err != nil {
		return result, err
	}
	return result, provisionErr
}

func mergeAgentIdentityMetadata(transaction *coreauth.MetadataTransaction, credentials codex.ManagedAgentIdentityCredentials) (*coreauth.Auth, error) {
	updates := map[string]any{
		"auth_mode":                         codex.CodexAuthModeChatGPT,
		"agent_identity_account_id":         strings.TrimSpace(credentials.AgentAccountID),
		"agent_runtime_id":                  strings.TrimSpace(credentials.AgentRuntimeID),
		"agent_private_key":                 strings.TrimSpace(credentials.AgentPrivateKey),
		"chatgpt_user_id":                   strings.TrimSpace(credentials.AgentChatGPTUserID),
		"agent_identity_account_is_fedramp": credentials.AgentAccountIsFedRAMP,
		"email":                             strings.TrimSpace(credentials.Email),
		"plan_type":                         strings.TrimSpace(credentials.PlanType),
		"chatgpt_account_is_fedramp":        credentials.ChatGPTAccountIsFedRAMP,
		"task_id":                           credentials.TaskID,
		"agent_identity_state":              string(credentials.State),
		"last_refresh":                      strings.TrimSpace(credentials.LastRefresh),
	}
	return transaction.Merge(updates)
}

func (h *Handler) agentIdentityAuthByIndex(authIndex string) (*coreauth.Auth, error) {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" || h == nil || h.authManager == nil {
		return nil, errAgentIdentityAuthNotFound
	}
	var match *coreauth.Auth
	for _, auth := range h.authManager.List() {
		if auth == nil || auth.EnsureIndex() != authIndex {
			continue
		}
		if match != nil {
			return nil, errAgentIdentityAuthConflict
		}
		match = auth
	}
	if match == nil {
		return nil, errAgentIdentityAuthNotFound
	}
	return match, nil
}

func (h *Handler) agentIdentityRegistrarFor(ctx context.Context, auth *coreauth.Auth) (agentIdentityRegistrar, error) {
	if h == nil {
		return nil, egress.RuntimeError(fmt.Errorf("%w: management handler is unavailable", egress.ErrEgressRequired))
	}
	if h.agentIdentityRegistrar != nil {
		return h.agentIdentityRegistrar(auth), nil
	}
	client, err := h.agentIdentityHTTPClient(ctx, auth)
	if err != nil {
		return nil, err
	}
	return codex.NewAgentIdentityClient(client), nil
}

func (h *Handler) agentIdentityHTTPClient(ctx context.Context, auth *coreauth.Auth) (*http.Client, error) {
	if agentIdentityUsesSharedProxy(auth) {
		return h.agentIdentitySharedProxyClient(auth)
	}
	accountID, err := agentIdentityAccountID(auth)
	if err != nil {
		return nil, err
	}
	service := h.egress()
	if service == nil {
		return nil, egress.RuntimeError(fmt.Errorf("%w: Codex egress resolver is unavailable", egress.ErrEgressRequired))
	}
	resolved, err := service.Resolve(ctx, accountID)
	if err != nil {
		return nil, egress.RuntimeError(err)
	}
	client, err := service.HTTPClient(ctx, resolved.Endpoint.ID, 0)
	if err != nil {
		return nil, egress.RuntimeError(err)
	}
	return client, nil
}

func agentIdentityAccountID(auth *coreauth.Auth) (string, error) {
	if auth == nil {
		return "", egress.RuntimeError(fmt.Errorf("%w: Codex auth is missing", egress.ErrEgressRequired))
	}
	accountID := codex.AccountIDFromMetadata(auth.Metadata)
	if accountID == "" {
		return "", egress.RuntimeError(fmt.Errorf("%w: Codex auth has no ChatGPT account ID", egress.ErrEgressRequired))
	}
	return accountID, nil
}

func agentIdentityUsesSharedProxy(auth *coreauth.Auth) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	mode, _ := auth.Metadata["egress_mode"].(string)
	return strings.EqualFold(strings.TrimSpace(mode), "shared_proxy")
}

func (h *Handler) agentIdentitySharedProxyClient(auth *coreauth.Auth) (*http.Client, error) {
	proxyURL := ""
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && h != nil {
		h.mu.Lock()
		if h.cfg != nil {
			proxyURL = strings.TrimSpace(h.cfg.ProxyURL)
		}
		h.mu.Unlock()
	}
	if proxyURL == "" {
		return nil, egress.RuntimeError(fmt.Errorf("%w: shared Codex proxy is required", egress.ErrEgressRequired))
	}
	transport, mode, err := proxyutil.BuildHTTPTransport(proxyURL)
	if err != nil || mode != proxyutil.ModeProxy || transport == nil {
		if err == nil {
			err = egress.ErrEndpointInvalid
		}
		return nil, egress.RuntimeError(fmt.Errorf("%w: shared Codex proxy is invalid: %v", egress.ErrEndpointInvalid, err))
	}
	return &http.Client{Transport: transport}, nil
}

func agentIdentityVersion() string {
	if version := strings.TrimSpace(buildinfo.Version); version != "" {
		return version
	}
	return "dev"
}

type agentIdentityStateError struct {
	message string
}

type agentIdentityCredentialError struct {
	cause error
}

func (err *agentIdentityCredentialError) Error() string {
	return err.cause.Error()
}

func (err *agentIdentityCredentialError) Unwrap() error {
	return err.cause
}

func (err *agentIdentityStateError) Error() string {
	return err.message
}

type agentIdentityUpstreamError struct {
	message           string
	state             codex.ManagedAgentIdentityState
	hasIdentity       bool
	hasTask           bool
	preservedExisting bool
	cause             error
}

func (err *agentIdentityUpstreamError) Error() string {
	return err.message
}

func (err *agentIdentityUpstreamError) Unwrap() error {
	return err.cause
}
