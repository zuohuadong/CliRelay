package executor

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// bedrockSigV4Signer signs AWS Bedrock Runtime requests using Signature Version 4.
// It replaces the upstream aws-sdk-go-v2 signer so the fork does not need to add
// the heavy AWS SDK dependency (which cannot be fetched in offline builds).
type bedrockSigV4Signer struct {
	accessKeyID     string
	secretAccessKey string
	sessionToken    string
	region          string
}

func newBedrockSigV4Signer(accessKeyID, secretAccessKey, sessionToken, region string) (*bedrockSigV4Signer, error) {
	accessKeyID = strings.TrimSpace(accessKeyID)
	secretAccessKey = strings.TrimSpace(secretAccessKey)
	if accessKeyID == "" {
		return nil, fmt.Errorf("bedrock executor: missing aws access key id")
	}
	if secretAccessKey == "" {
		return nil, fmt.Errorf("bedrock executor: missing aws secret access key")
	}
	if strings.TrimSpace(region) == "" {
		region = defaultBedrockRegion
	}
	return &bedrockSigV4Signer{
		accessKeyID:     accessKeyID,
		secretAccessKey: secretAccessKey,
		sessionToken:    strings.TrimSpace(sessionToken),
		region:          region,
	}, nil
}

func (s *bedrockSigV4Signer) signRequest(req *http.Request, body []byte) error {
	if req == nil {
		return fmt.Errorf("bedrock sigv4: request is nil")
	}
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	if s.sessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", s.sessionToken)
	}

	payloadHash := hashSHA256(body)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	canonicalHeaders, signedHeaders := s.canonicalHeaders(req)
	canonicalRequest := strings.Join([]string{
		req.Method,
		s.canonicalPath(req.URL),
		s.canonicalQuery(req.URL),
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	credentialScope := strings.Join([]string{dateStamp, s.region, "bedrock", "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		hashSHA256([]byte(canonicalRequest)),
	}, "\n")

	signingKey := s.deriveSigningKey(dateStamp)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	authorization := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		s.accessKeyID, credentialScope, signedHeaders, signature,
	)
	req.Header.Set("Authorization", authorization)
	return nil
}

func (s *bedrockSigV4Signer) canonicalHeaders(req *http.Request) (canonical string, signed string) {
	headerSet := map[string][]string{
		"host":       {req.URL.Host},
		"x-amz-date": {req.Header.Get("X-Amz-Date")},
	}
	if v := req.Header.Get("X-Amz-Content-Sha256"); v != "" {
		headerSet["x-amz-content-sha256"] = []string{v}
	}
	if v := req.Header.Get("X-Amz-Security-Token"); v != "" {
		headerSet["x-amz-security-token"] = []string{v}
	}
	keys := make([]string, 0, len(headerSet))
	for k := range headerSet {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var canonicalBuf strings.Builder
	var signedBuf strings.Builder
	for i, k := range keys {
		for _, val := range headerSet[k] {
			canonicalBuf.WriteString(k)
			canonicalBuf.WriteString(":")
			canonicalBuf.WriteString(strings.TrimSpace(val))
			canonicalBuf.WriteString("\n")
		}
		signedBuf.WriteString(k)
		if i < len(keys)-1 {
			signedBuf.WriteString(";")
		}
	}
	return canonicalBuf.String(), signedBuf.String()
}

func (s *bedrockSigV4Signer) canonicalPath(u *url.URL) string {
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	return path
}

func (s *bedrockSigV4Signer) canonicalQuery(u *url.URL) string {
	values := u.Query()
	type queryPair struct {
		key   string
		value string
	}
	pairs := make([]queryPair, 0, len(values))
	for k := range values {
		for _, v := range values[k] {
			pairs = append(pairs, queryPair{
				key:   awsSigV4Escape(k),
				value: awsSigV4Escape(v),
			})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].key == pairs[j].key {
			return pairs[i].value < pairs[j].value
		}
		return pairs[i].key < pairs[j].key
	})
	var buf strings.Builder
	for i, pair := range pairs {
		if i > 0 {
			buf.WriteString("&")
		}
		buf.WriteString(pair.key)
		buf.WriteString("=")
		buf.WriteString(pair.value)
	}
	return buf.String()
}

func awsSigV4Escape(value string) string {
	escaped := url.QueryEscape(value)
	escaped = strings.ReplaceAll(escaped, "+", "%20")
	escaped = strings.ReplaceAll(escaped, "%7E", "~")
	return escaped
}

func (s *bedrockSigV4Signer) deriveSigningKey(dateStamp string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+s.secretAccessKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(s.region))
	kService := hmacSHA256(kRegion, []byte("bedrock"))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	return kSigning
}

func hashSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}
