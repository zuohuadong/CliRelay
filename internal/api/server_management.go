package api

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/managementasset"
	log "github.com/sirupsen/logrus"
)

func (s *Server) registerManagementRoutes() {
	if s == nil || s.engine == nil || s.mgmt == nil {
		return
	}
	if !s.managementRoutesRegistered.CompareAndSwap(false, true) {
		return
	}

	log.Info("management routes registered after secret key configuration")

	s.engine.POST("/v0/management/oauth-callback", s.managementAvailabilityMiddleware(), s.mgmt.PostOAuthCallback)
	s.engine.GET("/v0/management/oauth-callback", s.managementAvailabilityMiddleware(), s.mgmt.GetOAuthCallback)

	mgmt := s.engine.Group("/v0/management")
	mgmt.Use(s.managementAvailabilityMiddleware(), s.mgmt.Middleware())
	{
		mgmt.GET("/dashboard-summary", s.mgmt.GetDashboardSummary)
		mgmt.GET("/system-stats", s.mgmt.GetSystemStats)
		mgmt.GET("/system-stats/ws", s.mgmt.SystemStatsWebSocket)

		mgmt.GET("/config", s.mgmt.GetConfig)
		mgmt.GET("/config.yaml", s.mgmt.GetConfigYAML)
		mgmt.PUT("/config.yaml", s.mgmt.PutConfigYAML)
		mgmt.GET("/latest-version", s.mgmt.GetLatestVersion)
		mgmt.GET("/auto-update/enabled", s.mgmt.GetAutoUpdateEnabled)
		mgmt.PUT("/auto-update/enabled", s.mgmt.PutAutoUpdateEnabled)
		mgmt.GET("/plugins", s.mgmt.ListPlugins)
		mgmt.GET("/plugin-store", s.mgmt.ListPluginStore)
		mgmt.POST("/plugin-store/:id/install", s.mgmt.InstallPluginFromStore)
		mgmt.DELETE("/plugins/:id", s.mgmt.DeletePlugin)
		mgmt.PATCH("/plugins/:id/enabled", s.mgmt.PatchPluginEnabled)
		mgmt.GET("/plugins/:id/config", s.mgmt.GetPluginConfig)
		mgmt.PUT("/plugins/:id/config", s.mgmt.PutPluginConfig)
		mgmt.PATCH("/plugins/:id/config", s.mgmt.PatchPluginConfig)

		mgmt.GET("/debug", s.mgmt.GetDebug)
		mgmt.PUT("/debug", s.mgmt.PutDebug)
		mgmt.PATCH("/debug", s.mgmt.PutDebug)

		mgmt.GET("/logging-to-file", s.mgmt.GetLoggingToFile)
		mgmt.PUT("/logging-to-file", s.mgmt.PutLoggingToFile)
		mgmt.PATCH("/logging-to-file", s.mgmt.PutLoggingToFile)

		mgmt.GET("/logs-max-total-size-mb", s.mgmt.GetLogsMaxTotalSizeMB)
		mgmt.PUT("/logs-max-total-size-mb", s.mgmt.PutLogsMaxTotalSizeMB)
		mgmt.PATCH("/logs-max-total-size-mb", s.mgmt.PutLogsMaxTotalSizeMB)

		mgmt.GET("/error-logs-max-files", s.mgmt.GetErrorLogsMaxFiles)
		mgmt.PUT("/error-logs-max-files", s.mgmt.PutErrorLogsMaxFiles)
		mgmt.PATCH("/error-logs-max-files", s.mgmt.PutErrorLogsMaxFiles)

		mgmt.GET("/usage-statistics-enabled", s.mgmt.GetUsageStatisticsEnabled)
		mgmt.PUT("/usage-statistics-enabled", s.mgmt.PutUsageStatisticsEnabled)
		mgmt.PATCH("/usage-statistics-enabled", s.mgmt.PutUsageStatisticsEnabled)

		mgmt.GET("/auto-update/channel", s.mgmt.GetAutoUpdateChannel)
		mgmt.PUT("/auto-update/channel", s.mgmt.PutAutoUpdateChannel)
		mgmt.PATCH("/auto-update/channel", s.mgmt.PutAutoUpdateChannel)
		mgmt.GET("/update/current", s.mgmt.GetCurrentUpdateState)
		mgmt.GET("/update/check", s.mgmt.CheckUpdate)
		mgmt.GET("/update/progress", s.mgmt.GetUpdateProgress)
		mgmt.POST("/update/apply", s.mgmt.ApplyUpdate)

		mgmt.GET("/proxy-url", s.mgmt.GetProxyURL)
		mgmt.PUT("/proxy-url", s.mgmt.PutProxyURL)
		mgmt.PATCH("/proxy-url", s.mgmt.PutProxyURL)
		mgmt.DELETE("/proxy-url", s.mgmt.DeleteProxyURL)

		mgmt.GET("/identity-fingerprint", s.mgmt.GetIdentityFingerprint)
		mgmt.PUT("/identity-fingerprint", s.mgmt.PutIdentityFingerprint)
		mgmt.PATCH("/identity-fingerprint", s.mgmt.PatchIdentityFingerprint)

		mgmt.POST("/api-call", s.mgmt.APICall)

		// Codex 出口网络管理路由（处理器位于 egress_network.go）。
		mgmt.GET("/egress/overview", s.mgmt.GetEgressOverview)
		mgmt.GET("/egress/endpoints", s.mgmt.GetEgressEndpoints)
		mgmt.POST("/egress/endpoints", s.mgmt.PostEgressEndpoint)
		mgmt.PATCH("/egress/endpoints/:id", s.mgmt.PatchEgressEndpoint)
		mgmt.DELETE("/egress/endpoints/:id", s.mgmt.DeleteEgressEndpoint)
		mgmt.POST("/egress/endpoints/:id/check", s.mgmt.PostEgressEndpointCheck)
		mgmt.POST("/egress/endpoints/:id/impact", s.mgmt.PostEgressEndpointImpact)
		mgmt.POST("/egress/endpoints/:id/actions", s.mgmt.PostEgressEndpointAction)
		mgmt.GET("/egress/bindings", s.mgmt.GetEgressBindings)
		mgmt.POST("/egress/bindings/preview", s.mgmt.PostEgressBindingPreview)
		mgmt.PUT("/egress/bindings/batch", s.mgmt.PutEgressBindingBatch)

		mgmt.GET("/quota-exceeded/switch-project", s.mgmt.GetSwitchProject)
		mgmt.PUT("/quota-exceeded/switch-project", s.mgmt.PutSwitchProject)
		mgmt.PATCH("/quota-exceeded/switch-project", s.mgmt.PutSwitchProject)

		mgmt.GET("/quota-exceeded/switch-preview-model", s.mgmt.GetSwitchPreviewModel)
		mgmt.PUT("/quota-exceeded/switch-preview-model", s.mgmt.PutSwitchPreviewModel)
		mgmt.PATCH("/quota-exceeded/switch-preview-model", s.mgmt.PutSwitchPreviewModel)
		mgmt.POST("/reset-quota", s.mgmt.ResetQuota)
		mgmt.POST("/quota/reconcile", s.mgmt.ReconcileQuota)

		mgmt.GET("/api-keys", s.mgmt.GetAPIKeys)
		mgmt.PUT("/api-keys", s.mgmt.PutAPIKeys)
		mgmt.PATCH("/api-keys", s.mgmt.PatchAPIKeys)
		mgmt.DELETE("/api-keys", s.mgmt.DeleteAPIKeys)
		mgmt.GET("/api-key-entries", s.mgmt.GetAPIKeyEntries)
		mgmt.PUT("/api-key-entries", s.mgmt.PutAPIKeyEntries)
		mgmt.PATCH("/api-key-entries", s.mgmt.PatchAPIKeyEntries)
		mgmt.DELETE("/api-key-entries", s.mgmt.DeleteAPIKeyEntries)
		mgmt.GET("/api-key-permission-profiles", s.mgmt.GetAPIKeyPermissionProfiles)
		mgmt.PUT("/api-key-permission-profiles", s.mgmt.PutAPIKeyPermissionProfiles)
		mgmt.GET("/api-key-usage", s.mgmt.GetAPIKeyUsage)
		mgmt.GET("/usage-queue", s.mgmt.GetUsageQueue)

		mgmt.GET("/usage", s.mgmt.GetUsageSummary)
		mgmt.GET("/usage/chart-data", s.mgmt.GetUsageChartData)
		mgmt.GET("/usage/entity-stats", s.mgmt.GetUsageEntityStats)
		mgmt.GET("/usage/logs", s.mgmt.GetUsageLogs)
		mgmt.DELETE("/usage/logs", s.mgmt.DeleteUsageLogs)
		mgmt.GET("/usage/logs/:id/content", s.mgmt.GetUsageLogContent)
		mgmt.GET("/usage/export", s.mgmt.ExportUsage)
		mgmt.POST("/usage/import", s.mgmt.ImportUsage)
		mgmt.POST("/usage/auth-file-quota-snapshot", s.mgmt.RecordAuthFileQuotaSnapshot)
		mgmt.GET("/usage/auth-file-group-trend", s.mgmt.GetAuthFileGroupTrend)
		mgmt.GET("/usage/auth-file-trend", s.mgmt.GetAuthFileTrend)
		mgmt.GET("/usage/summary/public", s.mgmt.GetUsageSummaryPublic)

		mgmt.GET("/gemini-api-key", s.mgmt.GetGeminiKeys)
		mgmt.PUT("/gemini-api-key", s.mgmt.PutGeminiKeys)
		mgmt.PATCH("/gemini-api-key", s.mgmt.PatchGeminiKey)
		mgmt.DELETE("/gemini-api-key", s.mgmt.DeleteGeminiKey)

		mgmt.GET("/opencode-go-api-key", s.mgmt.GetOpenCodeGoKeys)
		mgmt.PUT("/opencode-go-api-key", s.mgmt.PutOpenCodeGoKeys)
		mgmt.PATCH("/opencode-go-api-key", s.mgmt.PutOpenCodeGoKeys)
		mgmt.DELETE("/opencode-go-api-key", s.mgmt.DeleteOpenCodeGoKey)

		mgmt.GET("/bigmodel-coding-api-key", s.mgmt.GetBigModelCodingKeys)
		mgmt.PUT("/bigmodel-coding-api-key", s.mgmt.PutBigModelCodingKeys)
		mgmt.PATCH("/bigmodel-coding-api-key", s.mgmt.PatchBigModelCodingKey)
		mgmt.DELETE("/bigmodel-coding-api-key", s.mgmt.DeleteBigModelCodingKey)

		mgmt.GET("/astron-code-api-key", s.mgmt.GetAstronCodeKeys)
		mgmt.PUT("/astron-code-api-key", s.mgmt.PutAstronCodeKeys)
		mgmt.PATCH("/astron-code-api-key", s.mgmt.PatchAstronCodeKey)
		mgmt.DELETE("/astron-code-api-key", s.mgmt.DeleteAstronCodeKey)

		mgmt.GET("/agnes-api-key", s.mgmt.GetAgnesKeys)
		mgmt.PUT("/agnes-api-key", s.mgmt.PutAgnesKeys)
		mgmt.PATCH("/agnes-api-key", s.mgmt.PatchAgnesKey)
		mgmt.DELETE("/agnes-api-key", s.mgmt.DeleteAgnesKey)

		mgmt.GET("/bedrock-api-key", s.mgmt.GetBedrockKeys)
		mgmt.PUT("/bedrock-api-key", s.mgmt.PutBedrockKeys)
		mgmt.DELETE("/bedrock-api-key", s.mgmt.DeleteBedrockKey)

		mgmt.GET("/iflow-api-key", s.mgmt.GetIFlowKeys)
		mgmt.PUT("/iflow-api-key", s.mgmt.PutIFlowKeys)
		mgmt.PATCH("/iflow-api-key", s.mgmt.PatchIFlowKey)
		mgmt.DELETE("/iflow-api-key", s.mgmt.DeleteIFlowKey)

		mgmt.GET("/interactions-api-key", s.mgmt.GetInteractionsKeys)
		mgmt.PUT("/interactions-api-key", s.mgmt.PutInteractionsKeys)
		mgmt.PATCH("/interactions-api-key", s.mgmt.PatchInteractionsKey)
		mgmt.DELETE("/interactions-api-key", s.mgmt.DeleteInteractionsKey)

		mgmt.GET("/logs", s.mgmt.GetLogs)
		mgmt.DELETE("/logs", s.mgmt.DeleteLogs)
		mgmt.GET("/request-error-logs", s.mgmt.GetRequestErrorLogs)
		mgmt.GET("/request-error-logs/:name", s.mgmt.DownloadRequestErrorLog)
		mgmt.GET("/request-log-by-id/:id", s.mgmt.GetRequestLogByID)
		mgmt.GET("/request-log", s.mgmt.GetRequestLog)
		mgmt.PUT("/request-log", s.mgmt.PutRequestLog)
		mgmt.PATCH("/request-log", s.mgmt.PutRequestLog)
		mgmt.GET("/request-log-body", s.mgmt.GetRequestLogBody)
		mgmt.PUT("/request-log-body", s.mgmt.PutRequestLogBody)
		mgmt.PATCH("/request-log-body", s.mgmt.PutRequestLogBody)
		mgmt.GET("/ws-auth", s.mgmt.GetWebsocketAuth)
		mgmt.PUT("/ws-auth", s.mgmt.PutWebsocketAuth)
		mgmt.PATCH("/ws-auth", s.mgmt.PutWebsocketAuth)

		mgmt.GET("/request-retry", s.mgmt.GetRequestRetry)
		mgmt.PUT("/request-retry", s.mgmt.PutRequestRetry)
		mgmt.PATCH("/request-retry", s.mgmt.PutRequestRetry)
		mgmt.GET("/max-retry-credentials", s.mgmt.GetMaxRetryCredentials)
		mgmt.PUT("/max-retry-credentials", s.mgmt.PutMaxRetryCredentials)
		mgmt.PATCH("/max-retry-credentials", s.mgmt.PutMaxRetryCredentials)
		mgmt.GET("/max-retry-interval", s.mgmt.GetMaxRetryInterval)
		mgmt.PUT("/max-retry-interval", s.mgmt.PutMaxRetryInterval)
		mgmt.PATCH("/max-retry-interval", s.mgmt.PutMaxRetryInterval)

		mgmt.GET("/force-model-prefix", s.mgmt.GetForceModelPrefix)
		mgmt.PUT("/force-model-prefix", s.mgmt.PutForceModelPrefix)
		mgmt.PATCH("/force-model-prefix", s.mgmt.PutForceModelPrefix)

		mgmt.GET("/routing/strategy", s.mgmt.GetRoutingStrategy)
		mgmt.PUT("/routing/strategy", s.mgmt.PutRoutingStrategy)
		mgmt.PATCH("/routing/strategy", s.mgmt.PutRoutingStrategy)
		mgmt.GET("/routing-config", s.mgmt.GetRoutingConfig)
		mgmt.PUT("/routing-config", s.mgmt.PutRoutingConfig)
		mgmt.GET("/channel-groups", s.mgmt.GetChannelGroups)
		mgmt.GET("/ccswitch-import-configs", s.mgmt.GetCCSwitchImportConfigs)
		mgmt.PUT("/ccswitch-import-configs", s.mgmt.PutCCSwitchImportConfigs)

		mgmt.GET("/ampcode", s.mgmt.GetAmpCode)
		mgmt.PUT("/ampcode/upstream-url", s.mgmt.PutAmpCodeUpstreamURL)
		mgmt.DELETE("/ampcode/upstream-url", s.mgmt.DeleteAmpCodeUpstreamURL)
		mgmt.PUT("/ampcode/upstream-api-key", s.mgmt.PutAmpCodeUpstreamAPIKey)
		mgmt.DELETE("/ampcode/upstream-api-key", s.mgmt.DeleteAmpCodeUpstreamAPIKey)
		mgmt.GET("/ampcode/model-mappings", s.mgmt.GetAmpCodeModelMappings)
		mgmt.PUT("/ampcode/model-mappings", s.mgmt.PutAmpCodeModelMappings)
		mgmt.PATCH("/ampcode/model-mappings", s.mgmt.PatchAmpCodeModelMappings)
		mgmt.DELETE("/ampcode/model-mappings", s.mgmt.DeleteAmpCodeModelMappings)
		mgmt.PUT("/ampcode/force-model-mappings", s.mgmt.PutAmpCodeForceModelMappings)

		mgmt.GET("/claude-api-key", s.mgmt.GetClaudeKeys)
		mgmt.PUT("/claude-api-key", s.mgmt.PutClaudeKeys)
		mgmt.PATCH("/claude-api-key", s.mgmt.PatchClaudeKey)
		mgmt.DELETE("/claude-api-key", s.mgmt.DeleteClaudeKey)

		mgmt.GET("/codex-api-key", s.mgmt.GetCodexKeys)
		mgmt.PUT("/codex-api-key", s.mgmt.PutCodexKeys)
		mgmt.PATCH("/codex-api-key", s.mgmt.PatchCodexKey)
		mgmt.DELETE("/codex-api-key", s.mgmt.DeleteCodexKey)

		mgmt.GET("/xai-api-key", s.mgmt.GetXAIKeys)
		mgmt.PUT("/xai-api-key", s.mgmt.PutXAIKeys)
		mgmt.PATCH("/xai-api-key", s.mgmt.PatchXAIKey)
		mgmt.DELETE("/xai-api-key", s.mgmt.DeleteXAIKey)

		mgmt.GET("/openai-compatibility", s.mgmt.GetOpenAICompat)
		mgmt.PUT("/openai-compatibility", s.mgmt.PutOpenAICompat)
		mgmt.PATCH("/openai-compatibility", s.mgmt.PatchOpenAICompat)
		mgmt.DELETE("/openai-compatibility", s.mgmt.DeleteOpenAICompat)

		mgmt.GET("/vertex-api-key", s.mgmt.GetVertexCompatKeys)
		mgmt.PUT("/vertex-api-key", s.mgmt.PutVertexCompatKeys)
		mgmt.PATCH("/vertex-api-key", s.mgmt.PatchVertexCompatKey)
		mgmt.DELETE("/vertex-api-key", s.mgmt.DeleteVertexCompatKey)

		mgmt.GET("/oauth-excluded-models", s.mgmt.GetOAuthExcludedModels)
		mgmt.PUT("/oauth-excluded-models", s.mgmt.PutOAuthExcludedModels)
		mgmt.PATCH("/oauth-excluded-models", s.mgmt.PatchOAuthExcludedModels)
		mgmt.DELETE("/oauth-excluded-models", s.mgmt.DeleteOAuthExcludedModels)

		mgmt.GET("/oauth-model-alias", s.mgmt.GetOAuthModelAlias)
		mgmt.PUT("/oauth-model-alias", s.mgmt.PutOAuthModelAlias)
		mgmt.PATCH("/oauth-model-alias", s.mgmt.PatchOAuthModelAlias)
		mgmt.DELETE("/oauth-model-alias", s.mgmt.DeleteOAuthModelAlias)

		mgmt.GET("/oauth-request-scoped-errors", s.mgmt.GetOAuthRequestScopedErrors)
		mgmt.PUT("/oauth-request-scoped-errors", s.mgmt.PutOAuthRequestScopedErrors)
		mgmt.PATCH("/oauth-request-scoped-errors", s.mgmt.PatchOAuthRequestScopedErrors)
		mgmt.DELETE("/oauth-request-scoped-errors", s.mgmt.DeleteOAuthRequestScopedErrors)

		mgmt.GET("/auth-files", s.mgmt.ListAuthFiles)
		mgmt.GET("/auth-files/download", s.mgmt.DownloadAuthFile)
		mgmt.POST("/auth-files/agent-identity/provision", s.mgmt.ProvisionAgentIdentity)
		mgmt.POST("/auth-files/agent-identity/export", s.mgmt.ExportAgentIdentityAuth)
		mgmt.GET("/auth-files/page", s.mgmt.ServeAuthFilesPage)
		mgmt.GET("/auth-files/models", s.mgmt.GetAuthFileModels)
		mgmt.GET("/models", s.mgmt.GetModels)
		mgmt.GET("/model-configs", s.mgmt.GetModelConfigs)
		mgmt.GET("/model-owner-presets", s.mgmt.GetModelOwnerPresets)
		mgmt.PUT("/model-owner-presets", s.mgmt.PutModelOwnerPresets)
		mgmt.GET("/model-path-availability", s.mgmt.GetModelPathAvailability)
		mgmt.GET("/models/configured-availability", s.mgmt.GetConfiguredModelAvailability)
		mgmt.GET("/model-definitions/:channel", s.mgmt.GetStaticModelDefinitions)
		mgmt.GET("/model-openrouter-sync", s.mgmt.GetOpenRouterSync)
		mgmt.PUT("/model-openrouter-sync", s.mgmt.PutOpenRouterSync)
		mgmt.POST("/model-openrouter-sync/run", s.mgmt.RunOpenRouterSync)
		mgmt.GET("/model-prices", s.mgmt.GetModelPrices)
		mgmt.PUT("/model-prices/:model", s.mgmt.PutModelPrice)
		mgmt.DELETE("/model-prices/:model", s.mgmt.DeleteModelPrice)
		mgmt.POST("/model-prices/refresh", s.mgmt.RefreshModelPrices)
		mgmt.POST("/auth-files", s.mgmt.UploadAuthFile)
		mgmt.DELETE("/auth-files", s.mgmt.DeleteAuthFile)
		mgmt.PATCH("/auth-files/status", s.mgmt.PatchAuthFileStatus)
		mgmt.PATCH("/auth-files/fields", s.mgmt.PatchAuthFileFields)
		mgmt.GET("/auth-files/codex-reset-credits", s.mgmt.GetCodexResetCredits)
		mgmt.POST("/auth-files/codex-reset-credits/consume", s.mgmt.ConsumeCodexResetCredit)
		mgmt.POST("/vertex/import", s.mgmt.ImportVertexCredential)

		mgmt.GET("/anthropic-auth-url", s.mgmt.RequestAnthropicToken)
		mgmt.GET("/codex-auth-url", s.mgmt.RequestCodexToken)
		mgmt.GET("/antigravity-auth-url", s.mgmt.RequestAntigravityToken)
		mgmt.GET("/kimi-auth-url", s.mgmt.RequestKimiToken)
		mgmt.GET("/qwen-auth-url", s.mgmt.RequestQwenToken)
		mgmt.GET("/iflow-auth-url", s.mgmt.RequestIFlowToken)
		mgmt.POST("/iflow-auth-url", s.mgmt.RequestIFlowTokenOrCookie)
		mgmt.GET("/xai-auth-url", s.mgmt.RequestXAIToken)
		mgmt.GET("/get-auth-status", s.mgmt.GetAuthStatus)
		mgmt.DELETE("/oauth-session", s.mgmt.CancelAuthSession)

		mgmt.GET("/image-generation/channels", s.mgmt.GetImageGenerationChannels)
		mgmt.POST("/image-generation/test", s.mgmt.StartImageGenerationTest)
		mgmt.GET("/image-generation/test/:task_id", s.mgmt.GetImageGenerationTest)
	}
}

