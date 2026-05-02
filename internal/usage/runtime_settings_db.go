package usage

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	log "github.com/sirupsen/logrus"
)

const (
	RuntimeSettingClaudeHeaderDefaults = "claude-header-defaults"
	RuntimeSettingKimiHeaderDefaults   = "kimi-header-defaults"
	RuntimeSettingIdentityFingerprint  = "identity-fingerprint"
	RuntimeSettingOAuthExcludedModels  = "oauth-excluded-models"
	RuntimeSettingOAuthModelAlias      = "oauth-model-alias"
	RuntimeSettingPayload              = "payload"
)

const createRuntimeSettingsTableSQL = `
CREATE TABLE IF NOT EXISTS runtime_settings (
  setting_key TEXT PRIMARY KEY NOT NULL,
  payload     TEXT NOT NULL DEFAULT '{}',
  updated_at  TEXT NOT NULL DEFAULT ''
);
`

func initRuntimeSettingsTable(db *sql.DB) {
	if db == nil {
		return
	}
	if _, err := db.Exec(createRuntimeSettingsTableSQL); err != nil {
		log.Errorf("usage: create runtime_settings table: %v", err)
	}
}

type runtimeSettingSpec struct {
	key        string
	meaningful func(*config.Config) bool
	value      func(*config.Config) any
	apply      func(*config.Config, json.RawMessage) bool
}

func runtimeSettingSpecs() []runtimeSettingSpec {
	return []runtimeSettingSpec{
		{
			key: RuntimeSettingClaudeHeaderDefaults,
			meaningful: func(cfg *config.Config) bool {
				return strings.TrimSpace(cfg.ClaudeHeaderDefaults.UserAgent) != "" ||
					strings.TrimSpace(cfg.ClaudeHeaderDefaults.PackageVersion) != "" ||
					strings.TrimSpace(cfg.ClaudeHeaderDefaults.RuntimeVersion) != "" ||
					strings.TrimSpace(cfg.ClaudeHeaderDefaults.Timeout) != ""
			},
			value: func(cfg *config.Config) any {
				out := cfg.ClaudeHeaderDefaults
				out.UserAgent = strings.TrimSpace(out.UserAgent)
				out.PackageVersion = strings.TrimSpace(out.PackageVersion)
				out.RuntimeVersion = strings.TrimSpace(out.RuntimeVersion)
				out.Timeout = strings.TrimSpace(out.Timeout)
				return out
			},
			apply: func(cfg *config.Config, raw json.RawMessage) bool {
				var value config.ClaudeHeaderDefaults
				if err := json.Unmarshal(raw, &value); err != nil {
					log.Warnf("usage: decode %s: %v", RuntimeSettingClaudeHeaderDefaults, err)
					return false
				}
				value.UserAgent = strings.TrimSpace(value.UserAgent)
				value.PackageVersion = strings.TrimSpace(value.PackageVersion)
				value.RuntimeVersion = strings.TrimSpace(value.RuntimeVersion)
				value.Timeout = strings.TrimSpace(value.Timeout)
				cfg.ClaudeHeaderDefaults = value
				return true
			},
		},
		{
			key: RuntimeSettingKimiHeaderDefaults,
			meaningful: func(cfg *config.Config) bool {
				return strings.TrimSpace(cfg.KimiHeaderDefaults.UserAgent) != "" ||
					strings.TrimSpace(cfg.KimiHeaderDefaults.Platform) != "" ||
					strings.TrimSpace(cfg.KimiHeaderDefaults.Version) != ""
			},
			value: func(cfg *config.Config) any {
				out := cfg.KimiHeaderDefaults
				out.UserAgent = strings.TrimSpace(out.UserAgent)
				out.Platform = strings.TrimSpace(out.Platform)
				out.Version = strings.TrimSpace(out.Version)
				return out
			},
			apply: func(cfg *config.Config, raw json.RawMessage) bool {
				var value config.KimiHeaderDefaults
				if err := json.Unmarshal(raw, &value); err != nil {
					log.Warnf("usage: decode %s: %v", RuntimeSettingKimiHeaderDefaults, err)
					return false
				}
				value.UserAgent = strings.TrimSpace(value.UserAgent)
				value.Platform = strings.TrimSpace(value.Platform)
				value.Version = strings.TrimSpace(value.Version)
				cfg.KimiHeaderDefaults = value
				return true
			},
		},
		{
			key: RuntimeSettingIdentityFingerprint,
			meaningful: func(cfg *config.Config) bool {
				return codexIdentityFingerprintMeaningful(cfg.IdentityFingerprint.Codex)
			},
			value: func(cfg *config.Config) any {
				return config.IdentityFingerprintConfig{
					Codex: config.NormalizeCodexIdentityFingerprint(cfg.IdentityFingerprint.Codex),
				}
			},
			apply: func(cfg *config.Config, raw json.RawMessage) bool {
				var value config.IdentityFingerprintConfig
				if err := json.Unmarshal(raw, &value); err != nil {
					log.Warnf("usage: decode %s: %v", RuntimeSettingIdentityFingerprint, err)
					return false
				}
				value.Codex = config.NormalizeCodexIdentityFingerprint(value.Codex)
				cfg.IdentityFingerprint = value
				return true
			},
		},
		{
			key: RuntimeSettingOAuthExcludedModels,
			meaningful: func(cfg *config.Config) bool {
				return len(config.NormalizeOAuthExcludedModels(cfg.OAuthExcludedModels)) > 0
			},
			value: func(cfg *config.Config) any {
				return config.NormalizeOAuthExcludedModels(cfg.OAuthExcludedModels)
			},
			apply: func(cfg *config.Config, raw json.RawMessage) bool {
				var value map[string][]string
				if err := json.Unmarshal(raw, &value); err != nil {
					log.Warnf("usage: decode %s: %v", RuntimeSettingOAuthExcludedModels, err)
					return false
				}
				cfg.OAuthExcludedModels = config.NormalizeOAuthExcludedModels(value)
				return true
			},
		},
		{
			key: RuntimeSettingOAuthModelAlias,
			meaningful: func(cfg *config.Config) bool {
				return len(normalizeOAuthModelAliasSetting(cfg.OAuthModelAlias)) > 0
			},
			value: func(cfg *config.Config) any {
				return normalizeOAuthModelAliasSetting(cfg.OAuthModelAlias)
			},
			apply: func(cfg *config.Config, raw json.RawMessage) bool {
				var value map[string][]config.OAuthModelAlias
				if err := json.Unmarshal(raw, &value); err != nil {
					log.Warnf("usage: decode %s: %v", RuntimeSettingOAuthModelAlias, err)
					return false
				}
				cfg.OAuthModelAlias = normalizeOAuthModelAliasSetting(value)
				return true
			},
		},
		{
			key: RuntimeSettingPayload,
			meaningful: func(cfg *config.Config) bool {
				return payloadConfigMeaningful(cfg.Payload)
			},
			value: func(cfg *config.Config) any {
				holder := &config.Config{Payload: cfg.Payload}
				holder.SanitizePayloadRules()
				return holder.Payload
			},
			apply: func(cfg *config.Config, raw json.RawMessage) bool {
				var value config.PayloadConfig
				if err := json.Unmarshal(raw, &value); err != nil {
					log.Warnf("usage: decode %s: %v", RuntimeSettingPayload, err)
					return false
				}
				holder := &config.Config{Payload: value}
				holder.SanitizePayloadRules()
				cfg.Payload = holder.Payload
				return true
			},
		},
	}
}

