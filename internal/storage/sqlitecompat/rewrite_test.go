package sqlitecompat

import (
	"strings"
	"testing"
)

func TestRewriteSQL(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "now is fixed width UTC",
			input: `SELECT now()`,
			want:  `SELECT (strftime('%Y-%m-%dT%H:%M:%f','now') || '000000Z')`,
		},
		{
			name:  "cast to jsonb is dropped",
			input: `INSERT INTO audit_logs (changes) VALUES (?::jsonb)`,
			want:  `INSERT INTO audit_logs (changes) VALUES (?)`,
		},
		{
			name:  "multi word cast is dropped",
			input: `SELECT * FROM t WHERE a > ?::double precision`,
			want:  `SELECT * FROM t WHERE a > ?`,
		},
		{
			name:  "interval subtraction shifts the timestamp",
			input: `DELETE FROM user_session_tokens WHERE expires_at < now() - interval '1 day'`,
			want:  `DELETE FROM user_session_tokens WHERE expires_at < (strftime('%Y-%m-%dT%H:%M:%f', (strftime('%Y-%m-%dT%H:%M:%f','now') || '000000Z'), (-86400) || ' seconds') || '000000Z')`,
		},
		{
			name:  "make_interval addition",
			input: `SELECT * FROM t WHERE locked_until > now() + make_interval(secs => ?)`,
			want:  `SELECT * FROM t WHERE locked_until > (strftime('%Y-%m-%dT%H:%M:%f', (strftime('%Y-%m-%dT%H:%M:%f','now') || '000000Z'), (+(?)) || ' seconds') || '000000Z')`,
		},
		{
			name:  "extract epoch difference",
			input: `SELECT EXTRACT(EPOCH FROM (b - a))::bigint FROM t`,
			want:  `SELECT ((julianday(b) - julianday(a)) * 86400.0) FROM t`,
		},
		{
			name:  "least and greatest",
			input: `SELECT LEAST(10, GREATEST(0, x)) FROM t`,
			want:  `SELECT min(10, max(0, x)) FROM t`,
		},
		{
			name:  "row locks are dropped",
			input: `SELECT id FROM t WHERE id = ? FOR UPDATE`,
			want:  `SELECT id FROM t WHERE id = ?`,
		},
		{
			name:  "ilike becomes like",
			input: `SELECT id FROM t WHERE name ILIKE ?`,
			want:  `SELECT id FROM t WHERE name LIKE ?`,
		},
		{
			name:  "values alias constructor",
			input: `CROSS JOIN (VALUES ('a'), ('b')) AS p(code)`,
			want:  `CROSS JOIN (SELECT 'a' AS code UNION ALL SELECT 'b') AS p`,
		},
		{
			name:  "ddl types and default now",
			input: `CREATE TABLE IF NOT EXISTS t (id BIGSERIAL PRIMARY KEY, tenant_id UUID NOT NULL, payload JSONB, at TIMESTAMPTZ NOT NULL DEFAULT now(), blob BYTEA NOT NULL DEFAULT decode('', 'hex'))`,
			want:  `CREATE TABLE IF NOT EXISTS t (id INTEGER PRIMARY KEY AUTOINCREMENT, tenant_id TEXT NOT NULL, payload TEXT, at DATETIME NOT NULL DEFAULT ((strftime('%Y-%m-%dT%H:%M:%f','now') || '000000Z')), blob BLOB NOT NULL DEFAULT X'')`,
		},
		{
			name:  "covering index include clause is dropped",
			input: `CREATE INDEX IF NOT EXISTS idx ON t(a) INCLUDE (b, c)`,
			want:  `CREATE INDEX IF NOT EXISTS idx ON t(a)`,
		},
		{
			name:  "insert or ignore stays",
			input: `INSERT OR IGNORE INTO t(a) VALUES (?)`,
			want:  `INSERT OR IGNORE INTO t(a) VALUES (?)`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := RewriteSQL(testCase.input)
			if got != testCase.want {
				t.Fatalf("RewriteSQL(%q)\n got: %q\nwant: %q", testCase.input, got, testCase.want)
			}
		})
	}
}

func TestRewriteSQLKeepsStringLiterals(t *testing.T) {
	input := `INSERT INTO t(a) VALUES ('::notacast', 'now()', 'interval ''9 days''')`
	if got := RewriteSQL(input); got != input {
		t.Fatalf("string literals were rewritten:\n got: %q\nwant: %q", got, input)
	}
}

func TestSplitStatements(t *testing.T) {
	script := `
-- leading comment; with a semicolon
CREATE TABLE a (x TEXT);
INSERT INTO b(x) VALUES ('semi;colon');
/* block ; comment */
UPDATE c SET y = 'it''s; fine';
`
	statements := SplitStatements(script)
	if len(statements) != 3 {
		t.Fatalf("got %d statements, want 3: %q", len(statements), statements)
	}
	for _, statement := range statements {
		if strings.Contains(statement, "semi;colon") {
			continue
		}
	}
	if !strings.Contains(statements[2], "it''s; fine") {
		t.Fatalf("quoted semicolon split the statement: %q", statements[2])
	}
}
