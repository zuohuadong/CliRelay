package usage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

type APIKeyPermissionProfileRow struct {
	ID                   string   `json:"id" yaml:"id"`
	Name                 string   `json:"name" yaml:"name"`
	DailyLimit           int      `json:"daily-limit" yaml:"daily-limit"`
	TotalQuota           int      `json:"total-quota" yaml:"total-quota"`
	ConcurrencyLimit     int      `json:"concurrency-limit" yaml:"concurrency-limit"`
	RPMLimit             int      `json:"rpm-limit" yaml:"rpm-limit"`
	TPMLimit             int      `json:"tpm-limit" yaml:"tpm-limit"`
	SpendingLimit        float64  `json:"spending-limit" yaml:"spending-limit"`
	AllowedModels        []string `json:"allowed-models" yaml:"allowed-models"`
	AllowedChannels      []string `json:"allowed-channels" yaml:"allowed-channels"`
	AllowedChannelGroups []string `json:"allowed-channel-groups" yaml:"allowed-channel-groups"`
	SystemPrompt         string   `json:"system-prompt" yaml:"system-prompt"`
	CreatedAt            string   `json:"created-at,omitempty" yaml:"created-at,omitempty"`
	UpdatedAt            string   `json:"updated-at,omitempty" yaml:"updated-at,omitempty"`
}

const createAPIKeyPermissionProfilesTableSQL = `
CREATE TABLE IF NOT EXISTS api_key_permission_profiles (
  id text primary key not null,
  name text not null default '',
  daily_limit integer not null default 0,
  total_quota integer not null default 0,
  concurrency_limit integer not null default 0,
  rpm_limit integer not null default 0,
  tpm_limit integer not null default 0,
  spending_limit real not null default 0,
  allowed_models text not null default '[]',
  allowed_channels text not null default '[]',
  allowed_channel_groups text not null default '[]',
  system_prompt text not null default '',
  created_at text not null default '',
  updated_at text not null default ''
)`

func initAPIKeyPermissionProfilesTable(db *sql.DB) {
	if _, err := db.Exec(createAPIKeyPermissionProfilesTableSQL); err != nil {
		log.Errorf("usage: create api_key_permission_profiles table: %v", err)
	}
}

func ListAPIKeyPermissionProfiles() []APIKeyPermissionProfileRow {
	db := getDB()
	if db == nil {
		return nil
	}
	initAPIKeyPermissionProfilesTable(db)

	rows, err := db.Query(`SELECT id, name, daily_limit, total_quota, concurrency_limit, rpm_limit, tpm_limit, spending_limit, allowed_models, allowed_channels, allowed_channel_groups, system_prompt, created_at, updated_at FROM api_key_permission_profiles ORDER BY created_at ASC, id ASC`)
	if err != nil {
		log.Errorf("usage: list api_key_permission_profiles: %v", err)
		return nil
	}
	defer func() { _ = rows.Close() }()

	var result []APIKeyPermissionProfileRow
	for rows.Next() {
		r := scanAPIKeyPermissionProfileFromRow(rows)
		if r != nil {
			result = append(result, *r)
		}
	}
	return result
}

