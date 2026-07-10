package management

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type countingCodexOAuthService struct {
	*fakeCodexOAuthService
	exchanges *atomic.Int32
}

func TestCallbackForwarderListensOnLoopbackOnly(t *testing.T) {
	forwarder, err := startCallbackForwarder(0, "codex-test", "http://127.0.0.1/target")
	if err != nil {
		t.Fatal(err)
	}
	defer stopForwarderInstance(0, forwarder)
	host, _, err := net.SplitHostPort(forwarder.address)
	if err != nil {
		t.Fatalf("forwarder address=%q: %v", forwarder.address, err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("forwarder host=%q, want 127.0.0.1", host)
	}
}

func (f *countingCodexOAuthService) ExchangeCodeForTokens(ctx context.Context, code string, pkceCodes *codex.PKCECodes) (*codex.CodexAuthBundle, error) {
	f.exchanges.Add(1)
	return f.fakeCodexOAuthService.ExchangeCodeForTokens(ctx, code, pkceCodes)
}

func TestRequestCodexTokenRejectsPreparationMode(t *testing.T) {
	handler, service, endpoint, _ := newCodexOAuthEgressFlow(t)
	updated := *handler.codexOAuthConfig()
	updated.EgressNetwork.Enabled = false
	handler.SetConfig(&updated)
	service.SetConfig(&updated)
	router := gin.New()
	router.GET("/codex-auth-url", handler.RequestCodexToken)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/codex-auth-url?egress_id="+endpoint.ID, nil)
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), `"code":"egress_not_ready"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestRequestCodexTokenRechecksSelectedEgressBeforeExchange(t *testing.T) {
	var exchanges atomic.Int32
	originalNewCodexOAuthService := newCodexOAuthService
	newCodexOAuthService = func(_ *config.Config, _ *http.Client) codexOAuthService {
		return &countingCodexOAuthService{fakeCodexOAuthService: &fakeCodexOAuthService{}, exchanges: &exchanges}
	}
	defer func() { newCodexOAuthService = originalNewCodexOAuthService }()

	handler, service, endpoint, authDir := newCodexOAuthEgressFlow(t)
	router := gin.New()
	router.GET("/codex-auth-url", handler.RequestCodexToken)
	state := requestCodexTokenState(t, router, endpoint.ID)
	defer CompleteOAuthSession(state)
	_, _, _, metadata, ok := GetOAuthSessionDetails(state)
	if !ok || metadata["egress_id"] != endpoint.ID {
		t.Fatalf("oauth session metadata=%#v ok=%v", metadata, ok)
	}

	updated := *handler.codexOAuthConfig()
	updated.EgressNetwork.Enabled = false
	handler.SetConfig(&updated)
	service.SetConfig(&updated)
	if _, err := WriteOAuthCallbackFileForPendingSession(authDir, "codex", state, "shutdown-code", ""); err != nil {
		t.Fatalf("write callback: %v", err)
	}
	waitForOAuthSessionDone(t, state)

	if exchanges.Load() != 0 {
		t.Fatalf("token exchange count=%d, want 0 after route shutdown", exchanges.Load())
	}
	provider, status, ok := GetOAuthSession(state)
	if !ok || provider != "codex" || !strings.Contains(status, "egress") {
		t.Fatalf("oauth session provider=%q status=%q ok=%v", provider, status, ok)
	}
}

func TestRequestCodexTokenRejectsEndpointInvalidationBeforeExchange(t *testing.T) {
	var exchanges atomic.Int32
	originalNewCodexOAuthService := newCodexOAuthService
	newCodexOAuthService = func(_ *config.Config, _ *http.Client) codexOAuthService {
		return &countingCodexOAuthService{fakeCodexOAuthService: &fakeCodexOAuthService{}, exchanges: &exchanges}
	}
	defer func() { newCodexOAuthService = originalNewCodexOAuthService }()

	handler, service, endpoint, authDir := newCodexOAuthEgressFlow(t)
	router := gin.New()
	router.GET("/codex-auth-url", handler.RequestCodexToken)
	state := requestCodexTokenState(t, router, endpoint.ID)
	defer CompleteOAuthSession(state)
	impact, err := service.EndpointImpact(context.Background(), endpoint.ID, egress.EndpointActionDisable)
	if err != nil {
		t.Fatalf("EndpointImpact() error=%v", err)
	}
	if err = service.ApplyEndpointAction(context.Background(), endpoint.ID, egress.EndpointActionDisable, true, impact.Revision); err != nil {
		t.Fatalf("ApplyEndpointAction() error=%v", err)
	}
	if _, err = WriteOAuthCallbackFileForPendingSession(authDir, "codex", state, "disabled-code", ""); err != nil {
		t.Fatalf("write callback: %v", err)
	}
	waitForOAuthSessionDone(t, state)
	if exchanges.Load() != 0 {
		t.Fatalf("token exchange count=%d, want 0 after endpoint invalidation", exchanges.Load())
	}
}

func TestRequestCodexTokenRejectsBindingCreatedBeforeExchange(t *testing.T) {
	var exchanges atomic.Int32
	originalNewCodexOAuthService := newCodexOAuthService
	newCodexOAuthService = func(_ *config.Config, _ *http.Client) codexOAuthService {
		return &countingCodexOAuthService{fakeCodexOAuthService: &fakeCodexOAuthService{}, exchanges: &exchanges}
	}
	defer func() { newCodexOAuthService = originalNewCodexOAuthService }()

	handler, service, endpoint, authDir := newCodexOAuthEgressFlow(t)
	router := gin.New()
	router.GET("/codex-auth-url", handler.RequestCodexToken)
	state := requestCodexTokenState(t, router, endpoint.ID)
	defer CompleteOAuthSession(state)
	identity, _ := egress.StableIdentity("acct-operator-bound")
	if err := service.PutBinding(context.Background(), egress.Binding{Identity: identity, EndpointID: endpoint.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteOAuthCallbackFileForPendingSession(authDir, "codex", state, "bound-code", ""); err != nil {
		t.Fatalf("write callback: %v", err)
	}
	waitForOAuthSessionDone(t, state)
	if exchanges.Load() != 0 {
		t.Fatalf("token exchange count=%d, want 0 after endpoint became bound", exchanges.Load())
	}
}

func newCodexOAuthEgressFlow(t *testing.T) (*Handler, *egress.Service, egress.Endpoint, string) {
	t.Helper()
	tempDir := t.TempDir()
	authDir := filepath.Join(tempDir, "auths")
	proxyServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(proxyServer.Close)
	host, portText, err := net.SplitHostPort(strings.TrimPrefix(proxyServer.URL, "http://"))
	if err != nil {
		t.Fatalf("SplitHostPort() error=%v", err)
	}
	port, _ := strconv.Atoi(portText)
	cfg := &config.Config{AuthDir: authDir, EgressNetwork: config.EgressNetworkConfig{Enabled: true, Headscale: config.HeadscaleConfig{ServiceTag: config.DefaultEgressServiceTag}}}
	service, err := egress.NewService(cfg, filepath.Join(tempDir, "usage.db"))
	if err != nil {
		t.Fatalf("NewService() error=%v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	ctx := context.Background()
	if err = service.Store().UpsertNodes(ctx, []egress.Node{{ID: "17", Name: "test", Addresses: []string{host}, Online: true, Tags: []string{config.DefaultEgressServiceTag}}}, time.Now()); err != nil {
		t.Fatalf("UpsertNodes() error=%v", err)
	}
	endpoint, err := service.CreateEndpoint(ctx, egress.Endpoint{Name: "test", NodeID: "17", Protocol: egress.ProtocolHTTP, Host: host, Port: port, Enabled: true, ExpectedPublicIP: "198.51.100.17"})
	if err != nil {
		t.Fatalf("CreateEndpoint() error=%v", err)
	}
	endpoint, err = service.Store().UpdateEndpointCheck(ctx, endpoint.ID, endpoint.ExpectedPublicIP, egress.EndpointStatusHealthy, "", 1, time.Now())
	if err != nil {
		t.Fatalf("UpdateEndpointCheck() error=%v", err)
	}
	handler := NewHandlerWithoutConfigFilePath(cfg, nil)
	handler.SetEgressService(service)
	return handler, service, endpoint, authDir
}

func TestSaveCodexTokenWithBindingDeletesNewFileOnBindingFailure(t *testing.T) {
	t.Parallel()

	handler, service, authDir := newCodexBindingTestHandler(t)
	record, storage := codexBindingTestRecord("new.json", "acct-new")
	savedPath, err := handler.saveCodexTokenWithBinding(context.Background(), service, "missing-endpoint", record, storage)
	if err == nil {
		t.Fatal("saveCodexTokenWithBinding() error = nil")
	}
	if savedPath == "" {
		savedPath = filepath.Join(authDir, record.FileName)
	}
	if _, statErr := os.Stat(savedPath); !os.IsNotExist(statErr) {
		t.Fatalf("new token still exists after compensation: %v", statErr)
	}
}

func TestSaveCodexTokenWithBindingRestoresExistingFileOnBindingFailure(t *testing.T) {
	t.Parallel()

	handler, service, authDir := newCodexBindingTestHandler(t)
	manager := coreauth.NewManager(nil, nil, nil)
	handler.authManager = manager
	handler.postAuthPersistHook = func(ctx context.Context, auth *coreauth.Auth) error {
		_, err := manager.Register(ctx, auth)
		return err
	}
	target := filepath.Join(authDir, "existing.json")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	original := []byte(`{"type":"codex","access_token":"old-token","account_id":"acct-old"}`)
	if err := os.WriteFile(target, original, 0o640); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	record, storage := codexBindingTestRecord("existing.json", "acct-new")
	if _, err := handler.saveCodexTokenWithBinding(context.Background(), service, "missing-endpoint", record, storage); err == nil {
		t.Fatal("saveCodexTokenWithBinding() error = nil")
	}
	restored, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(restored) != string(original) {
		t.Fatalf("restored bytes = %s, want %s", restored, original)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("restored mode = %o, want 640", info.Mode().Perm())
	}
	if _, ok := manager.GetByID(record.ID); ok {
		t.Fatal("new runtime auth remained after existing-file compensation")
	}
}

func TestSaveCodexTokenWithBindingSerializesSameFileCompensation(t *testing.T) {
	handler, service, authDir := newCodexBindingTestHandler(t)
	manager := coreauth.NewManager(nil, nil, nil)
	handler.authManager = manager

	endpoint, err := service.CreateEndpoint(context.Background(), egress.Endpoint{
		Name:             "local-success",
		Protocol:         egress.ProtocolHTTP,
		Host:             "127.0.0.1",
		Port:             8080,
		Enabled:          true,
		LocalServer:      true,
		ExpectedPublicIP: "198.51.100.10",
	})
	if err != nil {
		t.Fatalf("CreateEndpoint() error = %v", err)
	}
	if _, err = service.Store().UpdateEndpointCheck(context.Background(), endpoint.ID, endpoint.ExpectedPublicIP, egress.EndpointStatusHealthy, "", 1, time.Now()); err != nil {
		t.Fatalf("UpdateEndpointCheck() error = %v", err)
	}

	aRecord, aStorage := codexBindingTestRecordWithToken("shared.json", "acct-shared", "token-a")
	bRecord, bStorage := codexBindingTestRecordWithToken("shared.json", "acct-shared", "token-b")
	aSaved := make(chan struct{})
	releaseA := make(chan struct{})
	handler.postAuthPersistHook = func(ctx context.Context, auth *coreauth.Auth) error {
		if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
			return errRegister
		}
		storage, _ := auth.Storage.(*codex.CodexTokenStorage)
		if storage != nil && storage.AccessToken == "token-a" {
			close(aSaved)
			<-releaseA
		}
		return nil
	}

	type saveResult struct {
		path string
		err  error
	}
	aResult := make(chan saveResult, 1)
	bResult := make(chan saveResult, 1)
	go func() {
		path, errSave := handler.saveCodexTokenWithBinding(context.Background(), service, "missing-endpoint", aRecord, aStorage)
		aResult <- saveResult{path: path, err: errSave}
	}()
	select {
	case <-aSaved:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first save barrier")
	}

	go func() {
		path, errSave := handler.saveCodexTokenWithBinding(context.Background(), service, endpoint.ID, bRecord, bStorage)
		bResult <- saveResult{path: path, err: errSave}
	}()
	select {
	case result := <-bResult:
		t.Fatalf("second save bypassed same-file lock: path=%q err=%v", result.path, result.err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseA)

	first := <-aResult
	if first.err == nil {
		t.Fatal("first save error = nil, want binding failure")
	}
	second := <-bResult
	if second.err != nil {
		t.Fatalf("second save error = %v", second.err)
	}

	target := filepath.Join(authDir, "shared.json")
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var saved map[string]any
	if err = json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got := strings.TrimSpace(saved["access_token"].(string)); got != "token-b" {
		t.Fatalf("saved access token = %q, want token-b", got)
	}
	runtimeAuth, ok := manager.GetByID(bRecord.ID)
	if !ok || runtimeAuth == nil {
		t.Fatal("successful runtime auth is missing")
	}
	runtimeStorage, ok := runtimeAuth.Storage.(*codex.CodexTokenStorage)
	if !ok || runtimeStorage.AccessToken != "token-b" {
		t.Fatalf("runtime storage = %#v, want token-b", runtimeAuth.Storage)
	}
	identity, err := egress.StableIdentity(bStorage.AccountID)
	if err != nil {
		t.Fatalf("StableIdentity() error = %v", err)
	}
	resolved, err := service.Store().ResolveIdentity(context.Background(), identity)
	if err != nil {
		t.Fatalf("ResolveIdentity() error = %v", err)
	}
	if resolved.Binding.EndpointID != endpoint.ID || resolved.Binding.AuthFileID != bRecord.ID {
		t.Fatalf("binding = %#v, want endpoint=%s auth=%s", resolved.Binding, endpoint.ID, bRecord.ID)
	}
}

func TestCodexTokenBindingKeyedLocksCleanupIdleEntries(t *testing.T) {
	var locks keyedMutexTable
	unlock := locks.lock("path:/tmp/codex.json", "identity:codex:test")
	locks.mu.Lock()
	activeEntries := len(locks.entries)
	locks.mu.Unlock()
	if activeEntries != 2 {
		t.Fatalf("active lock entries = %d, want 2", activeEntries)
	}

	unlock()
	locks.mu.Lock()
	idleEntries := len(locks.entries)
	locks.mu.Unlock()
	if idleEntries != 0 {
		t.Fatalf("idle lock entries = %d, want 0", idleEntries)
	}
}

func newCodexBindingTestHandler(t *testing.T) (*Handler, *egress.Service, string) {
	t.Helper()
	tempDir := t.TempDir()
	authDir := filepath.Join(tempDir, "auth")
	cfg := &config.Config{AuthDir: authDir, EgressNetwork: config.EgressNetworkConfig{Enabled: true, LocalEndpointEnabled: true, Headscale: config.HeadscaleConfig{ServiceTag: config.DefaultEgressServiceTag}}}
	service, err := egress.NewService(cfg, filepath.Join(tempDir, "usage.db"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	handler := NewHandlerWithoutConfigFilePath(cfg, nil)
	handler.SetEgressService(service)
	return handler, service, authDir
}

func codexBindingTestRecord(fileName, accountID string) (*coreauth.Auth, *codex.CodexTokenStorage) {
	return codexBindingTestRecordWithToken(fileName, accountID, "new-token")
}

func codexBindingTestRecordWithToken(fileName, accountID, accessToken string) (*coreauth.Auth, *codex.CodexTokenStorage) {
	storage := &codex.CodexTokenStorage{AccessToken: accessToken, RefreshToken: "new-refresh", AccountID: accountID, Email: "user@example.test"}
	metadata := map[string]any{"account_id": accountID, "email": storage.Email, "egress_id": "missing-endpoint"}
	storage.SetMetadata(metadata)
	return &coreauth.Auth{ID: fileName, FileName: fileName, Provider: "codex", Storage: storage, Metadata: metadata}, storage
}
