package video

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

const (
	defaultSignedURLTTL   = 15 * time.Minute
	defaultMaxSourceBytes = int64(2 << 30)
)

type objectStore interface {
	Put(ctx context.Context, key string, body io.ReadSeeker, size int64, contentType string) error
	SignedURL(key string, ttl time.Duration) (string, error)
}

// Service combines durable video task routing with optional S3-compatible
// object persistence.
type Service struct {
	store     *Store
	tempDir   string
	archiveMu sync.Mutex

	mu        sync.RWMutex
	objects   objectStore
	prefix    string
	signedTTL time.Duration
	maxBytes  int64
	lookupIP  func(context.Context, string) ([]net.IPAddr, error)
}

type pinnedPublicVideoTransport struct {
	base     *http.Transport
	lookupIP func(context.Context, string) ([]net.IPAddr, error)
}

type bufferedVideoProxyConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedVideoProxyConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func NewService(cfg config.VideoStorageConfig, databasePath string) (*Service, error) {
	store, err := OpenStore(databasePath)
	if err != nil {
		return nil, err
	}
	service := &Service{
		store:     store,
		tempDir:   filepath.Dir(databasePath),
		prefix:    "videos",
		signedTTL: defaultSignedURLTTL,
		maxBytes:  defaultMaxSourceBytes,
	}
	if err = service.SetConfig(cfg); err != nil {
		_ = store.Close()
		return nil, err
	}
	return service, nil
}

func (s *Service) Close() error {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Close()
}

func (s *Service) Ready(ctx context.Context) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("video service is unavailable")
	}
	return s.store.Ping(ctx)
}

func (s *Service) SetConfig(cfg config.VideoStorageConfig) error {
	prefix := strings.Trim(strings.TrimSpace(cfg.Prefix), "/")
	if prefix == "" {
		prefix = "videos"
	}
	ttl := defaultSignedURLTTL
	if raw := strings.TrimSpace(cfg.SignedURLTTL); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed < time.Second {
			return fmt.Errorf("video storage signed-url-ttl must be at least one second")
		}
		ttl = parsed
	}

	var objects objectStore
	if cfg.Enabled {
		accessEnv := strings.TrimSpace(cfg.AccessKeyIDEnv)
		if accessEnv == "" {
			accessEnv = "VIDEO_STORAGE_ACCESS_KEY_ID"
		}
		secretEnv := strings.TrimSpace(cfg.SecretAccessKeyEnv)
		if secretEnv == "" {
			secretEnv = "VIDEO_STORAGE_SECRET_ACCESS_KEY"
		}
		tokenEnv := strings.TrimSpace(cfg.SessionTokenEnv)
		if tokenEnv == "" {
			tokenEnv = "VIDEO_STORAGE_SESSION_TOKEN"
		}
		created, err := NewS3ObjectStore(S3Config{
			Endpoint:        cfg.Endpoint,
			Region:          cfg.Region,
			Bucket:          cfg.Bucket,
			AccessKeyID:     os.Getenv(accessEnv),
			SecretAccessKey: os.Getenv(secretEnv),
			SessionToken:    os.Getenv(tokenEnv),
			PathStyle:       cfg.PathStyle,
		}, nil)
		if err != nil {
			return err
		}
		objects = created
	}
	maxBytes := cfg.MaxSourceBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxSourceBytes
	}

	s.mu.Lock()
	s.objects = objects
	s.prefix = prefix
	s.signedTTL = ttl
	s.maxBytes = maxBytes
	s.mu.Unlock()
	return nil
}

func (s *Service) CreateJob(ctx context.Context, job Job) (Job, error) {
	if s == nil || s.store == nil {
		return Job{}, fmt.Errorf("video service is unavailable")
	}
	if strings.TrimSpace(job.ID) == "" {
		job.ID = "video_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	if err := s.store.Create(ctx, job); err != nil {
		return Job{}, err
	}
	return s.store.Get(ctx, job.ID)
}

