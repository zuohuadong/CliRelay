// Package sqlitecompat adapts the runtime's PostgreSQL-oriented SQL to an
// embedded SQLite database.
//
// The runtime schema and most query text were written for PostgreSQL (see
// docs/postgres-redis-migration.md). This package keeps one schema definition
// and one set of queries authoritative while allowing a single-node deployment
// to run on an embedded SQLite file:
//
//   - rewrite.go translates the syntax SQLite cannot parse: interval
//     arithmetic, EXTRACT(EPOCH ...), LEAST/GREATEST, ::type casts, FOR UPDATE.
//   - driver.go registers a database/sql driver that rewrites statements and
//     normalises time.Time arguments to a fixed-width UTC layout.
//   - schema.go applies the PostgreSQL migration list to SQLite with type
//     translation, so there is no second schema to maintain by hand.
//
// Timestamps are stored as fixed-width UTC text ("2006-01-02T15:04:05.000000000Z").
// SQLite compares DATETIME as text, so lexical order must match chronological
// order: RFC3339Nano is variable width, and "…:05.5Z" sorts before "…:05Z".
// Every SQL expression this package emits produces that exact layout, as does
// every time.Time bound through the driver.
package sqlitecompat

import (
	"time"
)

const (
	// DriverName is the database/sql driver name this package registers.
	DriverName = "sqlitecompat"
	// TimeLayout is the fixed-width UTC layout used for every timestamp this
	// package writes and for every time.Time bound through the driver.
	TimeLayout = "2006-01-02T15:04:05.000000000Z"
)

func formatTime(t time.Time) string { return t.UTC().Format(TimeLayout) }

func nowUTC() time.Time { return time.Now().UTC() }
