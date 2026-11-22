package sharedstate

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"sync"
	"time"
)

// Per-subject request and token windows.
//
// Each subject (an API key or an end user) has one counter per wall-clock
// minute for requests and one for tokens. The trailing-minute value is the
// current bucket plus the previous bucket weighted by the part of it that is
// still inside the last 60 seconds. That approximates the node-local sliding
// window closely without storing one entry per request, and unlike a plain
// per-minute counter it does not let a client spend two minutes of budget
// across a minute boundary.
const (
	windowMillis = int64(time.Minute / time.Millisecond)
	// bucketTTL keeps a bucket while it can still be the previous minute.
	bucketTTL = 3 * time.Minute
)

// Admission is the cluster-wide view of one admitted request.
type Admission struct {
	// RPM and TPM are the subject's trailing-minute request and token counts
	// across the cluster, including this request.
	RPM float64
	TPM float64
	// SlotTaken reports whether a concurrency slot is now held for the
	// request. Held is the number of slots in use, including this one when
	// taken.
	SlotTaken bool
	Held      int
	// Release gives the slot back. It is safe to call more than once and is a
	// no-op when no slot was taken.
	Release func()
}

// Rate is one subject's cluster-wide trailing-minute usage.
type Rate struct {
	RPM float64
	TPM float64
}

func subjectKey(subject string) string { return stateKey("quota", subject) }

func minuteOf(now time.Time) (minute, elapsed int64) {
	ms := now.UnixMilli()
	return ms / windowMillis, ms % windowMillis
}

// AdmitRequest counts one request for subject and, when concurrencyLimit is
// positive, tries to take one of its concurrency slots, all atomically. The
// caller decides admission from the returned view and must call Release when
// the request ends, whether or not it was admitted.
func (s *Store) AdmitRequest(ctx context.Context, subject string, concurrencyLimit int) (Admission, error) {
	scope := subjectKey(subject)
	member := ""
	if s.Available() && concurrencyLimit > 0 {
		member = s.newLeaseMember()
	}
	adm := Admission{Release: func() {}}
	err := s.transaction(ctx, scope, func(ctx context.Context, tx *sql.Tx, now time.Time) error {
		minute, elapsed := minuteOf(now)
		_, err := tx.ExecContext(ctx, `
			INSERT INTO cluster_runtime_rates (subject, minute, requests, tokens, expires_at)
			VALUES ($1, $2, 1, 0, $3)
			ON CONFLICT (subject, minute) DO UPDATE
			SET requests = cluster_runtime_rates.requests + 1, expires_at = EXCLUDED.expires_at`,
			scope, minute, now.Add(bucketTTL))
		if err != nil {
			return err
		}
		weight := float64(windowMillis-elapsed) / float64(windowMillis)
		err = tx.QueryRowContext(ctx, `SELECT `+weightedRatesSQL+`
			FROM cluster_runtime_rates WHERE subject = $1 AND minute BETWEEN $2 - 1 AND $2 AND expires_at > $4`,
			scope, minute, weight, now).Scan(&adm.RPM, &adm.TPM)
		if err != nil {
			return err
		}
		if concurrencyLimit > 0 {
			adm.SlotTaken, adm.Held, err = s.takeSlot(ctx, tx, scope, member, concurrencyLimit, now)
		}
		return err
	})
	if err != nil {
		return Admission{}, err
	}
	adm.RPM = math.Floor(adm.RPM*1000) / 1000
	adm.TPM = math.Floor(adm.TPM*1000) / 1000
	if adm.SlotTaken {
		s.keeper.track(scope, member)
		var once sync.Once
		adm.Release = func() {
			once.Do(func() { s.keeper.release(scope, member) })
		}
	}
	return adm, nil
}

// CountRequest counts one request for subject without waiting for PostgreSQL. It
// serves subjects that have no rate or concurrency limit and are only counted
// for the dashboard, so their requests never pay a database round trip.
func (s *Store) CountRequest(subject string) {
	if s == nil || subject == "" {
		return
	}
	s.counts.add(subject, 1, 0)
}

// AddTokens adds token usage to subject's window without waiting for PostgreSQL.
// Tokens are known only after a request completes, so a flush delay of about
// a second is immaterial next to the one-minute window.
func (s *Store) AddTokens(subject string, tokens int64) {
	if s == nil || subject == "" || tokens <= 0 {
		return
	}
	s.counts.add(subject, 0, tokens)
}

// Rates reads the cluster-wide windows of subjects, for the dashboard.
const weightedRatesSQL = `COALESCE(SUM(requests * CASE WHEN minute = $2 THEN 1 ELSE $3::double precision END), 0),
	COALESCE(SUM(tokens * CASE WHEN minute = $2 THEN 1 ELSE $3::double precision END), 0)`

