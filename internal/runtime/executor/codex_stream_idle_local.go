package executor

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
)

var codexHTTPStreamIdleTimeout = 3 * time.Minute

func startCodexHTTPStreamIdleWatch(ctx context.Context, body io.Closer) (chan struct{}, func(), *atomic.Bool) {
	idleReset := make(chan struct{}, 1)
	stop := make(chan struct{})
	done := make(chan struct{})
	timedOut := new(atomic.Bool)
	var stopOnce sync.Once

	go func() {
		defer close(done)
		timer := time.NewTimer(codexHTTPStreamIdleTimeout)
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				timedOut.Store(true)
				helps.LogWithRequestID(ctx).Warnf("codex executor: stream idle timeout after %s without upstream data, aborting read", codexHTTPStreamIdleTimeout)
				_ = body.Close()
				return
			case <-idleReset:
				resetCodexHTTPStreamIdleTimer(timer)
			case <-stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	return idleReset, func() {
		stopOnce.Do(func() {
			close(stop)
			<-done
		})
	}, timedOut
}

func resetCodexHTTPStreamIdleTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(codexHTTPStreamIdleTimeout)
}
