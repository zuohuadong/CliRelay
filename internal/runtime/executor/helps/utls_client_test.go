package helps

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

type utlsClientRoundTripFunc func(*http.Request) (*http.Response, error)

func (f utlsClientRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type fakeHTTP2ClientConn struct {
	canTake      bool
	reserve      bool
	state        http2.ClientConnState
	roundTripErr error
	closeCalls   int
}

func (f *fakeHTTP2ClientConn) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, f.roundTripErr
}

func (f *fakeHTTP2ClientConn) CanTakeNewRequest() bool { return f.canTake }

func (f *fakeHTTP2ClientConn) ReserveNewRequest() bool { return f.reserve }

func (f *fakeHTTP2ClientConn) State() http2.ClientConnState { return f.state }

func (f *fakeHTTP2ClientConn) Close() error {
	f.closeCalls++
	return nil
}

func TestRetireHTTP2ConnectionPreservesActiveStreams(t *testing.T) {
	conn := &fakeHTTP2ClientConn{
		state: http2.ClientConnState{Closing: true, StreamsActive: 1},
	}

	retireHTTP2Connection(conn)

	if conn.closeCalls != 0 {
		t.Fatalf("Close() calls = %d, want 0 while an SSE stream is active", conn.closeCalls)
	}
}

func TestUtlsHTTP2TransportClosesIdlePoolConnections(t *testing.T) {
	transport := newUtlsHTTP2Transport()
	if transport.IdleConnTimeout != 90*time.Second {
		t.Fatalf("IdleConnTimeout = %v, want 90s", transport.IdleConnTimeout)
	}
}

func TestUtlsRoundTripperDoesNotRetireReservedConnectionAtCapacity(t *testing.T) {
	conn := &fakeHTTP2ClientConn{
		reserve: false,
		state:   http2.ClientConnState{StreamsReserved: 1, MaxConcurrentStreams: 1},
	}
	roundTripper := newUtlsRoundTripperWithDialer(nil)
	roundTripper.connections["chatgpt.com"] = []http2ClientConn{conn}
	roundTripper.pending["chatgpt.com"] = make(chan struct{})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := roundTripper.getOrCreateConnection(ctx, "chatgpt.com", "chatgpt.com:443")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("getOrCreateConnection() error = %v, want context deadline exceeded", err)
	}
	if conn.closeCalls != 0 {
		t.Fatalf("Close() calls = %d, want 0 while another request owns a reservation", conn.closeCalls)
	}
	connections := roundTripper.connections["chatgpt.com"]
	if len(connections) != 1 || connections[0] != conn {
		t.Fatal("connection with an outstanding reservation was retired from the pool")
	}
}

func TestUtlsRoundTripperStreamErrorDoesNotCloseSharedConnection(t *testing.T) {
	conn := &fakeHTTP2ClientConn{
		canTake:      true,
		reserve:      true,
		state:        http2.ClientConnState{StreamsActive: 1},
		roundTripErr: http2.StreamError{StreamID: 3, Code: http2.ErrCodeRefusedStream},
	}
	roundTripper := newUtlsRoundTripperWithDialer(nil)
	roundTripper.connections["chatgpt.com"] = []http2ClientConn{conn}
	req, errRequest := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	if errRequest != nil {
		t.Fatalf("http.NewRequest returned error: %v", errRequest)
	}

	_, errRoundTrip := roundTripper.RoundTrip(req)
	var streamErr http2.StreamError
	if !errors.As(errRoundTrip, &streamErr) {
		t.Fatalf("RoundTrip() error = %v, want http2.StreamError", errRoundTrip)
	}
	if conn.closeCalls != 0 {
		t.Fatalf("Close() calls = %d, want 0 for a single-stream error", conn.closeCalls)
	}
	connections := roundTripper.connections["chatgpt.com"]
	if len(connections) != 1 || connections[0] != conn {
		t.Fatal("shared connection was removed after a single-stream error")
	}
}

func TestUtlsRoundTripperPendingConnectionWaitHonorsContext(t *testing.T) {
	roundTripper := newUtlsRoundTripperWithDialer(nil)
	roundTripper.pending["chatgpt.com"] = make(chan struct{})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := roundTripper.getOrCreateConnection(ctx, "chatgpt.com", "chatgpt.com:443")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("getOrCreateConnection() error = %v, want context deadline exceeded", err)
	}
}

func TestUtlsRoundTripperConnectionCreationHonorsContext(t *testing.T) {
	roundTripper := newUtlsRoundTripperWithDialer(blockingContextProxyDialer{})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := roundTripper.getOrCreateConnection(ctx, "chatgpt.com", "chatgpt.com:443")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("getOrCreateConnection() error = %v, want context deadline exceeded", err)
	}
	if len(roundTripper.pending) != 0 {
		t.Fatalf("pending connections = %d, want 0 after cancellation", len(roundTripper.pending))
	}
	if len(roundTripper.connections) != 0 {
		t.Fatalf("cached connections = %d, want 0 after cancellation", len(roundTripper.connections))
	}
}

type blockingContextProxyDialer struct{}

func (blockingContextProxyDialer) Dial(string, string) (net.Conn, error) {
	return nil, errors.New("Dial should not be used when DialContext is available")
}

func (blockingContextProxyDialer) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

var _ proxy.ContextDialer = blockingContextProxyDialer{}

func TestNewUtlsHTTPClientUsesContextRoundTripperForProtectedHost(t *testing.T) {
	t.Parallel()

	called := false
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", utlsClientRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		if req.URL.Hostname() != "chatgpt.com" {
			t.Fatalf("hostname = %q, want chatgpt.com", req.URL.Hostname())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("{}")),
			Request:    req,
		}, nil
	}))

	client := NewUtlsHTTPClient(ctx, nil, nil, 0)
	resp, err := client.Get("https://chatgpt.com/backend-api/codex/responses")
	if err != nil {
		t.Fatalf("client.Get returned error: %v", err)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatalf("response body close returned error: %v", errClose)
	}
	if !called {
		t.Fatal("expected context RoundTripper to handle protected host request")
	}
}
