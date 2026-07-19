package management

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	maxCodexResetCreditIDLength  = 512
	maxCodexResetRequestIDLength = 128
)

type codexResetCreditConsumeRequest struct {
	Name           string `json:"name"`
	CreditID       string `json:"credit_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (h *Handler) GetCodexResetCredits(c *gin.Context) {
	auth, ok := h.codexResetAuth(c, c.Query("name"))
	if !ok {
		return
	}
	credits, err := h.codexResetExecutor().ListRateLimitResetCredits(c.Request.Context(), auth)
	if err != nil {
		writeCodexResetError(c, err)
		return
	}
	c.JSON(http.StatusOK, credits)
}

func (h *Handler) ConsumeCodexResetCredit(c *gin.Context) {
	var consumeRequest codexResetCreditConsumeRequest
	if err := c.ShouldBindJSON(&consumeRequest); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	consumeRequest.CreditID = strings.TrimSpace(consumeRequest.CreditID)
	consumeRequest.IdempotencyKey = strings.TrimSpace(consumeRequest.IdempotencyKey)
	if consumeRequest.CreditID == "" || len(consumeRequest.CreditID) > maxCodexResetCreditIDLength {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid credit_id"})
		return
	}
	if consumeRequest.IdempotencyKey == "" || len(consumeRequest.IdempotencyKey) > maxCodexResetRequestIDLength {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid idempotency_key"})
		return
	}
	auth, ok := h.codexResetAuth(c, consumeRequest.Name)
	if !ok {
		return
	}
	outcome, err := h.codexResetExecutor().ConsumeRateLimitResetCredit(
		c.Request.Context(), auth, consumeRequest.CreditID, consumeRequest.IdempotencyKey,
	)
	if err != nil {
		writeCodexResetError(c, err)
		return
	}
	c.JSON(http.StatusOK, outcome)
}

func (h *Handler) codexResetAuth(c *gin.Context, name string) (*coreauth.Auth, bool) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return nil, false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return nil, false
	}
	auth := h.findCodexResetAuth(name)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return nil, false
	}
	if auth.Disabled || auth.Status == coreauth.StatusDisabled {
		c.JSON(http.StatusConflict, gin.H{"error": "auth is disabled"})
		return nil, false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Codex OAuth auth is required"})
		return nil, false
	}
	if auth.AuthKind() != coreauth.AuthKindOAuth {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Codex OAuth auth is required"})
		return nil, false
	}
	accountID, _ := auth.Metadata["account_id"].(string)
	if strings.TrimSpace(accountID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Codex OAuth auth has no ChatGPT account ID"})
		return nil, false
	}
	return auth, true
}

func (h *Handler) findCodexResetAuth(name string) *coreauth.Auth {
	if auth, found := h.authManager.GetByID(name); found {
		return auth
	}
	for _, candidate := range h.authManager.List() {
		if candidate != nil && strings.TrimSpace(candidate.FileName) == name {
			return candidate
		}
	}
	return nil
}

func (h *Handler) codexResetExecutor() *executor.CodexExecutor {
	h.mu.Lock()
	cfg := h.cfg
	egressService := h.egressService
	h.mu.Unlock()
	if egressService == nil {
		return executor.NewCodexExecutorWithEgress(cfg, nil)
	}
	return executor.NewCodexExecutorWithEgress(cfg, egressService)
}

func writeCodexResetError(c *gin.Context, err error) {
	statusCode := http.StatusBadGateway
	message := "Codex reset credit request failed"
	var statusErr interface{ StatusCode() int }
	if errors.As(err, &statusErr) {
		upstreamStatus := statusErr.StatusCode()
		if upstreamStatus == http.StatusNotFound {
			statusCode = http.StatusNotFound
			message = "Codex reset credits are not available (404)"
		} else if upstreamStatus >= 500 && upstreamStatus <= 599 {
			statusCode = upstreamStatus
		}
	}
	log.WithError(err).Warn("management: Codex reset credit request failed")
	c.JSON(statusCode, gin.H{"error": message})
}
