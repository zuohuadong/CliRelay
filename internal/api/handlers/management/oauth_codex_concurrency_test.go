package management

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
)

type fakeCodexOAuthService struct{}

func (f *fakeCodexOAuthService) GenerateAuthURL(state string, pkceCodes *codex.PKCECodes) (string, error) {
	return "https://auth.example.test/oauth?state=" + state, nil
}

func (f *fakeCodexOAuthService) ExchangeCodeForTokens(ctx context.Context, code string, pkceCodes *codex.PKCECodes) (*codex.CodexAuthBundle, error) {
	now := time.Now()
	return &codex.CodexAuthBundle{
		TokenData: codex.CodexTokenData{
			IDToken:      "invalid-test-id-token",
			AccessToken:  "access-" + code,
			RefreshToken: "refresh-" + code,
			Email:        "codex-" + code + "@example.test",
			AccountID:    "acct-" + code,
			Expire:       now.Add(time.Hour).Format(time.RFC3339),
		},
		LastRefresh: now.Format(time.RFC3339),
	}, nil
}

func (f *fakeCodexOAuthService) CreateTokenStorage(bundle *codex.CodexAuthBundle) *codex.CodexTokenStorage {
	return &codex.CodexTokenStorage{
		IDToken:      bundle.TokenData.IDToken,
		AccessToken:  bundle.TokenData.AccessToken,
		RefreshToken: bundle.TokenData.RefreshToken,
		AccountID:    bundle.TokenData.AccountID,
		LastRefresh:  bundle.LastRefresh,
		Email:        bundle.TokenData.Email,
		Expire:       bundle.TokenData.Expire,
	}
}

