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
var xaiHTTPStreamIdleTimeout = 3 * time.Minute

func startCodexHTTPStreamIdleWatch(ctx context.Context, body io.Closer) (chan struct{}, func(), *atomic.Bool) {
	return startHTTPStreamIdleWatch(ctx, body, "codex executor", &codexHTTPStreamIdleTimeout)
}

func startXAIHTTPStreamIdleWatch(ctx context.Context, body io.Closer) (chan struct{}, func(), *atomic.Bool) {
	return startHTTPStreamIdleWatch(ctx, body, "xai executor", &xaiHTTPStreamIdleTimeout)
}

func startHTTPStreamIdleWatch(ctx context.Context, body io.Closer, label string, timeout *time.Duration) (chan struct{}, func(), *atomic.Bool) {
	idleReset := make(chan struct{}, 1)
	stop := make(chan struct{})
	done := make(chan struct{})
	timedOut := new(atomic.Bool)
	var stopOnce sync.Once
	idle := 3 * time.Minute
	if timeout != nil && *timeout > 0 {
		idle = *timeout
	}

	go func() {
		defer close(done)
		timer := time.NewTimer(idle)
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				timedOut.Store(true)
				helps.LogWithRequestID(ctx).Warnf("%s: stream idle timeout after %s without upstream data, aborting read", label, idle)
				_ = body.Close()
				return
			case <-idleReset:
				resetHTTPStreamIdleTimer(timer, idle)
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
	resetHTTPStreamIdleTimer(timer, codexHTTPStreamIdleTimeout)
}

func resetHTTPStreamIdleTimer(timer *time.Timer, idle time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(idle)
}
