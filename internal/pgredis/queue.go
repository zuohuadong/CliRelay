package pgredis

import (
	"context"
	"errors"
	"time"
)

// Job is one queued unit of work.
type Job struct {
	ID       int64
	Queue    string
	Kind     string
	Payload  []byte
	Attempts int
	// RunAt is when the job became eligible; a retry pushes it into the future.
	RunAt time.Time
}

// EnqueuedJob is the outcome of Enqueue.
type EnqueuedJob struct {
	ID int64
}

// Enqueue stores a job. The insert is one statement, so a caller that gets an
// ID may rely on the job being recoverable after a crash: unlike the Redis list
// this replaces, an accepted job is never lost.
//
// delay postpones the first attempt (retry backoff); zero makes it immediately
// available.
func (s *Store) Enqueue(ctx context.Context, queue, kind string, payload []byte, delay time.Duration) (EnqueuedJob, error) {
	if queue == "" {
		return EnqueuedJob{}, errors.New("pgredis: queue name is required")
	}
	if kind == "" {
		return EnqueuedJob{}, errors.New("pgredis: job kind is required")
	}
	if payload == nil {
		payload = []byte("null")
	}
	var id int64
	err := s.q.QueryRowContext(ctx,
		`INSERT INTO `+s.tables.queue+` (namespace, queue, kind, payload, run_at)
		 VALUES ($1, $2, $3, $4::jsonb, now() + ($5::bigint * INTERVAL '1 millisecond'))
		 RETURNING id`,
		s.ns(), queue, kind, payload, maxInt64(delay.Milliseconds(), 0)).Scan(&id)
	return EnqueuedJob{ID: id}, err
}

// Dequeue claims one ready job for worker until the visibility timeout expires.
//
// SKIP LOCKED is why this beats BLPOP: two workers never block each other, a
// worker that dies holding a job leaks it only until locked_until passes, and
// the job is still in the table the whole time, so it can be inspected.
//
// It returns ok=false when nothing is ready; there is no blocking wait. Callers
// that want to sleep should use Enqueue's LISTEN/NOTIFY sibling
// (internal/cluster) or poll with a backoff.
func (s *Store) Dequeue(ctx context.Context, queue, worker string, visibility time.Duration) (Job, bool, error) {
	if visibility <= 0 {
		visibility = 30 * time.Second
	}
	row := s.q.QueryRowContext(ctx,
		`WITH ready AS (
		   SELECT id FROM `+s.tables.queue+`
		    WHERE namespace = $1 AND queue = $2
		      AND run_at <= now()
		      AND (locked_until IS NULL OR locked_until < now())
		    ORDER BY run_at ASC, id ASC
		    FOR UPDATE SKIP LOCKED
		    LIMIT 1
		 )
		 UPDATE `+s.tables.queue+` job
		    SET locked_until = now() + ($3::bigint * INTERVAL '1 millisecond'),
		        locked_by = $4,
		        attempts = job.attempts + 1
		   FROM ready
		  WHERE job.id = ready.id
		 RETURNING job.id, job.queue, job.kind, job.payload, job.attempts, job.run_at`,
		s.ns(), queue, visibility.Milliseconds(), worker)
	var job Job
	if err := row.Scan(&job.ID, &job.Queue, &job.Kind, &job.Payload, &job.Attempts, &job.RunAt); err != nil {
		if isNoRows(err) {
			return Job{}, false, nil
		}
		return Job{}, false, err
	}
	return job, true, nil
}

// Ack removes a finished job. Doing nothing is not an error, so a worker that
// finished after its visibility timeout expired cannot corrupt the queue.
func (s *Store) Ack(ctx context.Context, id int64) (bool, error) {
	tag, err := s.q.ExecContext(ctx,
		`DELETE FROM `+s.tables.queue+` WHERE namespace = $1 AND id = $2`,
		s.ns(), id)
	if err != nil {
		return false, err
	}
	return rowsAffected(tag) > 0, nil
}