func TestRequestCodexTokenCompletionKeepsConcurrentSessionPending(t *testing.T) {
	originalNewCodexOAuthService := newCodexOAuthService
	newCodexOAuthService = func(cfg *config.Config, _ *http.Client) codexOAuthService {
		return &fakeCodexOAuthService{}
	}
	defer func() {
		newCodexOAuthService = originalNewCodexOAuthService
	}()

	tempDir := t.TempDir()
	authDir := filepath.Join(tempDir, "auths")
	proxyServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer proxyServer.Close()
	host, portText, _ := net.SplitHostPort(strings.TrimPrefix(proxyServer.URL, "http://"))
	port, _ := strconv.Atoi(portText)
	cfg := &config.Config{AuthDir: authDir, EgressNetwork: config.EgressNetworkConfig{Enabled: true}}
	service, err := egress.NewService(cfg, filepath.Join(tempDir, "usage.db"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	defer service.Close()
	endpoint, err := service.CreateEndpoint(context.Background(), egress.Endpoint{Name: "test", Protocol: egress.ProtocolHTTP, Host: host, Port: port, Enabled: true, ExpectedPublicIP: "198.51.100.17"})
	if err != nil {
		t.Fatalf("CreateEndpoint() error = %v", err)
	}
	endpoint, err = service.Store().UpdateEndpointCheck(context.Background(), endpoint.ID, endpoint.ExpectedPublicIP, egress.EndpointStatusHealthy, "", 1, time.Now())
	if err != nil {
		t.Fatalf("UpdateEndpointCheck() error = %v", err)
	}
	handler := NewHandlerWithoutConfigFilePath(cfg, nil)
	handler.SetEgressService(service)
	router := gin.New()
	router.GET("/codex-auth-url", handler.RequestCodexToken)
	secondEndpoint, err := service.CreateEndpoint(context.Background(), egress.Endpoint{Name: "test-2", Protocol: egress.ProtocolHTTP, Host: host, Port: port, Enabled: true, ExpectedPublicIP: "198.51.100.18"})
	if err != nil {
		t.Fatalf("CreateEndpoint(second) error = %v", err)
	}
	secondEndpoint, err = service.Store().UpdateEndpointCheck(context.Background(), secondEndpoint.ID, secondEndpoint.ExpectedPublicIP, egress.EndpointStatusHealthy, "", 1, time.Now())
	if err != nil {
		t.Fatalf("UpdateEndpointCheck(second) error = %v", err)
	}

	firstState := requestCodexTokenState(t, router, endpoint.ID)
	secondState := requestCodexTokenState(t, router, secondEndpoint.ID)
	defer CompleteOAuthSession(firstState)
	defer CompleteOAuthSession(secondState)

	if _, errWrite := WriteOAuthCallbackFileForPendingSession(authDir, "codex", firstState, "first-code", ""); errWrite != nil {
		t.Fatalf("write first callback file: %v", errWrite)
	}

	waitForOAuthSessionDone(t, firstState)
	if !IsOAuthSessionPending(secondState, "codex") {
		t.Fatalf("expected concurrent codex session %s to remain pending after %s completed", secondState, firstState)
	}
}

func TestRequestCodexTokenReservesEndpointAcrossPendingSessions(t *testing.T) {
	originalNewCodexOAuthService := newCodexOAuthService
	newCodexOAuthService = func(_ *config.Config, _ *http.Client) codexOAuthService { return &fakeCodexOAuthService{} }
	defer func() { newCodexOAuthService = originalNewCodexOAuthService }()

	handler, _, endpoint, _ := newCodexOAuthEgressFlow(t)
	router := gin.New()
	router.GET("/codex-auth-url", handler.RequestCodexToken)
	firstState := requestCodexTokenState(t, router, endpoint.ID)
	defer CompleteOAuthSession(firstState)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/codex-auth-url?egress_id="+endpoint.ID, nil))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), `"code":"egress_endpoint_in_use"`) {
		t.Fatalf("reserved endpoint status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	SetOAuthSessionError(firstState, "test failure releases reservation")
	thirdState := requestCodexTokenState(t, router, endpoint.ID)
	defer CompleteOAuthSession(thirdState)
}

func TestRequestCodexTokenRejectsAlreadyBoundEndpoint(t *testing.T) {
	handler, service, endpoint, _ := newCodexOAuthEgressFlow(t)
	identity, _ := egress.StableIdentity("acct-already-bound")
	if err := service.PutBinding(context.Background(), egress.Binding{Identity: identity, EndpointID: endpoint.ID}); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.GET("/codex-auth-url", handler.RequestCodexToken)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/codex-auth-url?egress_id="+endpoint.ID, nil))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), `"code":"egress_endpoint_in_use"`) {
		t.Fatalf("bound endpoint status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestOAuthEndpointReservationExpiresWithSession(t *testing.T) {
	store := newOAuthSessionStore(time.Millisecond)
	if err := store.RegisterBuiltinWithEndpointReservation("state-1", "codex", "endpoint-1", nil); err != nil {
		t.Fatalf("register first reservation: %v", err)
	}
	if err := store.RegisterBuiltinWithEndpointReservation("state-2", "codex", "endpoint-1", nil); !errors.Is(err, errOAuthEndpointReserved) {
		t.Fatalf("expected endpoint reservation conflict, got %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := store.RegisterBuiltinWithEndpointReservation("state-3", "codex", "endpoint-1", nil); err != nil {
		t.Fatalf("expected expired reservation to be released, got %v", err)
	}
}

func requestCodexTokenState(t *testing.T, router http.Handler, endpointID string) string {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/codex-auth-url?egress_id="+endpointID, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}

	var payload struct {
		State string `json:"state"`
	}
	if errDecode := json.Unmarshal(w.Body.Bytes(), &payload); errDecode != nil {
		t.Fatalf("decode codex auth URL response: %v", errDecode)
	}
	if payload.State == "" {
		t.Fatalf("expected codex auth URL response to include state")
	}
	return payload.State
}

func waitForOAuthSessionDone(t *testing.T, state string) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !IsOAuthSessionPending(state, "codex") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for codex session %s to complete", state)
}
