package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type RuntimeJob struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	EnqueuedAt time.Time       `json:"enqueued_at"`
}

func AcquireRuntimeLock(ctx context.Context, name, owner string, ttl time.Duration) (bool, error) {
	db, name, owner := RuntimeDB(), strings.TrimSpace(name), strings.TrimSpace(owner)
	if db == nil || RuntimeDriver() != "postgres" || name == "" || owner == "" || ttl <= 0 {
		return false, errors.New("runtime lock requires PostgreSQL, name, owner and positive TTL")
	}
	result, err := db.ExecContext(ctx, `
		INSERT INTO runtime_named_locks (lock_name, owner, expires_at) VALUES
		($1, $2, clock_timestamp() + $3 * interval '1 microsecond')
		ON CONFLICT (lock_name) DO UPDATE SET owner = EXCLUDED.owner,
			expires_at = clock_timestamp() + $3 * interval '1 microsecond'
		WHERE runtime_named_locks.expires_at <= clock_timestamp()`, name, owner, max(1, ttl.Microseconds()))
	return changedRow(result, err)
}

func ReleaseRuntimeLock(ctx context.Context, name, owner string) (bool, error) {
	db, name, owner := RuntimeDB(), strings.TrimSpace(name), strings.TrimSpace(owner)
	if db == nil || RuntimeDriver() != "postgres" || name == "" || owner == "" {
		return false, errors.New("runtime lock requires PostgreSQL, name and owner")
	}
	result, err := db.ExecContext(ctx, `DELETE FROM runtime_named_locks
		WHERE lock_name = $1 AND owner = $2 AND expires_at > clock_timestamp()`, name, owner)
	return changedRow(result, err)
}

func changedRow(result sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// A queue lock orders both sequence allocation and deletion by commit order.
func withRuntimeQueue(ctx context.Context, queue string, op func(*sql.Tx) error) error {
	db := RuntimeDB()
	if db == nil || RuntimeDriver() != "postgres" || queue == "" {
		return errors.New("runtime queue requires PostgreSQL and a queue name")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 824049))`, queue); err != nil {
		return err
	}
	if err = op(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func EnqueueRuntimeJob(ctx context.Context, queue string, job RuntimeJob) error {
	if strings.TrimSpace(job.ID) == "" || strings.TrimSpace(job.Kind) == "" {
		return errors.New("runtime queue requires a job ID and kind")
	}
	if job.EnqueuedAt.IsZero() {
		job.EnqueuedAt = time.Now().UTC()
	}
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	queue = strings.TrimSpace(queue)
	return withRuntimeQueue(ctx, queue, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO runtime_job_queue (queue_name, payload) VALUES ($1, $2)`, queue, data)
		return err
	})
}

// DequeueRuntimeJob atomically consumes the oldest item; zero waits until
// cancellation, matching the former blocking pop.
func DequeueRuntimeJob(ctx context.Context, queue string, timeout time.Duration) (*RuntimeJob, error) {
	if timeout < 0 {
		return nil, errors.New("runtime queue timeout cannot be negative")
	}
	parent := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	queue = strings.TrimSpace(queue)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var job *RuntimeJob
		err := withRuntimeQueue(ctx, queue, func(tx *sql.Tx) error {
			var data []byte
			err := tx.QueryRowContext(ctx, `DELETE FROM runtime_job_queue WHERE sequence = (
				SELECT sequence FROM runtime_job_queue WHERE queue_name = $1 ORDER BY sequence LIMIT 1
			) RETURNING payload`, queue).Scan(&data)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			return json.Unmarshal(data, &job)
		})
		if parent.Err() != nil {
			if err == nil && job != nil {
				return job, nil
			}
			return nil, parent.Err()
		}
		if err == nil && job != nil {
			return job, nil
		}
		if timeout > 0 && ctx.Err() != nil {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, parent.Err()
		case <-ticker.C:
		}
	}
}
