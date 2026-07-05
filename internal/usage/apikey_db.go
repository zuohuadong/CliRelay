package usage

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

var (
	timeMinute = time.Minute
	timeNowUTC = func() time.Time { return time.Now().UTC() }
)

type APIKeyRow struct {
	Key                  string   `json:"key"`
	Name                 string   `json:"name,omitempty"`
	Disabled             bool     `json:"disabled,omitempty"`
	DailyLimit           int      `json:"daily-limit,omitempty"`
	TotalQuota           int      `json:"total-quota,omitempty"`
	SpendingLimit        float64  `json:"spending-limit,omitempty"`
	MonthlySpendingLimit float64  `json:"monthly-spending-limit,omitempty"`
	BillingCycleAnchor   string   `json:"billing-cycle-anchor,omitempty"`
	ConcurrencyLimit     int      `json:"concurrency-limit,omitempty"`
	RPMLimit             int      `json:"rpm-limit,omitempty"`
	TPMLimit             int      `json:"tpm-limit,omitempty"`
	AllowedModels        []string `json:"allowed-models,omitempty"`
	AllowedChannels      []string `json:"allowed-channels,omitempty"`
	AllowedChannelGroups []string `json:"allowed-channel-groups,omitempty"`
	PermissionProfileID  string   `json:"permission-profile-id,omitempty"`
	SystemPrompt         string   `json:"system-prompt,omitempty"`
	CreatedAt            string   `json:"created-at,omitempty"`
}

var apiKeysTableColumns = []struct {
	name       string
	definition string
}{
	{"name", "text not null default ''"},
	{"disabled", "integer not null default 0"},
	{"daily_limit", "integer not null default 0"},
	{"total_quota", "integer not null default 0"},
	{"spending_limit", "real not null default 0"},
	{"monthly_spending_limit", "real not null default 0"},
	{"billing_cycle_anchor", "text not null default ''"},
	{"concurrency_limit", "integer not null default 0"},
	{"rpm_limit", "integer not null default 0"},
	{"tpm_limit", "integer not null default 0"},
	{"allowed_models", "text not null default '[]'"},
	{"allowed_channels", "text not null default '[]'"},
	{"allowed_channel_groups", "text not null default '[]'"},
	{"system_prompt", "text not null default ''"},
	{"created_at", "text not null default ''"},
	{"updated_at", "text not null default ''"},
	{"permission_profile_id", "text not null default ''"},
}

// EnsureAPIKeysTable creates and migrates the management API key table.
func EnsureAPIKeysTable(db *sql.DB) {
	initAPIKeysTable(db)
}

