package auth

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

var (
	// ErrAuthIndexNotFound indicates that no manager auth has the requested index.
	ErrAuthIndexNotFound = errors.New("auth index not found")
	// ErrAuthIndexAmbiguous indicates that more than one manager auth has the requested index.
	ErrAuthIndexAmbiguous = errors.New("auth index is ambiguous")
	// ErrAuthStoreUnavailable indicates that a durable metadata transaction cannot be started.
	ErrAuthStoreUnavailable = errors.New("durable auth store unavailable")
)

// MetadataTransaction serializes durable metadata changes with both credential
// refresh and ordinary Manager mutations for one auth. It is valid only for the
// duration of the callback passed to WithMetadataTransactionByIndex.
type MetadataTransaction struct {
	manager   *Manager
	ctx       context.Context
	authID    string
	authIndex string
	current   *Auth
	published []*Auth
	active    bool
}

// Auth returns the latest auth snapshot visible to the transaction.
func (transaction *MetadataTransaction) Auth() *Auth {
	if transaction == nil || transaction.current == nil {
		return nil
	}
	return cloneAuthForMetadataMerge(transaction.current)
}

// Persisted reports whether this transaction has durably published at least one merge.
func (transaction *MetadataTransaction) Persisted() bool {
	return transaction != nil && len(transaction.published) > 0
}

// Merge replaces the specified top-level metadata fields. Persistence completes
// before the new snapshot is published; a failed Save leaves manager state unchanged.
func (transaction *MetadataTransaction) Merge(updates map[string]any) (*Auth, error) {
	if transaction == nil || transaction.manager == nil || !transaction.active {
		return nil, errors.New("metadata transaction is not active")
	}
	if errContext := transaction.ctx.Err(); errContext != nil {
		return nil, errContext
	}
	if len(updates) == 0 {
		return transaction.Auth(), nil
	}

	manager := transaction.manager
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current, err := transaction.currentManagerAuthLocked()
	if err != nil {
		return nil, err
	}
	persisted, err := transaction.persistMetadataLocked(current, updates)
	if err != nil {
		return nil, err
	}
	return transaction.publishMetadataLocked(persisted), nil
}

func (transaction *MetadataTransaction) currentManagerAuthLocked() (*Auth, error) {
	currentID, current, err := transaction.manager.authByUniqueIndexLocked(transaction.authIndex)
	if err != nil {
		return nil, err
	}
	if currentID != transaction.authID {
		return nil, fmt.Errorf("%w: %s changed during transaction", ErrAuthIndexNotFound, transaction.authIndex)
	}
	return current, nil
}

func (transaction *MetadataTransaction) persistMetadataLocked(current *Auth, updates map[string]any) (*Auth, error) {
	updated := authWithMetadataUpdates(current, updates)
	storage := updated.Storage
	persisted := cloneAuthForMetadataMerge(updated)
	// Metadata is canonical here; runtime TokenStorage may still contain tokens
	// from before the latest refresh and must not overwrite the merged snapshot.
	persisted.Storage = nil
	// Keep manager.mu through Save because availability paths persist under that
	// lock without taking the durable mutation lock.
	if err := transaction.manager.persist(transaction.ctx, persisted); err != nil {
		return nil, fmt.Errorf("persist merged auth metadata: %w", err)
	}
	persisted.Storage = storage
	return persisted, nil
}

func authWithMetadataUpdates(current *Auth, updates map[string]any) *Auth {
	updated := cloneAuthForMetadataMerge(current)
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any, len(updates))
	}
	for key, value := range updates {
		updated.Metadata[key] = cloneMetadataValue(value)
	}
	updated.UpdatedAt = time.Now()
	return updated
}

func (transaction *MetadataTransaction) publishMetadataLocked(persisted *Auth) *Auth {
	published := cloneAuthForMetadataMerge(persisted)
	transaction.manager.auths[transaction.authID] = published
	transaction.current = cloneAuthForMetadataMerge(published)
	transaction.published = append(transaction.published, cloneAuthForMetadataMerge(published))
	return cloneAuthForMetadataMerge(published)
}

