package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	requestlogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	wsRequestTypeCreate  = "response.create"
	wsRequestTypeAppend  = "response.append"
	wsEventTypeError     = "error"
	wsEventTypeCompleted = "response.completed"
	wsDoneMarker         = "[DONE]"
	wsTurnStateHeader    = "x-codex-turn-state"
	wsTimelineBodyKey    = "WEBSOCKET_TIMELINE_OVERRIDE"

	responsesWebsocketTimelineMaxBytes        = 1 << 20
	responsesWebsocketTimelinePayloadMaxBytes = 64 << 10

	// 下游 websocket 心跳：定期发送 ping frame 防止客户端 idle timeout
	responsesWebsocketReadTimeout               = 60 * time.Second
	responsesWebsocketWriteTimeout              = 10 * time.Second
	responsesWebsocketHeartbeatInterval         = 30 * time.Second
	responsesWebsocketTurnOutputLimitContextKey = "clirelay.responses.websocket_turn_output_limit"
)

var responsesWebsocketUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type websocketTimelineAppender interface {
	Append(eventType string, payload []byte, timestamp time.Time)
}

type websocketTimelineLog struct {
	enabled bool
	source  *requestlogging.FileBodySource
	builder *strings.Builder

	currentPart       io.WriteCloser
	currentPartHasLog bool
}

func newWebsocketTimelineLog(enabled bool, source *requestlogging.FileBodySource) *websocketTimelineLog {
	if !enabled {
		return &websocketTimelineLog{}
	}
	if source == nil {
		return newInMemoryWebsocketTimelineLog()
	}
	return &websocketTimelineLog{
		enabled: true,
		source:  source,
	}
}

func newInMemoryWebsocketTimelineLog() *websocketTimelineLog {
	return &websocketTimelineLog{
		enabled: true,
		builder: &strings.Builder{},
	}
}

func websocketTimelineSourceFromContext(c *gin.Context) *requestlogging.FileBodySource {
	if c == nil {
		return nil
	}
	value, exists := c.Get(requestlogging.WebsocketTimelineSourceContextKey)
	if !exists {
		return nil
	}
	source, ok := value.(*requestlogging.FileBodySource)
	if !ok {
		return nil
	}
	return source
}

func (l *websocketTimelineLog) BeginRequest() {
	if l == nil || !l.enabled || l.source == nil {
		return
	}
	l.closeCurrentPart()
	part, errCreate := l.source.CreatePart("request")
	if errCreate != nil {
		log.WithError(errCreate).Warn("failed to create websocket request detail log")
		return
	}
	l.currentPart = part
	l.currentPartHasLog = false
}

func (l *websocketTimelineLog) Append(eventType string, payload []byte, timestamp time.Time) {
	if l == nil || !l.enabled {
		return
	}
	data := formatWebsocketTimelineEvent(eventType, payload, timestamp)
	if len(data) == 0 {
		return
	}
	if l.source != nil {
		if l.currentPart == nil {
			l.BeginRequest()
		}
		if l.currentPart == nil {
			return
		}
		if errWrite := writeWebsocketTimelinePart(l.currentPart, data, l.currentPartHasLog); errWrite != nil {
			log.WithError(errWrite).Warn("failed to write websocket request detail log")
			return
		}
		l.currentPartHasLog = true
		return
	}
	if l.builder != nil {
		writeWebsocketTimelineBuilder(l.builder, data)
	}
}

func (l *websocketTimelineLog) SetContext(c *gin.Context) {
	if l == nil || !l.enabled {
		return
	}
	l.closeCurrentPart()
	if l.source != nil {
		if l.source.HasPayload() {
			c.Set(requestlogging.WebsocketTimelineSourceContextKey, l.source)
			return
		}
		if errCleanup := l.source.Cleanup(); errCleanup != nil {
			log.WithError(errCleanup).Warn("failed to clean up empty websocket timeline log parts")
		}
	}
	if l.builder != nil {
		setWebsocketTimelineBody(c, l.builder.String())
	}
}

func (l *websocketTimelineLog) String() string {
	if l == nil || !l.enabled {
		return ""
	}
	l.closeCurrentPart()
	if l.source != nil {
		data, errRead := l.source.Bytes()
		if errRead != nil {
			return ""
		}
		return string(data)
	}
	if l.builder == nil {
		return ""
	}
	return l.builder.String()
}

func (l *websocketTimelineLog) closeCurrentPart() {
	if l == nil || l.currentPart == nil {
		return
	}
	if errClose := l.currentPart.Close(); errClose != nil {
		log.WithError(errClose).Warn("failed to close websocket request detail log")
	}
	l.currentPart = nil
	l.currentPartHasLog = false
}

func writeWebsocketTimelinePart(w io.Writer, data []byte, prependNewline bool) error {
	if w == nil || len(data) == 0 {
		return nil
	}
	if prependNewline {
		if _, errWrite := io.WriteString(w, "\n"); errWrite != nil {
			return errWrite
		}
	}
	_, errWrite := w.Write(data)
	return errWrite
}

func writeWebsocketTimelineBuilder(builder *strings.Builder, data []byte) {
	if builder == nil || len(data) == 0 || builder.Len() >= responsesWebsocketTimelineMaxBytes {
		return
	}
	if builder.Len() > 0 {
		if builder.Len()+1 > responsesWebsocketTimelineMaxBytes {
			return
		}
		builder.WriteString("\n")
	}
	remaining := responsesWebsocketTimelineMaxBytes - builder.Len()
	if len(data) > remaining {
		data = data[:remaining]
	}
	builder.Write(data)
}

