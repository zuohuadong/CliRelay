package sqlitecompat

import (
	"regexp"
	"strconv"
	"strings"
)

// ddlStatementRE matches the statements whose column types need translation.
var ddlStatementRE = regexp.MustCompile(`(?is)^\s*(?:CREATE\s+(?:UNIQUE\s+)?(?:TABLE|INDEX)|ALTER\s+TABLE)\b`)

// ddlTypeRewrites maps PostgreSQL column types to SQLite ones. Order matters:
// longer and more specific patterns come first.
var ddlTypeRewrites = []struct{ old, new string }{
	{"BIGSERIAL PRIMARY KEY", "INTEGER PRIMARY KEY AUTOINCREMENT"},
	{"BIGSERIAL NOT NULL PRIMARY KEY", "INTEGER PRIMARY KEY AUTOINCREMENT"},
	{"SERIAL PRIMARY KEY", "INTEGER PRIMARY KEY AUTOINCREMENT"},
	{"TIMESTAMP WITH TIME ZONE", "DATETIME"},
	{"TIMESTAMPTZ", "DATETIME"},
	{"TIMESTAMP", "DATETIME"},
	{"DOUBLE PRECISION", "REAL"},
	{"BIGSERIAL", "INTEGER"},
	{"SERIAL", "INTEGER"},
	{"BIGINT", "INTEGER"},
	{"SMALLINT", "INTEGER"},
	{"JSONB", "TEXT"},
	{"UUID", "TEXT"},
	{"BYTEA", "BLOB"},
	{"NUMERIC", "REAL"},
	{"DECIMAL", "REAL"},
}

var (
	extractEpochDiffRE   = regexp.MustCompile(`(?is)extract\s*\(\s*epoch\s+from\s*\(\s*([^()]+?)\s*\)\s*\)`)
	extractEpochRE       = regexp.MustCompile(`(?is)extract\s*\(\s*epoch\s+from\s*([A-Za-z_][\w.]*)\s*\)`)
	intervalOpRE         = regexp.MustCompile(`(?is)(` + sqlOperand + `)\s*([+-])\s*interval\s*'([^']+)'`)
	makeIntervalOpRE     = regexp.MustCompile(`(?is)(` + sqlOperand + `)\s*([+-])\s*make_interval\s*\(\s*secs\s*=>\s*([^()]+?)\s*\)`)
	makeIntervalRE       = regexp.MustCompile(`(?is)make_interval\s*\(\s*secs\s*=>\s*([^()]+?)\s*\)`)
	intervalLiteralRE    = regexp.MustCompile(`(?is)interval\s*'([^']+)'`)
	tsDiffIntervalRE     = regexp.MustCompile(`(?is)\b([A-Za-z_][\w.]*)\s*-\s*([A-Za-z_][\w.]*)\s*(>=|<=|<>|!=|>|<|=)\s*interval\s*'([^']+)'`)
	nowCallRE            = regexp.MustCompile(`(?is)\bnow\s*\(\s*\)`)
	alterAddColumnRE     = regexp.MustCompile(`(?is)(ALTER\s+TABLE\s+\S+\s+ADD\s+COLUMN)\s+IF\s+NOT\s+EXISTS\s+`)
	defaultNowRE         = regexp.MustCompile(`(?is)DEFAULT\s+now\s*\(\s*\)`)
	decodeEmptyRE        = regexp.MustCompile(`(?is)DEFAULT\s+decode\s*\(\s*''\s*,\s*'hex'\s*\)`)
	forLockRE            = regexp.MustCompile(`(?is)\s+FOR\s+(?:NO\s+KEY\s+)?(?:UPDATE|SHARE)(?:\s+OF\s+[\w.,\s]+)?(?:\s+(?:NOWAIT|SKIP\s+LOCKED))?`)
	skipLockedRE         = regexp.MustCompile(`(?is)\s+SKIP\s+LOCKED`)
	leastRE              = regexp.MustCompile(`(?is)\bLEAST\s*\(`)
	greatestRE           = regexp.MustCompile(`(?is)\bGREATEST\s*\(`)
	ilikeRE              = regexp.MustCompile(`(?is)\bILIKE\b`)
	intervalUnitRE       = regexp.MustCompile(`(?is)^\s*([0-9]+(?:\.[0-9]+)?)\s*([a-z]+)\s*$`)
	indexIncludeRE       = regexp.MustCompile(`(?is)\s+INCLUDE\s*\([^)]*\)`)
	insertSelectUpsertRE = regexp.MustCompile(`(?is)^\s*INSERT\s+INTO\b.*?\bSELECT\b.*\bON\s+CONFLICT\b`)
	selectWhereRE        = regexp.MustCompile(`(?is)\bWHERE\b`)
	valuesAliasRE        = regexp.MustCompile(`(?is)\(\s*VALUES\s*((?:\([^()]*\)\s*,?\s*)+)\)\s*AS\s+(\w+)\s*\(\s*(\w+)\s*\)`)
	valuesRowRE          = regexp.MustCompile(`(?is)\(([^()]*)\)`)
)

