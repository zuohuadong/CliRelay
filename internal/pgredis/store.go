package pgredis

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Querier is the slice of database/sql a Store needs. *sql.DB, *sql.Tx and
// *sql.Conn all satisfy it, so a Store can share the connection pool the rest
// of the application already holds.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Options configures a Store. The zero value is usable.
type Options struct {
	// Namespace separates two applications sharing one database. It is the
	// first column of every table and the first argument of every statement,
	// so nothing leaks between namespaces.
	Namespace string
	// TablePrefix prefixes every table name, default "pgredis".
	TablePrefix string
	// Logged keeps the tables WAL-backed instead of UNLOGGED. Only worth it
	// for state that must survive a crash (the queue does this by itself).
	Logged bool
	// Now overrides the clock used for the few decisions the database cannot
	// make on its own (lease renewal bookkeeping in tests).
	Now func() time.Time
}

// Store is the process handle on the PostgreSQL-backed primitives. It holds no
// state of its own: every method is one round trip, so two processes sharing a
// database share the state without coordination.
type Store struct {
	q         Querier
	namespace string
	tables    tables
	logged    bool
	now       func() time.Time
}

type tables struct {
	kv        string
	counter   string
	zset      string
	rateLimit string
	queue     string
}

func (s *Store) ns() string { return s.namespace }
func (s *Store) clock() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// New builds a Store. It does not touch the database; call EnsureSchema once
// per process before the first operation.
func New(q Querier, opts Options) *Store {
	prefix := strings.TrimSpace(opts.TablePrefix)
	if prefix == "" {
		prefix = "pgredis"
	}
	namespace := strings.TrimSpace(opts.Namespace)
	if namespace == "" {
		namespace = "default"
	}
	return &Store{
		q:         q,
		namespace: namespace,
		logged:    opts.Logged,
		now:       opts.Now,
		tables: tables{
			kv:        pgRedisTable(prefix, "kv"),
			counter:   pgRedisTable(prefix, "counter"),
			zset:      pgRedisTable(prefix, "zset"),
			rateLimit: pgRedisTable(prefix, "rate_limit"),
			queue:     pgRedisTable(prefix, "queue"),
		},
	}
}

// EnsureSchema creates every table the Store uses. It is idempotent and safe
// to run concurrently from several nodes.
func (s *Store) EnsureSchema(ctx context.Context) error {
	persistence := "UNLOGGED "
	if s.logged {
		persistence = ""
	}
	// The queue is WAL-backed regardless: a job accepted from a caller must
	// survive a crash, which is the whole point of moving it into the database.
	statements := []string{
		`CREATE ` + persistence + `TABLE IF NOT EXISTS ` + s.tables.kv + ` (
			namespace TEXT NOT NULL,
			key TEXT NOT NULL,
			value JSONB NOT NULL,
			expires_at TIMESTAMPTZ,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (namespace, key)
		)`,
		`CREATE INDEX IF NOT EXISTS ` + indexName(s.tables.kv, "expires") + ` ON ` + s.tables.kv + ` (expires_at)
			WHERE expires_at IS NOT NULL`,
		`CREATE ` + persistence + `TABLE IF NOT EXISTS ` + s.tables.counter + ` (
			namespace TEXT NOT NULL,
			key TEXT NOT NULL,
			value BIGINT NOT NULL DEFAULT 0,
			expires_at TIMESTAMPTZ,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (namespace, key)
		)`,
		`CREATE ` + persistence + `TABLE IF NOT EXISTS ` + s.tables.zset + ` (
			namespace TEXT NOT NULL,
			key TEXT NOT NULL,
			member TEXT NOT NULL,
			score DOUBLE PRECISION NOT NULL,
			expires_at TIMESTAMPTZ,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (namespace, key, member)
		)`,
		`CREATE INDEX IF NOT EXISTS ` + indexName(s.tables.zset, "score") + ` ON ` + s.tables.zset + `
			(namespace, key, score, member)`,
		`CREATE INDEX IF NOT EXISTS ` + indexName(s.tables.zset, "expires") + ` ON ` + s.tables.zset + ` (expires_at)
			WHERE expires_at IS NOT NULL`,
		`CREATE ` + persistence + `TABLE IF NOT EXISTS ` + s.tables.rateLimit + ` (
			namespace TEXT NOT NULL,
			key TEXT NOT NULL,
			window_start BIGINT NOT NULL,
			count BIGINT NOT NULL DEFAULT 0,
			blocked_until TIMESTAMPTZ,
			expires_at TIMESTAMPTZ,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (namespace, key, window_start)
		)`,
		`CREATE TABLE IF NOT EXISTS ` + s.tables.queue + ` (
			id BIGSERIAL PRIMARY KEY,
			namespace TEXT NOT NULL,
			queue TEXT NOT NULL,
			kind TEXT NOT NULL,
			payload JSONB NOT NULL,
			attempts INT NOT NULL DEFAULT 0,
			run_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			locked_until TIMESTAMPTZ,
			locked_by TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS ` + indexName(s.tables.queue, "ready") + ` ON ` + s.tables.queue + `
			(namespace, queue, run_at, id)`,
	}
	for _, statement := range statements {
		if _, err := s.q.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("pgredis: ensure schema: %w", err)
		}
	}
	return nil
}

// Tables reports the fully qualified table names, for migrations and
// operational tooling.
func (s *Store) Tables() map[string]string {
	return map[string]string{
		"kv":         s.tables.kv,
		"counter":    s.tables.counter,
		"zset":       s.tables.zset,
		"rate_limit": s.tables.rateLimit,
		"queue":      s.tables.queue,
	}
}

// pgRedisTable builds a table name from prefix and suffix. Both are validated
// to keep identifiers free of quoting tricks; the names are not user input at
// runtime, but a typo should fail loudly rather than reach the server.
func pgRedisTable(prefix, suffix string) string {
	return quoteIdentifier(prefix+"_"+suffix, "table")
}

// indexName derives an index name from a quoted table name. PostgreSQL
// truncates identifiers past 63 bytes, so the suffix is what has to survive.
func indexName(quotedTable, suffix string) string {
	base := strings.Trim(quotedTable, `"`)
	name := base + "_" + suffix
	if len(name) > 63 {
		name = name[:63]
	}
	return `"` + name + `"`
}

func quoteIdentifier(name, kind string) string {
	for _, r := range name {
		if r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			continue
		}
		panic("pgredis: invalid " + kind + " name: " + name)
	}
	if name == "" {
		panic("pgredis: empty " + kind + " name")
	}
	return `"` + name + `"`
}
