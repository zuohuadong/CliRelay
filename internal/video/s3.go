package video

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const s3UnsignedPayload = "UNSIGNED-PAYLOAD"

type S3Config struct {
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	PathStyle       bool
}

type S3ObjectStore struct {
	endpoint        *url.URL
	region          string
	bucket          string
	accessKeyID     string
	secretAccessKey string
	sessionToken    string
	pathStyle       bool
	client          *http.Client
	now             func() time.Time
}

func NewS3ObjectStore(cfg S3Config, client *http.Client) (*S3ObjectStore, error) {
	endpoint, err := url.Parse(strings.TrimSpace(cfg.Endpoint))
	if err != nil || endpoint == nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" {
		return nil, fmt.Errorf("video storage endpoint must be an absolute HTTP URL")
	}
	if endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, fmt.Errorf("video storage endpoint must not contain credentials, query parameters, or fragments")
	}
	if endpoint.Scheme != "https" && !isLoopbackObjectEndpoint(endpoint.Hostname()) {
		return nil, fmt.Errorf("video storage endpoint must use HTTPS; plain HTTP is only allowed for loopback development endpoints")
	}
	bucket := strings.Trim(strings.TrimSpace(cfg.Bucket), "/")
	if bucket == "" {
		return nil, fmt.Errorf("video storage bucket is required")
	}
	accessKeyID := strings.TrimSpace(cfg.AccessKeyID)
	secretAccessKey := strings.TrimSpace(cfg.SecretAccessKey)
	if accessKeyID == "" || secretAccessKey == "" {
		return nil, fmt.Errorf("video storage access key and secret key are required")
	}
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = "us-east-1"
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &S3ObjectStore{
		endpoint:        endpoint,
		region:          region,
		bucket:          bucket,
		accessKeyID:     accessKeyID,
		secretAccessKey: secretAccessKey,
		sessionToken:    strings.TrimSpace(cfg.SessionToken),
		pathStyle:       cfg.PathStyle,
		client:          client,
		now:             func() time.Time { return time.Now().UTC() },
	}, nil
}

func (s *S3ObjectStore) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, contentType string) error {
	if body == nil {
		return fmt.Errorf("video storage PUT body is required")
	}
	hasher := sha256.New()
	hashedSize, err := io.Copy(hasher, body)
	if err != nil {
		return fmt.Errorf("hash video object payload: %w", err)
	}
	if size >= 0 && hashedSize != size {
		return fmt.Errorf("video object size changed while hashing: got %d, want %d", hashedSize, size)
	}
	if _, err = body.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind video object payload: %w", err)
	}
	payloadHash := hex.EncodeToString(hasher.Sum(nil))
	objectURL, err := s.objectURL(key)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, objectURL.String(), body)
	if err != nil {
		return fmt.Errorf("create video storage PUT request: %w", err)
	}
	if size >= 0 {
		req.ContentLength = size
	}
	if strings.TrimSpace(contentType) != "" {
		req.Header.Set("Content-Type", strings.TrimSpace(contentType))
	}
	if err = s.sign(req, payloadHash, s.now()); err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("upload video object: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		message := strings.TrimSpace(string(limited))
		if message == "" {
			message = resp.Status
		}
		return fmt.Errorf("upload video object: HTTP %d: %s", resp.StatusCode, message)
	}
	return nil
}

func (s *S3ObjectStore) SignedURL(key string, ttl time.Duration) (string, error) {
	objectURL, err := s.objectURL(key)
	if err != nil {
		return "", err
	}
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	if ttl < time.Second {
		ttl = time.Second
	}
	if ttl > 7*24*time.Hour {
		ttl = 7 * 24 * time.Hour
	}
	now := s.now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	credentialScope := strings.Join([]string{dateStamp, s.region, "s3", "aws4_request"}, "/")
	query := objectURL.Query()
	query.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	query.Set("X-Amz-Credential", s.accessKeyID+"/"+credentialScope)
	query.Set("X-Amz-Date", amzDate)
	query.Set("X-Amz-Expires", strconv.FormatInt(int64(ttl/time.Second), 10))
	query.Set("X-Amz-SignedHeaders", "host")
	if s.sessionToken != "" {
		query.Set("X-Amz-Security-Token", s.sessionToken)
	}
	objectURL.RawQuery = canonicalS3Query(query)
	canonicalRequest := strings.Join([]string{
		http.MethodGet,
		canonicalS3Path(objectURL),
		objectURL.RawQuery,
		"host:" + objectURL.Host + "\n",
		"host",
		s3UnsignedPayload,
	}, "\n")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")
	signature := hex.EncodeToString(hmacSHA256Bytes(s.signingKey(dateStamp), []byte(stringToSign)))
	query = objectURL.Query()
	query.Set("X-Amz-Signature", signature)
	objectURL.RawQuery = canonicalS3Query(query)
	return objectURL.String(), nil
}

