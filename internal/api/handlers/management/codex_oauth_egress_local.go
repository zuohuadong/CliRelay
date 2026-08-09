package management

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type keyedMutexEntry struct {
	mu   sync.Mutex
	refs int
}

type keyedMutexTable struct {
	mu      sync.Mutex
	entries map[string]*keyedMutexEntry
}

func (table *keyedMutexTable) lock(keys ...string) func() {
	if table == nil {
		return func() {}
	}
	unique := make(map[string]struct{}, len(keys))
	normalized := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, exists := unique[key]; exists {
			continue
		}
		unique[key] = struct{}{}
		normalized = append(normalized, key)
	}
	if len(normalized) == 0 {
		return func() {}
	}
	sort.Strings(normalized)

	table.mu.Lock()
	if table.entries == nil {
		table.entries = make(map[string]*keyedMutexEntry)
	}
	entries := make([]*keyedMutexEntry, len(normalized))
	for index, key := range normalized {
		entry := table.entries[key]
		if entry == nil {
			entry = &keyedMutexEntry{}
			table.entries[key] = entry
		}
		entry.refs++
		entries[index] = entry
	}
	table.mu.Unlock()

	for _, entry := range entries {
		entry.mu.Lock()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for index := len(entries) - 1; index >= 0; index-- {
				entries[index].mu.Unlock()
			}
			table.mu.Lock()
			for index, key := range normalized {
				entry := entries[index]
				entry.refs--
				if entry.refs == 0 && table.entries[key] == entry {
					delete(table.entries, key)
				}
			}
			table.mu.Unlock()
		})
	}
}

func (h *Handler) codexOAuthEgressClient(ctx context.Context, egressID string) (*egress.Service, *http.Client, error) {
	if !h.egressRuntimeEnabled() {
		return nil, nil, fmt.Errorf("%w: egress runtime is disabled", egress.ErrEgressRequired)
	}
	service := h.egress()
	if service == nil {
		return nil, nil, fmt.Errorf("%w: egress service is unavailable", egress.ErrEgressRequired)
	}
	readiness, err := service.EndpointReadiness(ctx, egressID)
	if err != nil {
		return nil, nil, err
	}
	if !readiness.RuntimeReady {
		return nil, nil, fmt.Errorf("%w: endpoint %s is not ready: %s", egress.ErrEndpointDisabled, egressID, strings.Join(readiness.Reasons, ","))
	}
	client, err := service.HTTPClient(ctx, egressID, 30*time.Second)
	if err != nil {
		return nil, nil, err
	}
	return service, client, nil
}

func codexOAuthEndpointAvailable(ctx context.Context, service *egress.Service, egressID string) error {
	if service == nil {
		return fmt.Errorf("%w: egress service is unavailable", egress.ErrEgressRequired)
	}
	if endpoint, err := service.GetEndpoint(ctx, egressID); err == nil && endpoint.SharingMode == egress.EndpointSharingModeShared {
		return nil
	}
	impact, err := service.EndpointImpact(ctx, egressID, egress.EndpointActionDisable)
	if err != nil {
		return err
	}
	if impact.BindingCount > 0 {
		return fmt.Errorf("%w: endpoint %s already has %d binding(s)", egress.ErrEndpointInUse, egressID, impact.BindingCount)
	}
	return nil
}

func (h *Handler) codexOAuthConfig() *config.Config {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg
}

func (h *Handler) saveCodexTokenWithBinding(ctx context.Context, service *egress.Service, egressID string, record *coreauth.Auth, storage *codex.CodexTokenStorage) (string, error) {
	if service == nil || record == nil || storage == nil {
		return "", fmt.Errorf("Codex token binding inputs are incomplete")
	}
	delete(record.Metadata, "egress_id")
	delete(record.Attributes, "egress_id")
	storage.SetMetadata(record.Metadata)
	identity, err := egress.StableIdentity(storage.AccountID)
	if err != nil {
		return "", fmt.Errorf("derive Codex egress identity: %w", err)
	}
	targetPath := record.FileName
	if cfg := h.codexOAuthConfig(); !filepath.IsAbs(targetPath) && cfg != nil {
		targetPath = filepath.Join(cfg.AuthDir, targetPath)
	}
	targetPath = cleanAuthFilePath(targetPath)
	pathLockKey := targetPath
	if pathLockKey == "" {
		pathLockKey = strings.TrimSpace(record.ID)
	}
	if runtime.GOOS == "windows" {
		pathLockKey = strings.ToLower(pathLockKey)
	}
	unlock := codexTokenBindingLocks.lock("path:"+pathLockKey, "identity:"+identity)
	defer unlock()

	previousBytes, readErr := os.ReadFile(targetPath)
	previousExists := readErr == nil
	previousMode := os.FileMode(0o600)
	if previousExists {
		if info, errStat := os.Stat(targetPath); errStat == nil {
			previousMode = info.Mode().Perm()
		}
	}
	var previousRuntime *coreauth.Auth
	manager := h.authManagerSnapshot()
	if manager != nil {
		if existing, ok := manager.GetByID(record.ID); ok && existing != nil {
			previousRuntime = existing.Clone()
		}
	}

	savedPath, err := h.saveTokenRecord(ctx, record)
	if err != nil {
		return savedPath, err
	}
	err = service.PutBinding(ctx, egress.Binding{Identity: identity, EndpointID: egressID, AuthFileID: record.ID})
	if err == nil {
		return savedPath, nil
	}

	rollbackCtx := coreauth.WithSkipPersist(context.WithoutCancel(ctx))
	if previousExists {
		if errRestore := os.WriteFile(targetPath, previousBytes, previousMode); errRestore != nil {
			return savedPath, fmt.Errorf("bind Codex egress: %w; restore previous token: %v", err, errRestore)
		}
		if manager != nil && previousRuntime != nil {
			if _, errUpdate := manager.Update(rollbackCtx, previousRuntime); errUpdate != nil {
				return savedPath, fmt.Errorf("bind Codex egress: %w; restore previous runtime: %v", err, errUpdate)
			}
		} else if manager != nil {
			manager.Remove(rollbackCtx, record.ID)
		}
	} else {
		if errDelete := h.deleteTokenRecord(rollbackCtx, savedPath); errDelete != nil {
			return savedPath, fmt.Errorf("bind Codex egress: %w; delete new token: %v", err, errDelete)
		}
		if manager != nil {
			manager.Remove(rollbackCtx, record.ID)
		}
	}
	return savedPath, fmt.Errorf("bind Codex egress endpoint: %w", err)
}