// sqlOperand matches an expression this package can place inside a SQL
// function argument list: a plain column, a zero-argument call such as now(),
// a bound parameter, a closing parenthesis (so `(a + b) - interval '1 day'`
// works), or a string literal.
const sqlOperand = `now\s*\(\s*\)|[A-Za-z_][\w.]*\s*\(\s*\)|[A-Za-z_][\w.]*|\?|'[^']*'`

// nowExpr renders now() in the fixed-width UTC layout the driver writes.
const nowExpr = `(strftime('%Y-%m-%dT%H:%M:%f','now') || '000000Z')`

// addIntervalSQL shifts a timestamp by a number of seconds, keeping the
// fixed-width layout so text comparisons stay chronological.
func addIntervalSQL(timestamp, seconds string) string {
	return "(strftime('%Y-%m-%dT%H:%M:%f', " + timestamp + ", (" + seconds + ") || ' seconds') || '000000Z')"
}

// epochSQL renders a timestamp as (fractional) Unix seconds.
func epochSQL(timestamp string) string {
	return "((julianday(" + timestamp + ") - 2440587.5) * 86400.0)"
}

// operandSQL renders an expression that may appear as the timestamp argument
// of a generated call. now() is expanded here so the generated SQL does not
// nest two strftime calls.
func operandSQL(operand string) string {
	if nowCallRE.MatchString(operand) && nowCallRE.FindString(operand) == strings.TrimSpace(operand) {
		return nowExpr
	}
	return operand
}

// rewriteTimestampIntervalComparison turns `a - b >= interval '1 day'` into a
// numeric comparison of seconds, which SQLite can evaluate on text timestamps.
func rewriteTimestampIntervalComparison(query string) string {
	return tsDiffIntervalRE.ReplaceAllStringFunc(query, func(match string) string {
		parts := tsDiffIntervalRE.FindStringSubmatch(match)
		if len(parts) != 5 {
			return match
		}
		seconds, ok := intervalSeconds(parts[4])
		if !ok {
			return match
		}
		return epochDiffSQL(parts[1], parts[2]) + " " + parts[3] + " " + formatSeconds(seconds)
	})
}

// epochDiffSQL renders the difference of two timestamps in seconds.
func epochDiffSQL(left, right string) string {
	return "((julianday(" + left + ") - julianday(" + right + ")) * 86400.0)"
}

