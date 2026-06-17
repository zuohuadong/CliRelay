package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	iflowauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/iflow"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/qwen"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func (h *Handler) RequestQwenToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	state := fmt.Sprintf("qwn-%d", time.Now().UnixNano())
	qwenAuth := qwen.NewQwenAuth(h.cfg)

	deviceFlow, err := qwenAuth.InitiateDeviceFlow(ctx)
	if err != nil {
		log.Errorf("failed to generate qwen authorization URL: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}

	authURL := deviceFlow.VerificationURIComplete
	if strings.TrimSpace(authURL) == "" {
		authURL = deviceFlow.VerificationURI
	}
	if strings.TrimSpace(authURL) == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "authorization url is empty"})
		return
	}

	RegisterOAuthSession(state, "qwen")

	go func() {
		tokenData, errPoll := qwenAuth.PollForToken(deviceFlow.DeviceCode, deviceFlow.CodeVerifier)
		if errPoll != nil {
			SetOAuthSessionError(state, "Authentication failed")
			log.Errorf("qwen authentication failed: %v", errPoll)
			return
		}

		tokenStorage := qwenAuth.CreateTokenStorage(tokenData)
		if tokenStorage == nil {
			SetOAuthSessionError(state, "Authentication failed")
			log.Error("qwen authentication failed: empty token storage")
			return
		}
		tokenStorage.Email = fmt.Sprintf("%d", time.Now().UnixMilli())

		fileName := fmt.Sprintf("qwen-%s.json", tokenStorage.Email)
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "qwen",
			FileName: fileName,
			Storage:  tokenStorage,
			Metadata: map[string]any{
				"email":         tokenStorage.Email,
				"access_token":  tokenStorage.AccessToken,
				"refresh_token": tokenStorage.RefreshToken,
				"resource_url":  tokenStorage.ResourceURL,
				"expired":       tokenStorage.Expire,
				"type":          "qwen",
			},
			Attributes: map[string]string{
				"api_key": tokenStorage.AccessToken,
			},
		}
		if _, errSave := h.saveTokenRecord(ctx, record); errSave != nil {
			log.Errorf("failed to save qwen authentication tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}

		CompleteOAuthSession(state)
		CompleteOAuthSessionsByProvider("qwen")
	}()

	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) RequestIFlowTokenOrCookie(c *gin.Context) {
	if c.Request.Method == http.MethodPost {
		body, errRead := io.ReadAll(c.Request.Body)
		if errRead == nil {
			c.Request.Body = io.NopCloser(strings.NewReader(string(body)))
			var payload struct {
				Cookie string `json:"cookie"`
			}
			if len(body) > 0 && json.Unmarshal(body, &payload) == nil && strings.TrimSpace(payload.Cookie) != "" {
				h.RequestIFlowCookieToken(c)
				return
			}
		}
	}

	h.RequestIFlowToken(c)
}

