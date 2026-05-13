package usage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	usageWriteQueueSize = 4096
	usageLogBatchSize   = 128
	usageLogBatchDelay  = 50 * time.Millisecond
)

type usageLogWrite struct {
	APIKey        string
	APIKeyName    string
	Model         string
	Source        string
	ChannelName   string
	AuthIndex     string
	Failed        bool
	Timestamp     time.Time
	LatencyMs     int64
	FirstTokenMs  int64
	Tokens        TokenStats
	InputContent  string
	OutputContent string
	DetailContent string
}

type usageSyncWrite struct {
	fn   func(*sql.DB) error
	done chan error
}

type usageFlushWrite struct {
	done chan struct{}
}

type usageDBWriteLoop struct {
	db       *sql.DB
	requests chan any
	done     chan struct{}
}

func newUsageDBWriter(db *sql.DB) *usageDBWriteLoop {
	writer := &usageDBWriteLoop{
		db:       db,
		requests: make(chan any, usageWriteQueueSize),
		done:     make(chan struct{}),
	}
	go writer.run()
	return writer
}

func (w *usageDBWriteLoop) run() {
	defer close(w.done)

	ticker := time.NewTicker(usageLogBatchDelay)
	defer ticker.Stop()

	batch := make([]usageLogWrite, 0, usageLogBatchSize)
	flushLogs := func() {
		if len(batch) == 0 {
			return
		}
		w.writeLogBatch(batch)
		batch = batch[:0]
	}

	for {
		select {
		case req, ok := <-w.requests:
			if !ok {
				flushLogs()
				return
			}
			switch typed := req.(type) {
			case usageLogWrite:
				batch = append(batch, typed)
				if len(batch) >= usageLogBatchSize {
					flushLogs()
				}
			case usageSyncWrite:
				flushLogs()
				typed.done <- typed.fn(w.db)
			case usageFlushWrite:
				flushLogs()
				close(typed.done)
			}
		case <-ticker.C:
			flushLogs()
		}
	}
}

func (w *usageDBWriteLoop) writeLogBatch(batch []usageLogWrite) {
	tx, err := w.db.BeginTx(context.Background(), nil)
	if err != nil {
		log.Errorf("usage: begin log batch tx: %v", err)
		return
	}

	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	for _, entry := range batch {
		if err := insertLogTx(tx, entry); err != nil {
			log.Errorf("usage: insert log batch item: %v", err)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		log.Errorf("usage: commit log batch: %v", err)
		return
	}
	committed = true

	if tokenUsageCallback != nil {
		for _, entry := range batch {
			if entry.Tokens.TotalTokens > 0 {
				tokenUsageCallback(entry.APIKey, entry.Tokens.TotalTokens)
			}
		}
	}
}

func enqueueUsageLog(entry usageLogWrite) {
	writer := currentUsageDBWriter()
	if writer == nil {
		return
	}
	select {
	case writer.requests <- entry:
	default:
		log.Warn("usage: write queue full; dropping request log entry")
	}
}

func currentUsageDBWriter() *usageDBWriteLoop {
	return usageDBWriter
}

func runUsageDBWrite(fn func(*sql.DB) error) error {
	writer := currentUsageDBWriter()
	if writer == nil {
		db := getDB()
		if db == nil {
			return fmt.Errorf("usage: database not initialised")
		}
		return fn(db)
	}
	done := make(chan error, 1)
	writer.requests <- usageSyncWrite{fn: fn, done: done}
	return <-done
}

func flushUsageDBWrites() {
	writer := currentUsageDBWriter()
	if writer == nil {
		return
	}
	done := make(chan struct{})
	writer.requests <- usageFlushWrite{done: done}
	<-done
}

func stopUsageDBWriter() {
	writer := currentUsageDBWriter()
	if writer == nil {
		return
	}
	close(writer.requests)
	<-writer.done
	usageDBWriter = nil
}