// RewriteSQL translates PostgreSQL-only syntax into the SQLite equivalent.
// It is safe to call on statements that are already SQLite-compatible.
func RewriteSQL(query string) string {
	if strings.TrimSpace(query) == "" {
		return query
	}
	if ddlStatementRE.MatchString(stripLeadingComments(query)) {
		query = rewriteDDL(query)
	}
	query = rewriteCasts(query)
	query = rewriteValuesAlias(query)
	query = rewriteInsertSelectUpsert(query)
	query = extractEpochDiffRE.ReplaceAllStringFunc(query, func(match string) string {
		parts := extractEpochDiffRE.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}
		left, right, ok := splitDifference(parts[1])
		if !ok {
			return epochSQL(parts[1])
		}
		return epochDiffSQL(left, right)
	})
	query = extractEpochRE.ReplaceAllStringFunc(query, func(match string) string {
		parts := extractEpochRE.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}
		return epochSQL(parts[1])
	})
	query = rewriteTimestampIntervalComparison(query)
	query = replaceIntervalArithmetic(query)
	query = makeIntervalOpRE.ReplaceAllStringFunc(query, func(match string) string {
		parts := makeIntervalOpRE.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match
		}
		return addIntervalSQL(operandSQL(parts[1]), parts[2]+"("+parts[3]+")")
	})
	query = makeIntervalRE.ReplaceAllString(query, "($1)")
	// Literals are masked from here on: the remaining patterns are plain
	// keywords that must not fire inside a string ('now()' in a message, for
	// instance). The timestamp rewrites above still need the literal text.
	query, literals := maskLiterals(query)
	// now() is last among the timestamp rewrites so the arithmetic forms above
	// can still recognise it as an operand.
	query = nowCallRE.ReplaceAllString(query, nowExpr)
	// A bare interval literal left over (compared or assigned on its own)
	// becomes its length in seconds.
	query = intervalLiteralRE.ReplaceAllStringFunc(query, func(match string) string {
		parts := intervalLiteralRE.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}
		seconds, ok := intervalSeconds(parts[1])
		if !ok {
			return match
		}
		return formatSeconds(seconds)
	})
	query = forLockRE.ReplaceAllString(query, "")
	query = skipLockedRE.ReplaceAllString(query, "")
	query = leastRE.ReplaceAllString(query, "min(")
	query = greatestRE.ReplaceAllString(query, "max(")
	query = ilikeRE.ReplaceAllString(query, "LIKE")
	return unmaskLiterals(query, literals)
}

// literalMask is the placeholder prefix used while rewriting. mongodb-style
// tokens keep the SQL parseable and cannot collide with identifiers.
const literalMask = "@@sqllit"

// maskLiterals replaces single-quoted string literals with placeholders so the
// expression rewrites only ever see code. The literal bodies are returned for
// unmaskLiterals to restore. Double-quoted identifiers are not masked: they
// never contain the constructs these rewrites look for.
func maskLiterals(query string) (string, []string) {
	var out strings.Builder
	out.Grow(len(query))
	var literals []string
	for i := 0; i < len(query); {
		if query[i] != '\'' {
			out.WriteByte(query[i])
			i++
			continue
		}
		start := i
		i++
		for i < len(query) {
			if query[i] != '\'' {
				i++
				continue
			}
			// A doubled quote escapes itself and keeps the literal open.
			if i+1 < len(query) && query[i+1] == '\'' {
				i += 2
				continue
			}
			i++
			break
		}
		literals = append(literals, query[start:i])
		out.WriteString(literalMask)
		out.WriteString(strconv.Itoa(len(literals) - 1))
		out.WriteString("@@")
	}
	return out.String(), literals
}

// unmaskLiterals restores the literals masked by maskLiterals.
func unmaskLiterals(query string, literals []string) string {
	if len(literals) == 0 {
		return query
	}
	for index, literal := range literals {
		query = strings.ReplaceAll(query, literalMask+strconv.Itoa(index)+"@@", literal)
	}
	return query
}

// stripLeadingComments drops whitespace, line comments and block comments
// before the first real token, so callers can classify a statement by its
// verb even when the migration annotates it.
func stripLeadingComments(query string) string {
	for {
		query = strings.TrimLeft(query, " \t\r\n")
		switch {
		case strings.HasPrefix(query, "--"):
			if idx := strings.IndexByte(query, '\n'); idx >= 0 {
				query = query[idx+1:]
				continue
			}
			return ""
		case strings.HasPrefix(query, "/*"):
			if idx := strings.Index(query, "*/"); idx >= 0 {
				query = query[idx+2:]
				continue
			}
			return ""
		default:
			return query
		}
	}
}