// Retry releases a job and schedules it again after backoff, or drops it when
// attempts already reached maxAttempts.
func (s *Store) Retry(ctx context.Context, id int64, backoff time.Duration, maxAttempts int) (requeued bool, err error) {
	if maxAttempts > 0 {
		var attempts int
		if scanErr := s.q.QueryRowContext(ctx,
			`SELECT attempts FROM `+s.tables.queue+` WHERE namespace = $1 AND id = $2`,
			s.ns(), id).Scan(&attempts); scanErr != nil {
			if isNoRows(scanErr) {
				return false, nil
			}
			return false, scanErr
		}
		if attempts >= maxAttempts {
			_, delErr := s.Ack(ctx, id)
			return false, delErr
		}
	}
	tag, err := s.q.ExecContext(ctx,
		`UPDATE `+s.tables.queue+`
		    SET locked_until = NULL,
		        locked_by = NULL,
		        run_at = now() + ($3::bigint * INTERVAL '1 millisecond')
		  WHERE namespace = $1 AND id = $2`,
		s.ns(), id, maxInt64(backoff.Milliseconds(), 0))
	if err != nil {
		return false, err
	}
	return rowsAffected(tag) > 0, nil
}

// QueueDepth reports how many jobs are waiting, ignoring claimed ones.
func (s *Store) QueueDepth(ctx context.Context, queue string) (int64, error) {
	var count int64
	err := s.q.QueryRowContext(ctx,
		`SELECT count(*) FROM `+s.tables.queue+`
		  WHERE namespace = $1 AND queue = $2
		    AND (locked_until IS NULL OR locked_until < now())`,
		s.ns(), queue).Scan(&count)
	return count, err
}

// ReclaimExpired clears claims whose worker went away, so the jobs become
// visible to the next worker without waiting for a manual repair.
func (s *Store) ReclaimExpired(ctx context.Context) (int64, error) {
	tag, err := s.q.ExecContext(ctx,
		`UPDATE `+s.tables.queue+`
		    SET locked_until = NULL, locked_by = NULL
		  WHERE namespace = $1 AND locked_until IS NOT NULL AND locked_until < now()`,
		s.ns())
	if err != nil {
		return 0, err
	}
	return rowsAffected(tag), nil
}

// WaitForJob waits until a job is ready or the timeout expires.
//
// This is a poll, not a blocking pop: pgredis makes the same trade for blpop,
// because a real wait needs a dedicated LISTEN connection per worker and the
// wakes are unreliable across a reconnect. A poll interval sized to the
// caller's tolerance (default 100ms) costs one cheap indexed query per idle
// worker per tick and needs no extra connections.
//
// wait returns true when something may be ready; the caller still calls Dequeue
// because another worker can claim it first.
func (s *Store) WaitForJob(ctx context.Context, queue string, timeout, poll time.Duration) (bool, error) {
	if poll <= 0 {
		poll = 100 * time.Millisecond
	}
	deadline := time.Time{}
	if timeout > 0 {
		deadline = s.clock().Add(timeout)
	}
	for {
		depth, err := s.QueueDepth(ctx, queue)
		if err != nil {
			return false, err
		}
		if depth > 0 {
			return true, nil
		}
		if !deadline.IsZero() && !s.clock().Before(deadline) {
			return false, nil
		}
		wait := poll
		if !deadline.IsZero() {
			if left := deadline.Sub(s.clock()); left < wait {
				wait = left
			}
		}
		if wait <= 0 {
			return false, nil
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		case <-timer.C:
		}
	}
}

// NotifyJob is what Enqueue's caller runs on the dedicated connection after a
// successful insert, so idle workers wake immediately.
func NotifyJob(ctx context.Context, q Querier, queue string) error {
	_, err := q.ExecContext(ctx, `SELECT pg_notify('pgredis_queue', $1)`, queue)
	return err
}
