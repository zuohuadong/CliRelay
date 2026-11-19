package sqlitecompat

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"time"

	"modernc.org/sqlite"
)

func init() {
	sql.Register(DriverName, &Driver{})
}

// Driver opens SQLite connections whose statements are rewritten to tolerate
// the runtime's PostgreSQL-flavoured SQL. Everything else is delegated to
// modernc.org/sqlite.
type Driver struct{}

// Open implements driver.Driver.
func (d *Driver) Open(name string) (driver.Conn, error) {
	conn, err := (&sqlite.Driver{}).Open(name)
	if err != nil {
		return nil, err
	}
	return &connWrapper{Conn: conn}, nil
}

type connWrapper struct {
	driver.Conn
}

func (c *connWrapper) Prepare(query string) (driver.Stmt, error) {
	stmt, err := c.Conn.Prepare(RewriteSQL(query))
	if err != nil {
		return nil, err
	}
	return &stmtWrapper{Stmt: stmt}, nil
}

func (c *connWrapper) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	preparer, ok := c.Conn.(driver.ConnPrepareContext)
	if !ok {
		return c.Prepare(query)
	}
	stmt, err := preparer.PrepareContext(ctx, RewriteSQL(query))
	if err != nil {
		return nil, err
	}
	return &stmtWrapper{Stmt: stmt}, nil
}

func (c *connWrapper) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	execer, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	named, err := normalizeArgs(args)
	if err != nil {
		return nil, err
	}
	return execer.ExecContext(ctx, RewriteSQL(query), named)
}

func (c *connWrapper) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	queryer, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	named, err := normalizeArgs(args)
	if err != nil {
		return nil, err
	}
	return queryer.QueryContext(ctx, RewriteSQL(query), named)
}

func (c *connWrapper) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	beginner, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return c.Conn.Begin()
	}
	return beginner.BeginTx(ctx, opts)
}

func (c *connWrapper) Ping(ctx context.Context) error {
	if pinger, ok := c.Conn.(driver.Pinger); ok {
		return pinger.Ping(ctx)
	}
	return nil
}

func (c *connWrapper) CheckNamedValue(value *driver.NamedValue) error {
	return normalizeNamedValue(value)
}

type stmtWrapper struct {
	driver.Stmt
}

func (s *stmtWrapper) Exec(args []driver.Value) (driver.Result, error) {
	values, err := normalizeValues(args)
	if err != nil {
		return nil, err
	}
	return s.Stmt.Exec(values)
}

func (s *stmtWrapper) Query(args []driver.Value) (driver.Rows, error) {
	values, err := normalizeValues(args)
	if err != nil {
		return nil, err
	}
	return s.Stmt.Query(values)
}

func (s *stmtWrapper) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	execer, ok := s.Stmt.(driver.StmtExecContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	named, err := normalizeArgs(args)
	if err != nil {
		return nil, err
	}
	return execer.ExecContext(ctx, named)
}

func (s *stmtWrapper) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	queryer, ok := s.Stmt.(driver.StmtQueryContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	named, err := normalizeArgs(args)
	if err != nil {
		return nil, err
	}
	return queryer.QueryContext(ctx, named)
}

func (s *stmtWrapper) CheckNamedValue(value *driver.NamedValue) error {
	return normalizeNamedValue(value)
}

// normalizeNamedValue pins time.Time arguments to the fixed-width UTC layout
// the schema stores, so text comparisons against them stay chronological.
// Timestamps that arrive as strings (the usage spool serialises records to
// JSON and replays the timestamp as text) are re-formatted the same way.
func normalizeNamedValue(value *driver.NamedValue) error {
	if value == nil {
		return nil
	}
	if ts, ok := value.Value.(time.Time); ok {
		value.Value = formatTime(ts)
		return nil
	}
	if text, ok := value.Value.(string); ok {
		if normalized, changed := normalizeTimestampString(text); changed {
			value.Value = normalized
			return nil
		}
	}
	converted, err := driver.DefaultParameterConverter.ConvertValue(value.Value)
	if err != nil {
		return err
	}
	value.Value = converted
	return nil
}

// normalizeTimestampString rewrites an RFC3339 timestamp string to the layout
// the schema stores. Only strings that are unambiguously RFC3339 timestamps are
// touched, so ordinary text passes through unchanged.
func normalizeTimestampString(text string) (string, bool) {
	if len(text) < 20 || len(text) > 40 {
		return text, false
	}
	if text[4] != '-' || text[7] != '-' || (text[10] != 'T' && text[10] != ' ') {
		return text, false
	}
	if !strings.HasSuffix(text, "Z") && !strings.ContainsAny(text[19:], "+-") {
		return text, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		parsed, err = time.Parse("2006-01-02 15:04:05.999999999Z07:00", text)
	}
	if err != nil {
		return text, false
	}
	return formatTime(parsed), true
}

func normalizeArgs(args []driver.NamedValue) ([]driver.NamedValue, error) {
	for i := range args {
		if err := normalizeNamedValue(&args[i]); err != nil {
			return nil, err
		}
	}
	return args, nil
}

func normalizeValues(args []driver.Value) ([]driver.Value, error) {
	for i, value := range args {
		named := driver.NamedValue{Ordinal: i + 1, Value: value}
		if err := normalizeNamedValue(&named); err != nil {
			return nil, err
		}
		args[i] = named.Value
	}
	return args, nil
}