func (s *Server) managementAvailabilityMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.managementAvailable(c) {
			return
		}
		c.Next()
	}
}

func (s *Server) managementAvailable(c *gin.Context) bool {
	if s == nil || s.cfg == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return false
	}
	if s.cfg.Home.Enabled {
		c.AbortWithStatus(http.StatusNotFound)
		return false
	}
	if !s.managementRoutesEnabled.Load() {
		c.AbortWithStatus(http.StatusNotFound)
		return false
	}
	return true
}

func (s *Server) refreshPluginManagementRoutes() {
	if s == nil || s.pluginHost == nil || s.engine == nil {
		return
	}
	s.pluginHost.RegisterManagementRoutes(context.Background(), s.registeredManagementRouteKeys())
}

// RefreshPluginManagementRoutes rebuilds plugin-owned Management API routes.
func (s *Server) RefreshPluginManagementRoutes() {
	s.refreshPluginManagementRoutes()
}

func (s *Server) registeredManagementRouteKeys() map[string]struct{} {
	out := make(map[string]struct{})
	if s == nil || s.engine == nil {
		return out
	}
	for _, route := range s.engine.Routes() {
		if strings.HasPrefix(route.Path, "/v0/management/") || route.Path == "/v0/management" {
			out[strings.ToUpper(strings.TrimSpace(route.Method))+" "+route.Path] = struct{}{}
		}
	}
	return out
}