// ResponsesWebsocket handles websocket requests for /v1/responses.
// It accepts `response.create` and `response.append` requests and streams
// response events back as JSON websocket text messages.
func (h *OpenAIResponsesAPIHandler) ResponsesWebsocket(c *gin.Context) {
	if h != nil && h.websocketConnections != nil && !h.websocketConnections.tryAcquire(h.responsesWebsocketMaxConnections()) {
		c.Header("Retry-After", "1")
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"message": "responses websocket connection limit exceeded", "type": "rate_limit_exceeded", "code": "websocket_connection_limit_exceeded"}})
		return
	}
	if h != nil && h.websocketConnections != nil {
		defer h.websocketConnections.release()
	}
	conn, err := responsesWebsocketUpgrader.Upgrade(c.Writer, c.Request, websocketUpgradeHeaders(c.Request))
	if err != nil {
		return
	}
	// Bound each downstream frame before ReadMessage allocates its payload. This
	// is independent of upstream request policies, which run only after parsing.
	conn.SetReadLimit(h.responsesMaxInboundBytes())
	passthroughSessionID := uuid.NewString()
	downstreamSessionKey := websocketDownstreamSessionKey(c.Request)
	retainResponsesWebsocketToolCaches(downstreamSessionKey)
	configureResponsesWebsocketToolCaches(downstreamSessionKey, h.responsesWebsocketToolCacheBytes())
	clientIP := websocketClientAddress(c)
	log.Infof("responses websocket: client connected id=%s remote=%s", passthroughSessionID, clientIP)

	requestLogEnabled := h != nil && h.Cfg != nil && h.Cfg.RequestLog
	wsTimelineLog := newWebsocketTimelineLog(requestLogEnabled, websocketTimelineSourceFromContext(c))

	wsDone := make(chan struct{})
	defer close(wsDone)
	startResponsesWebsocketHeartbeat(conn, wsDone, passthroughSessionID, responsesWebsocketHeartbeatInterval)

	if h != nil && h.AuthManager != nil {
		if exec, ok := h.AuthManager.Executor("codex"); ok && exec != nil {
			type upstreamDisconnectSubscriber interface {
				UpstreamDisconnectChan(sessionID string) <-chan error
			}
			if subscriber, ok := exec.(upstreamDisconnectSubscriber); ok && subscriber != nil {
				disconnectCh := subscriber.UpstreamDisconnectChan(passthroughSessionID)
				if disconnectCh != nil {
					go func() {
						select {
						case <-wsDone:
							return
						case <-disconnectCh:
							_ = conn.Close()
						}
					}()
				}
			}
		}
	}

	var wsTerminateErr error
	defer func() {
		releaseResponsesWebsocketToolCaches(downstreamSessionKey)
		if wsTerminateErr != nil {
			appendWebsocketTimelineDisconnect(wsTimelineLog, wsTerminateErr, time.Now())
			// log.Infof("responses websocket: session closing id=%s reason=%v", passthroughSessionID, wsTerminateErr)
		} else {
			log.Infof("responses websocket: session closing id=%s", passthroughSessionID)
		}
		if h != nil && h.AuthManager != nil {
			h.AuthManager.CloseExecutionSession(passthroughSessionID)
			log.Infof("responses websocket: upstream execution session closed id=%s", passthroughSessionID)
		}
		wsTimelineLog.SetContext(c)
		if errClose := conn.Close(); errClose != nil {
			log.Warnf("responses websocket: close connection error: %v", errClose)
		}
	}()

	var lastRequest []byte
	lastResponseOutput := []byte("[]")
	pinnedAuthID := ""
	sessionAuthByID := func(authID string) (*coreauth.Auth, bool) {
		if h == nil || h.AuthManager == nil {
			return nil, false
		}
		if auth, ok := h.AuthManager.GetExecutionSessionAuthByID(passthroughSessionID, authID); ok {
			return auth, true
		}
		return h.AuthManager.GetByID(authID)
	}
	forceTranscriptReplayNextRequest := false
	var stateReservation *responsesWebsocketMemoryReservation
	if h != nil && h.websocketMemoryBudget != nil {
		stateReservation = h.websocketMemoryBudget.newReservation()
		defer stateReservation.release()
	}

	for {
		conn.SetReadLimit(h.responsesMaxInboundBytes())
		msgType, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			wsTerminateErr = errReadMessage
			if websocket.IsCloseError(errReadMessage, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) {
				log.Infof("responses websocket: client disconnected id=%s error=%v", passthroughSessionID, errReadMessage)
			} else {
				// log.Warnf("responses websocket: read message failed id=%s error=%v", passthroughSessionID, errReadMessage)
			}
			return
		}
		if msgType != websocket.TextMessage && msgType != websocket.BinaryMessage {
			continue
		}
		// log.Infof(
		// 	"responses websocket: downstream_in id=%s type=%d event=%s payload=%s",
		// 	passthroughSessionID,
		// 	msgType,
		// 	websocketPayloadEventType(payload),
		// 	websocketPayloadPreview(payload),
		// )
		wsTimelineLog.BeginRequest()
		wsTimelineLog.Append("request", payload, time.Now())

		maxReplayRetries := handlers.ResponsesWebsocketReplayRetries(h.Cfg)
		for replayAttempt := 0; replayAttempt <= maxReplayRetries; replayAttempt++ {
			limits := h.responsesWebsocketTurnLimitsSnapshot()
			configureResponsesWebsocketToolCaches(downstreamSessionKey, limits.toolCacheBytes)
			allowIncrementalInputWithPreviousResponseID := false
			if pinnedAuthID != "" {
				if pinnedAuth, ok := sessionAuthByID(pinnedAuthID); ok && pinnedAuth != nil {
					allowIncrementalInputWithPreviousResponseID = websocketUpstreamSupportsIncrementalInput(pinnedAuth.Attributes, pinnedAuth.Metadata)
				}
			} else {
				requestModelName := strings.TrimSpace(gjson.GetBytes(payload, "model").String())
				if requestModelName == "" {
					requestModelName = strings.TrimSpace(gjson.GetBytes(lastRequest, "model").String())
				}
				allowIncrementalInputWithPreviousResponseID = h.websocketUpstreamSupportsIncrementalInputForModel(requestModelName)
			}
			if forceTranscriptReplayNextRequest {
				allowIncrementalInputWithPreviousResponseID = false
			}
			allowCompactionReplayBypass := false
			if pinnedAuthID != "" {
				if pinnedAuth, ok := sessionAuthByID(pinnedAuthID); ok && pinnedAuth != nil {
					allowCompactionReplayBypass = responsesWebsocketAuthSupportsCompactionReplay(pinnedAuth)
				}
			} else {
				requestModelName := strings.TrimSpace(gjson.GetBytes(payload, "model").String())
				if requestModelName == "" {
					requestModelName = strings.TrimSpace(gjson.GetBytes(lastRequest, "model").String())
				}
				allowCompactionReplayBypass = h.websocketUpstreamSupportsCompactionReplayForModel(requestModelName)
			}
			if !responsesWebsocketStatePreflight(payload, lastRequest, lastResponseOutput, allowIncrementalInputWithPreviousResponseID, allowCompactionReplayBypass, limits.sessionBytes) {
				if errWrite := writeResponsesWebsocketBudgetError(conn, wsTimelineLog, "websocket_session_state_limit_exceeded", "websocket session state exceeds configured limit", passthroughSessionID); errWrite != nil {
					wsTerminateErr = errWrite
					return
				}
				break
			}

			var requestJSON []byte
			var updatedLastRequest []byte
			var errMsg *interfaces.ErrorMessage
			requestJSON, updatedLastRequest, errMsg = normalizeResponsesWebsocketRequestWithMode(
				payload,
				lastRequest,
				lastResponseOutput,
				allowIncrementalInputWithPreviousResponseID,
				allowCompactionReplayBypass,
			)
			if errMsg != nil {
				h.LoggingAPIResponseError(context.WithValue(context.Background(), "gin", c), errMsg)
				markAPIResponseTimestamp(c)
				errorPayload, errWrite := writeResponsesWebsocketError(conn, wsTimelineLog, errMsg)
				log.Infof(
					"responses websocket: downstream_out id=%s type=%d event=%s payload=%s",
					passthroughSessionID,
					websocket.TextMessage,
					websocketPayloadEventType(errorPayload),
					websocketPayloadPreview(errorPayload),
				)
				if errWrite != nil {
					log.Warnf(
						"responses websocket: downstream_out write failed id=%s event=%s error=%v",
						passthroughSessionID,
						websocketPayloadEventType(errorPayload),
						errWrite,
					)
					return
				}
				break
			}
			if shouldHandleResponsesWebsocketPrewarmLocally(payload, lastRequest, allowIncrementalInputWithPreviousResponseID) {
				if updated, errDelete := sjson.DeleteBytes(requestJSON, "generate"); errDelete == nil {
					requestJSON = updated
				}
				if updated, errDelete := sjson.DeleteBytes(updatedLastRequest, "generate"); errDelete == nil {
					updatedLastRequest = updated
				}
				if !responsesWebsocketStateWithinLimit(updatedLastRequest, nil, limits.sessionBytes) {
					if errWrite := writeResponsesWebsocketBudgetError(conn, wsTimelineLog, "websocket_session_state_limit_exceeded", "websocket session state exceeds configured limit", passthroughSessionID); errWrite != nil {
						wsTerminateErr = errWrite
						return
					}
					break
				}
				if !resizeResponsesWebsocketTransitionReservation(stateReservation, limits.memoryBudgetBytes, int64(len(lastRequest)), int64(len(lastResponseOutput)), int64(len(updatedLastRequest)), 2) {
					if errWrite := writeResponsesWebsocketBudgetError(conn, wsTimelineLog, "websocket_memory_budget_exceeded", "websocket aggregate memory budget exceeded", passthroughSessionID); errWrite != nil {
						wsTerminateErr = errWrite
						return
					}
					break
				}
				lastRequest = updatedLastRequest
				lastResponseOutput = []byte("[]")
				if errWrite := writeResponsesWebsocketSyntheticPrewarm(c, conn, requestJSON, wsTimelineLog, passthroughSessionID); errWrite != nil {
					wsTerminateErr = errWrite
					return
				}
				resizeResponsesWebsocketStateReservation(stateReservation, limits.memoryBudgetBytes, int64(len(lastRequest)), int64(len(lastResponseOutput)))
				break
			}

			requestJSON = sanitizeResponsesInputToolCallNames(requestJSON)
			requestJSON = repairResponsesWebsocketToolCalls(downstreamSessionKey, requestJSON)
			requestJSON = dedupeResponsesWebsocketInputItemsByID(requestJSON)
			updatedLastRequest = requestJSON
			if !responsesWebsocketStateWithinLimit(updatedLastRequest, nil, limits.sessionBytes) {
				clearResponsesWebsocketToolCaches(downstreamSessionKey)
				if errWrite := writeResponsesWebsocketBudgetError(conn, wsTimelineLog, "websocket_session_state_limit_exceeded", "websocket session state exceeds configured limit", passthroughSessionID); errWrite != nil {
					wsTerminateErr = errWrite
					return
				}
				break
			}
			previousLastRequest := lastRequest
			previousLastResponseOutput := lastResponseOutput
			effectiveTurnLimit := responsesWebsocketEffectiveTurnOutputLimit(limits.turnOutputBytes, limits.sessionBytes, len(updatedLastRequest))
			if !responsesWebsocketCanStartTurn(effectiveTurnLimit) {
				clearResponsesWebsocketToolCaches(downstreamSessionKey)
				if errWrite := writeResponsesWebsocketBudgetError(conn, wsTimelineLog, "websocket_session_state_limit_exceeded", "websocket session has no room for response output", passthroughSessionID); errWrite != nil {
					wsTerminateErr = errWrite
					return
				}
				break
			}
			if !resizeResponsesWebsocketTransitionReservation(stateReservation, limits.memoryBudgetBytes, int64(len(previousLastRequest)), int64(len(previousLastResponseOutput)), int64(len(updatedLastRequest)), effectiveTurnLimit) {
				clearResponsesWebsocketToolCaches(downstreamSessionKey)
				if errWrite := writeResponsesWebsocketBudgetError(conn, wsTimelineLog, "websocket_memory_budget_exceeded", "websocket aggregate memory budget exceeded", passthroughSessionID); errWrite != nil {
					wsTerminateErr = errWrite
					return
				}
				break
			}
			forcedTranscriptReplay := forceTranscriptReplayNextRequest
			lastRequest = updatedLastRequest
			setResponsesWebsocketTurnOutputLimit(c, effectiveTurnLimit)
			if forcedTranscriptReplay {
				forceTranscriptReplayNextRequest = false
			}

			modelName := gjson.GetBytes(requestJSON, "model").String()
			cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
			cliCtx = cliproxyexecutor.WithDownstreamWebsocket(cliCtx)
			cliCtx = handlers.WithExecutionSessionID(cliCtx, passthroughSessionID)
			if pinnedAuthID != "" {
				cliCtx = handlers.WithPinnedAuthID(cliCtx, pinnedAuthID)
			} else {
				cliCtx = handlers.WithSelectedAuthIDCallback(cliCtx, func(authID string) {
					authID = strings.TrimSpace(authID)
					if authID == "" || h == nil || h.AuthManager == nil {
						return
					}
					selectedAuth, ok := sessionAuthByID(authID)
					if !ok || selectedAuth == nil {
						return
					}
					if websocketUpstreamSupportsIncrementalInput(selectedAuth.Attributes, selectedAuth.Metadata) {
						pinnedAuthID = authID
					}
				})
			}
			dataChan, _, errChan := h.ExecuteStreamWithAuthManager(cliCtx, h.HandlerType(), modelName, requestJSON, "")

			canReplayForwardErr := replayAttempt < maxReplayRetries
			completedOutput, forwardErrMsg, shouldReplayForwardErr, errForward := h.forwardResponsesWebsocket(c, conn, cliCancel, dataChan, errChan, wsTimelineLog, passthroughSessionID, canReplayForwardErr)
			if errForward != nil {
				wsTerminateErr = errForward
				log.Warnf("responses websocket: forward failed id=%s error=%v", passthroughSessionID, errForward)
				return
			}
			if canReplayForwardErr && shouldReplayForwardErr {
				forceTranscriptReplayNextRequest = true
				lastRequest = previousLastRequest
				lastResponseOutput = previousLastResponseOutput
				resizeResponsesWebsocketStateReservation(stateReservation, limits.memoryBudgetBytes, int64(len(previousLastRequest)), int64(len(previousLastResponseOutput)))
				continue
			}
			if shouldReleaseResponsesWebsocketPinnedAuth(forwardErrMsg) {
				pinnedAuthID = ""
				forceTranscriptReplayNextRequest = true
				lastRequest = previousLastRequest
				lastResponseOutput = previousLastResponseOutput
				resizeResponsesWebsocketStateReservation(stateReservation, limits.memoryBudgetBytes, int64(len(previousLastRequest)), int64(len(previousLastResponseOutput)))
				break
			}
			if isResponsesWebsocketBudgetError(forwardErrMsg) {
				lastRequest = previousLastRequest
				lastResponseOutput = previousLastResponseOutput
				clearResponsesWebsocketToolCaches(downstreamSessionKey)
				resizeResponsesWebsocketStateReservation(stateReservation, limits.memoryBudgetBytes, int64(len(previousLastRequest)), int64(len(previousLastResponseOutput)))
				break
			}
			if !responsesWebsocketStateWithinLimit(lastRequest, completedOutput, limits.sessionBytes) {
				lastRequest = previousLastRequest
				lastResponseOutput = previousLastResponseOutput
				clearResponsesWebsocketToolCaches(downstreamSessionKey)
				resizeResponsesWebsocketStateReservation(stateReservation, limits.memoryBudgetBytes, int64(len(previousLastRequest)), int64(len(previousLastResponseOutput)))
				break
			}
			lastResponseOutput = completedOutput
			resizeResponsesWebsocketStateReservation(stateReservation, limits.memoryBudgetBytes, int64(len(lastRequest)), int64(len(lastResponseOutput)))
			break
		}
	}
}

