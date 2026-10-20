package helps

import "testing"

func TestCachedUtlsRoundTripperReusesTransportPerProxy(t *testing.T) {
	first := cachedUtlsRoundTripper("")
	second := cachedUtlsRoundTripper("")
	if first != second {
		t.Fatal("cached uTLS transport was rebuilt for the same proxy")
	}
	other := cachedUtlsRoundTripper("http://127.0.0.1:1")
	if other == first {
		t.Fatal("cached uTLS transport was shared across different proxies")
	}
}

func TestUtlsRoundTripperCloseIdleConnectionsIsSafe(t *testing.T) {
	transport := newUtlsRoundTripper("")
	transport.CloseIdleConnections()
}
