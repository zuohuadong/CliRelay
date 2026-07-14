package helps

import (
	"net/http"
	"testing"
	"time"
)

func TestWithResponseHeaderTimeoutCancelsTransportWait(t *testing.T) {
	transportDone := make(chan struct{})
	client := WithResponseHeaderTimeout(&http.Client{Transport: responseHeaderTimeoutTestRoundTripper(func(req *http.Request) (*http.Response, error) {
		defer close(transportDone)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}, 50*time.Millisecond)

	request, err := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	_, errDo := client.Do(request)
	if !IsResponseHeaderTimeout(errDo) {
		t.Fatalf("client.Do() error = %v, want response header timeout", errDo)
	}

	select {
	case <-transportDone:
	case <-time.After(time.Second):
		t.Fatal("underlying transport did not exit after timeout cancellation")
	}
}

type responseHeaderTimeoutTestRoundTripper func(*http.Request) (*http.Response, error)

func (f responseHeaderTimeoutTestRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
