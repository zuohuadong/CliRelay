package pgredis

import (
	"errors"
	"fmt"
	"time"
)

// ErrUnavailable reports that the store cannot answer, so the caller should
// degrade to its node-local behaviour. It is the only error callers must
// distinguish: everything else is a real fault.
var ErrUnavailable = errors.New("pgredis: store unavailable")

// InvalidScoreError rejects a score PostgreSQL cannot store faithfully.
type InvalidScoreError struct {
	Score float64
}

func (e *InvalidScoreError) Error() string {
	return fmt.Sprintf("pgredis: invalid sorted-set score %v: NaN and infinities are not representable", e.Score)
}

// InvalidRetentionError rejects a non-positive slot retention. A zero retention
// would create a member that is expired the moment it is written, so the pool
// would appear to grant a slot and immediately lose it.
type InvalidRetentionError struct {
	Retention time.Duration
}

func (e *InvalidRetentionError) Error() string {
	return fmt.Sprintf("pgredis: retention must be positive, got %s", e.Retention)
}

// InvalidWindowError rejects a rate-limit window or limit that cannot describe
// a meaningful budget.
type InvalidWindowError struct {
	Window time.Duration
	Limit  int64
}

func (e *InvalidWindowError) Error() string {
	return fmt.Sprintf("pgredis: invalid rate limit window=%s limit=%d", e.Window, e.Limit)
}