// rewriteInsertSelectUpsert disambiguates `INSERT ... SELECT ... ON CONFLICT`
// for SQLite, whose parser reads the ON as a join condition unless the SELECT
// carries a WHERE clause. Adding `WHERE true` is the documented workaround.
func rewriteInsertSelectUpsert(query string) string {
	if !insertSelectUpsertRE.MatchString(query) {
		return query
	}
	conflict := strings.Index(strings.ToUpper(query), "ON CONFLICT")
	if conflict < 0 {
		return query
	}
	if selectWhereRE.MatchString(query[:conflict]) {
		return query
	}
	return query[:conflict] + "WHERE true " + query[conflict:]
}

// rewriteValuesAlias turns PostgreSQL's `(VALUES (a), (b)) AS p(code)` table
// constructor into a union of single-row selects, which SQLite parses. Only
// single-column value lists are used in this codebase.
func rewriteValuesAlias(query string) string {
	return valuesAliasRE.ReplaceAllStringFunc(query, func(match string) string {
		parts := valuesAliasRE.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match
		}
		alias, column := parts[2], parts[3]
		rows := valuesRowRE.FindAllStringSubmatch(parts[1], -1)
		if len(rows) == 0 {
			return match
		}
		var out strings.Builder
		out.WriteString("(SELECT ")
		out.WriteString(strings.TrimSpace(rows[0][1]))
		out.WriteString(" AS ")
		out.WriteString(column)
		for _, row := range rows[1:] {
			out.WriteString(" UNION ALL SELECT ")
			out.WriteString(strings.TrimSpace(row[1]))
		}
		out.WriteString(") AS ")
		out.WriteString(alias)
		return out.String()
	})
}

// rewriteDDL translates PostgreSQL types and defaults. It runs before the
// expression rewrites so `DEFAULT now()` is still recognisable.
func rewriteDDL(query string) string {
	query = alterAddColumnRE.ReplaceAllString(query, "$1 ")
	// PostgreSQL covering indexes add their INCLUDE columns to the index.
	// SQLite has no equivalent, and dropping the clause keeps the same
	// lookups working with a narrower index.
	query = indexIncludeRE.ReplaceAllString(query, "")
	query = defaultNowRE.ReplaceAllString(query, "DEFAULT (now())")
	query = decodeEmptyRE.ReplaceAllString(query, "DEFAULT X''")
	// Replace types longest-first in column-type position: a plain
	// strings.Replace would rename the `timestamp` column of a table that also
	// declares a TIMESTAMPTZ type.
	for _, replacement := range ddlTypeRewrites {
		query = replaceColumnType(query, replacement.old, replacement.new)
	}
	// Structural clauses are matched anywhere.
	query = replaceWordSequence(query, "DEFERRABLE INITIALLY DEFERRED", "")
	query = replaceWordSequence(query, "CONCURRENTLY", "")
	return query
}

// typeFollowerTokens are the words that can legitimately follow a column type
// in a definition. Anything else means the matched word was a column name or a
// keyword in an expression, not a type.
var typeFollowerTokens = map[string]bool{
	"NOT": true, "NULL": true, "DEFAULT": true, "PRIMARY": true, "UNIQUE": true,
	"CHECK": true, "REFERENCES": true, "CONSTRAINT": true, "COLLATE": true,
	"GENERATED": true, "USING": true,
}