func (s *Store) Rates(ctx context.Context, subjects []string) (map[string]Rate, error) {
	if !s.Available() {
		return nil, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, backgroundTimeout)
	defer cancel()
	now, err := s.databaseTime(ctx, s.db)
	if err != nil {
		return nil, err
	}
	minute, elapsed := minuteOf(now)
	weight := float64(windowMillis-elapsed) / float64(windowMillis)
	out := make(map[string]Rate, len(subjects))
	byKey := make(map[string]string, len(subjects))
	keys := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		key := subjectKey(subject)
		byKey[key] = subject
		keys = append(keys, key)
		out[subject] = Rate{}
	}
	data, err := json.Marshal(keys)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT subject, `+weightedRatesSQL+`
		FROM cluster_runtime_rates WHERE subject IN (SELECT jsonb_array_elements_text($1))
		AND minute BETWEEN $2 - 1 AND $2 AND expires_at > $4 GROUP BY subject`, data, minute, weight, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var rate Rate
		if err := rows.Scan(&key, &rate.RPM, &rate.TPM); err != nil {
			return nil, err
		}
		out[byKey[key]] = rate
	}
	return out, rows.Err()
}

// countBatcher accumulates asynchronously counted requests and tokens per
// subject and minute, and writes them in one pipeline per flush interval.
type countBatcher struct {
	store    *Store
	interval time.Duration

	mu      sync.Mutex
	pending map[countKey]*countDelta

	startOnce sync.Once
	stop      chan struct{}
	done      chan struct{}
	started   bool
	closeOnce sync.Once
}

type countKey struct {
	subject string
	minute  int64
}

type countDelta struct{ requests, tokens int64 }

func newCountBatcher(s *Store, interval time.Duration) *countBatcher {
	return &countBatcher{
		store:    s,
		interval: interval,
		pending:  make(map[countKey]*countDelta),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

func (b *countBatcher) add(subject string, requests, tokens int64) {
	b.startOnce.Do(func() {
		b.mu.Lock()
		b.started = true
		b.mu.Unlock()
		go b.run()
	})
	minute, _ := minuteOf(b.store.localTime())
	key := countKey{subject: subject, minute: minute}
	b.mu.Lock()
	delta := b.pending[key]
	if delta == nil {
		delta = &countDelta{}
		b.pending[key] = delta
	}
	delta.requests += requests
	delta.tokens += tokens
	b.mu.Unlock()
}

func (b *countBatcher) run() {
	defer close(b.done)
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()
	for {
		select {
		case <-b.stop:
			b.flush()
			return
		case <-ticker.C:
			b.flush()
		}
	}
}

// flush writes the accumulated deltas. When PostgreSQL is unavailable they are
// dropped: those requests and tokens are still in the node-local windows,
// which are what limits fall back to while PostgreSQL is down.
func (b *countBatcher) flush() {
	b.mu.Lock()
	batch := b.pending
	b.pending = make(map[countKey]*countDelta)
	b.mu.Unlock()
	if len(batch) == 0 || !b.store.Available() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), backgroundTimeout)
	defer cancel()
	type rateDelta struct {
		Subject  string `json:"subject"`
		Minute   int64  `json:"minute"`
		Requests int64  `json:"requests"`
		Tokens   int64  `json:"tokens"`
	}
	deltas := make([]rateDelta, 0, len(batch))
	for key, delta := range batch {
		deltas = append(deltas, rateDelta{subjectKey(key.subject), key.minute, delta.requests, delta.tokens})
	}
	data, err := json.Marshal(deltas)
	if err != nil {
		return
	}
	_, _ = b.store.db.ExecContext(ctx, `
		INSERT INTO cluster_runtime_rates (subject, minute, requests, tokens, expires_at)
		SELECT subject, minute, requests, tokens, $2 FROM
			jsonb_to_recordset($1) AS r(subject text, minute bigint, requests bigint, tokens bigint)
		ORDER BY subject, minute
		ON CONFLICT (subject, minute) DO UPDATE
		SET requests = cluster_runtime_rates.requests + EXCLUDED.requests,
		    tokens = cluster_runtime_rates.tokens + EXCLUDED.tokens,
		    expires_at = GREATEST(cluster_runtime_rates.expires_at, EXCLUDED.expires_at)`,
		data, b.store.localTime().Add(bucketTTL))
}

func (b *countBatcher) close() {
	b.closeOnce.Do(func() {
		close(b.stop)
		b.mu.Lock()
		started := b.started
		b.mu.Unlock()
		if started {
			<-b.done
		}
	})
}