func codexIdentityFingerprintMeaningful(fp config.CodexIdentityFingerprintConfig) bool {
	normalized := config.NormalizeCodexIdentityFingerprint(fp)
	defaults := config.DefaultCodexIdentityFingerprint()
	if normalized.Enabled || strings.TrimSpace(normalized.SessionID) != "" || len(normalized.CustomHeaders) > 0 {
		return true
	}
	return normalized.UserAgent != defaults.UserAgent ||
		normalized.Version != defaults.Version ||
		normalized.Originator != defaults.Originator ||
		normalized.WebsocketBeta != defaults.WebsocketBeta ||
		normalized.SessionMode != defaults.SessionMode
}

func normalizeOAuthModelAliasSetting(entries map[string][]config.OAuthModelAlias) map[string][]config.OAuthModelAlias {
	if len(entries) == 0 {
		return nil
	}
	holder := &config.Config{OAuthModelAlias: entries}
	holder.SanitizeOAuthModelAlias()
	if len(holder.OAuthModelAlias) == 0 {
		return nil
	}
	return holder.OAuthModelAlias
}

func payloadConfigMeaningful(payload config.PayloadConfig) bool {
	return len(payload.Default) > 0 ||
		len(payload.DefaultRaw) > 0 ||
		len(payload.Override) > 0 ||
		len(payload.OverrideRaw) > 0 ||
		len(payload.Filter) > 0
}

func runtimeSettingPayload(key string) (json.RawMessage, bool) {
	db := getDB()
	if db == nil {
		return nil, false
	}
	var payload string
	if err := db.QueryRow(`SELECT payload FROM runtime_settings WHERE setting_key = ?`, key).Scan(&payload); err != nil {
		if err != sql.ErrNoRows {
			log.Warnf("usage: load runtime setting %s: %v", key, err)
		}
		return nil, false
	}
	payload = strings.TrimSpace(payload)
	if payload == "" {
		payload = "{}"
	}
	return json.RawMessage(payload), true
}

func runtimeSettingExists(key string) bool {
	_, ok := runtimeSettingPayload(key)
	return ok
}

func UpsertRuntimeSetting(key string, value any) error {
	db := getDB()
	if db == nil {
		return nil
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = db.Exec(
		`INSERT INTO runtime_settings (setting_key, payload, updated_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(setting_key) DO UPDATE SET payload = excluded.payload, updated_at = excluded.updated_at`,
		key,
		string(payload),
		time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

func ApplyStoredRuntimeSettings(cfg *config.Config) bool {
	if cfg == nil || !ConfigStoreAvailable() {
		return false
	}
	applied := false
	for _, spec := range runtimeSettingSpecs() {
		raw, ok := runtimeSettingPayload(spec.key)
		if !ok {
			continue
		}
		if spec.apply(cfg, raw) {
			applied = true
		}
	}
	return applied
}

func MigrateRuntimeSettingsFromConfig(cfg *config.Config, configFilePath string) int {
	if cfg == nil || !ConfigStoreAvailable() {
		return 0
	}
	migrated := 0
	hadStored := false
	for _, spec := range runtimeSettingSpecs() {
		if runtimeSettingExists(spec.key) {
			hadStored = true
			continue
		}
		if !spec.meaningful(cfg) {
			continue
		}
		if err := UpsertRuntimeSetting(spec.key, spec.value(cfg)); err != nil {
			log.Errorf("usage: migrate runtime setting %s: %v", spec.key, err)
			continue
		}
		migrated++
	}
	if strings.TrimSpace(configFilePath) == "" {
		return migrated
	}
	if migrated > 0 {
		if backupConfigForMigration(configFilePath, runtimeSettingsBackupSuffix) {
			cleanRuntimeSettingsFromYAML(configFilePath)
		}
		return migrated
	}
	if hadStored {
		cleanRuntimeSettingsFromYAML(configFilePath)
	}
	return migrated
}