func (h *Handler) RequestIFlowToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	state := fmt.Sprintf("ifl-%d", time.Now().UnixNano())
	authSvc := iflowauth.NewIFlowAuth(h.cfg)
	authURL, redirectURI := authSvc.AuthorizationURL(state, iflowauth.CallbackPort)

	RegisterOAuthSession(state, "iflow")

	isWebUI := isWebUIRequest(c)
	var forwarder *callbackForwarder
	if isWebUI {
		targetURL, errTarget := h.managementCallbackURL("/iflow/callback")
		if errTarget != nil {
			log.WithError(errTarget).Error("failed to compute iflow callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "callback server unavailable"})
			return
		}
		var errStart error
		forwarder, errStart = startCallbackForwarder(iflowauth.CallbackPort, "iflow", targetURL)
		if errStart != nil {
			log.WithError(errStart).Error("failed to start iflow callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "failed to start callback server"})
			return
		}
	}

	go func() {
		if isWebUI {
			defer stopCallbackForwarderInstance(iflowauth.CallbackPort, forwarder)
		}

		waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-iflow-%s.oauth", state))
		deadline := time.Now().Add(5 * time.Minute)
		var resultMap map[string]string
		for {
			if !IsOAuthSessionPending(state, "iflow") {
				return
			}
			if time.Now().After(deadline) {
				SetOAuthSessionError(state, "Authentication failed")
				log.Error("iflow authentication failed: timeout waiting for callback")
				return
			}
			data, errRead := os.ReadFile(waitFile)
			if errRead == nil {
				_ = os.Remove(waitFile)
				_ = json.Unmarshal(data, &resultMap)
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		if errStr := strings.TrimSpace(resultMap["error"]); errStr != "" {
			SetOAuthSessionError(state, "Authentication failed")
			log.Errorf("iflow authentication failed: %s", errStr)
			return
		}
		if strings.TrimSpace(resultMap["state"]) != state {
			SetOAuthSessionError(state, "Authentication failed")
			log.Error("iflow authentication failed: state mismatch")
			return
		}
		code := strings.TrimSpace(resultMap["code"])
		if code == "" {
			SetOAuthSessionError(state, "Authentication failed")
			log.Error("iflow authentication failed: code missing")
			return
		}

		tokenData, errExchange := authSvc.ExchangeCodeForTokens(ctx, code, redirectURI)
		if errExchange != nil {
			SetOAuthSessionError(state, "Authentication failed")
			log.Errorf("iflow authentication failed: %v", errExchange)
			return
		}

		tokenStorage := authSvc.CreateTokenStorage(tokenData)
		if tokenStorage == nil {
			SetOAuthSessionError(state, "Authentication failed")
			log.Error("iflow authentication failed: empty token storage")
			return
		}
		identifier := strings.TrimSpace(tokenStorage.Email)
		if identifier == "" {
			identifier = fmt.Sprintf("%d", time.Now().UnixMilli())
			tokenStorage.Email = identifier
		}
		fileName := fmt.Sprintf("iflow-%s.json", iflowauth.SanitizeIFlowFileName(identifier))
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "iflow",
			FileName: fileName,
			Storage:  tokenStorage,
			Metadata: map[string]any{
				"email":         identifier,
				"access_token":  tokenStorage.AccessToken,
				"refresh_token": tokenStorage.RefreshToken,
				"api_key":       tokenStorage.APIKey,
				"expired":       tokenStorage.Expire,
				"type":          "iflow",
			},
			Attributes: map[string]string{"api_key": tokenStorage.APIKey},
		}
		if _, errSave := h.saveTokenRecord(ctx, record); errSave != nil {
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			log.Errorf("failed to save iflow authentication tokens: %v", errSave)
			return
		}

		CompleteOAuthSession(state)
		CompleteOAuthSessionsByProvider("iflow")
	}()

	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) RequestIFlowCookieToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	var payload struct {
		Cookie string `json:"cookie"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "cookie is required"})
		return
	}

	cookieValue, errNormalize := iflowauth.NormalizeCookie(payload.Cookie)
	if errNormalize != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": errNormalize.Error()})
		return
	}

	bxAuth := iflowauth.ExtractBXAuth(cookieValue)
	if existingFile, errDuplicate := iflowauth.CheckDuplicateBXAuth(h.cfg.AuthDir, bxAuth); errDuplicate != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "failed to check duplicate"})
		return
	} else if existingFile != "" {
		c.JSON(http.StatusConflict, gin.H{
			"status":        "error",
			"error":         "duplicate BXAuth found",
			"existing_file": filepath.Base(existingFile),
		})
		return
	}

	authSvc := iflowauth.NewIFlowAuth(h.cfg)
	tokenData, errAuth := authSvc.AuthenticateWithCookie(ctx, cookieValue)
	if errAuth != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": errAuth.Error()})
		return
	}
	tokenData.Cookie = cookieValue

	tokenStorage := authSvc.CreateCookieTokenStorage(tokenData)
	if tokenStorage == nil || strings.TrimSpace(tokenStorage.Email) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "failed to extract email from token"})
		return
	}

	email := strings.TrimSpace(tokenStorage.Email)
	baseName := iflowauth.SanitizeIFlowFileName(email)
	if baseName == "" {
		baseName = fmt.Sprintf("iflow-%d", time.Now().UnixMilli())
	} else {
		baseName = "iflow-" + baseName
	}
	fileName := fmt.Sprintf("%s-%d.json", baseName, time.Now().Unix())

	record := &coreauth.Auth{
		ID:       fileName,
		Provider: "iflow",
		FileName: fileName,
		Storage:  tokenStorage,
		Metadata: map[string]any{
			"email":        email,
			"api_key":      tokenStorage.APIKey,
			"expired":      tokenStorage.Expire,
			"cookie":       tokenStorage.Cookie,
			"type":         "iflow",
			"last_refresh": tokenStorage.LastRefresh,
		},
		Attributes: map[string]string{"api_key": tokenStorage.APIKey},
	}

	savedPath, errSave := h.saveTokenRecord(ctx, record)
	if errSave != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "failed to save authentication tokens"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":     "ok",
		"saved_path": savedPath,
		"email":      email,
		"expired":    tokenStorage.Expire,
		"type":       tokenStorage.Type,
	})
}
