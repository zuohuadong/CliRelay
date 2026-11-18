package util

import (
	"net"
	"net/http"
	"testing"
	"time"
)

func TestNewDefaultTransportUsesLongResponseHeaderTimeout(t *testing.T) {
	transport := NewDefaultTransport(false)
	if transport == nil {
		t.Fatal("NewDefaultTransport returned nil")
	}
	if transport.ResponseHeaderTimeout != 600*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %s, want 600s", transport.ResponseHeaderTimeout)
	}
}

func TestBuildProxyTransportUsesLongResponseHeaderTimeout(t *testing.T) {
	transport := BuildProxyTransport("http://127.0.0.1:8080", false)
	if transport == nil {
		t.Fatal("BuildProxyTransport returned nil")
	}
	if transport.Proxy == nil {
		t.Fatal("Proxy is nil")
	}
	if transport.ResponseHeaderTimeout != 600*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %s, want 600s", transport.ResponseHeaderTimeout)
	}

	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	proxyURL, err := transport.Proxy(req)
	if err != nil {
		t.Fatalf("Proxy returned error: %v", err)
	}
	if proxyURL == nil || proxyURL.String() != "http://127.0.0.1:8080" {
		t.Fatalf("Proxy URL = %v, want http://127.0.0.1:8080", proxyURL)
	}
}

func TestBuildProxyTransportSocks5HonorsClientTimeout(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	defer func() {
		close(done)
		_ = listener.Close()
	}()

	accepted := make(chan struct{}, 1)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			select {
			case accepted <- struct{}{}:
			default:
			}
			go func() {
				defer conn.Close()
				<-done
			}()
		}
	}()

	transport := BuildProxyTransport("socks5://"+listener.Addr().String(), false)
	if transport == nil {
		t.Fatal("BuildProxyTransport returned nil")
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   200 * time.Millisecond,
	}

	started := time.Now()
	resp, err := client.Get("http://example.com/")
	if resp != nil {
		_ = resp.Body.Close()
	}
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("client.Get returned nil error, want timeout while SOCKS5 server stalls")
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("SOCKS5 dial elapsed %s, want client timeout to cancel promptly", elapsed)
	}
	select {
	case <-accepted:
	default:
		t.Fatal("test SOCKS5 listener did not receive a connection")
	}
}
