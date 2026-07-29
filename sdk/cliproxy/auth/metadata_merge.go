package auth

import (
	"context"
	"errors"
	"fmt"
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