// replaceColumnType replaces old with new only where old is followed by a
// constraint, a separator, or the end of the column definition.
func replaceColumnType(query, old, new string) string {
	if old == "" {
		return query
	}
	upperQuery := strings.ToUpper(query)
	upperOld := strings.ToUpper(old)
	var out strings.Builder
	out.Grow(len(query))
	last := 0
	inSingle := false
	inDouble := false
	for i := 0; i+len(upperOld) <= len(upperQuery); i++ {
		ch := query[i]
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
		} else if ch == '"' && !inSingle {
			inDouble = !inDouble
		}
		if inSingle || inDouble {
			continue
		}
		if !strings.HasPrefix(upperQuery[i:], upperOld) {
			continue
		}
		if i > 0 && isIdentByte(query[i-1]) {
			continue
		}
		end := i + len(upperOld)
		if end < len(query) && isIdentByte(query[end]) {
			continue
		}
		if !isTypeFollower(query, end) {
			continue
		}
		out.WriteString(query[last:i])
		out.WriteString(new)
		last = end
		i = end - 1
	}
	if last == 0 {
		return query
	}
	out.WriteString(query[last:])
	return out.String()
}

func isTypeFollower(query string, end int) bool {
	i := end
	for i < len(query) && (query[i] == ' ' || query[i] == '\t' || query[i] == '\n' || query[i] == '\r') {
		i++
	}
	if i >= len(query) {
		return true
	}
	switch query[i] {
	case ',', ')', ';':
		return true
	}
	wordEnd := scanQueryWord(query, i)
	if wordEnd == i {
		return false
	}
	return typeFollowerTokens[strings.ToUpper(query[i:wordEnd])]
}

// replaceWordSequence replaces old (which may contain spaces) only where it
// appears as a whole word sequence outside of quoted text.
func replaceWordSequence(query, old, new string) string {
	if old == "" {
		return query
	}
	upperQuery := strings.ToUpper(query)
	upperOld := strings.ToUpper(old)
	var out strings.Builder
	out.Grow(len(query))
	last := 0
	inSingle := false
	inDouble := false
	for i := 0; i+len(upperOld) <= len(upperQuery); i++ {
		ch := query[i]
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
		} else if ch == '"' && !inSingle {
			inDouble = !inDouble
		}
		if inSingle || inDouble {
			continue
		}
		if !strings.HasPrefix(upperQuery[i:], upperOld) {
			continue
		}
		if i > 0 && isIdentByte(query[i-1]) {
			continue
		}
		end := i + len(upperOld)
		if end < len(query) && isIdentByte(query[end]) {
			continue
		}
		out.WriteString(query[last:i])
		out.WriteString(new)
		last = end
		i = end - 1
	}
	if last == 0 {
		return query
	}
	out.WriteString(query[last:])
	return out.String()
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// rewriteCasts drops `::type` casts and translates $N placeholders. SQLite has
// no cast syntax of that shape, and the underlying value already has the right
// affinity, so `?::jsonb` and `tenant_id::text` become `?` and `tenant_id`.
// `$N` becomes `?N`: both number their parameters, so a reused or out-of-order
// value keeps working.
func rewriteCasts(query string) string {
	var out strings.Builder
	out.Grow(len(query))
	inSingle := false
	inDouble := false
	for i := 0; i < len(query); i++ {
		ch := query[i]
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			out.WriteByte(ch)
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			out.WriteByte(ch)
			continue
		}
		if !inSingle && !inDouble && ch == ':' && i+1 < len(query) && query[i+1] == ':' {
			i = skipCastType(query, i+2) - 1
			continue
		}
		// PostgreSQL numbers its placeholders ($1, $2) so a value can be
		// reused; SQLite's ?NNN form has the same meaning, and plain "?"
		// does not, so translate rather than strip the number.
		if !inSingle && !inDouble && ch == '$' && i+1 < len(query) && query[i+1] >= '0' && query[i+1] <= '9' {
			out.WriteByte('?')
			continue
		}
		out.WriteByte(ch)
	}
	return out.String()
}