type responsesWebsocketTurnLimits struct {
	sessionBytes      int64
	turnOutputBytes   int64
	memoryBudgetBytes int64
	toolCacheBytes    int64
}

type responsesWebsocketConnectionLimiter struct{ current atomic.Int64 }

func (l *responsesWebsocketConnectionLimiter) tryAcquire(limit int) bool {
	if l == nil || limit <= 0 {
		return false
	}
	for {
		current := l.current.Load()
		if current >= int64(limit) {
			return false
		}
		if l.current.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

func (l *responsesWebsocketConnectionLimiter) release() {
	if l != nil {
		l.current.Add(-1)
	}
}

type responsesWebsocketMemoryBudget struct {
	mu    sync.Mutex
	inUse int64
}

type responsesWebsocketMemoryReservation struct {
	budget *responsesWebsocketMemoryBudget
	amount int64
}

func (b *responsesWebsocketMemoryBudget) newReservation() *responsesWebsocketMemoryReservation {
	return &responsesWebsocketMemoryReservation{budget: b}
}

func (b *responsesWebsocketMemoryBudget) inUseBytes() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.inUse
}

func (r *responsesWebsocketMemoryReservation) resize(amount, limit int64) bool {
	if r == nil || r.budget == nil || amount < 0 || limit <= 0 {
		return false
	}
	r.budget.mu.Lock()
	defer r.budget.mu.Unlock()
	delta := amount - r.amount
	if delta > 0 && (delta > limit || r.budget.inUse > limit-delta) {
		return false
	}
	r.budget.inUse += delta
	r.amount = amount
	return true
}

func (r *responsesWebsocketMemoryReservation) release() {
	if r == nil || r.budget == nil {
		return
	}
	r.budget.mu.Lock()
	r.budget.inUse -= r.amount
	if r.budget.inUse < 0 {
		r.budget.inUse = 0
	}
	r.amount = 0
	r.budget.mu.Unlock()
}

func resizeResponsesWebsocketStateReservation(r *responsesWebsocketMemoryReservation, limit int64, sizes ...int64) bool {
	if r == nil {
		return true
	}
	var total int64
	for _, size := range sizes {
		if size < 0 || size > math.MaxInt64-total {
			return false
		}
		total += size
	}
	return r.resize(total, limit)
}

func resizeResponsesWebsocketTransitionReservation(r *responsesWebsocketMemoryReservation, limit, previousRequestBytes, previousOutputBytes, updatedRequestBytes, turnOutputBytes int64) bool {
	return resizeResponsesWebsocketStateReservation(r, limit, previousRequestBytes, previousOutputBytes, updatedRequestBytes, turnOutputBytes)
}

func responsesWebsocketEffectiveTurnOutputLimit(turnLimit, sessionLimit int64, requestBytes int) int64 {
	remaining := sessionLimit - int64(requestBytes)
	if remaining < 0 {
		return 0
	}
	if turnLimit < remaining {
		return turnLimit
	}
	return remaining
}

func responsesWebsocketCanStartTurn(effectiveOutputLimit int64) bool {
	return effectiveOutputLimit >= 2
}

func setResponsesWebsocketTurnOutputLimit(c *gin.Context, limit int64) {
	if c != nil {
		c.Set(responsesWebsocketTurnOutputLimitContextKey, limit)
	}
}

func (h *OpenAIResponsesAPIHandler) responsesWebsocketTurnOutputLimit(c *gin.Context) int64 {
	if c != nil {
		if value, ok := c.Get(responsesWebsocketTurnOutputLimitContextKey); ok {
			if limit, ok := value.(int64); ok {
				return limit
			}
		}
	}
	return h.responsesWebsocketMaxTurnOutputBytes()
}

// startResponsesWebsocketHeartbeat 定期向下游客户端发送 websocket ping frame，
// 防止客户端在等待上游响应期间因 idle timeout 断开连接（"正在思考"卡住）。
// 同时设置读超时，如果客户端长时间无响应则关闭连接。
func startResponsesWebsocketHeartbeat(conn *websocket.Conn, done <-chan struct{}, sessionID string, interval time.Duration) {
	if conn == nil || interval <= 0 {
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(responsesWebsocketReadTimeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(responsesWebsocketReadTimeout))
	})
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(responsesWebsocketWriteTimeout)); err != nil {
					log.Warnf("responses websocket: ping failed id=%s error=%v", sessionID, err)
					_ = conn.Close()
					return
				}
			}
		}
	}()
}

func websocketClientAddress(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return strings.TrimSpace(c.ClientIP())
}

func websocketUpgradeHeaders(req *http.Request) http.Header {
	headers := http.Header{}
	if req == nil {
		return headers
	}

	// Keep the same sticky turn-state across reconnects when provided by the client.
	turnState := strings.TrimSpace(req.Header.Get(wsTurnStateHeader))
	if turnState != "" {
		headers.Set(wsTurnStateHeader, turnState)
	}
	return headers
}

func normalizeResponsesWebsocketRequest(rawJSON []byte, lastRequest []byte, lastResponseOutput []byte) ([]byte, []byte, *interfaces.ErrorMessage) {
	return normalizeResponsesWebsocketRequestWithMode(rawJSON, lastRequest, lastResponseOutput, true, true)
}

func normalizeResponsesWebsocketRequestWithMode(rawJSON []byte, lastRequest []byte, lastResponseOutput []byte, allowIncrementalInputWithPreviousResponseID bool, allowCompactionReplayBypass bool) ([]byte, []byte, *interfaces.ErrorMessage) {
	requestType := strings.TrimSpace(gjson.GetBytes(rawJSON, "type").String())
	switch requestType {
	case wsRequestTypeCreate:
		// log.Infof("responses websocket: response.create request")
		if len(lastRequest) == 0 {
			return normalizeResponseCreateRequest(rawJSON)
		}
		return normalizeResponseSubsequentRequest(rawJSON, lastRequest, lastResponseOutput, allowIncrementalInputWithPreviousResponseID, allowCompactionReplayBypass)
	case wsRequestTypeAppend:
		// log.Infof("responses websocket: response.append request")
		return normalizeResponseSubsequentRequest(rawJSON, lastRequest, lastResponseOutput, allowIncrementalInputWithPreviousResponseID, allowCompactionReplayBypass)
	default:
		return nil, lastRequest, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("unsupported websocket request type: %s", requestType),
		}
	}
}

func normalizeResponseCreateRequest(rawJSON []byte) ([]byte, []byte, *interfaces.ErrorMessage) {
	normalized, errDelete := sjson.DeleteBytes(rawJSON, "type")
	if errDelete != nil {
		normalized = bytes.Clone(rawJSON)
	}
	normalized, _ = sjson.SetBytes(normalized, "stream", true)
	if !gjson.GetBytes(normalized, "input").Exists() {
		normalized, _ = sjson.SetRawBytes(normalized, "input", []byte("[]"))
	} else if input := gjson.GetBytes(normalized, "input"); input.Type == gjson.String {
		normalized, _ = sjson.SetRawBytes(normalized, "input", []byte(responsesStringInputArrayRaw(input.String())))
	} else if input.IsArray() {
		normalized, _ = sjson.SetRawBytes(normalized, "input", []byte(inputWithCurrentCompactionTriggerFinal(input)))
	}

	modelName := strings.TrimSpace(gjson.GetBytes(normalized, "model").String())
	if modelName == "" {
		return nil, nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("missing model in response.create request"),
		}
	}
	return normalized, bytes.Clone(normalized), nil
}