// WithMetadataTransactionByIndex runs callback against a fresh auth snapshot.
// Credential refresh and ordinary durable mutations for the auth are excluded for
// the callback duration, but the manager-wide mutex is not held while callback runs.
// Manager hooks and scheduler notifications run only after all transaction locks
// have been released.
func (m *Manager) WithMetadataTransactionByIndex(ctx context.Context, authIndex string, callback func(*MetadataTransaction) error) (result *Auth, err error) {
	ctx, authIndex, err = m.validateMetadataTransaction(ctx, authIndex, callback)
	if err != nil {
		return nil, err
	}

	for {
		if errContext := ctx.Err(); errContext != nil {
			return nil, errContext
		}
		transaction, locks, retry, errBegin := m.beginMetadataTransaction(ctx, authIndex)
		if errBegin != nil {
			return nil, errBegin
		}
		if retry {
			continue
		}
		result, err = runMetadataTransaction(transaction, locks, callback)
		m.publishMetadataTransaction(ctx, transaction)
		return result, err
	}
}

func (m *Manager) validateMetadataTransaction(ctx context.Context, authIndex string, callback func(*MetadataTransaction) error) (context.Context, string, error) {
	if m == nil {
		return ctx, authIndex, errors.New("auth manager is nil")
	}
	if m.store == nil {
		return ctx, authIndex, ErrAuthStoreUnavailable
	}
	if callback == nil {
		return ctx, authIndex, errors.New("metadata transaction callback is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return ctx, authIndex, fmt.Errorf("%w: index is empty", ErrAuthIndexNotFound)
	}
	return ctx, authIndex, nil
}

type metadataTransactionLocks struct {
	refresh  *authRefreshLock
	mutation *authMutationLock
}

func (locks metadataTransactionLocks) unlock() {
	locks.mutation.mu.Unlock()
	locks.refresh.mu.Unlock()
}

func (m *Manager) beginMetadataTransaction(ctx context.Context, authIndex string) (*MetadataTransaction, metadataTransactionLocks, bool, error) {
	authID, _, err := m.authByUniqueIndex(authIndex)
	if err != nil {
		return nil, metadataTransactionLocks{}, false, err
	}
	locks := metadataTransactionLocks{
		refresh:  m.refreshLockForAuth(authID),
		mutation: m.mutationLockForAuth(authID),
	}
	locks.refresh.mu.Lock()
	locks.mutation.mu.Lock()

	m.mu.Lock()
	currentID, current, err := m.authByUniqueIndexLocked(authIndex)
	if err != nil || currentID != authID {
		m.mu.Unlock()
		locks.unlock()
		if err != nil {
			return nil, metadataTransactionLocks{}, false, err
		}
		return nil, metadataTransactionLocks{}, true, nil
	}
	transaction := &MetadataTransaction{
		manager: m, ctx: ctx, authID: authID, authIndex: authIndex,
		current: cloneAuthForMetadataMerge(current), active: true,
	}
	m.mu.Unlock()
	return transaction, locks, false, nil
}

func runMetadataTransaction(transaction *MetadataTransaction, locks metadataTransactionLocks, callback func(*MetadataTransaction) error) (*Auth, error) {
	defer func() {
		transaction.active = false
		locks.unlock()
	}()
	err := callback(transaction)
	return transaction.Auth(), err
}

// MergeMetadataByIndex durably merges metadata on the uniquely indexed auth.
func (m *Manager) MergeMetadataByIndex(ctx context.Context, authIndex string, updates map[string]any) (*Auth, error) {
	var merged *Auth
	_, err := m.WithMetadataTransactionByIndex(ctx, authIndex, func(transaction *MetadataTransaction) error {
		var errMerge error
		merged, errMerge = transaction.Merge(updates)
		return errMerge
	})
	if err != nil {
		return nil, err
	}
	return cloneAuthForMetadataMerge(merged), nil
}

func (m *Manager) publishMetadataTransaction(ctx context.Context, transaction *MetadataTransaction) {
	if transaction == nil || len(transaction.published) == 0 {
		return
	}
	latest := transaction.published[len(transaction.published)-1]
	if !shouldDeferAPIKeyModelAliasRebuild(ctx) {
		m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	}
	if m.scheduler != nil {
		m.scheduler.upsertAuth(latest)
	}
	m.queueRefreshReschedule(transaction.authID)
	for _, published := range transaction.published {
		m.hook.OnAuthUpdated(ctx, cloneAuthForMetadataMerge(published))
	}
}

func (m *Manager) authByUniqueIndex(authIndex string) (string, *Auth, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.authByUniqueIndexLocked(authIndex)
}

// authByUniqueIndexLocked may assign a missing index and therefore requires m.mu.
func (m *Manager) authByUniqueIndexLocked(authIndex string) (string, *Auth, error) {
	var matchedID string
	var matched *Auth
	for id, auth := range m.auths {
		if auth == nil || strings.TrimSpace(auth.EnsureIndex()) != authIndex {
			continue
		}
		if matched != nil {
			return "", nil, fmt.Errorf("%w: %s", ErrAuthIndexAmbiguous, authIndex)
		}
		matchedID = id
		matched = auth
	}
	if matched == nil {
		return "", nil, fmt.Errorf("%w: %s", ErrAuthIndexNotFound, authIndex)
	}
	return matchedID, matched, nil
}

func cloneAuthForMetadataMerge(auth *Auth) *Auth {
	if auth == nil {
		return nil
	}
	cloned := auth.Clone()
	cloned.Metadata = cloneMetadataMap(auth.Metadata)
	return cloned
}

func cloneMetadataMap(metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}
	cloned := make(map[string]any, len(metadata))
	for key, value := range metadata {
		cloned[key] = cloneMetadataValue(value)
	}
	return cloned
}

func cloneMetadataValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMetadataMap(typed)
	case map[string]string:
		cloned := make(map[string]string, len(typed))
		for key, item := range typed {
			cloned[key] = item
		}
		return cloned
	case []any:
		cloned := make([]any, len(typed))
		for i, item := range typed {
			cloned[i] = cloneMetadataValue(item)
		}
		return cloned
	case []map[string]any:
		cloned := make([]map[string]any, len(typed))
		for i, item := range typed {
			cloned[i] = cloneMetadataMap(item)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	case []byte:
		return append([]byte(nil), typed...)
	default:
		return value
	}
}

// IsAuthTokenPayloadKey returns true if key is a credential or token lifecycle field
// that should not overwrite newly acquired OAuth credentials during metadata merge.
func IsAuthTokenPayloadKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "access_token", "refresh_token", "id_token", "session_id",
		"expired", "last_refresh", "expires_in", "timestamp",
		"token_type", "user_code", "verification_uri", "verification_uri_complete":
		return true
	default:
		return false
	}
}

// MergeExistingAuthMetadata merges user-configured metadata fields from existingMap
// into target.Metadata and target.Storage if target does not already define them.
func MergeExistingAuthMetadata(target *Auth, existingMap map[string]any) {
	if target == nil || len(existingMap) == 0 {
		return
	}
	if target.Metadata == nil {
		target.Metadata = make(map[string]any)
	}
	for k, v := range existingMap {
		if IsAuthTokenPayloadKey(k) {
			continue
		}
		if _, exists := target.Metadata[k]; !exists {
			target.Metadata[k] = v
		}
	}
	if setter, ok := target.Storage.(interface{ SetMetadata(map[string]any) }); ok {
		setter.SetMetadata(target.Metadata)
	}
}

// MergePreparedAuth merges prepared request auth updates into current without modifying
// refresh lifecycle fields (such as LastRefreshedAt, LastError, or cooldown status).
func MergePreparedAuth(base, current, updated *Auth) *Auth {
	return mergeAuthContent(base, current, updated)
}