func ReplaceAllAPIKeyPermissionProfiles(profiles []APIKeyPermissionProfileRow) error {
	db := getDB()
	if db == nil {
		return fmt.Errorf("database not initialised")
	}
	initAPIKeyPermissionProfilesTable(db)

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec("DELETE FROM api_key_permission_profiles"); err != nil {
		return err
	}

	stmt, err := tx.Prepare(`INSERT INTO api_key_permission_profiles (id, name, daily_limit, total_quota, concurrency_limit, rpm_limit, tpm_limit, spending_limit, allowed_models, allowed_channels, allowed_channel_groups, system_prompt, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()

	now := time.Now().UTC().Format(time.RFC3339)
	seen := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		profile = normalizeAPIKeyPermissionProfile(profile)
		if profile.ID == "" {
			return fmt.Errorf("id is required")
		}
		if profile.Name == "" {
			return fmt.Errorf("name is required")
		}
		if _, exists := seen[profile.ID]; exists {
			return fmt.Errorf("duplicate id %q", profile.ID)
		}
		seen[profile.ID] = struct{}{}

		if profile.CreatedAt == "" {
			profile.CreatedAt = now
		}
		profile.UpdatedAt = now

		modelsJSON := mustJSONStringList(profile.AllowedModels)
		channelsJSON := mustJSONStringList(profile.AllowedChannels)
		channelGroupsJSON := mustJSONStringList(profile.AllowedChannelGroups)

		if _, err := stmt.Exec(
			profile.ID, profile.Name,
			profile.DailyLimit, profile.TotalQuota,
			profile.ConcurrencyLimit, profile.RPMLimit, profile.TPMLimit,
			profile.SpendingLimit,
			modelsJSON, channelsJSON, channelGroupsJSON,
			profile.SystemPrompt,
			profile.CreatedAt, profile.UpdatedAt,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func EffectiveAPIKeyRowWithProfiles(row APIKeyRow, profiles []APIKeyPermissionProfileRow) APIKeyRow {
	if row.PermissionProfileID == "" {
		return row
	}
	for _, profile := range profiles {
		if profile.ID != row.PermissionProfileID {
			continue
		}
		if row.DailyLimit == 0 && profile.DailyLimit > 0 {
			row.DailyLimit = profile.DailyLimit
		}
		if row.TotalQuota == 0 && profile.TotalQuota > 0 {
			row.TotalQuota = profile.TotalQuota
		}
		if row.ConcurrencyLimit == 0 && profile.ConcurrencyLimit > 0 {
			row.ConcurrencyLimit = profile.ConcurrencyLimit
		}
		if row.RPMLimit == 0 && profile.RPMLimit > 0 {
			row.RPMLimit = profile.RPMLimit
		}
		if row.TPMLimit == 0 && profile.TPMLimit > 0 {
			row.TPMLimit = profile.TPMLimit
		}
		if row.SpendingLimit == 0 && profile.SpendingLimit > 0 {
			row.SpendingLimit = profile.SpendingLimit
		}
		if len(row.AllowedModels) == 0 && len(profile.AllowedModels) > 0 {
			row.AllowedModels = profile.AllowedModels
		}
		if len(row.AllowedChannels) == 0 && len(profile.AllowedChannels) > 0 {
			row.AllowedChannels = profile.AllowedChannels
		}
		if len(row.AllowedChannelGroups) == 0 && len(profile.AllowedChannelGroups) > 0 {
			row.AllowedChannelGroups = profile.AllowedChannelGroups
		}
		if row.SystemPrompt == "" && profile.SystemPrompt != "" {
			row.SystemPrompt = profile.SystemPrompt
		}
		break
	}
	return row
}

func normalizeAPIKeyPermissionProfile(profile APIKeyPermissionProfileRow) APIKeyPermissionProfileRow {
	profile.ID = strings.TrimSpace(profile.ID)
	profile.Name = strings.TrimSpace(profile.Name)
	profile.DailyLimit = normalizeNonNegativeInt(profile.DailyLimit)
	profile.TotalQuota = normalizeNonNegativeInt(profile.TotalQuota)
	profile.ConcurrencyLimit = normalizeNonNegativeInt(profile.ConcurrencyLimit)
	profile.RPMLimit = normalizeNonNegativeInt(profile.RPMLimit)
	profile.TPMLimit = normalizeNonNegativeInt(profile.TPMLimit)
	profile.AllowedModels = normalizeStringSlice(profile.AllowedModels)
	profile.AllowedChannels = normalizeStringSlice(profile.AllowedChannels)
	profile.AllowedChannelGroups = normalizeStringSlice(profile.AllowedChannelGroups)
	profile.SystemPrompt = strings.TrimSpace(profile.SystemPrompt)
	return profile
}

func normalizeNonNegativeInt(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func normalizeStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, exists := seen[v]; exists {
			continue
		}
		seen[v] = struct{}{}
		result = append(result, v)
	}
	return result
}

func mustJSONStringList(values []string) string {
	normalized := normalizeStringSlice(values)
	if normalized == nil {
		normalized = []string{}
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return "[]"
	}
	return string(data)
}

func scanAPIKeyPermissionProfileFromRow(row scannable) *APIKeyPermissionProfileRow {
	var r APIKeyPermissionProfileRow
	var modelsJSON, channelsJSON, channelGroupsJSON string
	if err := row.Scan(
		&r.ID, &r.Name,
		&r.DailyLimit, &r.TotalQuota,
		&r.ConcurrencyLimit, &r.RPMLimit, &r.TPMLimit,
		&r.SpendingLimit,
		&modelsJSON, &channelsJSON, &channelGroupsJSON,
		&r.SystemPrompt,
		&r.CreatedAt, &r.UpdatedAt,
	); err != nil {
		return nil
	}
	r.AllowedModels = decodeJSONStringList(modelsJSON)
	r.AllowedChannels = decodeJSONStringList(channelsJSON)
	r.AllowedChannelGroups = decodeJSONStringList(channelGroupsJSON)
	return &r
}

type scannable interface {
	Scan(dest ...any) error
}