func normalizeResponseSubsequentRequest(rawJSON []byte, lastRequest []byte, lastResponseOutput []byte, allowIncrementalInputWithPreviousResponseID bool, allowCompactionReplayBypass bool) ([]byte, []byte, *interfaces.ErrorMessage) {
	if len(lastRequest) == 0 {
		return nil, lastRequest, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("websocket request received before response.create"),
		}
	}

	nextInput := gjson.GetBytes(rawJSON, "input")
	if !nextInput.Exists() || !nextInput.IsArray() {
		return nil, lastRequest, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("websocket request requires array field: input"),
		}
	}

	// Compaction can cause clients to replace local websocket history with a new
	// compact transcript on the next `response.create`. When the input already
	// contains historical model output items, treating it as an incremental append
	// duplicates stale turn-state and can leave late orphaned function_call items.
	if shouldReplaceWebsocketTranscript(rawJSON, nextInput) {
		normalized := normalizeResponseTranscriptReplacement(rawJSON, lastRequest)
		return normalized, bytes.Clone(normalized), nil
	}

	// Websocket v2 mode uses response.create with previous_response_id + incremental input.
	// Do not expand it into a full input transcript; upstream expects the incremental payload.
	if allowIncrementalInputWithPreviousResponseID {
		if prev := strings.TrimSpace(gjson.GetBytes(rawJSON, "previous_response_id").String()); prev != "" {
			if strings.HasPrefix(prev, "resp_") {
				// Use unmarshal-modify-marshal to avoid O(n*m) allocations
				// from sequential sjson calls on the full payload.
				var normalized []byte
				var obj map[string]json.RawMessage
				if errUnmarshal := json.Unmarshal(rawJSON, &obj); errUnmarshal != nil {
					normalized = bytes.Clone(rawJSON)
				} else {
					delete(obj, "type")
					obj["stream"] = json.RawMessage(`true`)
					if _, ok := obj["model"]; !ok {
						modelName := strings.TrimSpace(gjson.GetBytes(lastRequest, "model").String())
						if modelName != "" {
							modelBytes, _ := json.Marshal(modelName)
							obj["model"] = json.RawMessage(modelBytes)
						}
					}
					if _, ok := obj["instructions"]; !ok {
						instructions := gjson.GetBytes(lastRequest, "instructions")
						if instructions.Exists() {
							obj["instructions"] = json.RawMessage(instructions.Raw)
						}
					}
					if input := gjson.GetBytes(rawJSON, "input"); input.IsArray() {
						obj["input"] = json.RawMessage(inputWithCurrentCompactionTriggerFinal(input))
					}
					if out, errMarshal := json.Marshal(obj); errMarshal == nil {
						normalized = out
					} else {
						normalized = bytes.Clone(rawJSON)
					}
				}
				return normalized, bytes.Clone(normalized), nil
			}
			log.Infof("responses websocket: stripping invalid previous_response_id (missing resp_ prefix): %s", prev)
		}
	}

	// When the client sends a compact replay for a downstream that can consume it
	// directly, the input already carries the canonical history. In that case,
	// skip merging with stale lastRequest/lastResponseOutput to avoid breaking
	// function_call / function_call_output pairings.
	// See: https://github.com/router-for-me/CLIProxyAPI/issues/2207
	var mergedInput string
	if allowCompactionReplayBypass && inputContainsFullTranscript(nextInput) {
		log.Infof("responses websocket: full transcript detected, skipping stale merge (input items=%d)", len(nextInput.Array()))
		mergedInput = inputWithCurrentCompactionTriggerFinal(nextInput)
	} else {
		appendInputRaw := nextInput.Raw
		if inputContainsFullTranscript(nextInput) {
			appendInputRaw = inputWithoutCompactionItems(nextInput)
		}
		appendInputRaw, currentCompactionTrigger := inputWithoutCompactionTriggerItems(gjson.Parse(appendInputRaw))

		existingInput := gjson.GetBytes(lastRequest, "input")
		existingInputRaw := inputWithoutCompactionTriggerItemsOnly(gjson.Parse(responsesInputArrayRaw(existingInput)))
		lastResponseOutputRaw := inputWithoutCompactionTriggerItemsOnly(gjson.Parse(normalizeJSONArrayRaw(lastResponseOutput)))
		var errMerge error
		mergedInput, errMerge = mergeJSONArrayRaw(existingInputRaw, lastResponseOutputRaw)
		if errMerge != nil {
			return nil, lastRequest, &interfaces.ErrorMessage{
				StatusCode: http.StatusBadRequest,
				Error:      fmt.Errorf("invalid previous request input: %w", errMerge),
			}
		}

		mergedInput, errMerge = mergeJSONArrayRaw(mergedInput, appendInputRaw)
		if errMerge != nil {
			return nil, lastRequest, &interfaces.ErrorMessage{
				StatusCode: http.StatusBadRequest,
				Error:      fmt.Errorf("invalid request input: %w", errMerge),
			}
		}
		if currentCompactionTrigger != "" {
			mergedInput, errMerge = mergeJSONArrayRaw(mergedInput, "["+currentCompactionTrigger+"]")
			if errMerge != nil {
				return nil, lastRequest, &interfaces.ErrorMessage{
					StatusCode: http.StatusBadRequest,
					Error:      fmt.Errorf("invalid request input: %w", errMerge),
				}
			}
		}
	}
	dedupedInput, errDedupeFunctionCalls := dedupeFunctionCallsByCallID(mergedInput)
	if errDedupeFunctionCalls == nil {
		mergedInput = dedupedInput
	}
	dedupedInput, errDedupeItemIDs := dedupeInputItemsByID(mergedInput)
	if errDedupeItemIDs == nil {
		mergedInput = dedupedInput
	}

	// Build the normalized request using unmarshal-modify-marshal to avoid
	// O(n*m) allocations from sequential sjson calls on the full payload.
	var obj map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(rawJSON, &obj); errUnmarshal != nil {
		return nil, lastRequest, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("invalid request JSON: %w", errUnmarshal),
		}
	}
	delete(obj, "type")
	delete(obj, "previous_response_id")
	obj["input"] = json.RawMessage(mergedInput)
	obj["stream"] = json.RawMessage(`true`)
	if _, ok := obj["model"]; !ok {
		modelName := strings.TrimSpace(gjson.GetBytes(lastRequest, "model").String())
		if modelName != "" {
			modelBytes, _ := json.Marshal(modelName)
			obj["model"] = json.RawMessage(modelBytes)
		}
	}
	normalized, errMarshal := json.Marshal(obj)
	if errMarshal != nil {
		return nil, lastRequest, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("failed to marshal normalized request: %w", errMarshal),
		}
	}
	normalized = copyMissingResponsesRequestFields(normalized, lastRequest)
	return normalized, bytes.Clone(normalized), nil
}

func shouldReplaceWebsocketTranscript(rawJSON []byte, nextInput gjson.Result) bool {
	requestType := strings.TrimSpace(gjson.GetBytes(rawJSON, "type").String())
	if requestType != wsRequestTypeCreate && requestType != wsRequestTypeAppend {
		return false
	}
	if strings.TrimSpace(gjson.GetBytes(rawJSON, "previous_response_id").String()) != "" {
		return false
	}
	if !nextInput.Exists() || !nextInput.IsArray() {
		return false
	}

	for _, item := range nextInput.Array() {
		switch strings.TrimSpace(item.Get("type").String()) {
		case "function_call", "custom_tool_call":
			return true
		case "message":
			if strings.TrimSpace(item.Get("role").String()) == "assistant" {
				return true
			}
		}
	}

	return false
}

func responsesWebsocketStatePreflight(rawJSON, lastRequest, lastResponseOutput []byte, allowIncremental, allowCompactionBypass bool, maxBytes int64) bool {
	if maxBytes <= 0 {
		return true
	}
	nextInput := gjson.GetBytes(rawJSON, "input")
	if shouldReplaceWebsocketTranscript(rawJSON, nextInput) ||
		(allowIncremental && strings.HasPrefix(strings.TrimSpace(gjson.GetBytes(rawJSON, "previous_response_id").String()), "resp_")) ||
		(allowCompactionBypass && inputContainsFullTranscript(nextInput)) {
		return int64(len(rawJSON)) <= maxBytes
	}
	total := int64(len(rawJSON))
	for _, size := range []int{len(lastRequest), len(lastResponseOutput)} {
		if int64(size) > maxBytes-total {
			return false
		}
		total += int64(size)
	}
	return total <= maxBytes
}

func responsesWebsocketStateWithinLimit(request, output []byte, maxBytes int64) bool {
	if maxBytes <= 0 {
		return true
	}
	return int64(len(request)) <= maxBytes && int64(len(output)) <= maxBytes-int64(len(request))
}

func normalizeResponseTranscriptReplacement(rawJSON []byte, lastRequest []byte) []byte {
	normalized, errDelete := sjson.DeleteBytes(rawJSON, "type")
	if errDelete != nil {
		normalized = bytes.Clone(rawJSON)
	}
	normalized, _ = sjson.DeleteBytes(normalized, "previous_response_id")
	normalized = copyMissingResponsesRequestFields(normalized, lastRequest)
	if input := gjson.GetBytes(normalized, "input"); input.IsArray() {
		normalized, _ = sjson.SetRawBytes(normalized, "input", []byte(inputWithCurrentCompactionTriggerFinal(input)))
	}
	normalized, _ = sjson.SetBytes(normalized, "stream", true)
	return bytes.Clone(normalized)
}

func copyMissingResponsesRequestFields(normalized []byte, lastRequest []byte) []byte {
	for _, path := range []string{
		"model",
		"instructions",
		"tools",
		"tool_choice",
		"parallel_tool_calls",
		"max_tool_calls",
		"reasoning",
		"text",
		"max_output_tokens",
		"temperature",
		"top_p",
	} {
		if gjson.GetBytes(normalized, path).Exists() {
			continue
		}
		value := gjson.GetBytes(lastRequest, path)
		if !value.Exists() {
			continue
		}
		if updated, errSet := sjson.SetRawBytes(normalized, path, []byte(value.Raw)); errSet == nil {
			normalized = updated
		}
	}
	return normalized
}

func dedupeFunctionCallsByCallID(rawArray string) (string, error) {
	rawArray = strings.TrimSpace(rawArray)
	if rawArray == "" {
		return "[]", nil
	}
	var items []json.RawMessage
	if errUnmarshal := json.Unmarshal([]byte(rawArray), &items); errUnmarshal != nil {
		return "", errUnmarshal
	}

	seenCallIDs := make(map[string]struct{}, len(items))
	filtered := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		if len(item) == 0 {
			continue
		}
		itemType := strings.TrimSpace(gjson.GetBytes(item, "type").String())
		if isResponsesToolCallType(itemType) {
			callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
			if callID != "" {
				if _, ok := seenCallIDs[callID]; ok {
					continue
				}
				seenCallIDs[callID] = struct{}{}
			}
		}
		filtered = append(filtered, item)
	}

	out, errMarshal := json.Marshal(filtered)
	if errMarshal != nil {
		return "", errMarshal
	}
	return string(out), nil
}

func dedupeResponsesWebsocketInputItemsByID(payload []byte) []byte {
	input := gjson.GetBytes(payload, "input")
	if !input.Exists() || !input.IsArray() {
		return payload
	}
	dedupedInput, errDedupe := dedupeInputItemsByID(input.Raw)
	if errDedupe != nil || dedupedInput == input.Raw {
		return payload
	}
	updated, errSet := sjson.SetRawBytes(payload, "input", []byte(dedupedInput))
	if errSet != nil {
		return payload
	}
	return updated
}

func dedupeInputItemsByID(rawArray string) (string, error) {
	rawArray = strings.TrimSpace(rawArray)
	if rawArray == "" {
		return "[]", nil
	}
	var items []json.RawMessage
	if errUnmarshal := json.Unmarshal([]byte(rawArray), &items); errUnmarshal != nil {
		return "", errUnmarshal
	}

	// Parse each item's type, id and call_id once; gjson is a scan-based
	// parser, so reusing this metadata avoids rescanning every item in each of
	// the loops below as the conversation history grows.
	type itemMetadata struct {
		itemType string
		id       string
		callID   string
	}
	meta := make([]itemMetadata, len(items))
	for i, item := range items {
		if len(item) == 0 {
			continue
		}
		res := gjson.GetManyBytes(item, "type", "id", "call_id")
		meta[i] = itemMetadata{
			itemType: strings.TrimSpace(res[0].String()),
			id:       strings.TrimSpace(res[1].String()),
			callID:   strings.TrimSpace(res[2].String()),
		}
	}

	// Collect the call_ids that are still referenced by tool-call output
	// items. When several input items share the same id, the one we keep must
	// preserve any call_id that has a matching output; otherwise the upstream
	// rejects the request with "No tool call found for function call output".
	referencedCallIDs := make(map[string]struct{}, len(items))
	for i := range items {
		switch meta[i].itemType {
		case "function_call_output", "custom_tool_call_output":
			if meta[i].callID != "" {
				referencedCallIDs[meta[i].callID] = struct{}{}
			}
		}
	}

	// For each id, choose the index to keep. The default is the last
	// occurrence (matching the original dedupe behavior), but we never replace
	// an item whose call_id still has a matching output with one that does not.
	// This keeps a single item per id while ensuring retained tool calls stay
	// paired with their outputs.
	keepIndexByID := make(map[string]int, len(items))
	keepReferencedByID := make(map[string]bool, len(items))
	for i := range items {
		itemID := meta[i].id
		if itemID == "" {
			continue
		}
		_, referenced := referencedCallIDs[meta[i].callID]
		referenced = referenced && meta[i].callID != ""
		if _, seen := keepIndexByID[itemID]; !seen {
			keepIndexByID[itemID] = i
			keepReferencedByID[itemID] = referenced
			continue
		}
		if referenced || !keepReferencedByID[itemID] {
			keepIndexByID[itemID] = i
			keepReferencedByID[itemID] = referenced
		}
	}

	filtered := make([]json.RawMessage, 0, len(items))
	for i, item := range items {
		if len(item) == 0 {
			continue
		}
		itemID := meta[i].id
		if itemID != "" {
			if keepIndexByID[itemID] != i {
				continue
			}
		}
		filtered = append(filtered, item)
	}

	// Build the output JSON array by concatenating raw items with commas,
	// avoiding json.Marshal which allocates a large contiguous buffer for
	// the entire array (a major heap allocation hotspot under load).
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, item := range filtered {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(item)
	}
	buf.WriteByte(']')
	return buf.String(), nil
}