func initAPIKeysTable(db *sql.DB) {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS api_keys (
		key text not null primary key,
		name text not null default '',
		disabled integer not null default 0,
		daily_limit integer not null default 0,
		total_quota integer not null default 0,
		spending_limit real not null default 0,
		monthly_spending_limit real not null default 0,
		billing_cycle_anchor text not null default '',
		concurrency_limit integer not null default 0,
		rpm_limit integer not null default 0,
		tpm_limit integer not null default 0,
		allowed_models text not null default '[]',
		allowed_channels text not null default '[]',
		allowed_channel_groups text not null default '[]',
		system_prompt text not null default '',
		created_at text not null default '',
		updated_at text not null default '',
		permission_profile_id text not null default ''
	)`); err != nil {
		log.Errorf("usage: create api_keys table: %v", err)
	}
	for _, column := range apiKeysTableColumns {
		if apiKeysDBHasColumn(db, column.name) {
			continue
		}
		if _, err := db.Exec(`alter table api_keys add column ` + column.name + ` ` + column.definition); err != nil {
			log.Errorf("usage: add %s column: %v", column.name, err)
		}
	}
}

func apiKeysDBHasColumn(db *sql.DB, column string) bool {
	rows, err := db.Query("pragma table_info(api_keys)")
	if err != nil {
		return false
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue any
		if errScan := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); errScan != nil {
			return false
		}
		if strings.EqualFold(name, column) {
			return true
		}
	}
	return false
}

func ListAPIKeys() []APIKeyRow {
	db := getDB()
	if db == nil {
		return nil
	}
	initAPIKeysTable(db)

	selectPermissionProfile := "''"
	if apiKeysDBHasColumn(db, "permission_profile_id") {
		selectPermissionProfile = "permission_profile_id"
	}

	rows, err := db.Query(`select key, name, disabled, daily_limit, total_quota, spending_limit, monthly_spending_limit, billing_cycle_anchor, concurrency_limit, rpm_limit, tpm_limit, allowed_models, allowed_channels, allowed_channel_groups, system_prompt, created_at, ` + selectPermissionProfile + ` from api_keys order by created_at, key`)
	if err != nil {
		log.Errorf("usage: list api keys: %v", err)
		return nil
	}
	defer func() { _ = rows.Close() }()

	var result []APIKeyRow
	for rows.Next() {
		var r APIKeyRow
		var disabled int
		var modelsRaw, channelsRaw, groupsRaw string
		if errScan := rows.Scan(
			&r.Key, &r.Name, &disabled,
			&r.DailyLimit, &r.TotalQuota, &r.SpendingLimit,
			&r.MonthlySpendingLimit, &r.BillingCycleAnchor,
			&r.ConcurrencyLimit, &r.RPMLimit, &r.TPMLimit,
			&modelsRaw, &channelsRaw, &groupsRaw,
			&r.SystemPrompt, &r.CreatedAt, &r.PermissionProfileID,
		); errScan != nil {
			log.Errorf("usage: scan api key row: %v", errScan)
			continue
		}
		r.Key = strings.TrimSpace(r.Key)
		if r.Key == "" {
			continue
		}
		r.Disabled = disabled != 0
		r.AllowedModels = decodeJSONStringList(modelsRaw)
		r.AllowedChannels = decodeJSONStringList(channelsRaw)
		r.AllowedChannelGroups = decodeJSONStringList(groupsRaw)
		result = append(result, r)
	}
	return result
}

func APIKeyUsageToday(apiKey string) (requestCount int, tokenCount int, cost float64) {
	db := getDB()
	if db == nil {
		return 0, 0, 0
	}
	today := timeNowUTC().Format("2006-01-02")
	err := db.QueryRow(`
		SELECT COALESCE(COUNT(*),0), COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost),0)
		FROM request_logs
		WHERE api_key = ? AND timestamp >= ? AND failed = 0`,
		strings.TrimSpace(apiKey), today,
	).Scan(&requestCount, &tokenCount, &cost)
	if err != nil && err != sql.ErrNoRows {
		log.Errorf("usage: query daily usage for api key: %v", err)
	}
	return
}

func APIKeyTotalUsage(apiKey string) (requestCount int, tokenCount int, cost float64) {
	db := getDB()
	if db == nil {
		return 0, 0, 0
	}
	err := db.QueryRow(`
		SELECT COALESCE(COUNT(*),0), COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost),0)
		FROM request_logs
		WHERE api_key = ? AND failed = 0`,
		strings.TrimSpace(apiKey),
	).Scan(&requestCount, &tokenCount, &cost)
	if err != nil && err != sql.ErrNoRows {
		log.Errorf("usage: query total usage for api key: %v", err)
	}
	return
}

func APIKeyUsageBetween(apiKey string, start, end time.Time) (requestCount int, tokenCount int, cost float64) {
	db := getDB()
	if db == nil {
		return 0, 0, 0
	}
	start = start.UTC()
	end = end.UTC()
	if end.IsZero() || !end.After(start) {
		end = timeNowUTC()
	}
	err := db.QueryRow(`
		SELECT COALESCE(COUNT(*),0), COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost),0)
		FROM request_logs
		WHERE api_key = ? AND timestamp >= ? AND timestamp < ? AND failed = 0`,
		strings.TrimSpace(apiKey), start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano),
	).Scan(&requestCount, &tokenCount, &cost)
	if err != nil && err != sql.ErrNoRows {
		log.Errorf("usage: query billing-cycle usage for api key: %v", err)
	}
	return
}

func CurrentMonthlyBillingCycle(anchorRaw string, now time.Time) (time.Time, time.Time) {
	now = now.UTC()
	if now.IsZero() {
		now = timeNowUTC()
	}
	anchor, err := parseBillingCycleAnchor(anchorRaw)
	if err != nil || anchor.IsZero() {
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 1, 0)
	}
	if now.Before(anchor) {
		return anchor, addMonthsClamped(anchor, 1)
	}
	months := (now.Year()-anchor.Year())*12 + int(now.Month()-anchor.Month())
	start := addMonthsClamped(anchor, months)
	for start.After(now) {
		months--
		start = addMonthsClamped(anchor, months)
	}
	end := addMonthsClamped(anchor, months+1)
	for !end.After(now) {
		months++
		start = end
		end = addMonthsClamped(anchor, months+1)
	}
	return start, end
}

func parseBillingCycleAnchor(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, sql.ErrNoRows
	}
	if ts, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return ts.UTC(), nil
	}
	if ts, err := time.Parse(time.RFC3339, raw); err == nil {
		return ts.UTC(), nil
	}
	if ts, err := time.Parse("2006-01-02T15:04", raw); err == nil {
		return ts.UTC(), nil
	}
	if ts, err := time.Parse("2006-01-02", raw); err == nil {
		return ts.UTC(), nil
	}
	return time.Time{}, sql.ErrNoRows
}

func addMonthsClamped(anchor time.Time, months int) time.Time {
	anchor = anchor.UTC()
	year := anchor.Year()
	month := int(anchor.Month()) + months
	for month > 12 {
		year++
		month -= 12
	}
	for month < 1 {
		year--
		month += 12
	}
	day := anchor.Day()
	maxDay := daysInMonth(year, time.Month(month))
	if day > maxDay {
		day = maxDay
	}
	return time.Date(year, time.Month(month), day, anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(), time.UTC)
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func APIKeyRPMCount(apiKey string) int {
	db := getDB()
	if db == nil {
		return 0
	}
	oneMinuteAgo := timeNowUTC().Add(-1 * timeMinute).Format(time.RFC3339Nano)
	var count int
	err := db.QueryRow(`
		SELECT COALESCE(COUNT(*),0)
		FROM request_logs
		WHERE api_key = ? AND timestamp >= ?`,
		strings.TrimSpace(apiKey), oneMinuteAgo,
	).Scan(&count)
	if err != nil && err != sql.ErrNoRows {
		log.Errorf("usage: query rpm count for api key: %v", err)
	}
	return count
}

func APIKeyTPMCount(apiKey string) int {
	db := getDB()
	if db == nil {
		return 0
	}
	oneMinuteAgo := timeNowUTC().Add(-1 * timeMinute).Format(time.RFC3339Nano)
	var count int
	err := db.QueryRow(`
		SELECT COALESCE(SUM(total_tokens),0)
		FROM request_logs
		WHERE api_key = ? AND timestamp >= ?`,
		strings.TrimSpace(apiKey), oneMinuteAgo,
	).Scan(&count)
	if err != nil && err != sql.ErrNoRows {
		log.Errorf("usage: query tpm count for api key: %v", err)
	}
	return count
}

func decodeJSONStringList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
