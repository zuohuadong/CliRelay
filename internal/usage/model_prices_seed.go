package usage

import (
	"database/sql"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// officialModelPrices holds the per-model pricing (USD per million tokens) for
// upstream models we route to. Keys are the upstream model names that actually
// appear in usage records (record.Model), so cost is always computed from the
// real model that handled the request, never from a client alias.
//
// Sources: official provider pricing pages (OpenAI, Anthropic, Google, etc.)
// as of 2026-06. Prices are input / output / cached per 1M tokens unless the
// entry is mode "call" with a per-call price.
var officialModelPrices = []ModelPriceRow{
	// --- OpenAI / Codex (codex channel, upstream OAuth) ---
	{Model: "gpt-5.5", Mode: "token", InputPricePerM: 1.25, OutputPricePerM: 10, CachedPricePerM: 0.125},
	{Model: "gpt-5.4", Mode: "token", InputPricePerM: 1.25, OutputPricePerM: 10, CachedPricePerM: 0.125},
	{Model: "gpt-5.4-mini", Mode: "token", InputPricePerM: 0.25, OutputPricePerM: 2, CachedPricePerM: 0.025},
	{Model: "gpt-5.3-codex", Mode: "token", InputPricePerM: 1.25, OutputPricePerM: 10, CachedPricePerM: 0.125},
	{Model: "gpt-5.3-codex-spark", Mode: "token", InputPricePerM: 1.25, OutputPricePerM: 10, CachedPricePerM: 0.125},
	{Model: "gpt-5.2", Mode: "token", InputPricePerM: 1.25, OutputPricePerM: 10, CachedPricePerM: 0.125},
	{Model: "gpt-5.1-codex-max", Mode: "token", InputPricePerM: 1.25, OutputPricePerM: 10, CachedPricePerM: 0.125},
	{Model: "gpt-5.1", Mode: "token", InputPricePerM: 1.25, OutputPricePerM: 10, CachedPricePerM: 0.125},
	{Model: "gpt-5", Mode: "token", InputPricePerM: 1.25, OutputPricePerM: 10, CachedPricePerM: 0.125},
	{Model: "gpt-4.1", Mode: "token", InputPricePerM: 2.5, OutputPricePerM: 10, CachedPricePerM: 0.625},
	{Model: "gpt-4.1-mini", Mode: "token", InputPricePerM: 0.4, OutputPricePerM: 1.6, CachedPricePerM: 0.1},
	{Model: "gpt-4o", Mode: "token", InputPricePerM: 2.5, OutputPricePerM: 10, CachedPricePerM: 1.25},
	{Model: "gpt-4o-mini", Mode: "token", InputPricePerM: 0.15, OutputPricePerM: 0.6, CachedPricePerM: 0.075},
	{Model: "o3", Mode: "token", InputPricePerM: 2, OutputPricePerM: 8, CachedPricePerM: 0.5},
	{Model: "o4-mini", Mode: "token", InputPricePerM: 1.1, OutputPricePerM: 4.4, CachedPricePerM: 0.275},

	// --- OpenAI image generation ---
	{Model: "gpt-image-2", Mode: "call", PricePerCall: 0.04},
	{Model: "dall-e-3", Mode: "call", PricePerCall: 0.04},

	// --- Anthropic Claude (claude channel) ---
	{Model: "claude-opus-4-8", Mode: "token", InputPricePerM: 15, OutputPricePerM: 75, CachedPricePerM: 1.5},
	{Model: "claude-opus-4-7", Mode: "token", InputPricePerM: 15, OutputPricePerM: 75, CachedPricePerM: 1.5},
	{Model: "claude-opus-4-6", Mode: "token", InputPricePerM: 15, OutputPricePerM: 75, CachedPricePerM: 1.5},
	{Model: "claude-opus-4-5-20251101", Mode: "token", InputPricePerM: 5, OutputPricePerM: 25, CachedPricePerM: 0.5},
	{Model: "claude-opus-4-1-20250805", Mode: "token", InputPricePerM: 15, OutputPricePerM: 75, CachedPricePerM: 1.5},
	{Model: "claude-opus-4-20250514", Mode: "token", InputPricePerM: 15, OutputPricePerM: 75, CachedPricePerM: 1.5},
	{Model: "claude-opus-4-6-thinking", Mode: "token", InputPricePerM: 15, OutputPricePerM: 75, CachedPricePerM: 1.5},
	{Model: "claude-sonnet-4-6", Mode: "token", InputPricePerM: 3, OutputPricePerM: 15, CachedPricePerM: 0.3},
	{Model: "claude-sonnet-4-5-20250929", Mode: "token", InputPricePerM: 3, OutputPricePerM: 15, CachedPricePerM: 0.3},
	{Model: "claude-sonnet-4-20250514", Mode: "token", InputPricePerM: 3, OutputPricePerM: 15, CachedPricePerM: 0.3},
	{Model: "claude-haiku-4-5-20251001", Mode: "token", InputPricePerM: 0.8, OutputPricePerM: 4, CachedPricePerM: 0.08},
	{Model: "claude-3-7-sonnet-20250219", Mode: "token", InputPricePerM: 3, OutputPricePerM: 15, CachedPricePerM: 0.3},
	{Model: "claude-3-5-haiku-20241022", Mode: "token", InputPricePerM: 0.8, OutputPricePerM: 4, CachedPricePerM: 0.08},
	{Model: "claude-fable-5", Mode: "token", InputPricePerM: 5, OutputPricePerM: 25, CachedPricePerM: 0.5},

	// --- Google Gemini / Vertex / AI Studio / Antigravity (gemini channel) ---
	{Model: "gemini-3-pro-preview", Mode: "token", InputPricePerM: 1.25, OutputPricePerM: 10, CachedPricePerM: 0.3125},
	{Model: "gemini-3-pro", Mode: "token", InputPricePerM: 1.25, OutputPricePerM: 10, CachedPricePerM: 0.3125},
	{Model: "gemini-3-pro-high", Mode: "token", InputPricePerM: 1.25, OutputPricePerM: 10, CachedPricePerM: 0.3125},
	{Model: "gemini-3-pro-low", Mode: "token", InputPricePerM: 1.25, OutputPricePerM: 10, CachedPricePerM: 0.3125},
	{Model: "gemini-3.1-pro-preview", Mode: "token", InputPricePerM: 1.25, OutputPricePerM: 10, CachedPricePerM: 0.3125},
	{Model: "gemini-3.1-pro-low", Mode: "token", InputPricePerM: 1.25, OutputPricePerM: 10, CachedPricePerM: 0.3125},
	{Model: "gemini-3-flash-preview", Mode: "token", InputPricePerM: 0.15, OutputPricePerM: 0.6, CachedPricePerM: 0.0375},
	{Model: "gemini-3-flash", Mode: "token", InputPricePerM: 0.15, OutputPricePerM: 0.6, CachedPricePerM: 0.0375},
	{Model: "gemini-3-flash-agent", Mode: "token", InputPricePerM: 0.15, OutputPricePerM: 0.6, CachedPricePerM: 0.0375},
	{Model: "gemini-3.1-flash-lite-preview", Mode: "token", InputPricePerM: 0.075, OutputPricePerM: 0.3, CachedPricePerM: 0.01875},
	{Model: "gemini-3.1-flash-lite", Mode: "token", InputPricePerM: 0.075, OutputPricePerM: 0.3, CachedPricePerM: 0.01875},
	{Model: "gemini-3.1-flash-image-preview", Mode: "token", InputPricePerM: 0.35, OutputPricePerM: 1.05, CachedPricePerM: 0.0875},
	{Model: "gemini-3-pro-image-preview", Mode: "token", InputPricePerM: 1.25, OutputPricePerM: 10, CachedPricePerM: 0.3125},
	{Model: "gemini-3.5-flash", Mode: "token", InputPricePerM: 0.15, OutputPricePerM: 0.6, CachedPricePerM: 0.0375},
	{Model: "gemini-3.5-flash-low", Mode: "token", InputPricePerM: 0.15, OutputPricePerM: 0.6, CachedPricePerM: 0.0375},
	{Model: "gemini-2.5-pro", Mode: "token", InputPricePerM: 1.25, OutputPricePerM: 10, CachedPricePerM: 0.3125},
	{Model: "gemini-2.5-flash", Mode: "token", InputPricePerM: 0.15, OutputPricePerM: 0.6, CachedPricePerM: 0.0375},
	{Model: "gemini-2.5-flash-image", Mode: "token", InputPricePerM: 0.35, OutputPricePerM: 1.05, CachedPricePerM: 0.0875},
	{Model: "gemini-2.5-flash-lite", Mode: "token", InputPricePerM: 0.075, OutputPricePerM: 0.3, CachedPricePerM: 0.01875},
	{Model: "gemini-pro-latest", Mode: "token", InputPricePerM: 1.25, OutputPricePerM: 10, CachedPricePerM: 0.3125},
	{Model: "gemini-flash-latest", Mode: "token", InputPricePerM: 0.15, OutputPricePerM: 0.6, CachedPricePerM: 0.0375},
	{Model: "gemini-flash-lite-latest", Mode: "token", InputPricePerM: 0.075, OutputPricePerM: 0.3, CachedPricePerM: 0.01875},

	// --- xAI Grok (xai channel) ---
	{Model: "grok-4.3", Mode: "token", InputPricePerM: 3, OutputPricePerM: 15, CachedPricePerM: 0.3},
	{Model: "grok-4.20-0309-reasoning", Mode: "token", InputPricePerM: 5, OutputPricePerM: 25, CachedPricePerM: 0.5},
	{Model: "grok-4.20-0309-non-reasoning", Mode: "token", InputPricePerM: 3, OutputPricePerM: 15, CachedPricePerM: 0.3},
	{Model: "grok-4.20-multi-agent-0309", Mode: "token", InputPricePerM: 5, OutputPricePerM: 25, CachedPricePerM: 0.5},
	{Model: "grok-3-mini", Mode: "token", InputPricePerM: 0.3, OutputPricePerM: 0.5, CachedPricePerM: 0.03},
	{Model: "grok-3-mini-fast", Mode: "token", InputPricePerM: 0.3, OutputPricePerM: 0.5, CachedPricePerM: 0.03},
	{Model: "grok-composer-2.5-fast", Mode: "token", InputPricePerM: 3, OutputPricePerM: 15, CachedPricePerM: 0.3},
	{Model: "grok-build-0.1", Mode: "token", InputPricePerM: 5, OutputPricePerM: 15, CachedPricePerM: 0.5},

	// --- Moonshot Kimi (kimi channel) ---
	{Model: "kimi-k2", Mode: "token", InputPricePerM: 0.6, OutputPricePerM: 2.5, CachedPricePerM: 0.06},
	{Model: "kimi-k2-thinking", Mode: "token", InputPricePerM: 0.6, OutputPricePerM: 2.5, CachedPricePerM: 0.06},
	{Model: "kimi-k2.5", Mode: "token", InputPricePerM: 0.6, OutputPricePerM: 2.5, CachedPricePerM: 0.06},
	{Model: "kimi-k2.6", Mode: "token", InputPricePerM: 0.6, OutputPricePerM: 2.5, CachedPricePerM: 0.06},

	// --- Antigravity / other upstream seen in production ---
	{Model: "gpt-oss-120b-medium", Mode: "token", InputPricePerM: 0.15, OutputPricePerM: 0.6, CachedPricePerM: 0.0375},
	{Model: "gemini-pro-agent", Mode: "token", InputPricePerM: 1.25, OutputPricePerM: 10, CachedPricePerM: 0.3125},

	// --- BigModel / Zhipu GLM (bigmodel-coding channel, upstream) ---
	{Model: "glm-5", Mode: "token", InputPricePerM: 0.5, OutputPricePerM: 1.5, CachedPricePerM: 0.05},
	{Model: "glm-5.1", Mode: "token", InputPricePerM: 0.5, OutputPricePerM: 1.5, CachedPricePerM: 0.05},
	{Model: "glm-5.2", Mode: "token", InputPricePerM: 0.8, OutputPricePerM: 2.4, CachedPricePerM: 0.08},
	{Model: "glm-4.6", Mode: "token", InputPricePerM: 0.5, OutputPricePerM: 1.5, CachedPricePerM: 0.05},
	{Model: "glm-4.5", Mode: "token", InputPricePerM: 0.5, OutputPricePerM: 1.5, CachedPricePerM: 0.05},

	// --- Astron / iFlytek (astron-code channel, upstream) ---
	{Model: "astron-code-latest", Mode: "token", InputPricePerM: 0.4, OutputPricePerM: 1.2, CachedPricePerM: 0.04},

	// --- Third-party OpenAI-compatible models observed in production ---
	{Model: "deepseek-v3.2", Mode: "token", InputPricePerM: 0.27, OutputPricePerM: 1.1, CachedPricePerM: 0.027},
	{Model: "deepseek-v4-flash", Mode: "token", InputPricePerM: 0.14, OutputPricePerM: 0.28, CachedPricePerM: 0.014},
	{Model: "qwen3.6-plus", Mode: "token", InputPricePerM: 0.4, OutputPricePerM: 1.2, CachedPricePerM: 0.04},
	{Model: "qwen3.6-flash", Mode: "token", InputPricePerM: 0.1, OutputPricePerM: 0.3, CachedPricePerM: 0.01},
	{Model: "qwen-image-2.0", Mode: "call", PricePerCall: 0.03},
	{Model: "qwen-image-2.0-pro", Mode: "call", PricePerCall: 0.04},
	{Model: "mimo-v2.5-pro", Mode: "token", InputPricePerM: 0.3, OutputPricePerM: 0.9, CachedPricePerM: 0.03},
	{Model: "MiniMax-M2.5", Mode: "token", InputPricePerM: 0.5, OutputPricePerM: 1.5, CachedPricePerM: 0.05},
	{Model: "wan2.7-image", Mode: "call", PricePerCall: 0.025},
	{Model: "wan2.7-image-pro", Mode: "call", PricePerCall: 0.04},
	{Model: "nano-banana", Mode: "call", PricePerCall: 0.03},
	{Model: "nano-banana-2", Mode: "call", PricePerCall: 0.04},
	{Model: "nano-banana-fast", Mode: "call", PricePerCall: 0.02},
	{Model: "xopglmv47flash", Mode: "call", PricePerCall: 0.02},
	{Model: "xsparkx2flash", Mode: "call", PricePerCall: 0.02},

	// --- Codex internal auto-review (no direct token cost) ---
	{Model: "codex-auto-review", Mode: "token", InputPricePerM: 0, OutputPricePerM: 0, CachedPricePerM: 0},
}

var (
	modelPricesSeededOnce sync.Once
	modelPricesSeededDone bool
	modelPricesSeedingMu  sync.Mutex
)

// SeedOfficialModelPrices inserts the built-in official platform prices into
// the model_prices table if they are not already present. Each call is
// idempotent: existing rows are left untouched so operator edits persist; only
// models that have no row at all are inserted. Runs at most once per process.
func SeedOfficialModelPrices(db *sql.DB) {
	if db == nil {
		return
	}
	modelPricesSeedingMu.Lock()
	defer modelPricesSeedingMu.Unlock()
	if modelPricesSeededDone {
		return
	}
	EnsureModelPricesTable(db)
	now := time.Now().UTC().Format(time.RFC3339)
	inserted := 0
	for _, row := range officialModelPrices {
		model := strings.TrimSpace(row.Model)
		if model == "" {
			continue
		}
		var existing string
		_ = db.QueryRow(`SELECT model FROM model_prices WHERE model = ?`, model).Scan(&existing)
		if existing != "" {
			continue // operator may have customized; never overwrite
		}
		mode := strings.TrimSpace(row.Mode)
		if mode == "" {
			mode = "token"
		}
		_, err := db.Exec(`INSERT INTO model_prices (model, mode, input_price_per_m, output_price_per_m, cached_price_per_m, price_per_call, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			model, mode, row.InputPricePerM, row.OutputPricePerM, row.CachedPricePerM, row.PricePerCall, now)
		if err != nil {
			log.Warnf("usage: seed model price for %s: %v", model, err)
			continue
		}
		inserted++
	}
	if inserted > 0 {
		log.Infof("usage: seeded %d official model price entries", inserted)
	}
	invalidateModelPricesCache()
	modelPricesSeededDone = true
}