func websocketUpstreamSupportsIncrementalInput(attributes map[string]string, metadata map[string]any) bool {
	if len(attributes) > 0 {
		if raw := strings.TrimSpace(attributes["websockets"]); raw != "" {
			parsed, errParse := strconv.ParseBool(raw)
			if errParse == nil {
				return parsed
			}
		}
	}
	if len(metadata) == 0 {
		return false
	}
	raw, ok := metadata["websockets"]
	if !ok || raw == nil {
		return false
	}
	switch value := raw.(type) {
	case bool:
		return value
	case string:
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(value))
		if errParse == nil {
			return parsed
		}
	default:
	}
	return false
}

func (h *OpenAIResponsesAPIHandler) websocketUpstreamSupportsIncrementalInputForModel(modelName string) bool {
	auths, _ := h.responsesWebsocketAvailableAuthsForModel(modelName)
	if len(auths) == 0 {
		return false
	}
	for _, auth := range auths {
		if !websocketUpstreamSupportsIncrementalInput(auth.Attributes, auth.Metadata) {
			return false
		}
	}
	return true
}

func (h *OpenAIResponsesAPIHandler) websocketUpstreamSupportsCompactionReplayForModel(modelName string) bool {
	auths, _ := h.responsesWebsocketAvailableAuthsForModel(modelName)
	if len(auths) == 0 {
		return false
	}
	for _, auth := range auths {
		if !responsesWebsocketAuthSupportsCompactionReplay(auth) {
			return false
		}
	}
	return true
}

func (h *OpenAIResponsesAPIHandler) responsesWebsocketAvailableAuthsForModel(modelName string) ([]*coreauth.Auth, string) {
	if h == nil || h.AuthManager == nil {
		return nil, ""
	}
	resolvedModelName := responsesWebsocketResolvedModelName(modelName)
	providerSet, modelKey := h.responsesWebsocketProviderSetForModel(resolvedModelName)
	if len(providerSet) == 0 {
		return nil, modelKey
	}

	registryRef := registry.GetGlobalRegistry()
	now := time.Now()
	auths := h.AuthManager.List()
	available := make([]*coreauth.Auth, 0, len(auths))
	for _, auth := range auths {
		if !responsesWebsocketAuthMatchesModel(auth, providerSet, modelKey, registryRef, now) {
			continue
		}
		available = append(available, auth)
	}
	return available, modelKey
}

func responsesWebsocketResolvedModelName(modelName string) string {
	initialSuffix := thinking.ParseSuffix(modelName)
	if initialSuffix.ModelName == "auto" {
		resolvedBase := util.ResolveAutoModel(initialSuffix.ModelName)
		if initialSuffix.HasSuffix {
			return fmt.Sprintf("%s(%s)", resolvedBase, initialSuffix.RawSuffix)
		}
		return resolvedBase
	}
	return util.ResolveAutoModel(modelName)
}

func (h *OpenAIResponsesAPIHandler) responsesWebsocketProviderSetForModel(resolvedModelName string) (map[string]struct{}, string) {
	parsed := thinking.ParseSuffix(resolvedModelName)
	baseModel := strings.TrimSpace(parsed.ModelName)
	providers := util.GetProviderName(baseModel)
	if len(providers) == 0 && baseModel != resolvedModelName {
		providers = util.GetProviderName(resolvedModelName)
	}
	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		providerKey := strings.TrimSpace(strings.ToLower(provider))
		if providerKey == "" {
			continue
		}
		providerSet[providerKey] = struct{}{}
	}
	modelKey := baseModel
	if modelKey == "" {
		modelKey = strings.TrimSpace(resolvedModelName)
	}
	return providerSet, modelKey
}

func responsesWebsocketAuthMatchesModel(auth *coreauth.Auth, providerSet map[string]struct{}, modelKey string, registryRef *registry.ModelRegistry, now time.Time) bool {
	if auth == nil {
		return false
	}
	providerKey := strings.TrimSpace(strings.ToLower(auth.Provider))
	if _, ok := providerSet[providerKey]; !ok {
		return false
	}
	if modelKey != "" && registryRef != nil && !registryRef.ClientSupportsModel(auth.ID, modelKey) {
		return false
	}
	return responsesWebsocketAuthAvailableForModel(auth, modelKey, now)
}

func responsesWebsocketAuthSupportsCompactionReplay(auth *coreauth.Auth) bool {
	if auth == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Provider), "codex")
}

func responsesWebsocketAuthAvailableForModel(auth *coreauth.Auth, modelName string, now time.Time) bool {
	if auth == nil {
		return false
	}
	if auth.Disabled || auth.Status == coreauth.StatusDisabled {
		return false
	}
	if modelName != "" && len(auth.ModelStates) > 0 {
		state, ok := auth.ModelStates[modelName]
		if (!ok || state == nil) && modelName != "" {
			baseModel := strings.TrimSpace(thinking.ParseSuffix(modelName).ModelName)
			if baseModel != "" && baseModel != modelName {
				state, ok = auth.ModelStates[baseModel]
			}
		}
		if ok && state != nil {
			if state.Status == coreauth.StatusDisabled {
				return false
			}
			if state.Unavailable && !state.NextRetryAfter.IsZero() && state.NextRetryAfter.After(now) {
				return false
			}
			return true
		}
	}
	if auth.Unavailable && !auth.NextRetryAfter.IsZero() && auth.NextRetryAfter.After(now) {
		return false
	}
	return true
}

func shouldHandleResponsesWebsocketPrewarmLocally(rawJSON []byte, lastRequest []byte, allowIncrementalInputWithPreviousResponseID bool) bool {
	if allowIncrementalInputWithPreviousResponseID || len(lastRequest) != 0 {
		return false
	}
	if strings.TrimSpace(gjson.GetBytes(rawJSON, "type").String()) != wsRequestTypeCreate {
		return false
	}
	generateResult := gjson.GetBytes(rawJSON, "generate")
	if !generateResult.Exists() || generateResult.Bool() {
		return false
	}
	input := gjson.GetBytes(rawJSON, "input")
	return !input.Exists() || (input.IsArray() && len(input.Array()) == 0)
}

func writeResponsesWebsocketSyntheticPrewarm(
	c *gin.Context,
	conn *websocket.Conn,
	requestJSON []byte,
	wsTimelineLog websocketTimelineAppender,
	sessionID string,
) error {
	payloads, errPayloads := syntheticResponsesWebsocketPrewarmPayloads(requestJSON)
	if errPayloads != nil {
		return errPayloads
	}
	for i := 0; i < len(payloads); i++ {
		markAPIResponseTimestamp(c)
		// log.Infof(
		// 	"responses websocket: downstream_out id=%s type=%d event=%s payload=%s",
		// 	sessionID,
		// 	websocket.TextMessage,
		// 	websocketPayloadEventType(payloads[i]),
		// 	websocketPayloadPreview(payloads[i]),
		// )
		if errWrite := writeResponsesWebsocketPayload(conn, wsTimelineLog, payloads[i], time.Now()); errWrite != nil {
			log.Warnf(
				"responses websocket: downstream_out write failed id=%s event=%s error=%v",
				sessionID,
				websocketPayloadEventType(payloads[i]),
				errWrite,
			)
			return errWrite
		}
	}
	return nil
}

