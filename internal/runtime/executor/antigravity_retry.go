package executor

import (
	"net/http"
	"time"
)

// antigravityStatusErr builds the error for an upstream rejection, attaching the
// wait the upstream named.
//
// A capacity answer (503, MODEL_CAPACITY_EXHAUSTED) states its wait the same way
// a throttled 429 does: as RetryInfo/quotaResetDelay in the body, or as an HTTP
// Retry-After header. Only the 429 path used to read either, so a capacity
// refusal reached the scheduler with no number at all and the pool backed off on
// a fixed guess instead of what the upstream asked for.
func antigravityStatusErr(statusCode int, headers http.Header, body []byte) statusErr {
	err := statusErr{code: statusCode, msg: string(body)}
	switch statusCode {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		if wait, parseErr := parseRetryDelayWithHeaders(headers, body, time.Now()); parseErr == nil && wait != nil {
			err.retryAfter = wait
		}
	}
	return err
}
