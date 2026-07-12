package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
)

// ErrRequestBodyTooLarge indicates that an inbound body exceeds its configured limit.
var ErrRequestBodyTooLarge = errors.New("request body exceeds configured size limit")

type requestBodyTooLargeError struct {
	limit int64
}

func (e *requestBodyTooLargeError) Error() string {
	if e == nil || e.limit <= 0 {
		return ErrRequestBodyTooLarge.Error()
	}
	return fmt.Sprintf("%s: limit is %d bytes", ErrRequestBodyTooLarge, e.limit)
}

func (e *requestBodyTooLargeError) Unwrap() error {
	return ErrRequestBodyTooLarge
}

// ReadRequestBody reads the incoming request body and decodes supported
// Content-Encoding values before handlers inspect JSON fields.
func ReadRequestBody(c *gin.Context) ([]byte, error) {
	return ReadRequestBodyWithLimit(c, 0)
}

// ReadRequestBodyWithLimit reads an incoming request body with an optional hard
// limit. The limit applies both to the wire body and to decoded content, so a
// compressed request cannot allocate an unbounded decoded payload.
// A non-positive limit preserves the legacy unbounded behavior.
func ReadRequestBodyWithLimit(c *gin.Context, maxBytes int64) ([]byte, error) {
	if c == nil || c.Request == nil {
		return nil, fmt.Errorf("request is nil")
	}
	if maxBytes > 0 {
		if c.Request.ContentLength > maxBytes {
			return nil, &requestBodyTooLargeError{limit: maxBytes}
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
	}

	raw, err := c.GetRawData()
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, &requestBodyTooLargeError{limit: maxBytes}
		}
		return nil, err
	}

	encoding := ""
	if c != nil && c.Request != nil {
		encoding = strings.TrimSpace(c.Request.Header.Get("Content-Encoding"))
	}
	if encoding == "" || strings.EqualFold(encoding, "identity") {
		return raw, nil
	}

	decoded, err := decodeRequestBody(raw, encoding, maxBytes)
	if err != nil {
		if json.Valid(raw) {
			return raw, nil
		}
		return nil, err
	}
	return decoded, nil
}

func decodeRequestBody(raw []byte, encoding string, maxBytes int64) ([]byte, error) {
	parts := strings.Split(encoding, ",")
	body := raw
	for i := len(parts) - 1; i >= 0; i-- {
		enc := strings.ToLower(strings.TrimSpace(parts[i]))
		switch enc {
		case "", "identity":
			continue
		case "zstd":
			decoded, err := decodeZstdRequestBody(body, maxBytes)
			if err != nil {
				return nil, err
			}
			body = decoded
		default:
			return nil, fmt.Errorf("unsupported request content encoding: %s", enc)
		}
	}
	return body, nil
}

func decodeZstdRequestBody(raw []byte, maxBytes int64) ([]byte, error) {
	options := []zstd.DOption{zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true)}
	if maxBytes > 0 {
		// Bound the frame window before the decoder allocates its history buffer.
		// io.LimitReader below only bounds the output retained by this handler.
		options = append(options, zstd.WithDecoderMaxMemory(uint64(maxBytes)))
	}
	decoder, err := zstd.NewReader(bytes.NewReader(raw), options...)
	if err != nil {
		return nil, fmt.Errorf("failed to create zstd request decoder: %w", err)
	}
	defer decoder.Close()

	reader := io.Reader(decoder)
	if maxBytes > 0 {
		reader = io.LimitReader(decoder, requestBodyReadLimit(maxBytes))
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		if maxBytes > 0 && (errors.Is(err, zstd.ErrDecoderSizeExceeded) || errors.Is(err, zstd.ErrWindowSizeExceeded)) {
			return nil, &requestBodyTooLargeError{limit: maxBytes}
		}
		return nil, fmt.Errorf("failed to decode zstd request body: %w", err)
	}
	if maxBytes > 0 && int64(len(decoded)) > maxBytes {
		return nil, &requestBodyTooLargeError{limit: maxBytes}
	}
	return decoded, nil
}

func requestBodyReadLimit(maxBytes int64) int64 {
	if maxBytes == math.MaxInt64 {
		return maxBytes
	}
	return maxBytes + 1
}