func (s *S3ObjectStore) objectURL(key string) (*url.URL, error) {
	if s == nil || s.endpoint == nil {
		return nil, fmt.Errorf("video object store is unavailable")
	}
	key = strings.TrimLeft(strings.TrimSpace(key), "/")
	if key == "" {
		return nil, fmt.Errorf("video object key is required")
	}
	out := *s.endpoint
	out.RawQuery = ""
	out.Fragment = ""
	basePath := strings.TrimRight(out.Path, "/")
	if s.pathStyle {
		out.Path = basePath + "/" + s.bucket + "/" + key
	} else {
		out.Host = s.bucket + "." + out.Host
		out.Path = basePath + "/" + key
	}
	out.RawPath = awsS3EscapePath(out.Path)
	return &out, nil
}

func (s *S3ObjectStore) sign(req *http.Request, payloadHash string, now time.Time) error {
	if req == nil || req.URL == nil {
		return fmt.Errorf("video storage request is nil")
	}
	now = now.UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if s.sessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", s.sessionToken)
	}
	headerNames := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	headerValues := map[string]string{
		"host":                 req.URL.Host,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           amzDate,
	}
	if s.sessionToken != "" {
		headerNames = append(headerNames, "x-amz-security-token")
		headerValues["x-amz-security-token"] = s.sessionToken
	}
	sort.Strings(headerNames)
	var canonicalHeaders strings.Builder
	for _, name := range headerNames {
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteString(":")
		canonicalHeaders.WriteString(strings.TrimSpace(headerValues[name]))
		canonicalHeaders.WriteString("\n")
	}
	signedHeaders := strings.Join(headerNames, ";")
	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalS3Path(req.URL),
		canonicalS3Query(req.URL.Query()),
		canonicalHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")
	credentialScope := strings.Join([]string{dateStamp, s.region, "s3", "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")
	signature := hex.EncodeToString(hmacSHA256Bytes(s.signingKey(dateStamp), []byte(stringToSign)))
	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		s.accessKeyID, credentialScope, signedHeaders, signature,
	))
	return nil
}

func (s *S3ObjectStore) signingKey(dateStamp string) []byte {
	kDate := hmacSHA256Bytes([]byte("AWS4"+s.secretAccessKey), []byte(dateStamp))
	kRegion := hmacSHA256Bytes(kDate, []byte(s.region))
	kService := hmacSHA256Bytes(kRegion, []byte("s3"))
	return hmacSHA256Bytes(kService, []byte("aws4_request"))
}

func canonicalS3Path(u *url.URL) string {
	path := u.RawPath
	if path == "" {
		path = awsS3EscapePath(u.Path)
	}
	if path == "" {
		return "/"
	}
	return path
}

func awsS3EscapePath(value string) string {
	var out strings.Builder
	const hexChars = "0123456789ABCDEF"
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if ch == '/' || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' || ch == '~' {
			out.WriteByte(ch)
			continue
		}
		out.WriteByte('%')
		out.WriteByte(hexChars[ch>>4])
		out.WriteByte(hexChars[ch&0x0f])
	}
	return out.String()
}

func isLoopbackObjectEndpoint(hostname string) bool {
	hostname = strings.TrimSpace(strings.ToLower(hostname))
	if hostname == "localhost" {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

func canonicalS3Query(values url.Values) string {
	type pair struct{ key, value string }
	pairs := make([]pair, 0)
	for key, valuesForKey := range values {
		if len(valuesForKey) == 0 {
			pairs = append(pairs, pair{key: awsS3Escape(key)})
			continue
		}
		for _, value := range valuesForKey {
			pairs = append(pairs, pair{key: awsS3Escape(key), value: awsS3Escape(value)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].key == pairs[j].key {
			return pairs[i].value < pairs[j].value
		}
		return pairs[i].key < pairs[j].key
	})
	var out strings.Builder
	for i, item := range pairs {
		if i > 0 {
			out.WriteByte('&')
		}
		out.WriteString(item.key)
		out.WriteByte('=')
		out.WriteString(item.value)
	}
	return out.String()
}

func awsS3Escape(value string) string {
	escaped := url.QueryEscape(value)
	escaped = strings.ReplaceAll(escaped, "+", "%20")
	escaped = strings.ReplaceAll(escaped, "%7E", "~")
	return escaped
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256Bytes(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(data)
	return mac.Sum(nil)
}
