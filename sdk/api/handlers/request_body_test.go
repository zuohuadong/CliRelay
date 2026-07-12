package handlers

import (
	"bytes"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
)

func TestReadRequestBodyWithLimitRejectsOversizedBody(t *testing.T) {
	t.Parallel()

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte("123456789")))

	_, err := ReadRequestBodyWithLimit(ctx, 8)
	if !errors.Is(err, ErrRequestBodyTooLarge) {
		t.Fatalf("ReadRequestBodyWithLimit() error = %v, want ErrRequestBodyTooLarge", err)
	}
}

func TestReadRequestBodyWithLimitRejectsOversizedUnknownLengthBody(t *testing.T) {
	t.Parallel()

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte("123456789")))
	ctx.Request.ContentLength = -1

	_, err := ReadRequestBodyWithLimit(ctx, 8)
	if !errors.Is(err, ErrRequestBodyTooLarge) {
		t.Fatalf("ReadRequestBodyWithLimit() error = %v, want ErrRequestBodyTooLarge", err)
	}
}

func TestReadRequestBodyWithLimitAcceptsBodyAtLimit(t *testing.T) {
	t.Parallel()

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte("12345678")))

	body, err := ReadRequestBodyWithLimit(ctx, 8)
	if err != nil {
		t.Fatalf("ReadRequestBodyWithLimit() error = %v", err)
	}
	if string(body) != "12345678" {
		t.Fatalf("body = %q, want %q", body, "12345678")
	}
}

func TestReadRequestBodyWithLimitRejectsOversizedZstdPayload(t *testing.T) {
	t.Parallel()

	var compressed bytes.Buffer
	writer, err := zstd.NewWriter(&compressed, zstd.WithWindowSize(1<<10))
	if err != nil {
		t.Fatalf("new zstd writer: %v", err)
	}
	if _, err = writer.Write(bytes.Repeat([]byte("a"), 8<<10)); err != nil {
		t.Fatalf("write zstd payload: %v", err)
	}
	if err = writer.Close(); err != nil {
		t.Fatalf("close zstd writer: %v", err)
	}

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(compressed.Bytes()))
	ctx.Request.Header.Set("Content-Encoding", "zstd")

	_, err = ReadRequestBodyWithLimit(ctx, 4<<10)
	if !errors.Is(err, ErrRequestBodyTooLarge) {
		t.Fatalf("ReadRequestBodyWithLimit() error = %v, want ErrRequestBodyTooLarge", err)
	}
}

func TestReadRequestBodyWithLimitRejectsZstdFrameExceedingDecoderMemory(t *testing.T) {
	t.Parallel()

	writer, err := zstd.NewWriter(nil, zstd.WithWindowSize(4<<10), zstd.WithSingleSegment(false))
	if err != nil {
		t.Fatalf("new zstd writer: %v", err)
	}
	compressed := writer.EncodeAll(bytes.Repeat([]byte("a"), 2<<10), nil)
	if err = writer.Close(); err != nil {
		t.Fatalf("close zstd writer: %v", err)
	}
	// A hostile frame can advertise a much larger decoder window than its
	// compressed or decoded body. The decoder must reject it before allocation.
	if len(compressed) < 6 {
		t.Fatalf("compressed frame too short: %d", len(compressed))
	}
	compressed[5] = 3 << 3 // 8 KiB window descriptor.

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(compressed))
	ctx.Request.Header.Set("Content-Encoding", "zstd")

	_, err = ReadRequestBodyWithLimit(ctx, 4<<10)
	if !errors.Is(err, ErrRequestBodyTooLarge) {
		t.Fatalf("ReadRequestBodyWithLimit() error = %v, want ErrRequestBodyTooLarge", err)
	}
}

func TestReadRequestBodyWithLimitHandlesMaxInt64ZstdLimit(t *testing.T) {
	t.Parallel()

	var compressed bytes.Buffer
	writer, err := zstd.NewWriter(&compressed, zstd.WithWindowSize(1<<10))
	if err != nil {
		t.Fatalf("new zstd writer: %v", err)
	}
	if _, err = writer.Write([]byte("ok")); err != nil {
		t.Fatalf("write zstd payload: %v", err)
	}
	if err = writer.Close(); err != nil {
		t.Fatalf("close zstd writer: %v", err)
	}

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(compressed.Bytes()))
	ctx.Request.Header.Set("Content-Encoding", "zstd")

	decoded, err := ReadRequestBodyWithLimit(ctx, math.MaxInt64)
	if err != nil {
		t.Fatalf("ReadRequestBodyWithLimit() error = %v", err)
	}
	if string(decoded) != "ok" {
		t.Fatalf("decoded body = %q, want %q", decoded, "ok")
	}
}