func (s *Server) pluginManagementNoRoute(c *gin.Context) {
	if s == nil || c == nil || c.Request == nil || c.Request.URL == nil {
		if c != nil {
			c.AbortWithStatus(http.StatusNotFound)
		}
		return
	}
	path := c.Request.URL.Path
	if strings.HasPrefix(path, "/v0/resource/plugins/") {
		s.pluginResourceNoRoute(c)
		return
	}
	if path != "/v0/management" && !strings.HasPrefix(path, "/v0/management/") {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if s.pluginHost == nil || s.mgmt == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !s.managementAvailable(c) {
		return
	}
	s.mgmt.Middleware()(c)
	if c.IsAborted() {
		return
	}
	if s.mgmt.ServePluginAuthURL(c) {
		c.Abort()
		return
	}
	if s.pluginHost.ServeManagementHTTP(c.Writer, c.Request) {
		c.Abort()
		return
	}
	c.AbortWithStatus(http.StatusNotFound)
}

func (s *Server) pluginResourceNoRoute(c *gin.Context) {
	if s == nil || c == nil || c.Request == nil || c.Request.URL == nil {
		if c != nil {
			c.AbortWithStatus(http.StatusNotFound)
		}
		return
	}
	if s.cfg == nil || s.cfg.Home.Enabled || s.pluginHost == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if s.pluginHost.ServeResourceHTTP(c.Writer, c.Request) {
		c.Abort()
		return
	}
	c.AbortWithStatus(http.StatusNotFound)
}

// ensureManagementControlPanel 确保管理面板入口资产存在，必要时同步下载。
// 返回 false 表示请求已被中止。
func (s *Server) ensureManagementControlPanel(c *gin.Context) bool {
	cfg := s.cfg
	if cfg == nil || cfg.Home.Enabled || cfg.RemoteManagement.DisableControlPanel {
		c.AbortWithStatus(http.StatusNotFound)
		return false
	}
	filePath := managementasset.FilePath(s.configFilePath)
	if strings.TrimSpace(filePath) == "" {
		c.AbortWithStatus(http.StatusNotFound)
		return false
	}

	if _, err := os.Stat(filePath); err != nil {
		if os.IsNotExist(err) {
			// Synchronously ensure the panel asset is available with a detached context.
			// Control panel bootstrap should not be canceled by client disconnects.
			if !managementasset.EnsureLatestManagementHTML(context.Background(), managementasset.StaticDir(s.configFilePath), cfg.ProxyURL, cfg.RemoteManagement.PanelGitHubRepository) {
				c.AbortWithStatus(http.StatusNotFound)
				return false
			}
		} else {
			log.WithError(err).Error("failed to stat management control panel asset")
			c.AbortWithStatus(http.StatusInternalServerError)
			return false
		}
	}

	return true
}

func (s *Server) serveManagementControlPanel(c *gin.Context) {
	if !s.ensureManagementControlPanel(c) {
		return
	}

	filePath := managementasset.FilePath(s.configFilePath)
	if strings.TrimSpace(filePath) == "" {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	c.File(filePath)
}

// serveManagementControlPanelAsset 服务 /manage/ 挂载点下的面板静态资源。
// assets/ 下的未命中资源直接 404（带哈希文件名，不缓存错误），其余未命中
// 路径回退到面板入口以支持 SPA 前端路由。
func (s *Server) serveManagementControlPanelAsset(c *gin.Context) {
	if !s.ensureManagementControlPanel(c) {
		return
	}

	requestedPath := strings.TrimPrefix(c.Param("filepath"), "/")
	if requestedPath == "" {
		c.File(managementasset.FilePath(s.configFilePath))
		return
	}

	filePath, ok := managementasset.AssetPath(s.configFilePath, requestedPath)
	if !ok {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	info, err := os.Stat(filePath)
	if err == nil && !info.IsDir() {
		c.File(filePath)
		return
	}
	if err != nil && !os.IsNotExist(err) {
		log.WithError(err).Error("failed to stat management control panel asset")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	if strings.HasPrefix(requestedPath, "assets/") {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	c.File(managementasset.FilePath(s.configFilePath))
}