func syntheticResponsesWebsocketPrewarmPayloads(requestJSON []byte) ([][]byte, error) {
	responseID := "resp_prewarm_" + uuid.NewString()
	createdAt := time.Now().Unix()
	modelName := strings.TrimSpace(gjson.GetBytes(requestJSON, "model").String())

	createdPayload := []byte(`{"type":"response.created","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","background":false,"error":null,"output":[]}}`)
	var errSet error
	createdPayload, errSet = sjson.SetBytes(createdPayload, "response.id", responseID)
	if errSet != nil {
		return nil, errSet
	}
	createdPayload, errSet = sjson.SetBytes(createdPayload, "response.created_at", createdAt)
	if errSet != nil {
		return nil, errSet
	}
	if modelName != "" {
		createdPayload, errSet = sjson.SetBytes(createdPayload, "response.model", modelName)
		if errSet != nil {
			return nil, errSet
		}
	}

	completedPayload := []byte(`{"type":"response.completed","sequence_number":1,"response":{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
	completedPayload, errSet = sjson.SetBytes(completedPayload, "response.id", responseID)
	if errSet != nil {
		return nil, errSet
	}
	completedPayload, errSet = sjson.SetBytes(completedPayload, "response.created_at", createdAt)
	if errSet != nil {
		return nil, errSet
	}
	if modelName != "" {
		completedPayload, errSet = sjson.SetBytes(completedPayload, "response.model", modelName)
		if errSet != nil {
			return nil, errSet
		}
	}

	return [][]byte{createdPayload, completedPayload}, nil
}

func mergeJSONArrayRaw(existingRaw, appendRaw string) (string, error) {
	existingRaw = strings.TrimSpace(existingRaw)
	appendRaw = strings.TrimSpace(appendRaw)
	if existingRaw == "" {
		existingRaw = "[]"
	}
	if appendRaw == "" {
		appendRaw = "[]"
	}

	var existing []json.RawMessage
	if err := json.Unmarshal([]byte(existingRaw), &existing); err != nil {
		return "", err
	}
	var appendItems []json.RawMessage
	if err := json.Unmarshal([]byte(appendRaw), &appendItems); err != nil {
		return "", err
	}

	merged := append(existing, appendItems...)
	out, err := json.Marshal(merged)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func responsesInputArrayRaw(input gjson.Result) string {
	if !input.Exists() {
		return "[]"
	}
	if input.IsArray() {
		return input.Raw
	}
	if input.Type == gjson.String {
		return responsesStringInputArrayRaw(input.String())
	}
	return "[]"
}

func responsesStringInputArrayRaw(text string) string {
	raw, err := json.Marshal([]map[string]any{{
		"type": "message",
		"role": "user",
		"content": []map[string]any{{
			"type": "input_text",
			"text": text,
		}},
	}})
	if err != nil {
		return "[]"
	}
	return string(raw)
}

// inputContainsFullTranscript returns true when the input array carries compact
// replay markers that indicate the client already sent the full conversation
// transcript. Merging that input with stale lastRequest/lastResponseOutput
// would duplicate or break function_call/function_call_output pairings, so the
// caller should use the input as-is.
//
// Assistant messages alone are not enough to classify the payload as a replay:
// incremental websocket requests may legitimately append assistant items.
func inputContainsFullTranscript(input gjson.Result) bool {
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		t := item.Get("type").String()
		if t == "compaction" || t == "compaction_summary" {
			return true
		}
	}
	return false
}

func inputWithoutCompactionItems(input gjson.Result) string {
	if !input.IsArray() {
		return normalizeJSONArrayRaw([]byte(input.Raw))
	}
	filtered := make([]string, 0, len(input.Array()))
	for _, item := range input.Array() {
		t := item.Get("type").String()
		if t == "compaction" || t == "compaction_summary" {
			continue
		}
		filtered = append(filtered, item.Raw)
	}
	return "[" + strings.Join(filtered, ",") + "]"
}

func inputWithCurrentCompactionTriggerFinal(input gjson.Result) string {
	withoutTrigger, trigger := inputWithoutCompactionTriggerItems(input)
	if trigger == "" {
		return withoutTrigger
	}
	merged, err := mergeJSONArrayRaw(withoutTrigger, "["+trigger+"]")
	if err != nil {
		return withoutTrigger
	}
	return merged
}

func inputWithoutCompactionTriggerItemsOnly(input gjson.Result) string {
	withoutTrigger, _ := inputWithoutCompactionTriggerItems(input)
	return withoutTrigger
}

func inputWithoutCompactionTriggerItems(input gjson.Result) (string, string) {
	if !input.IsArray() {
		return normalizeJSONArrayRaw([]byte(input.Raw)), ""
	}
	filtered := make([]string, 0, len(input.Array()))
	trigger := ""
	for _, item := range input.Array() {
		if item.Get("type").String() == "compaction_trigger" {
			trigger = item.Raw
			continue
		}
		filtered = append(filtered, item.Raw)
	}
	return "[" + strings.Join(filtered, ",") + "]", trigger
}

func normalizeJSONArrayRaw(raw []byte) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "[]"
	}
	result := gjson.Parse(trimmed)
	if result.Type == gjson.JSON && result.IsArray() {
		return trimmed
	}
	return "[]"
}

func (h *OpenAIResponsesAPIHandler) forwardResponsesWebsocket(
	c *gin.Context,
	conn *websocket.Conn,
	cancel handlers.APIHandlerCancelFunc,
	data <-chan []byte,
	errs <-chan *interfaces.ErrorMessage,
	wsTimelineLog websocketTimelineAppender,
	sessionID string,
	suppressReplayableErrors bool,
) ([]byte, *interfaces.ErrorMessage, bool, error) {
	completed := false
	sawReplayBlockingPayload := false
	pendingReplayablePayloads := make([][]byte, 0, 2)
	outputAccumulator := newResponsesWebsocketOutputAccumulatorWithLimit(h.responsesWebsocketTurnOutputLimit(c))
	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	downstreamSessionKey := ""
	if c != nil && c.Request != nil {
		downstreamSessionKey = websocketDownstreamSessionKey(c.Request)
	}
	flushPendingReplayablePayloads := func() error {
		for _, payload := range pendingReplayablePayloads {
			markAPIResponseTimestamp(c)
			if errWrite := writeResponsesWebsocketPayload(conn, wsTimelineLog, payload, time.Now()); errWrite != nil {
				log.Warnf(
					"responses websocket: downstream_out write failed id=%s event=%s error=%v",
					sessionID,
					websocketPayloadEventType(payload),
					errWrite,
				)
				return errWrite
			}
		}
		pendingReplayablePayloads = pendingReplayablePayloads[:0]
		return nil
	}

	for {
		select {
		case <-c.Request.Context().Done():
			cancel(c.Request.Context().Err())
			return outputAccumulator.Output(), nil, false, c.Request.Context().Err()
		case errMsg, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if errMsg != nil {
				output := outputAccumulator.Output()
				if suppressReplayableErrors && shouldReplayResponsesWebsocketRequest(errMsg, output, sawReplayBlockingPayload) {
					cancel(errMsg.Error)
					return output, errMsg, true, nil
				}
				if errFlush := flushPendingReplayablePayloads(); errFlush != nil {
					cancel(errFlush)
					return outputAccumulator.Output(), errMsg, false, errFlush
				}
				h.LoggingAPIResponseError(context.WithValue(context.Background(), "gin", c), errMsg)
				markAPIResponseTimestamp(c)
				errorPayload, errWrite := writeResponsesWebsocketError(conn, wsTimelineLog, errMsg)
				log.Infof(
					"responses websocket: downstream_out id=%s type=%d event=%s payload=%s",
					sessionID,
					websocket.TextMessage,
					websocketPayloadEventType(errorPayload),
					websocketPayloadPreview(errorPayload),
				)
				if errWrite != nil {
					// log.Warnf(
					// 	"responses websocket: downstream_out write failed id=%s event=%s error=%v",
					// 	sessionID,
					// 	websocketPayloadEventType(errorPayload),
					// 	errWrite,
					// )
					cancel(errMsg.Error)
					return outputAccumulator.Output(), errMsg, false, errWrite
				}
			}
			if errMsg != nil {
				cancel(errMsg.Error)
			} else {
				cancel(nil)
			}
			return outputAccumulator.Output(), errMsg, false, nil
		case chunk, ok := <-data:
			if !ok {
				if !completed {
					var errMsg *interfaces.ErrorMessage
					if errs != nil {
					drainErrs:
						for {
							select {
							case e, eok := <-errs:
								if !eok {
									errs = nil
									break drainErrs
								}
								if e != nil {
									errMsg = e
								}
							default:
								break drainErrs
							}
						}
					}
					if errMsg == nil {
						output := outputAccumulator.Output()
						outputItemCount := outputAccumulator.Count()
						if responsesWebsocketOutputHasActionableToolCall(output) {
							completedPayload, errBuild := buildResponsesWebsocketEOFCompletedPayload(output)
							if errBuild != nil {
								cancel(errBuild)
								return output, nil, false, errBuild
							}
							if errFlush := flushPendingReplayablePayloads(); errFlush != nil {
								cancel(errFlush)
								return output, nil, false, errFlush
							}
							markAPIResponseTimestamp(c)
							if errWrite := writeResponsesWebsocketPayload(conn, wsTimelineLog, completedPayload, time.Now()); errWrite != nil {
								log.Warnf(
									"responses websocket: downstream_out write failed id=%s event=%s error=%v",
									sessionID,
									websocketPayloadEventType(completedPayload),
									errWrite,
								)
								cancel(errWrite)
								return output, nil, false, errWrite
							}
							log.Warnf(
								"responses websocket: synthesized terminal completed after actionable upstream EOF id=%s output_items=%d",
								sessionID,
								outputItemCount,
							)
							cancel(nil)
							return output, nil, false, nil
						}
						errMsg = &interfaces.ErrorMessage{
							StatusCode: http.StatusRequestTimeout,
							Error:      fmt.Errorf("stream closed before response.completed"),
						}
						log.Warnf(
							"responses websocket: upstream EOF before response.completed id=%s output_items=%d",
							sessionID,
							outputItemCount,
						)
					}
					output := outputAccumulator.Output()
					if suppressReplayableErrors && shouldReplayResponsesWebsocketRequest(errMsg, output, sawReplayBlockingPayload) {
						cancel(errMsg.Error)
						return output, errMsg, true, nil
					}
					if errFlush := flushPendingReplayablePayloads(); errFlush != nil {
						cancel(errFlush)
						return outputAccumulator.Output(), errMsg, false, errFlush
					}
					h.LoggingAPIResponseError(context.WithValue(context.Background(), "gin", c), errMsg)
					markAPIResponseTimestamp(c)
					errorPayload, errWrite := writeResponsesWebsocketError(conn, wsTimelineLog, errMsg)
					log.Infof(
						"responses websocket: downstream_out id=%s type=%d event=%s payload=%s",
						sessionID,
						websocket.TextMessage,
						websocketPayloadEventType(errorPayload),
						websocketPayloadPreview(errorPayload),
					)
					if errWrite != nil {
						log.Warnf(
							"responses websocket: downstream_out write failed id=%s event=%s error=%v",
							sessionID,
							websocketPayloadEventType(errorPayload),
							errWrite,
						)
						cancel(errMsg.Error)
						return outputAccumulator.Output(), errMsg, false, errWrite
					}
					cancel(errMsg.Error)
					return outputAccumulator.Output(), errMsg, false, nil
				}
				cancel(nil)
				return outputAccumulator.Output(), nil, false, nil
			}

			payloads := websocketJSONPayloadsFromChunk(chunk)
			for i := range payloads {
				var accepted bool
				payloads[i], accepted = accumulateResponsesWebsocketPayload(payloads[i], outputAccumulator, outputItemsByIndex, &outputItemsFallback)
				if !accepted {
					return h.failResponsesWebsocketTurnOutputLimit(c, conn, cancel, wsTimelineLog, downstreamSessionKey, sessionID, outputAccumulator.Output())
				}
				eventType := gjson.GetBytes(payloads[i], "type").String()
				recordResponsesWebsocketToolCallsFromPayload(downstreamSessionKey, payloads[i])
				if suppressReplayableErrors && !sawReplayBlockingPayload {
					if replayErrMsg := replayableResponsesWebsocketPayloadError(payloads[i], outputAccumulator.Output(), sawReplayBlockingPayload); replayErrMsg != nil {
						cancel(replayErrMsg.Error)
						return outputAccumulator.Output(), replayErrMsg, true, nil
					}
				}
				if eventType == wsEventTypeError && h != nil {
					h.LoggingAPIResponseError(context.WithValue(context.Background(), "gin", c), responsesWebsocketErrorMessageFromPayload(payloads[i]))
				}
				if !responsesWebsocketReplayableLifecycleEvent(eventType) {
					sawReplayBlockingPayload = true
				}
				if eventType == wsEventTypeCompleted {
					completed = true
				}
				if suppressReplayableErrors && !sawReplayBlockingPayload && responsesWebsocketReplayableLifecycleEvent(eventType) {
					pendingReplayablePayloads = append(pendingReplayablePayloads, bytes.Clone(payloads[i]))
					continue
				}
				if errFlush := flushPendingReplayablePayloads(); errFlush != nil {
					cancel(errFlush)
					return outputAccumulator.Output(), nil, false, errFlush
				}
				markAPIResponseTimestamp(c)
				// log.Infof(
				// 	"responses websocket: downstream_out id=%s type=%d event=%s payload=%s",
				// 	sessionID,
				// 	websocket.TextMessage,
				// 	websocketPayloadEventType(payloads[i]),
				// 	websocketPayloadPreview(payloads[i]),
				// )
				if errWrite := writeResponsesWebsocketPayload(conn, wsTimelineLog, payloads[i], time.Now()); errWrite != nil {
					log.Warnf(
						"responses websocket: downstream_out write failed id=%s event=%s error=%v",
						sessionID,
						websocketPayloadEventType(payloads[i]),
						errWrite,
					)
					cancel(errWrite)
					return outputAccumulator.Output(), nil, false, errWrite
				}
			}
		}
	}
}

func accumulateResponsesWebsocketPayload(
	payload []byte,
	accumulator *responsesWebsocketOutputAccumulator,
	outputItemsByIndex map[int64][]byte,
	outputItemsFallback *[][]byte,
) ([]byte, bool) {
	eventType := gjson.GetBytes(payload, "type").String()
	switch eventType {
	case "response.output_item.done":
		if !accumulator.AppendOutputItemDone(payload) {
			return payload, false
		}
		collectResponsesWebsocketOutputItem(payload, outputItemsByIndex, outputItemsFallback)
	case wsEventTypeCompleted, wsDoneMarker:
		payload = restoreResponsesWebsocketCompletionOutput(payload, outputItemsByIndex, *outputItemsFallback)
		if !accumulator.SetCompleted(payload) {
			return payload, false
		}
	}
	return payload, true
}

type responsesWebsocketBudgetError struct {
	code    string
	message string
}

func (e *responsesWebsocketBudgetError) Error() string {
	if e == nil {
		return "websocket memory budget exceeded"
	}
	return e.message
}

func isResponsesWebsocketBudgetError(errMsg *interfaces.ErrorMessage) bool {
	if errMsg == nil || errMsg.Error == nil {
		return false
	}
	var budgetErr *responsesWebsocketBudgetError
	return errors.As(errMsg.Error, &budgetErr)
}

func writeResponsesWebsocketBudgetError(conn *websocket.Conn, timeline websocketTimelineAppender, code, message, sessionID string) error {
	log.Warnf("responses websocket: budget error id=%s code=%s", sessionID, code)
	payload := buildResponsesWebsocketBudgetErrorPayload(code, message)
	return writeResponsesWebsocketPayload(conn, timeline, payload, time.Now())
}

func buildResponsesWebsocketBudgetErrorPayload(code, message string) []byte {
	payload := handlers.BuildOpenAIResponsesResponseFailedChunk(http.StatusRequestEntityTooLarge, message, 0)
	payload, _ = sjson.SetBytes(payload, "response.error.code", code)
	payload, _ = sjson.SetBytes(payload, "response.error.type", "invalid_request_error")
	return payload
}

func (h *OpenAIResponsesAPIHandler) failResponsesWebsocketTurnOutputLimit(
	c *gin.Context,
	conn *websocket.Conn,
	cancel handlers.APIHandlerCancelFunc,
	timeline websocketTimelineAppender,
	sessionKey string,
	sessionID string,
	output []byte,
) ([]byte, *interfaces.ErrorMessage, bool, error) {
	budgetErr := &responsesWebsocketBudgetError{code: "websocket_turn_output_limit_exceeded", message: "websocket turn output exceeds configured limit"}
	cancel(budgetErr)
	clearResponsesWebsocketToolCaches(sessionKey)
	if err := writeResponsesWebsocketBudgetError(conn, timeline, budgetErr.code, budgetErr.message, sessionID); err != nil {
		return output, &interfaces.ErrorMessage{StatusCode: http.StatusRequestEntityTooLarge, Error: budgetErr}, false, err
	}
	markAPIResponseTimestamp(c)
	return output, &interfaces.ErrorMessage{StatusCode: http.StatusRequestEntityTooLarge, Error: budgetErr}, false, nil
}

func shouldReleaseResponsesWebsocketPinnedAuth(errMsg *interfaces.ErrorMessage) bool {
	if errMsg == nil {
		return false
	}
	status := errMsg.StatusCode
	if status <= 0 && errMsg.Error != nil {
		if se, ok := errMsg.Error.(interface{ StatusCode() int }); ok && se != nil {
			status = se.StatusCode()
		}
	}
	switch status {
	case http.StatusUnauthorized,
		http.StatusPaymentRequired,
		http.StatusForbidden,
		http.StatusTooManyRequests,
		http.StatusRequestTimeout,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
	}
	if errMsg.Error != nil {
		msg := strings.ToLower(errMsg.Error.Error())
		switch {
		case strings.Contains(msg, "stream closed before response.completed"),
			strings.Contains(msg, "previous_response_not_found"),
			strings.Contains(msg, "ws_failed"),
			strings.Contains(msg, "upstream stream closed before first payload"),
			strings.Contains(msg, "empty_stream"):
			return true
		}
	}
	return false
}

func collectResponsesWebsocketOutputItem(payload []byte, outputItemsByIndex map[int64][]byte, outputItemsFallback *[][]byte) {
	if gjson.GetBytes(payload, "type").String() != "response.output_item.done" {
		return
	}
	item := gjson.GetBytes(payload, "item")
	if !item.Exists() || !item.IsObject() {
		return
	}
	outputIndex := gjson.GetBytes(payload, "output_index")
	if outputIndex.Exists() {
		outputItemsByIndex[outputIndex.Int()] = bytes.Clone([]byte(item.Raw))
		return
	}
	*outputItemsFallback = append(*outputItemsFallback, bytes.Clone([]byte(item.Raw)))
}

func restoreResponsesWebsocketCompletionOutput(payload []byte, outputItemsByIndex map[int64][]byte, outputItemsFallback [][]byte) []byte {
	output := gjson.GetBytes(payload, "response.output")
	if output.Exists() && output.IsArray() && len(output.Array()) > 0 {
		return payload
	}
	if len(outputItemsByIndex) == 0 && len(outputItemsFallback) == 0 {
		return payload
	}

	restored, errSet := sjson.SetRawBytes(payload, "response.output", responseCompletedOutputFromPayload(payload, outputItemsByIndex, outputItemsFallback))
	if errSet != nil {
		return payload
	}
	return restored
}

func responseCompletedOutputFromPayload(payload []byte, outputItemsByIndex map[int64][]byte, outputItemsFallback [][]byte) []byte {
	output := gjson.GetBytes(payload, "response.output")
	if output.Exists() && output.IsArray() && len(output.Array()) > 0 {
		return bytes.Clone([]byte(output.Raw))
	}
	if len(outputItemsByIndex) == 0 && len(outputItemsFallback) == 0 {
		return []byte("[]")
	}

	indexes := make([]int64, 0, len(outputItemsByIndex))
	for index := range outputItemsByIndex {
		indexes = append(indexes, index)
	}
	sort.Slice(indexes, func(i, j int) bool {
		return indexes[i] < indexes[j]
	})

	items := make([]json.RawMessage, 0, len(outputItemsByIndex)+len(outputItemsFallback))
	for _, index := range indexes {
		items = append(items, json.RawMessage(outputItemsByIndex[index]))
	}
	for _, item := range outputItemsFallback {
		items = append(items, json.RawMessage(item))
	}

	marshaledOutput, errMarshal := json.Marshal(items)
	if errMarshal != nil {
		return []byte("[]")
	}
	return marshaledOutput
}

type responsesWebsocketOutputAccumulator struct {
	completedOutput []byte
	items           []string
	maxBytes        int64
	usedBytes       int64
}

func newResponsesWebsocketOutputAccumulator() *responsesWebsocketOutputAccumulator {
	return newResponsesWebsocketOutputAccumulatorWithLimit(-1)
}

func newResponsesWebsocketOutputAccumulatorWithLimit(maxBytes int64) *responsesWebsocketOutputAccumulator {
	return &responsesWebsocketOutputAccumulator{completedOutput: []byte("[]"), maxBytes: maxBytes, usedBytes: 2}
}

func (a *responsesWebsocketOutputAccumulator) SetCompleted(payload []byte) bool {
	if a == nil {
		return false
	}
	output := responseCompletedOutputFromPayload(payload, nil, nil)
	if a.maxBytes >= 0 && int64(len(output)) > a.maxBytes {
		return false
	}
	a.completedOutput = output
	a.items = nil
	a.usedBytes = int64(len(output))
	return true
}

func (a *responsesWebsocketOutputAccumulator) AppendOutputItemDone(payload []byte) bool {
	if a == nil {
		return false
	}
	item := gjson.GetBytes(payload, "item")
	if !item.Exists() || !item.IsObject() {
		return true
	}
	projected := int64(2 + len(item.Raw))
	if len(a.items) > 0 {
		projected = a.usedBytes + 1 + int64(len(item.Raw))
	}
	if a.maxBytes >= 0 && projected > a.maxBytes {
		return false
	}
	a.items = append(a.items, item.Raw)
	a.usedBytes = projected
	return true
}

func (a *responsesWebsocketOutputAccumulator) Output() []byte {
	if a == nil {
		return []byte("[]")
	}
	if len(a.items) == 0 {
		return bytes.Clone(a.completedOutput)
	}
	var builder strings.Builder
	builder.WriteByte('[')
	for i, item := range a.items {
		if i > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(item)
	}
	builder.WriteByte(']')
	return []byte(builder.String())
}

func (a *responsesWebsocketOutputAccumulator) Count() int {
	if a == nil {
		return 0
	}
	if len(a.items) > 0 {
		return len(a.items)
	}
	return responsesWebsocketOutputItemCount(a.completedOutput)
}

func responsesWebsocketOutputItemCount(output []byte) int {
	result := gjson.ParseBytes(output)
	if !result.IsArray() {
		return 0
	}
	return len(result.Array())
}

func responsesWebsocketOutputHasActionableToolCall(output []byte) bool {
	result := gjson.ParseBytes(output)
	if !result.IsArray() {
		return false
	}
	for _, item := range result.Array() {
		switch item.Get("type").String() {
		case "function_call", "custom_tool_call":
			if strings.TrimSpace(item.Get("call_id").String()) != "" &&
				strings.TrimSpace(item.Get("name").String()) != "" {
				return true
			}
		}
	}
	return false
}

func buildResponsesWebsocketEOFCompletedPayload(output []byte) ([]byte, error) {
	if !gjson.ParseBytes(output).IsArray() {
		output = []byte("[]")
	}
	payload := []byte(`{"type":"response.completed","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
	var errSet error
	payload, errSet = sjson.SetBytes(payload, "response.created_at", time.Now().Unix())
	if errSet != nil {
		return nil, errSet
	}
	payload, errSet = sjson.SetRawBytes(payload, "response.output", output)
	if errSet != nil {
		return nil, errSet
	}
	return payload, nil
}

func websocketJSONPayloadsFromChunk(chunk []byte) [][]byte {
	payloads := make([][]byte, 0, 2)
	lines := bytes.Split(chunk, []byte("\n"))
	for i := range lines {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 || bytes.HasPrefix(line, []byte("event:")) {
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			line = bytes.TrimSpace(line[len("data:"):])
		}
		if len(line) == 0 || bytes.Equal(line, []byte(wsDoneMarker)) {
			continue
		}
		if json.Valid(line) {
			payloads = append(payloads, bytes.Clone(line))
		}
	}

	if len(payloads) > 0 {
		return payloads
	}

	trimmed := bytes.TrimSpace(chunk)
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		trimmed = bytes.TrimSpace(trimmed[len("data:"):])
	}
	if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte(wsDoneMarker)) && json.Valid(trimmed) {
		payloads = append(payloads, bytes.Clone(trimmed))
	}
	return payloads
}

