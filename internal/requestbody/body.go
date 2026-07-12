package requestbody

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
)

const canonicalContextKey = "clirelay.responses.canonical_body"
const originalHeadersContextKey = "clirelay.responses.original_headers"

var ErrTooLarge = errors.New("request body exceeds configured size limit")

type TooLargeError struct{ Limit int64 }

func (e *TooLargeError) Error() string {
	if e == nil || e.Limit <= 0 {
		return ErrTooLarge.Error()
	}
	return fmt.Sprintf("%s: limit is %d bytes", ErrTooLarge, e.Limit)
}

func (e *TooLargeError) Unwrap() error { return ErrTooLarge }

func Canonical(c *gin.Context) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	value, ok := c.Get(canonicalContextKey)
	if !ok {
		return nil, false
	}
	body, ok := value.([]byte)
	return body, ok
}

func SetCanonical(c *gin.Context, body []byte) {
	if c == nil {
		return
	}
	if _, exists := c.Get(originalHeadersContextKey); !exists && c.Request != nil {
		headers := c.Request.Header.Clone()
		if c.Request.ContentLength >= 0 && headers.Get("Content-Length") == "" {
			headers.Set("Content-Length", strconv.FormatInt(c.Request.ContentLength, 10))
		}
		c.Set(originalHeadersContextKey, headers)
	}
	c.Set(canonicalContextKey, body)
	Restore(c.Request, body)
}

func OriginalHeaders(c *gin.Context) (http.Header, bool) {
	if c == nil {
		return nil, false
	}
	value, ok := c.Get(originalHeadersContextKey)
	if !ok {
		return nil, false
	}
	headers, ok := value.(http.Header)
	return headers, ok
}

func Restore(req *http.Request, body []byte) {
	if req == nil {
		return
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header.Del("Content-Encoding")
	req.Header.Del("Content-Length")
}

func ReadAndDecode(w http.ResponseWriter, req *http.Request, maxBytes int64) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("request is nil")
	}
	if maxBytes > 0 && req.ContentLength > maxBytes {
		return nil, &TooLargeError{Limit: maxBytes}
	}
	body := req.Body
	if body == nil {
		return nil, nil
	}
	if maxBytes > 0 {
		body = http.MaxBytesReader(w, body, maxBytes)
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, &TooLargeError{Limit: maxBytes}
		}
		return nil, err
	}
	decoded, err := Decode(raw, req.Header.Get("Content-Encoding"), maxBytes)
	if err != nil && json.Valid(raw) {
		return raw, nil
	}
	return decoded, err
}

func Decode(raw []byte, encoding string, maxBytes int64) ([]byte, error) {
	parts := strings.Split(encoding, ",")
	body := raw
	for i := len(parts) - 1; i >= 0; i-- {
		switch strings.ToLower(strings.TrimSpace(parts[i])) {
		case "", "identity":
		case "zstd":
			decoded, err := decodeZstd(body, maxBytes)
			if err != nil {
				return nil, err
			}
			body = decoded
		default:
			return nil, fmt.Errorf("unsupported request content encoding: %s", strings.TrimSpace(parts[i]))
		}
	}
	if maxBytes > 0 && int64(len(body)) > maxBytes {
		return nil, &TooLargeError{Limit: maxBytes}
	}
	return body, nil
}

func decodeZstd(raw []byte, maxBytes int64) ([]byte, error) {
	options := []zstd.DOption{zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true)}
	if maxBytes > 0 {
		options = append(options, zstd.WithDecoderMaxMemory(uint64(maxBytes)))
	}
	decoder, err := zstd.NewReader(bytes.NewReader(raw), options...)
	if err != nil {
		if maxBytes > 0 && (errors.Is(err, zstd.ErrDecoderSizeExceeded) || errors.Is(err, zstd.ErrWindowSizeExceeded)) {
			return nil, &TooLargeError{Limit: maxBytes}
		}
		return nil, fmt.Errorf("failed to create zstd request decoder: %w", err)
	}
	defer decoder.Close()
	reader := io.Reader(decoder)
	if maxBytes > 0 {
		limit := maxBytes
		if maxBytes != math.MaxInt64 {
			limit++
		}
		reader = io.LimitReader(decoder, limit)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		if maxBytes > 0 && (errors.Is(err, zstd.ErrDecoderSizeExceeded) || errors.Is(err, zstd.ErrWindowSizeExceeded)) {
			return nil, &TooLargeError{Limit: maxBytes}
		}
		return nil, fmt.Errorf("failed to decode zstd request body: %w", err)
	}
	if maxBytes > 0 && int64(len(decoded)) > maxBytes {
		return nil, &TooLargeError{Limit: maxBytes}
	}
	return decoded, nil
}