// skipCastType returns the index just past a PostgreSQL type name starting at
// start. Only the multi-word spellings PostgreSQL actually uses are consumed:
// an unrestricted scan would swallow the next keywords, turning
// `x::bigint FROM t` into `x`.
func skipCastType(query string, start int) int {
	i := skipQuerySpaces(query, start)
	wordStart := i
	i = scanQueryWord(query, i)
	if i == wordStart {
		return start
	}
	switch strings.ToUpper(query[wordStart:i]) {
	case "DOUBLE":
		i = consumeCastWord(query, i, "PRECISION")
	case "CHARACTER", "BIT":
		i = consumeCastWord(query, i, "VARYING")
	case "TIMESTAMP", "TIME":
		if afterWith := consumeCastWord(query, i, "WITH"); afterWith != i {
			afterWith = consumeCastWord(query, afterWith, "TIME")
			i = consumeCastWord(query, afterWith, "ZONE")
		}
	}
	// Precision/length modifiers, e.g. varchar(64).
	if i < len(query) && query[i] == '(' {
		for i < len(query) && query[i] != ')' {
			i++
		}
		if i < len(query) {
			i++
		}
	}
	for i+1 < len(query) && query[i] == '[' && query[i+1] == ']' {
		i += 2
	}
	return i
}

// scanQueryWord returns the index past an identifier starting at start.
func scanQueryWord(query string, start int) int {
	i := start
	for i < len(query) && isIdentByte(query[i]) {
		i++
	}
	return i
}

func skipQuerySpaces(query string, start int) int {
	i := start
	for i < len(query) && (query[i] == ' ' || query[i] == '\t' || query[i] == '\n' || query[i] == '\r') {
		i++
	}
	return i
}

// consumeCastWord consumes want (case-insensitive) after i and returns the new
// position, or i unchanged when the next word is different.
func consumeCastWord(query string, i int, want string) int {
	start := skipQuerySpaces(query, i)
	end := scanQueryWord(query, start)
	if end == start {
		return i
	}
	if !strings.EqualFold(query[start:end], want) {
		return i
	}
	return end
}

func replaceIntervalArithmetic(query string) string {
	return intervalOpRE.ReplaceAllStringFunc(query, func(match string) string {
		parts := intervalOpRE.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match
		}
		seconds, ok := intervalSeconds(parts[3])
		if !ok {
			return match
		}
		sign := ""
		if parts[2] == "-" {
			sign = "-"
		}
		return addIntervalSQL(operandSQL(parts[1]), sign+formatSeconds(seconds))
	})
}

// splitDifference splits "a - b" into its two operands. The inputs come from a
// parenthesised PostgreSQL expression, so a single top-level minus is the only
// shape the callers rely on.
func splitDifference(expression string) (string, string, bool) {
	depth := 0
	for i := 0; i < len(expression); i++ {
		switch expression[i] {
		case '(':
			depth++
		case ')':
			depth--
		case '-':
			if depth != 0 {
				continue
			}
			left := strings.TrimSpace(expression[:i])
			right := strings.TrimSpace(expression[i+1:])
			if left == "" || right == "" {
				return "", "", false
			}
			return left, right, true
		}
	}
	return "", "", false
}

var intervalUnits = map[string]float64{
	"second": 1, "seconds": 1, "sec": 1, "secs": 1, "s": 1,
	"minute": 60, "minutes": 60, "min": 60, "mins": 60,
	"hour": 3600, "hours": 3600, "h": 3600,
	"day": 86400, "days": 86400, "d": 86400,
	"week": 604800, "weeks": 604800, "w": 604800,
	// Months and years have no fixed length; 30/365 days matches the
	// granularity every caller of these literals uses (session and audit
	// windows measured in weeks).
	"month": 2592000, "months": 2592000,
	"year": 31536000, "years": 31536000,
}

// intervalSeconds converts a PostgreSQL interval literal body ("1 day",
// "30 minutes") to seconds.
func intervalSeconds(literal string) (float64, bool) {
	parts := intervalUnitRE.FindStringSubmatch(literal)
	if len(parts) != 3 {
		return 0, false
	}
	multiplier, ok := intervalUnits[strings.ToLower(parts[2])]
	if !ok {
		return 0, false
	}
	amount, err := strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return 0, false
	}
	return amount * multiplier, true
}

func formatSeconds(seconds float64) string {
	return strconv.FormatFloat(seconds, 'f', -1, 64)
}