func writeResponsesWebsocketError(conn *websocket.Conn, wsTimelineLog websocketTimelineAppender, errMsg *interfaces.ErrorMessage) ([]byte, error) {
	status := http.StatusInternalServerError
	errText := http.StatusText(status)
	if errMsg != nil {
		if errMsg.StatusCode > 0 {
			status = errMsg.StatusCode
			errText = http.StatusText(status)
		}
		if errMsg.Error != nil && strings.TrimSpace(errMsg.Error.Error()) != "" {
			errText = errMsg.Error.Error()
		}
	}

	if handlers.IsOpenAIResponsesContextWindowError(status, errText) {
		failedPayload := handlers.BuildOpenAIResponsesResponseFailedChunk(status, errText, 0)
		return failedPayload, writeResponsesWebsocketPayload(conn, wsTimelineLog, failedPayload, time.Now())
	}

	itemPayload, completedPayload := buildResponsesTerminalErrorPayloads(status, errText)
	if err := writeResponsesWebsocketPayload(conn, wsTimelineLog, itemPayload, time.Now()); err != nil {
		return itemPayload, err
	}
	return completedPayload, writeResponsesWebsocketPayload(conn, wsTimelineLog, completedPayload, time.Now())
}

func appendWebsocketEvent(builder *strings.Builder, eventType string, payload []byte) {
	if builder == nil {
		return
	}
	trimmedPayload := bytes.TrimSpace(payload)
	if len(trimmedPayload) == 0 {
		return
	}
	if builder.Len() > 0 {
		builder.WriteString("\n")
	}
	builder.WriteString("websocket.")
	builder.WriteString(eventType)
	builder.WriteString("\n")
	builder.Write(trimmedPayload)
	builder.WriteString("\n")
}

