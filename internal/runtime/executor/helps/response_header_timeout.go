package helps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// responseHeaderTimeoutError reports that an upstream failed to produce HTTP
// response headers within the configured bootstrap window.
type responseHeaderTimeoutError struct {
	timeout time.Duration
}

func (e *responseHeaderTimeoutError) Error() string {
	return fmt.Sprintf("response header timeout after %s", e.timeout)
}

func (e *responseHeaderTimeoutError) Timeout() bool   { return true }
func (e *responseHeaderTimeoutError) Temporary() bool { return true }

// IsResponseHeaderTimeout reports whether err came from WithResponseHeaderTimeout.
func IsResponseHeaderTimeout(err error) bool {
	var target *responseHeaderTimeoutError
	return errors.As(err, &target)
}

// WithResponseHeaderTimeout limits only the wait for upstream response headers.
// Once headers arrive, the response body keeps using the caller's original
// lifetime and may stream for longer than timeout.
func WithResponseHeaderTimeout(client *http.Client, timeout time.Duration) *http.Client {
	if client == nil || timeout <= 0 {
		return client
	}
	wrapped := *client
	base := wrapped.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	wrapped.Transport = responseHeaderTimeoutRoundTripper{base: base, timeout: timeout}
	return &wrapped
}

type responseHeaderTimeoutRoundTripper struct {
	base    http.RoundTripper
	timeout time.Duration
}

func (t responseHeaderTimeoutRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.timeout <= 0 {
		return t.base.RoundTrip(req)
	}
	requestCtx, cancel := context.WithCancel(req.Context())
	timedOut := make(chan struct{})
	timer := time.AfterFunc(t.timeout, func() {
		close(timedOut)
		cancel()
	})

	resp, err := t.base.RoundTrip(req.Clone(requestCtx))
	if !timer.Stop() {
		<-timedOut
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, &responseHeaderTimeoutError{timeout: t.timeout}
	}
	if err != nil {
		cancel()
		return resp, err
	}
	if resp == nil || resp.Body == nil {
		cancel()
		return resp, nil
	}
	resp.Body = &cancelOnDoneReadCloser{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelOnDoneReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (r *cancelOnDoneReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if err != nil {
		r.once.Do(r.cancel)
	}
	return n, err
}

func (r *cancelOnDoneReadCloser) Close() error {
	r.once.Do(r.cancel)
	return r.ReadCloser.Close()
}
