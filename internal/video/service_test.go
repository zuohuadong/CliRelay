package video

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

type discardObjectStore struct{}

func (discardObjectStore) Put(_ context.Context, _ string, body io.ReadSeeker, _ int64, _ string) error {
	_, err := io.Copy(io.Discard, body)
	return err
}

func (discardObjectStore) SignedURL(string, time.Duration) (string, error) {
	return "https://objects.example/video.mp4", nil
}

func TestServiceCreatesDurablePublicVideoID(t *testing.T) {
	service, err := NewService(config.VideoStorageConfig{}, filepath.Join(t.TempDir(), "video.db"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })

	created, err := service.CreateJob(context.Background(), Job{
		UpstreamID: "upstream_123",
		Provider:   "xai",
		AuthID:     "auth_123",
		Model:      "grok-imagine-video",
		Status:     "queued",
	})
	if err != nil {
		t.Fatalf("CreateJob() error = %v", err)
	}
	if !strings.HasPrefix(created.ID, "video_") || created.ID == created.UpstreamID {
		t.Fatalf("public ID = %q, upstream ID = %q", created.ID, created.UpstreamID)
	}

	got, err := service.GetJob(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetJob() error = %v", err)
	}
	if got.UpstreamID != "upstream_123" || got.AuthID != "auth_123" || got.Model != "grok-imagine-video" {
		t.Fatalf("persisted job = %+v", got)
	}
}

func TestServiceStoresCompletedVideoAndReturnsSignedURL(t *testing.T) {
	t.Setenv("TEST_VIDEO_ACCESS_KEY", "access-key")
	t.Setenv("TEST_VIDEO_SECRET_KEY", "secret-key")

	var uploadedPath string
	var uploadedBody string
	objectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploadedPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		uploadedBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(objectServer.Close)
	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("completed-video"))
	}))
	t.Cleanup(sourceServer.Close)
	var dialedTarget string
	sourceClient := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		dialedTarget = address
		return (&net.Dialer{}).DialContext(ctx, network, sourceServer.Listener.Addr().String())
	}}}

	service, err := NewService(config.VideoStorageConfig{
		Enabled:            true,
		Endpoint:           objectServer.URL,
		Region:             "auto",
		Bucket:             "videos",
		PathStyle:          true,
		Prefix:             "generated",
		SignedURLTTL:       "10m",
		AccessKeyIDEnv:     "TEST_VIDEO_ACCESS_KEY",
		SecretAccessKeyEnv: "TEST_VIDEO_SECRET_KEY",
	}, filepath.Join(t.TempDir(), "video.db"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	service.mu.Lock()
	service.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	service.mu.Unlock()

	job, err := service.CreateJob(context.Background(), Job{UpstreamID: "upstream_123", Provider: "openai-compatibility", AuthID: "auth_123", Model: "agnes-video-v2.0", Status: "completed"})
	if err != nil {
		t.Fatalf("CreateJob() error = %v", err)
	}
	stored, err := service.StoreCompleted(context.Background(), job.ID, sourceClient, "http://video.example/video.mp4")
	if err != nil {
		t.Fatalf("StoreCompleted() error = %v", err)
	}
	if uploadedPath != "/videos/generated/"+job.ID+".mp4" || uploadedBody != "completed-video" {
		t.Fatalf("uploaded path/body = %q %q", uploadedPath, uploadedBody)
	}
	if dialedTarget != "8.8.8.8:80" {
		t.Fatalf("download dial target = %q, want pinned public IP", dialedTarget)
	}
	if stored.ObjectKey == "" || stored.ContentType != "video/mp4" {
		t.Fatalf("stored job = %+v", stored)
	}
	signed, err := service.SignedURL(stored)
	if err != nil {
		t.Fatalf("SignedURL() error = %v", err)
	}
	if !strings.Contains(signed, "X-Amz-Signature=") || !strings.Contains(signed, "X-Amz-Expires=600") {
		t.Fatalf("signed URL = %s", signed)
	}
}

func TestServiceRejectsPrivateAndRedirectedVideoSources(t *testing.T) {
	service, err := NewService(config.VideoStorageConfig{}, filepath.Join(t.TempDir(), "video.db"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })

	job, err := service.CreateJob(context.Background(), Job{UpstreamID: "upstream_123", Provider: "xai", AuthID: "auth_123", Model: "grok-imagine-video", Status: "completed"})
	if err != nil {
		t.Fatalf("CreateJob() error = %v", err)
	}
	service.mu.Lock()
	service.objects = &S3ObjectStore{}
	service.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	service.mu.Unlock()
	if _, err = service.StoreCompleted(context.Background(), job.ID, nil, "http://169.254.169.254/latest/meta-data"); err == nil {
		t.Fatal("StoreCompleted() error = nil, want private source rejection")
	}

	redirectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://127.0.0.1/private.mp4")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(redirectServer.Close)
	redirectClient := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, redirectServer.Listener.Addr().String())
	}}}
	if _, err = service.StoreCompleted(context.Background(), job.ID, redirectClient, "http://video.example/video.mp4"); err == nil {
		t.Fatal("StoreCompleted() error = nil, want private redirect rejection")
	}
	service.mu.Lock()
	service.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("127.0.0.1")}}, nil
	}
	service.mu.Unlock()
	if _, err = service.StoreCompleted(context.Background(), job.ID, redirectClient, "http://mixed.example/video.mp4"); err == nil {
		t.Fatal("StoreCompleted() error = nil, want mixed public/private DNS rejection")
	}
}