func websocketPayloadEventType(payload []byte) string {
	eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
	if eventType == "" {
		return "-"
	}
	return eventType
}

func websocketPayloadPreview(payload []byte) string {
	trimmedPayload := bytes.TrimSpace(payload)
	if len(trimmedPayload) == 0 {
		return "<empty>"
	}
	previewText := strings.ReplaceAll(string(trimmedPayload), "\n", "\\n")
	previewText = strings.ReplaceAll(previewText, "\r", "\\r")
	return previewText
}

func isResponsesWebsocketCompletionEvent(eventType string) bool {
	return eventType == wsEventTypeCompleted || eventType == wsDoneMarker
}

func responsesWebsocketErrorMessageFromPayload(payload []byte) *interfaces.ErrorMessage {
	status := int(gjson.GetBytes(payload, "status").Int())
	if status <= 0 {
		status = int(gjson.GetBytes(payload, "status_code").Int())
	}
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	errText := strings.TrimSpace(gjson.GetBytes(payload, "error.message").String())
	if errText == "" {
		errText = strings.TrimSpace(gjson.GetBytes(payload, "message").String())
	}
	if errText == "" {
		errText = strings.TrimSpace(string(payload))
	}
	if errText == "" {
		errText = http.StatusText(status)
	}
	return &interfaces.ErrorMessage{StatusCode: status, Error: fmt.Errorf("%s", errText)}
}

func setWebsocketTimelineBody(c *gin.Context, body string) {
	setWebsocketBody(c, wsTimelineBodyKey, body)
}

func setWebsocketBody(c *gin.Context, key string, body string) {
	if c == nil {
		return
	}
	trimmedBody := strings.TrimSpace(body)
	if trimmedBody == "" {
		return
	}
	c.Set(key, []byte(trimmedBody))
}

func writeResponsesWebsocketPayload(conn *websocket.Conn, wsTimelineLog websocketTimelineAppender, payload []byte, timestamp time.Time) error {
	if wsTimelineLog != nil {
		wsTimelineLog.Append("response", payload, timestamp)
	}
	return conn.WriteMessage(websocket.TextMessage, payload)
}

func appendWebsocketTimelineDisconnect(timeline websocketTimelineAppender, err error, timestamp time.Time) {
	if err == nil {
		return
	}
	if timeline != nil {
		timeline.Append("disconnect", []byte(err.Error()), timestamp)
	}
}

func appendWebsocketTimelineEvent(builder *strings.Builder, eventType string, payload []byte, timestamp time.Time) {
	if builder == nil {
		return
	}
	writeWebsocketTimelineBuilder(builder, formatWebsocketTimelineEvent(eventType, payload, timestamp))
}

func formatWebsocketTimelineEvent(eventType string, payload []byte, timestamp time.Time) []byte {
	trimmedPayload := bytes.TrimSpace(payload)
	if len(trimmedPayload) == 0 {
		return nil
	}
	truncatedPayload := false
	if len(trimmedPayload) > responsesWebsocketTimelinePayloadMaxBytes {
		trimmedPayload = trimmedPayload[:responsesWebsocketTimelinePayloadMaxBytes]
		truncatedPayload = true
	}
	var builder strings.Builder
	builder.WriteString("Timestamp: ")
	builder.WriteString(timestamp.Format(time.RFC3339Nano))
	builder.WriteString("\n")
	builder.WriteString("Event: websocket.")
	builder.WriteString(eventType)
	builder.WriteString("\n")
	builder.Write(trimmedPayload)
	if truncatedPayload {
		builder.WriteString("\n... websocket payload truncated ...")
	}
	builder.WriteString("\n")
	return []byte(builder.String())
}

func markAPIResponseTimestamp(c *gin.Context) {
	if c == nil {
		return
	}
	if _, exists := c.Get("API_RESPONSE_TIMESTAMP"); exists {
		return
	}
	c.Set("API_RESPONSE_TIMESTAMP", time.Now())
}

func shouldReplayResponsesWebsocketTranscript(errMsg *interfaces.ErrorMessage) bool {
	if errMsg == nil {
		return false
	}
	status := errMsg.StatusCode
	if status <= 0 && errMsg.Error != nil {
		if se, ok := errMsg.Error.(interface{ StatusCode() int }); ok && se != nil {
			status = se.StatusCode()
		}
	}
	if status != http.StatusBadRequest {
		return false
	}

	text := responsesWebsocketErrorText(errMsg)
	if !strings.Contains(text, "previous_response_id") {
		return false
	}
	return strings.Contains(text, "previous_response_not_found") ||
		strings.Contains(text, "not found") ||
		strings.Contains(text, "invalid_id_prefix") ||
		strings.Contains(text, "expected an id that begins with 'resp'")
}

func responsesWebsocketReplayableLifecycleEvent(eventType string) bool {
	switch eventType {
	case "response.created", "response.in_progress":
		return true
	default:
		return false
	}
}

func shouldReplayResponsesWebsocketRequest(errMsg *interfaces.ErrorMessage, completedOutput []byte, sawReplayBlockingPayload bool) bool {
	return shouldReplayResponsesWebsocketTranscript(errMsg) ||
		shouldReplayResponsesWebsocketZeroOutputEOF(errMsg, completedOutput, sawReplayBlockingPayload) ||
		shouldReplayResponsesWebsocketZeroOutputTransientError(errMsg, completedOutput, sawReplayBlockingPayload)
}

func shouldReplayResponsesWebsocketZeroOutputEOF(errMsg *interfaces.ErrorMessage, completedOutput []byte, sawReplayBlockingPayload bool) bool {
	if errMsg == nil || sawReplayBlockingPayload || responsesWebsocketOutputItemCount(completedOutput) > 0 {
		return false
	}
	status := errMsg.StatusCode
	if status <= 0 && errMsg.Error != nil {
		if se, ok := errMsg.Error.(interface{ StatusCode() int }); ok && se != nil {
			status = se.StatusCode()
		}
	}
	if status != http.StatusRequestTimeout && status != http.StatusGatewayTimeout {
		return false
	}
	text := responsesWebsocketErrorText(errMsg)
	return strings.Contains(text, "stream closed before response.completed") ||
		strings.Contains(text, "upstream stream closed before first payload") ||
		strings.Contains(text, "empty_stream")
}

func shouldReplayResponsesWebsocketZeroOutputTransientError(errMsg *interfaces.ErrorMessage, completedOutput []byte, sawReplayBlockingPayload bool) bool {
	if errMsg == nil || sawReplayBlockingPayload || responsesWebsocketOutputItemCount(completedOutput) > 0 {
		return false
	}
	text := responsesWebsocketErrorText(errMsg)
	return strings.Contains(text, "system is busy") ||
		strings.Contains(text, "try again later") ||
		strings.Contains(text, "engineinternalerror") ||
		strings.Contains(text, "code: 10012") ||
		strings.Contains(text, "code\":10012") ||
		strings.Contains(text, "code 10012")
}

func replayableResponsesWebsocketPayloadError(payload []byte, completedOutput []byte, sawReplayBlockingPayload bool) *interfaces.ErrorMessage {
	if sawReplayBlockingPayload || responsesWebsocketOutputItemCount(completedOutput) > 0 {
		return nil
	}
	if gjson.GetBytes(payload, "type").String() != wsEventTypeError {
		return nil
	}
	message := strings.TrimSpace(gjson.GetBytes(payload, "error.message").String())
	if message == "" {
		message = strings.TrimSpace(gjson.GetBytes(payload, "error").String())
	}
	if message == "" {
		message = "upstream websocket error"
	}
	errMsg := &interfaces.ErrorMessage{
		StatusCode: http.StatusRequestTimeout,
		Error:      fmt.Errorf("%s", message),
	}
	if !shouldReplayResponsesWebsocketRequest(errMsg, completedOutput, sawReplayBlockingPayload) {
		return nil
	}
	return errMsg
}

func responsesWebsocketErrorCode(errMsg *interfaces.ErrorMessage) string {
	if errMsg == nil {
		return ""
	}
	text := responsesWebsocketErrorText(errMsg)
	switch {
	case strings.Contains(text, "previous_response_not_found"), strings.Contains(text, "not found"):
		return "previous_response_not_found"
	case strings.Contains(text, "invalid_id_prefix"):
		return "invalid_id_prefix"
	default:
		return "previous_response_id"
	}
}

func responsesWebsocketErrorText(errMsg *interfaces.ErrorMessage) string {
	if errMsg == nil || errMsg.Error == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(errMsg.Error.Error()))
}