// MergeRefreshedAuth merges the refresh results from updated (derived from base)
// into the latest runtime auth current, preserving concurrent user modifications
// and active cooldowns.
func MergeRefreshedAuth(base, current, updated *Auth) *Auth {
	merged := mergeAuthContent(base, current, updated)
	if merged == nil || current == nil || updated == nil {
		return merged
	}
	if base != nil && current.RegistrationEpoch != base.RegistrationEpoch {
		return merged
	}

	// 1. Refresh Lifecycle Timestamps
	if !updated.LastRefreshedAt.IsZero() {
		merged.LastRefreshedAt = updated.LastRefreshedAt
	}
	if !updated.NextRefreshAfter.IsZero() || (base != nil && !base.NextRefreshAfter.IsZero()) {
		merged.NextRefreshAfter = updated.NextRefreshAfter
	}

	// 2. Error and Status recovery
	baseErrMsg := ""
	if base != nil && base.LastError != nil {
		baseErrMsg = base.LastError.Message
	}
	currentErrMsg := ""
	if current.LastError != nil {
		currentErrMsg = current.LastError.Message
	}
	hasNewConcurrentError := currentErrMsg != "" && currentErrMsg != baseErrMsg

	// 2. Disabled status three-way merge
	baseDisabled := base != nil && (base.Disabled || base.Status == StatusDisabled)
	currentDisabled := current.Disabled || current.Status == StatusDisabled
	updatedDisabled := updated.Disabled || updated.Status == StatusDisabled

	disabledChangedByExecutor := updatedDisabled != baseDisabled
	disabledChangedByUser := currentDisabled != baseDisabled

	finalDisabled := currentDisabled
	if disabledChangedByExecutor && !disabledChangedByUser {
		finalDisabled = updatedDisabled
	}

	if finalDisabled {
		merged.Disabled = true
		merged.Status = StatusDisabled
		merged.Metadata["disabled"] = true
	} else {
		merged.Disabled = false
		if merged.Status == StatusDisabled {
			merged.Status = StatusActive
		}
		merged.Metadata["disabled"] = false

		if hasNewConcurrentError {
			// A new error occurred concurrently (e.g. 503, 429, timeout). Preserve it.
			merged.LastError = current.LastError
			merged.Status = current.Status
			merged.Unavailable = current.Unavailable
			merged.StatusMessage = current.StatusMessage
		} else if current.Quota.Exceeded && current.Quota.Reason == "credential_quota" && current.Quota.NextRecoverAt.After(time.Now()) {
			// Preserve active credential quota
			merged.Unavailable = current.Unavailable
			merged.Status = current.Status
			merged.StatusMessage = current.StatusMessage
		} else if current.Unavailable && current.NextRetryAfter.After(time.Now()) {
			// Preserve active cooldown
			merged.Unavailable = current.Unavailable
			merged.Status = current.Status
			merged.StatusMessage = current.StatusMessage
		} else if updated.Status == StatusActive || updated.Status == "" {
			// Successful refresh clears previous auth error and restores active
			merged.Status = StatusActive
			merged.Unavailable = false
			merged.StatusMessage = ""
			merged.LastError = nil
		}
	}

	// 3. ModelStates: three-way merge to preserve concurrent cooldown/quota
	var baseModels map[string]*ModelState
	if base != nil {
		baseModels = base.ModelStates
	}
	if updated.ModelStates != nil {
		if merged.ModelStates == nil {
			merged.ModelStates = make(map[string]*ModelState)
		}
		for model, updState := range updated.ModelStates {
			baseState := baseModels[model]
			currentState := current.ModelStates[model]

			changedByExecutor := !reflect.DeepEqual(baseState, updState)
			changedByUser := !reflect.DeepEqual(baseState, currentState)

			if changedByExecutor && !changedByUser {
				merged.ModelStates[model] = updState
			}
		}
		if baseModels != nil {
			for model, baseState := range baseModels {
				if _, inUpdated := updated.ModelStates[model]; !inUpdated {
					if currentState, ok := current.ModelStates[model]; ok {
						if reflect.DeepEqual(baseState, currentState) {
							delete(merged.ModelStates, model)
						}
					}
				}
			}
		}
	}

	return merged
}