func TestServiceLimitsCompletedVideoSpoolSize(t *testing.T) {
	service, err := NewService(config.VideoStorageConfig{}, filepath.Join(t.TempDir(), "video.db"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	service.mu.Lock()
	service.objects = &S3ObjectStore{}
	service.maxBytes = 4
	service.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	service.mu.Unlock()

	job, err := service.CreateJob(context.Background(), Job{UpstreamID: "upstream_123", Provider: "xai", AuthID: "auth_123", Model: "grok-imagine-video", Status: "completed"})
	if err != nil {
		t.Fatalf("CreateJob() error = %v", err)
	}
	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("12345"))
	}))
	t.Cleanup(sourceServer.Close)
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, sourceServer.Listener.Addr().String())
	}}}
	if _, err = service.StoreCompleted(context.Background(), job.ID, client, "http://video.example/video.mp4"); err == nil || !strings.Contains(err.Error(), "max-source-bytes") {
		t.Fatalf("StoreCompleted() error = %v, want size limit rejection", err)
	}
}

func TestPinnedVideoTransportSeparatesHTTPSProxyAndOriginSNI(t *testing.T) {
	originSNI := make(chan string, 1)
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("proxy-video"))
	}))
	origin.TLS = &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		select {
		case originSNI <- hello.ServerName:
		default:
		}
		return nil, nil
	}}
	origin.StartTLS()
	t.Cleanup(origin.Close)

	proxySNI := make(chan string, 1)
	connectTarget := make(chan string, 1)
	proxyServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		select {
		case connectTarget <- r.Host:
		default:
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijacking unavailable", http.StatusInternalServerError)
			return
		}
		clientConn, buffered, errHijack := hijacker.Hijack()
		if errHijack != nil {
			return
		}
		originConn, errDial := net.Dial("tcp", origin.Listener.Addr().String())
		if errDial != nil {
			_ = clientConn.Close()
			return
		}
		_, _ = io.WriteString(buffered, "HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffered.Flush()
		copyDone := make(chan struct{})
		go func() {
			_, _ = io.Copy(originConn, buffered)
			_ = originConn.Close()
			close(copyDone)
		}()
		_, _ = io.Copy(clientConn, originConn)
		_ = clientConn.Close()
		<-copyDone
	}))
	proxyServer.TLS = &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		select {
		case proxySNI <- hello.ServerName:
		default:
		}
		return nil, nil
	}}
	proxyServer.StartTLS()
	t.Cleanup(proxyServer.Close)

	parsedProxyURL, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}
	parsedProxyURL.Host = net.JoinHostPort("proxy.example", parsedProxyURL.Port())
	client := &http.Client{Transport: &http.Transport{
		Proxy: http.ProxyURL(parsedProxyURL),
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, proxyServer.Listener.Addr().String())
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // test servers use ephemeral certificates
	}}

	service, err := NewService(config.VideoStorageConfig{}, filepath.Join(t.TempDir(), "video.db"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	service.mu.Lock()
	service.objects = discardObjectStore{}
	service.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	service.mu.Unlock()
	job, err := service.CreateJob(context.Background(), Job{UpstreamID: "upstream_proxy", Provider: "xai", AuthID: "auth_proxy", Model: "grok-imagine-video", Status: "completed"})
	if err != nil {
		t.Fatalf("CreateJob() error = %v", err)
	}
	if _, err = service.StoreCompleted(context.Background(), job.ID, client, "https://video.example/video.mp4"); err != nil {
		t.Fatalf("StoreCompleted() error = %v", err)
	}

	select {
	case got := <-proxySNI:
		if got != "proxy.example" {
			t.Fatalf("proxy SNI = %q, want proxy.example", got)
		}
	case <-time.After(time.Second):
		t.Fatal("proxy TLS handshake was not observed")
	}
	select {
	case got := <-originSNI:
		if got != "video.example" {
			t.Fatalf("origin SNI = %q, want video.example", got)
		}
	case <-time.After(time.Second):
		t.Fatal("origin TLS handshake was not observed")
	}
	select {
	case got := <-connectTarget:
		if got != "8.8.8.8:443" {
			t.Fatalf("proxy CONNECT target = %q, want pinned public IP", got)
		}
	case <-time.After(time.Second):
		t.Fatal("proxy CONNECT target was not observed")
	}
}
