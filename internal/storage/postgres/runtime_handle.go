package postgres

import (
	"database/sql"
	"sync/atomic"
)

var runtimeHandle atomic.Pointer[sql.DB]

// SetRuntimeHandle publishes the process runtime pool to packages that need
// PostgreSQL-backed runtime primitives without importing the usage package.
func SetRuntimeHandle(db *sql.DB) {
	runtimeHandle.Store(db)
}

// RuntimeHandle returns the process runtime pool, if one is active.
func RuntimeHandle() *sql.DB {
	return runtimeHandle.Load()
}
