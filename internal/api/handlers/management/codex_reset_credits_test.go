package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestGetCodexResetCreditsValidatesSelectedAuth(t *testing.T) {
	tests := []struct {
		name       string
		auth       *coreauth.Auth
		queryName  string
		wantStatus int
	}{
		{
			name:       "missing auth",
			queryName:  "missing.json",
			wantStatus: http.StatusNotFound,
		},
		{
			name: "non Codex auth",
			auth: &coreauth.Auth{
				ID:       "claude-auth",
				FileName: "claude.json",
				Provider: "claude",
			},
			queryName:  "claude.json",
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "disabled Codex auth",
			auth: &coreauth.Auth{
				ID:       "codex-disabled",
				FileName: "codex-disabled.json",
				Provider: "codex",
				Disabled: true,
			},
			queryName:  "codex-disabled.json",
			wantStatus: http.StatusConflict,
		},
		{
			name: "Codex API key auth",
			auth: &coreauth.Auth{
				ID:       "codex-api-key",
				FileName: "codex-api-key.json",
				Provider: "codex",
				Attributes: map[string]string{
					coreauth.AttributeAPIKey: "sk-test",
				},
			},
			queryName:  "codex-api-key.json",
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "Codex OAuth without account identity",
			auth: &coreauth.Auth{
				ID:       "codex-missing-account",
				FileName: "codex-missing-account.json",
				Provider: "codex",
				Metadata: map[string]any{
					"access_token": "test-token",
				},
			},
			queryName:  "codex-missing-account.json",
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			if testCase.auth != nil {
				if _, err := manager.Register(context.Background(), testCase.auth); err != nil {
					t.Fatalf("register auth: %v", err)
				}
			}
			handler := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
			recorder := httptest.NewRecorder()
			ginContext, _ := gin.CreateTestContext(recorder)
			ginContext.Request = httptest.NewRequest(
				http.MethodGet,
				"/v0/management/auth-files/codex-reset-credits?name="+testCase.queryName,
				nil,
			)

			handler.GetCodexResetCredits(ginContext)

			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
		})
	}
}

func TestConsumeCodexResetCreditValidatesOneCreditAndIdempotencyKey(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{ID: "codex-auth", FileName: "codex.json", Provider: "codex"}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	handler := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)

	tests := []struct {
		name string
		body string
	}{
		{name: "missing credit", body: `{"name":"codex.json","idempotency_key":"request-1"}`},
		{name: "missing idempotency key", body: `{"name":"codex.json","credit_id":"credit-1"}`},
		{name: "array credit is rejected", body: `{"name":"codex.json","credit_id":["credit-1"],"idempotency_key":"request-1"}`},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ginContext, _ := gin.CreateTestContext(recorder)
			ginContext.Request = httptest.NewRequest(
				http.MethodPost,
				"/v0/management/auth-files/codex-reset-credits/consume",
				strings.NewReader(testCase.body),
			)
			ginContext.Request.Header.Set("Content-Type", "application/json")

			handler.ConsumeCodexResetCredit(ginContext)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
		})
	}
}

func TestGetCodexResetCreditsFailsClosedWithoutEgressResolver(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "codex-auth",
		FileName: "codex.json",
		Provider: "codex",
		Metadata: map[string]any{
			"access_token": "test-token",
			"account_id":   "acct-123",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	handler := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(
		http.MethodGet,
		"/v0/management/auth-files/codex-reset-credits?name=codex.json",
		nil,
	)

	handler.GetCodexResetCredits(ginContext)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}
}

type codexResetStatusError int

func (status codexResetStatusError) Error() string   { return http.StatusText(int(status)) }
func (status codexResetStatusError) StatusCode() int { return int(status) }

func TestWriteCodexResetErrorDoesNotInvalidateManagementSessionForUpstreamAuthFailure(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)

	writeCodexResetError(ginContext, codexResetStatusError(http.StatusUnauthorized))

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadGateway, recorder.Body.String())
	}
}

func TestWriteCodexResetErrorPreservesUnsupportedUpstreamSignal(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)

	writeCodexResetError(ginContext, codexResetStatusError(http.StatusNotFound))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusNotFound, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "(404)") {
		t.Fatalf("body=%s, want explicit unsupported status marker", recorder.Body.String())
	}
}
