package middleware

import (
	"errors"
	"math"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/requestbody"
)

const responsesIngressAmplification int64 = 4
const responsesIngressReservationContextKey = "clirelay.responses.ingress_reservation"

var ErrResponsesMemoryBudgetExceeded = errors.New("responses request memory budget exceeded")

type ResponsesIngressConfig struct {
	MaxInboundBytes   int64
	MemoryBudgetBytes int64
}

type ResponsesIngressController struct {
	mu     sync.Mutex
	config ResponsesIngressConfig
	inUse  int64
}

type responsesIngressReservation struct {
	controller *ResponsesIngressController
	maxBytes   int64
	budget     int64
	amount     int64
}

func NewResponsesIngressController(cfg ResponsesIngressConfig) *ResponsesIngressController {
	c := &ResponsesIngressController{}
	c.Update(cfg)
	return c
}

func (c *ResponsesIngressController) Update(cfg ResponsesIngressConfig) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.config = cfg
	c.mu.Unlock()
}

func (c *ResponsesIngressController) InUseBytes() int64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inUse
}

func CanonicalResponsesBody(c *gin.Context) ([]byte, bool) { return requestbody.Canonical(c) }

func SetCanonicalResponsesBody(c *gin.Context, body []byte) { requestbody.SetCanonical(c, body) }

func OriginalResponsesHeaders(c *gin.Context) (http.Header, bool) {
	return requestbody.OriginalHeaders(c)
}

func (c *ResponsesIngressController) Middleware() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if c == nil || !isResponsesHTTPAdmissionRequest(ctx.Request) {
			ctx.Next()
			return
		}
		cfg := c.snapshot()
		if cfg.MaxInboundBytes > 0 && ctx.Request.ContentLength > cfg.MaxInboundBytes {
			ctx.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": gin.H{"message": "request body exceeds configured size limit", "type": "invalid_request_error", "code": "request_body_too_large"}})
			return
		}
		reserveSource := cfg.MaxInboundBytes
		encoding := strings.TrimSpace(ctx.Request.Header.Get("Content-Encoding"))
		if (encoding == "" || strings.EqualFold(encoding, "identity")) && ctx.Request.ContentLength >= 0 {
			reserveSource = ctx.Request.ContentLength
		}
		reservation, ok := amplifiedBytes(reserveSource)
		if !ok || !c.reserve(reservation, cfg.MemoryBudgetBytes) {
			writeResponsesMemoryBudgetExceeded(ctx)
			return
		}
		state := &responsesIngressReservation{controller: c, maxBytes: cfg.MaxInboundBytes, budget: cfg.MemoryBudgetBytes, amount: reservation}
		defer state.release()

		body, err := requestbody.ReadAndDecode(ctx.Writer, ctx.Request, cfg.MaxInboundBytes)
		if err != nil {
			if errors.Is(err, requestbody.ErrTooLarge) {
				ctx.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": gin.H{"message": err.Error(), "type": "invalid_request_error", "code": "request_body_too_large"}})
				return
			}
			ctx.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error(), "type": "invalid_request_error", "code": "invalid_request_body"}})
			return
		}
		actual, ok := amplifiedBytes(int64(len(body)))
		if !ok {
			writeResponsesMemoryBudgetExceeded(ctx)
			return
		}
		if actual > state.amount {
			if !c.reserve(actual-state.amount, cfg.MemoryBudgetBytes) {
				writeResponsesMemoryBudgetExceeded(ctx)
				return
			}
			state.amount = actual
		} else if actual < state.amount {
			c.release(state.amount - actual)
			state.amount = actual
		}
		ctx.Set(responsesIngressReservationContextKey, state)
		requestbody.SetCanonical(ctx, body)
		ctx.Next()
	}
}

func (r *responsesIngressReservation) release() {
	if r == nil || r.controller == nil {
		return
	}
	r.controller.release(r.amount)
	r.amount = 0
}

func UpdateCanonicalResponsesBody(c *gin.Context, body []byte) error {
	if c == nil {
		return nil
	}
	value, ok := c.Get(responsesIngressReservationContextKey)
	if !ok {
		requestbody.SetCanonical(c, body)
		return nil
	}
	state, ok := value.(*responsesIngressReservation)
	if !ok || state == nil || state.controller == nil {
		requestbody.SetCanonical(c, body)
		return nil
	}
	if state.maxBytes > 0 && int64(len(body)) > state.maxBytes {
		return &requestbody.TooLargeError{Limit: state.maxBytes}
	}
	amount, ok := amplifiedBytes(int64(len(body)))
	if !ok {
		return ErrResponsesMemoryBudgetExceeded
	}
	if amount > state.amount {
		if !state.controller.reserve(amount-state.amount, state.budget) {
			return ErrResponsesMemoryBudgetExceeded
		}
	} else if amount < state.amount {
		state.controller.release(state.amount - amount)
	}
	state.amount = amount
	requestbody.SetCanonical(c, body)
	return nil
}

func (c *ResponsesIngressController) snapshot() ResponsesIngressConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.config
}

func (c *ResponsesIngressController) reserve(amount, budget int64) bool {
	if amount < 0 || budget <= 0 || amount > budget {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inUse > budget-amount {
		return false
	}
	c.inUse += amount
	return true
}

func (c *ResponsesIngressController) release(amount int64) {
	if c == nil || amount <= 0 {
		return
	}
	c.mu.Lock()
	c.inUse -= amount
	if c.inUse < 0 {
		c.inUse = 0
	}
	c.mu.Unlock()
}

func amplifiedBytes(n int64) (int64, bool) {
	if n < 0 || n > math.MaxInt64/responsesIngressAmplification {
		return 0, false
	}
	return n * responsesIngressAmplification, true
}

func isResponsesHTTPAdmissionRequest(req *http.Request) bool {
	if req == nil || req.URL == nil || req.Method != http.MethodPost {
		return false
	}
	switch req.URL.Path {
	case "/v1/responses", "/v1/responses/compact", "/backend-api/codex/responses", "/backend-api/codex/responses/compact":
		return true
	default:
		return false
	}
}

func writeResponsesMemoryBudgetExceeded(c *gin.Context) {
	c.Header("Retry-After", "1")
	c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": gin.H{"message": "responses request memory budget exceeded", "type": "rate_limit_exceeded", "code": "request_memory_budget_exceeded"}})
}