func (s *Service) GetJob(ctx context.Context, id string) (Job, error) {
	if s == nil || s.store == nil {
		return Job{}, fmt.Errorf("video service is unavailable")
	}
	return s.store.Get(ctx, id)
}

func (s *Service) UpdateResult(ctx context.Context, id string, update ResultUpdate) (Job, error) {
	if s == nil || s.store == nil {
		return Job{}, fmt.Errorf("video service is unavailable")
	}
	if err := s.store.UpdateResult(ctx, id, update); err != nil {
		return Job{}, err
	}
	return s.store.Get(ctx, id)
}

func (s *Service) ObjectStorageEnabled() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	enabled := s.objects != nil
	s.mu.RUnlock()
	return enabled
}

func (s *Service) StoreCompleted(ctx context.Context, id string, client *http.Client, sourceURL string) (Job, error) {
	job, err := s.GetJob(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if strings.TrimSpace(job.ObjectKey) != "" {
		return job, nil
	}

	s.mu.RLock()
	objects := s.objects
	s.mu.RUnlock()
	if objects == nil {
		return job, nil
	}
	// Only one completed video is spooled per process at a time, bounding
	// temporary disk use to max-source-bytes for this service instance.
	s.archiveMu.Lock()
	defer s.archiveMu.Unlock()
	job, err = s.GetJob(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if strings.TrimSpace(job.ObjectKey) != "" {
		return job, nil
	}
	s.mu.RLock()
	objects = s.objects
	prefix := s.prefix
	maxBytes := s.maxBytes
	lookupIP := s.lookupIP
	s.mu.RUnlock()
	if objects == nil {
		return job, nil
	}

	parsed, err := parsePublicVideoSourceURL(sourceURL)
	if err != nil {
		return Job{}, err
	}
	if client == nil {
		client = http.DefaultClient
	}
	baseTransport, err := cloneVideoDownloadTransport(client.Transport)
	if err != nil {
		return Job{}, err
	}
	downloadClient := *client
	downloadClient.Transport = &pinnedPublicVideoTransport{base: baseTransport, lookupIP: lookupIP}
	previousCheckRedirect := client.CheckRedirect
	downloadClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("completed video download stopped after too many redirects")
		}
		if _, errValidate := parsePublicVideoSourceURL(req.URL.String()); errValidate != nil {
			return errValidate
		}
		if previousCheckRedirect != nil {
			return previousCheckRedirect(req, via)
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Job{}, fmt.Errorf("create completed video download request: %w", err)
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		return Job{}, fmt.Errorf("download completed video: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Job{}, fmt.Errorf("download completed video: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(limited)))
	}
	if resp.ContentLength > maxBytes {
		return Job{}, fmt.Errorf("completed video exceeds configured max-source-bytes (%d)", maxBytes)
	}

	temp, err := os.CreateTemp(s.tempDir, ".video-upload-*.tmp")
	if err != nil {
		return Job{}, fmt.Errorf("create completed video spool: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()
	if err = os.Chmod(tempPath, 0o600); err != nil {
		return Job{}, fmt.Errorf("harden completed video spool: %w", err)
	}
	size, err := io.Copy(temp, io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return Job{}, fmt.Errorf("spool completed video: %w", err)
	}
	if size == maxBytes {
		var extra [1]byte
		n, errExtra := resp.Body.Read(extra[:])
		if n > 0 {
			return Job{}, fmt.Errorf("completed video exceeds configured max-source-bytes (%d)", maxBytes)
		}
		if errExtra != nil && errExtra != io.EOF {
			return Job{}, fmt.Errorf("check completed video size limit: %w", errExtra)
		}
	}
	if _, err = temp.Seek(0, io.SeekStart); err != nil {
		return Job{}, fmt.Errorf("rewind completed video spool: %w", err)
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "video/mp4"
	}
	objectKey := strings.Trim(prefix, "/") + "/" + job.ID + ".mp4"
	if err = objects.Put(ctx, objectKey, temp, size, contentType); err != nil {
		return Job{}, err
	}
	return s.UpdateResult(ctx, job.ID, ResultUpdate{
		Status:      job.Status,
		Progress:    job.Progress,
		ResultURL:   sourceURL,
		ObjectKey:   objectKey,
		ContentType: contentType,
	})
}

func cloneVideoDownloadTransport(roundTripper http.RoundTripper) (*http.Transport, error) {
	if roundTripper == nil {
		if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok && defaultTransport != nil {
			return defaultTransport.Clone(), nil
		}
		return &http.Transport{}, nil
	}
	transport, ok := roundTripper.(*http.Transport)
	if !ok || transport == nil {
		return nil, fmt.Errorf("completed video download requires an HTTP transport that supports pinned IP dialing")
	}
	return transport.Clone(), nil
}

func (t *pinnedPublicVideoTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t == nil || t.base == nil || req == nil || req.URL == nil {
		return nil, fmt.Errorf("completed video download transport is unavailable")
	}
	parsed, pinnedIP, err := resolvePublicVideoSourceURL(req.Context(), req.URL.String(), t.lookupIP)
	if err != nil {
		return nil, err
	}
	port := parsed.Port()
	if port == "" {
		port = "80"
		if parsed.Scheme == "https" {
			port = "443"
		}
	}

	requestCopy := req.Clone(req.Context())
	urlCopy := *req.URL
	urlCopy.Host = net.JoinHostPort(pinnedIP.String(), port)
	requestCopy.URL = &urlCopy
	requestCopy.Host = parsed.Host

	transport := t.base.Clone()
	transport.DisableKeepAlives = true
	if parsed.Scheme == "https" {
		var proxyURL *url.URL
		if transport.Proxy != nil {
			var errProxy error
			proxyURL, errProxy = transport.Proxy(req)
			if errProxy != nil {
				return nil, fmt.Errorf("resolve completed video proxy: %w", errProxy)
			}
		}
		if proxyURL != nil {
			transport.Proxy = nil
			transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialPinnedVideoProxyTunnel(ctx, t.base, proxyURL, network, urlCopy.Host)
			}
		}
		// Force the standard origin TLS handshake to run after direct, SOCKS, or
		// HTTP CONNECT dialing. This keeps the source SNI separate from HTTPS
		// proxy TLS, which is handled inside dialPinnedVideoProxyTunnel.
		transport.DialTLSContext = nil
		tlsConfig := &tls.Config{}
		if transport.TLSClientConfig != nil {
			tlsConfig = transport.TLSClientConfig.Clone()
		}
		tlsConfig.ServerName = parsed.Hostname()
		transport.TLSClientConfig = tlsConfig
	}
	return transport.RoundTrip(requestCopy)
}

func dialPinnedVideoProxyTunnel(ctx context.Context, base *http.Transport, proxyURL *url.URL, network, target string) (net.Conn, error) {
	if base == nil || proxyURL == nil || (proxyURL.Scheme != "http" && proxyURL.Scheme != "https") {
		return nil, fmt.Errorf("completed video HTTPS source requires an HTTP or HTTPS forward proxy")
	}
	proxyPort := proxyURL.Port()
	if proxyPort == "" {
		proxyPort = "80"
		if proxyURL.Scheme == "https" {
			proxyPort = "443"
		}
	}
	proxyAddress := net.JoinHostPort(proxyURL.Hostname(), proxyPort)
	dialContext := base.DialContext
	if dialContext == nil {
		dialer := &net.Dialer{}
		dialContext = dialer.DialContext
	}
	conn, err := dialContext(ctx, network, proxyAddress)
	if err != nil {
		return nil, fmt.Errorf("dial completed video proxy: %w", err)
	}
	closeOnError := func(cause error) (net.Conn, error) {
		_ = conn.Close()
		return nil, cause
	}
	if proxyURL.Scheme == "https" {
		proxyTLSConfig := &tls.Config{}
		if base.TLSClientConfig != nil {
			proxyTLSConfig = base.TLSClientConfig.Clone()
		}
		proxyTLSConfig.ServerName = proxyURL.Hostname()
		tlsConn := tls.Client(conn, proxyTLSConfig)
		if err = tlsConn.HandshakeContext(ctx); err != nil {
			return closeOnError(fmt.Errorf("handshake completed video HTTPS proxy: %w", err))
		}
		conn = tlsConn
	}

	header := make(http.Header)
	for key, values := range base.ProxyConnectHeader {
		header[key] = append([]string(nil), values...)
	}
	if base.GetProxyConnectHeader != nil {
		dynamicHeader, errHeader := base.GetProxyConnectHeader(ctx, proxyURL, target)
		if errHeader != nil {
			return closeOnError(fmt.Errorf("build completed video proxy CONNECT headers: %w", errHeader))
		}
		for key, values := range dynamicHeader {
			header[key] = append([]string(nil), values...)
		}
	}
	if proxyURL.User != nil && header.Get("Proxy-Authorization") == "" {
		password, _ := proxyURL.User.Password()
		encoded := base64.StdEncoding.EncodeToString([]byte(proxyURL.User.Username() + ":" + password))
		header.Set("Proxy-Authorization", "Basic "+encoded)
	}
	connectRequest := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Host: target},
		Host:   target,
		Header: header,
	}
	if err = connectRequest.Write(conn); err != nil {
		return closeOnError(fmt.Errorf("write completed video proxy CONNECT: %w", err))
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, connectRequest)
	if err != nil {
		return closeOnError(fmt.Errorf("read completed video proxy CONNECT: %w", err))
	}
	if response.Body != nil {
		_ = response.Body.Close()
	}
	if response.StatusCode != http.StatusOK {
		return closeOnError(fmt.Errorf("completed video proxy CONNECT returned %s", response.Status))
	}
	if reader.Buffered() > 0 {
		return &bufferedVideoProxyConn{Conn: conn, reader: reader}, nil
	}
	return conn, nil
}

func parsePublicVideoSourceURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return nil, fmt.Errorf("completed video URL is invalid")
	}
	hostname := strings.TrimSpace(parsed.Hostname())
	if hostname == "" {
		return nil, fmt.Errorf("completed video URL is invalid")
	}
	return parsed, nil
}

func resolvePublicVideoSourceURL(ctx context.Context, rawURL string, lookupIP func(context.Context, string) ([]net.IPAddr, error)) (*url.URL, net.IP, error) {
	parsed, err := parsePublicVideoSourceURL(rawURL)
	if err != nil {
		return nil, nil, err
	}
	hostname := strings.TrimSpace(parsed.Hostname())
	if ip := net.ParseIP(hostname); ip != nil {
		if isBlockedVideoSourceIP(ip) {
			return nil, nil, fmt.Errorf("completed video URL resolves to a private or non-routable address")
		}
		return parsed, ip, nil
	}
	if lookupIP == nil {
		lookupIP = net.DefaultResolver.LookupIPAddr
	}
	addresses, err := lookupIP(ctx, hostname)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve completed video URL host: %w", err)
	}
	if len(addresses) == 0 {
		return nil, nil, fmt.Errorf("resolve completed video URL host: no addresses returned")
	}
	for _, address := range addresses {
		if isBlockedVideoSourceIP(address.IP) {
			return nil, nil, fmt.Errorf("completed video URL resolves to a private or non-routable address")
		}
	}
	return parsed, addresses[0].IP, nil
}

func isBlockedVideoSourceIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		// Carrier-grade NAT is not reported by net.IP.IsPrivate but is still an
		// internal address range and must not be reachable through video URLs.
		return ipv4[0] == 100 && ipv4[1]&0xc0 == 64
	}
	return false
}

func (s *Service) SignedURL(job Job) (string, error) {
	if s == nil || strings.TrimSpace(job.ObjectKey) == "" {
		return "", fmt.Errorf("video object is not stored")
	}
	s.mu.RLock()
	objects := s.objects
	ttl := s.signedTTL
	s.mu.RUnlock()
	if objects == nil {
		return "", fmt.Errorf("video object storage is disabled")
	}
	return objects.SignedURL(job.ObjectKey, ttl)
}