func mergeAuthContent(base, current, updated *Auth) *Auth {
	if current == nil {
		if updated != nil {
			return updated.Clone()
		}
		if base != nil {
			return base.Clone()
		}
		return nil
	}
	if updated == nil {
		return current.Clone()
	}
	if base != nil && current.RegistrationEpoch != base.RegistrationEpoch {
		// Stale update from a previous registration cycle; keep current state.
		return current.Clone()
	}

	merged := current.Clone()
	if merged.Metadata == nil {
		merged.Metadata = make(map[string]any)
	}

	var baseMeta map[string]any
	if base != nil {
		baseMeta = base.Metadata
	}

	// 1. Three-way merge for Metadata (excluding proxy_url which has dedicated canonical merge)
	if updated.Metadata != nil {
		for k, v := range updated.Metadata {
			if strings.EqualFold(strings.TrimSpace(k), "proxy_url") {
				continue
			}
			baseVal, hadInBase := baseMeta[k]
			currentVal, hadInCurrent := current.Metadata[k]

			changedByExecutor := !hadInBase || !reflect.DeepEqual(baseVal, v)
			changedByUser := hadInBase != hadInCurrent || (hadInBase && !reflect.DeepEqual(baseVal, currentVal))

			if changedByExecutor {
				// Apply executor change if user didn't modify it, or if it is a token payload field
				if !changedByUser || IsAuthTokenPayloadKey(k) {
					merged.Metadata[k] = v
				}
			}
		}
		// Deletions by executor: only delete if user didn't modify the field concurrently
		if baseMeta != nil {
			for k, baseVal := range baseMeta {
				if strings.EqualFold(strings.TrimSpace(k), "proxy_url") {
					continue
				}
				if _, inUpdated := updated.Metadata[k]; !inUpdated {
					if currentVal, ok := current.Metadata[k]; ok {
						if reflect.DeepEqual(baseVal, currentVal) {
							delete(merged.Metadata, k)
						}
					}
				}
			}
		}
	}

	// 2. Storage and Runtime
	if updated.Storage != nil {
		merged.Storage = updated.Storage
	}
	if updated.Runtime != nil {
		merged.Runtime = updated.Runtime
	}

	// 3. ProxyURL three-way merge (supporting both struct field and metadata modifications)
	baseStruct := ""
	if base != nil {
		baseStruct = strings.TrimSpace(base.ProxyURL)
	}
	currentStruct := strings.TrimSpace(current.ProxyURL)
	updatedStruct := strings.TrimSpace(updated.ProxyURL)

	baseMetaProxy := ""
	if base != nil && base.Metadata != nil {
		if s, ok := base.Metadata["proxy_url"].(string); ok {
			baseMetaProxy = strings.TrimSpace(s)
		}
	}
	currentMetaProxy := ""
	if current.Metadata != nil {
		if s, ok := current.Metadata["proxy_url"].(string); ok {
			currentMetaProxy = strings.TrimSpace(s)
		}
	}
	updatedMetaProxy := ""
	if updated.Metadata != nil {
		if s, ok := updated.Metadata["proxy_url"].(string); ok {
			updatedMetaProxy = strings.TrimSpace(s)
		}
	}

	userChangedStruct := currentStruct != baseStruct
	userChangedMeta := currentMetaProxy != baseMetaProxy
	execChangedStruct := updatedStruct != baseStruct
	execChangedMeta := updatedMetaProxy != baseMetaProxy

	finalProxy := currentStruct
	if currentMetaProxy != "" && currentStruct == "" && !userChangedStruct {
		finalProxy = currentMetaProxy
	}

	if userChangedStruct || userChangedMeta {
		// User modified proxy concurrently; user takes precedence over executor.
		if userChangedStruct && !userChangedMeta {
			finalProxy = currentStruct
		} else if userChangedMeta && !userChangedStruct {
			finalProxy = currentMetaProxy
		} else if currentStruct != "" {
			finalProxy = currentStruct
		} else {
			finalProxy = currentMetaProxy
		}
	} else if execChangedStruct || execChangedMeta {
		// Executor modified proxy and user did not touch it.
		if execChangedStruct && !execChangedMeta {
			finalProxy = updatedStruct
		} else if execChangedMeta && !execChangedStruct {
			finalProxy = updatedMetaProxy
		} else if updatedStruct != "" {
			finalProxy = updatedStruct
		} else {
			finalProxy = updatedMetaProxy
		}
	}

	if finalProxy != "" {
		merged.ProxyURL = finalProxy
		merged.Metadata["proxy_url"] = finalProxy
	} else {
		merged.ProxyURL = ""
		delete(merged.Metadata, "proxy_url")
	}

	// 4. Prefix (three-way merge, user modification takes precedence)
	basePrefix := ""
	if base != nil {
		basePrefix = strings.TrimSpace(base.Prefix)
	}
	currentPrefix := strings.TrimSpace(current.Prefix)
	updatedPrefix := strings.TrimSpace(updated.Prefix)

	if updatedPrefix != basePrefix && currentPrefix == basePrefix {
		merged.Prefix = updatedPrefix
	} else {
		merged.Prefix = currentPrefix
	}

	// 5. Attributes (three-way merge)
	if updated.Attributes != nil {
		if merged.Attributes == nil {
			merged.Attributes = make(map[string]string)
		}
		var baseAttrs map[string]string
		if base != nil {
			baseAttrs = base.Attributes
		}
		for k, v := range updated.Attributes {
			baseVal, hadInBase := baseAttrs[k]
			currentVal, hadInCurrent := current.Attributes[k]

			changedByExecutor := !hadInBase || baseVal != v
			changedByUser := hadInBase != hadInCurrent || (hadInBase && baseVal != currentVal)

			if changedByExecutor && !changedByUser {
				merged.Attributes[k] = v
			}
		}
		if baseAttrs != nil {
			for k, baseVal := range baseAttrs {
				if _, inUpdated := updated.Attributes[k]; !inUpdated {
					if currentVal, ok := current.Attributes[k]; ok {
						if baseVal == currentVal {
							delete(merged.Attributes, k)
						}
					}
				}
			}
		}
	}

	return merged
}
