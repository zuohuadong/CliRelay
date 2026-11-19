package sqlitecompat

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	log "github.com/sirupsen/logrus"
)

// Migration is one versioned schema step. The runtime migration list in
// internal/storage/postgres is written for PostgreSQL; ApplyMigrations runs
// the same list against SQLite.
type Migration struct {
	Version string
	SQL     string
}

var (
	addPrimaryKeyRE  = regexp.MustCompile(`(?is)^\s*ALTER\s+TABLE\s+(\w+)\s+ADD\s+(?:CONSTRAINT\s+(\w+)\s+)?PRIMARY\s+KEY\s*\(([^)]*)\)\s*;?\s*$`)
	dropConstraintRE = regexp.MustCompile(`(?is)^\s*ALTER\s+TABLE\s+\w+\s+DROP\s+CONSTRAINT\b`)
	addConstraintRE  = regexp.MustCompile(`(?is)^\s*ALTER\s+TABLE\s+\w+\s+ADD\s+(?:CONSTRAINT\s+\w+\s+)?(?:FOREIGN\s+KEY|CHECK)\b`)
	duplicateColumn  = regexp.MustCompile(`(?is)duplicate column name`)
	alreadyExistsRE  = regexp.MustCompile(`(?is)already exists`)

	// PostgreSQL accepts several ADD COLUMN clauses in one ALTER TABLE.
	multiAddColumnRE      = regexp.MustCompile(`(?is)^\s*ALTER\s+TABLE\s+(\S+)\s+(ADD\s+COLUMN\b.+)$`)
	multiAddColumnSplitRE = regexp.MustCompile(`(?is),\s*ADD\s+COLUMN\s+`)
)

// untranslatableStatements are PostgreSQL statements without a SQLite
// equivalent that this port does not emulate. They are recorded as skipped
// instead of aborting startup: a fresh single-node install has no data for
// them to migrate, and refusing to boot would be worse than a loud warning.
var untranslatableStatements = []struct {
	re     *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(`(?is)\bDISTINCT\s+ON\s*\(`), "SELECT DISTINCT ON"},
	{regexp.MustCompile(`(?is)^\s*UPDATE\s+[\w."]+\s+(?:AS\s+)?\w+\s+SET\b`), "UPDATE with a table alias"},
	{regexp.MustCompile(`(?is)^\s*DELETE\s+FROM\s+\w+\s+\w+\s+USING\b`), "DELETE ... USING"},
}

const schemaMigrationsSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version    TEXT PRIMARY KEY,
  applied_at DATETIME NOT NULL
);
`

// ApplyMigrations brings db up to the given migration list, recording applied
// versions so restarts skip them. Statements SQLite cannot express are
// translated when an equivalent exists and skipped (with a log line) when it
// does not; the PostgreSQL check constraints and foreign keys they carry are
// either already part of CREATE TABLE or unenforced by a single-node SQLite
// deployment.
func ApplyMigrations(ctx context.Context, db *sql.DB, migrations []Migration) error {
	if db == nil {
		return fmt.Errorf("sqlitecompat: nil database")
	}
	if _, err := db.ExecContext(ctx, schemaMigrationsSQL); err != nil {
		return fmt.Errorf("sqlitecompat: create schema_migrations: %w", err)
	}
	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return err
	}
	for _, migration := range migrations {
		if _, ok := applied[migration.Version]; ok {
			continue
		}
		if err := applyMigration(ctx, db, migration); err != nil {
			return fmt.Errorf("sqlitecompat: migration %s: %w", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			migration.Version, formatTime(nowUTC()),
		); err != nil {
			return fmt.Errorf("sqlitecompat: record migration %s: %w", migration.Version, err)
		}
	}
	return nil
}

func appliedVersions(ctx context.Context, db *sql.DB) (map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("sqlitecompat: read schema_migrations: %w", err)
	}
	defer rows.Close()
	applied := make(map[string]struct{})
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("sqlitecompat: scan schema_migrations: %w", err)
		}
		applied[version] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlitecompat: iterate schema_migrations: %w", err)
	}
	return applied, nil
}

func applyMigration(ctx context.Context, db *sql.DB, migration Migration) error {
	for _, statement := range SplitStatements(migration.SQL) {
		if err := executeStatement(ctx, db, migration.Version, statement); err != nil {
			return err
		}
	}
	return nil
}

func executeStatement(ctx context.Context, db *sql.DB, version, statement string) error {
	trimmed := strings.TrimSpace(statement)
	if trimmed == "" {
		return nil
	}
	// Migration statements carry explanatory comments; classify by the code.
	code := stripLeadingComments(trimmed)
	if code == "" {
		return nil
	}
	switch {
	case dropConstraintRE.MatchString(code):
		// SQLite cannot drop a constraint. The composite primary keys these
		// statements remove are re-added below as unique indexes.
		return nil
	case addConstraintRE.MatchString(code):
		log.Debugf("sqlitecompat: skipping unenforceable constraint in %s: %s", version, firstLine(code))
		return nil
	}
	for _, unsupported := range untranslatableStatements {
		if !unsupported.re.MatchString(code) {
			continue
		}
		log.Warnf("sqlitecompat: skipping %s in migration %s; SQLite has no equivalent: %s",
			unsupported.reason, version, firstLine(code))
		return nil
	}
	if statements := splitMultiColumnAdd(code); len(statements) > 0 {
		for _, single := range statements {
			if err := executeStatement(ctx, db, version, single); err != nil {
				return err
			}
		}
		return nil
	}
	if match := addPrimaryKeyRE.FindStringSubmatch(code); match != nil {
		// A composite primary key becomes a unique index: it enforces the same
		// uniqueness and satisfies the ON CONFLICT (a, b) targets the runtime
		// SQL uses for upserts.
		table := match[1]
		name := strings.TrimSpace(match[2])
		if name == "" {
			name = table + "_pkey"
		}
		stmt := fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s(%s)", name, table, match[3])
		_, err := db.ExecContext(ctx, stmt)
		if err != nil && alreadyExistsRE.MatchString(err.Error()) {
			return nil
		}
		return err
	}
	_, err := db.ExecContext(ctx, RewriteSQL(trimmed))
	if err == nil {
		return nil
	}
	// PostgreSQL's ADD COLUMN IF NOT EXISTS is idempotent; SQLite's is not.
	if duplicateColumn.MatchString(err.Error()) {
		log.Debugf("sqlitecompat: column already present in %s: %v", version, err)
		return nil
	}
	if alreadyExistsRE.MatchString(err.Error()) {
		log.Debugf("sqlitecompat: object already exists in %s: %v", version, err)
		return nil
	}
	return fmt.Errorf("%w (statement: %s)", err, firstLine(trimmed))
}

// splitMultiColumnAdd rewrites one ALTER TABLE with several ADD COLUMN
// clauses into one ALTER per column, which is all SQLite accepts.
func splitMultiColumnAdd(code string) []string {
	match := multiAddColumnRE.FindStringSubmatch(code)
	if match == nil {
		return nil
	}
	table := match[1]
	parts := multiAddColumnSplitRE.Split(match[2], -1)
	if len(parts) < 2 {
		return nil
	}
	statements := make([]string, 0, len(parts))
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if i > 0 {
			part = "ADD COLUMN " + part
		}
		if !strings.HasPrefix(strings.ToUpper(part), "ADD COLUMN") {
			return nil
		}
		statements = append(statements, "ALTER TABLE "+table+" "+part)
	}
	return statements
}

func firstLine(statement string) string {
	if idx := strings.IndexByte(statement, '\n'); idx >= 0 {
		return strings.TrimSpace(statement[:idx])
	}
	return statement
}

// SplitStatements splits a migration script on semicolons that are outside
// string literals, quoted identifiers, dollar-quoted blocks and comments.
func SplitStatements(script string) []string {
	var statements []string
	current := strings.Builder{}
	inSingle := false
	inDouble := false
	inLineComment := false
	inBlockComment := false
	dollarTag := ""

	for i := 0; i < len(script); i++ {
		ch := script[i]
		if inLineComment {
			if ch == '\n' {
				inLineComment = false
			}
			current.WriteByte(ch)
			continue
		}
		if inBlockComment {
			if ch == '*' && i+1 < len(script) && script[i+1] == '/' {
				inBlockComment = false
				current.WriteString("*/")
				i++
				continue
			}
			current.WriteByte(ch)
			continue
		}
		if dollarTag != "" {
			if strings.HasPrefix(script[i:], dollarTag) {
				current.WriteString(dollarTag)
				i += len(dollarTag) - 1
				dollarTag = ""
				continue
			}
			current.WriteByte(ch)
			continue
		}
		if inSingle {
			current.WriteByte(ch)
			if ch == '\'' {
				// Doubled quotes escape themselves.
				if i+1 < len(script) && script[i+1] == '\'' {
					current.WriteByte('\'')
					i++
					continue
				}
				inSingle = false
			}
			continue
		}
		if inDouble {
			current.WriteByte(ch)
			if ch == '"' {
				inDouble = false
			}
			continue
		}
		switch {
		case ch == '-' && i+1 < len(script) && script[i+1] == '-':
			inLineComment = true
			current.WriteString("--")
			i++
		case ch == '/' && i+1 < len(script) && script[i+1] == '*':
			inBlockComment = true
			current.WriteString("/*")
			i++
		case ch == '\'':
			inSingle = true
			current.WriteByte(ch)
		case ch == '"':
			inDouble = true
			current.WriteByte(ch)
		case ch == '$':
			if tag, length := dollarQuoteTag(script[i:]); length > 0 {
				dollarTag = tag
				current.WriteString(tag)
				i += length - 1
			} else {
				current.WriteByte(ch)
			}
		case ch == ';':
			statements = append(statements, current.String())
			current.Reset()
		default:
			current.WriteByte(ch)
		}
	}
	if strings.TrimSpace(current.String()) != "" {
		statements = append(statements, current.String())
	}
	return statements
}

func dollarQuoteTag(rest string) (string, int) {
	if len(rest) < 2 || rest[0] != '$' {
		return "", 0
	}
	for i := 1; i < len(rest); i++ {
		switch {
		case rest[i] == '$':
			return rest[:i+1], i + 1
		case isIdentByte(rest[i]):
		default:
			return "", 0
		}
	}
	return "", 0
}
