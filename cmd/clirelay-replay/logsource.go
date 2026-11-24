package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/klauspost/compress/zstd"
)

// loadClientBody reads the request body a client sent for logID out of
// request_log_content, where it is stored zstd-compressed.
func loadClientBody(dsn string, logID int64) ([]byte, error) {
	var compressed []byte
	err := withLogContent(context.Background(), dsn, logID, func(_, input, _ []byte) error {
		compressed = input
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(compressed) == 0 {
		return nil, fmt.Errorf("log %d has no stored request body (body storage is off by default; see request-log-storage.store-content)", logID)
	}
	return decompress(compressed)
}

// loadStoredUpstreamTail returns the tail of the upstream log that went with
// logID: what the upstream actually answered, which is usually the only place
// the reason for a failure is written down.
func loadStoredUpstreamTail(dsn string, logID int64) string {
	var detail []byte
	err := withLogContent(context.Background(), dsn, logID, func(detailRaw, _, _ []byte) error {
		detail = detailRaw
		return nil
	})
	if err != nil || len(detail) == 0 {
		return ""
	}
	plain, err := decompress(detail)
	if err != nil {
		return ""
	}
	return upstreamLogTail(plain, 2000)
}

func withLogContent(ctx context.Context, dsn string, logID int64, fn func(detail, input, output []byte) error) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(ctx)

	var detail, input, output []byte
	err = conn.QueryRow(ctx,
		`SELECT coalesce(detail_content,''::bytea), coalesce(input_content,''::bytea), coalesce(output_content,''::bytea)
		   FROM request_log_content WHERE log_id = $1`, logID,
	).Scan(&detail, &input, &output)
	if err != nil {
		if err == pgx.ErrNoRows {
			return fmt.Errorf("log %d has no stored content row", logID)
		}
		return fmt.Errorf("read log %d: %w", logID, err)
	}
	return fn(detail, input, output)
}

func decompress(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	decoder, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	defer decoder.Close()
	return decoder.DecodeAll(data, nil)
}

// upstreamLogTail pulls the upstream response section out of a stored request
// detail and keeps its last limit bytes: the end of a stream is where a failure
// is explained, and the head is only the conversation the client already knows.
func upstreamLogTail(detail []byte, limit int) string {
	var payload struct {
		Response struct {
			UpstreamLog string `json:"upstream_log"`
		} `json:"response"`
	}
	if err := json.Unmarshal(detail, &payload); err != nil {
		return ""
	}
	log := payload.Response.UpstreamLog
	if log == "" {
		return ""
	}
	if len(log) > limit {
		log = "…" + log[len(log)-limit:]
	}
	return log
}
