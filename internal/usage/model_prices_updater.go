package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	pricesFetchTimeout    = 30 * time.Second
	pricesRefreshInterval = 6 * time.Hour
)

// LiteLLM is the community-maintained, no-auth, single-JSON source of official
// per-provider pricing (input/output/cache per token). It covers the major
// international models we route to (OpenAI, Anthropic, Google, DeepSeek, etc.).
// Models it does NOT cover (grok, kimi, glm, astron, qwen, etc.) fall back to
// the local officialModelPrices seed.
var pricesURLs = []string{
	"https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json",
	"https://litellm-proxy-store.s3.amazonaws.com/model_prices_and_context_window.json",
}

// litellmModelEntry mirrors the relevant fields of a LiteLLM price entry.
// Prices are per-token (not per-million); we convert on insert.
type litellmModelEntry struct {
	InputCostPerToken           *float64 `json:"input_cost_per_token"`
	OutputCostPerToken          *float64 `json:"output_cost_per_token"`
	OutputCostPerReasoningToken *float64 `json:"output_cost_per_reasoning_token"`
	CacheReadInputTokenCost     *float64 `json:"cache_read_input_token_cost"`
	CacheCreationInputTokenCost *float64 `json:"cache_creation_input_token_cost"`
	InputCostPerImage           *float64 `json:"input_cost_per_image"`
	Mode                        string   `json:"mode"`
}

// litellmPricesFile is the raw JSON map (model id -> entry) plus the sample_spec.
type litellmPricesFile map[string]json.RawMessage

var (
	pricesUpdaterOnce sync.Once
	pricesUpdaterStop context.CancelFunc
)

// StartModelPricesUpdater starts a background loop that fetches official
// platform pricing from LiteLLM every pricesRefreshInterval and upserts it into
// the model_prices table. Safe to call multiple times; only one loop runs.
// Operator-locked rows (mode == "locked") are never overwritten; every other
// row is refreshed so prices stay current with upstream changes.
func StartModelPricesUpdater(ctx context.Context) {
	pricesUpdaterOnce.Do(func() {
		runCtx, cancel := context.WithCancel(ctx)
		pricesUpdaterStop = cancel
		go runModelPricesUpdater(runCtx)
	})
}

// StopModelPricesUpdater stops the background prices loop.
func StopModelPricesUpdater() {
	if pricesUpdaterStop != nil {
		pricesUpdaterStop()
	}
}

func runModelPricesUpdater(ctx context.Context) {
	// Initial fetch shortly after startup so the table is populated quickly.
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(pricesRefreshInterval)
	defer ticker.Stop()

	log.Infof("model prices updater started (interval=%s)", pricesRefreshInterval)

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			RefreshModelPricesFromRemote()
		case <-ticker.C:
			RefreshModelPricesFromRemote()
		}
	}
}

// RefreshModelPricesFromRemote performs a single fetch + upsert cycle. Exported
// so management endpoints can trigger a manual refresh. Returns counts.
func RefreshModelPricesFromRemote() (added, updated, skipped int) {
	data, url := fetchLitellmPrices(context.Background())
	if data == nil {
		log.Warnf("model prices updater: fetch failed from all URLs, keeping current prices")
		return 0, 0, 0
	}

	entries := parseLitellmPrices(data)
	if len(entries) == 0 {
		log.Warnf("model prices updater: parsed 0 entries from %s, skipping", url)
		return 0, 0, 0
	}

	db := getDB()
	if db == nil {
		return 0, 0, 0
	}

	added, updated, skipped = upsertLitellmPrices(db, entries)
	invalidateModelPricesCache()
	log.Infof("model prices updater: sync from %s complete (added=%d updated=%d skipped=%d total_seen=%d)",
		url, added, updated, skipped, len(entries))
	return added, updated, skipped
}

func fetchLitellmPrices(ctx context.Context) (litellmPricesFile, string) {
	client := &http.Client{Timeout: pricesFetchTimeout}
	for _, url := range pricesURLs {
		reqCtx, cancel := context.WithTimeout(ctx, pricesFetchTimeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
		if err != nil {
			cancel()
			log.Debugf("model prices fetch: request creation failed for %s: %v", url, err)
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			log.Debugf("model prices fetch failed from %s: %v", url, err)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			cancel()
			log.Debugf("model prices fetch returned %d from %s", resp.StatusCode, url)
			continue
		}
		body, errRead := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		if errRead != nil {
			log.Debugf("model prices fetch read error from %s: %v", url, errRead)
			continue
		}
		var parsed litellmPricesFile
		if errJSON := json.Unmarshal(body, &parsed); errJSON != nil {
			log.Warnf("model prices updater: parse failed from %s: %v", url, errJSON)
			continue
		}
		return parsed, url
	}
	return nil, ""
}

// parseLitellmPrices decodes the raw JSON map into a model->entry map, skipping
// the sample_spec and any provider-prefixed variants so only bare model ids are
// used (e.g. "gpt-4o", not "azure/gpt-4o").
func parseLitellmPrices(data litellmPricesFile) map[string]litellmModelEntry {
	out := make(map[string]litellmModelEntry, len(data))
	for model, raw := range data {
		model = strings.TrimSpace(model)
		if model == "" || model == "sample_spec" {
			continue
		}
		// Skip provider-prefixed variants; bare keys are authoritative and map
		// directly to the upstream model names that appear in usage records.
		if strings.Contains(model, "/") || strings.Contains(model, ":") {
			continue
		}
		var entry litellmModelEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			continue
		}
		if !litellmEntryHasPrice(&entry) {
			continue
		}
		out[model] = entry
	}
	return out
}

func litellmEntryHasPrice(e *litellmModelEntry) bool {
	if e == nil {
		return false
	}
	if e.Mode == "image_generation" {
		return e.InputCostPerImage != nil && *e.InputCostPerImage > 0
	}
	return (e.InputCostPerToken != nil && *e.InputCostPerToken > 0) ||
		(e.OutputCostPerToken != nil && *e.OutputCostPerToken > 0)
}

// upsertLitellmPrices inserts new models and refreshes existing rows. Rows the
// operator has locked (mode == "locked") are skipped. Returns add/update/skip.
func upsertLitellmPrices(db *sql.DB, entries map[string]litellmModelEntry) (added, updated, skipped int) {
	EnsureModelPricesTable(db)
	now := time.Now().UTC().Format(time.RFC3339)

	for model, entry := range entries {
		row := litellmEntryToPriceRow(model, &entry, now)

		// Check for an existing row to decide insert vs update vs skip.
		var existingMode string
		errQuery := db.QueryRow(`SELECT mode FROM model_prices WHERE model = ?`, model).Scan(&existingMode)
		if errQuery != nil && errQuery != sql.ErrNoRows {
			log.Debugf("model prices updater: lookup %s failed: %v", model, errQuery)
			skipped++
			continue
		}

		if errQuery == sql.ErrNoRows {
			// New model: insert.
			if _, err := db.Exec(`INSERT INTO model_prices (model, mode, input_price_per_m, output_price_per_m, cached_price_per_m, price_per_call, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				row.Model, row.Mode, row.InputPricePerM, row.OutputPricePerM, row.CachedPricePerM, row.PricePerCall, now); err != nil {
				log.Debugf("model prices updater: insert %s failed: %v", model, err)
				skipped++
				continue
			}
			added++
			continue
		}

		// Existing row: skip operator-locked entries, otherwise refresh.
		if strings.EqualFold(strings.TrimSpace(existingMode), "locked") {
			skipped++
			continue
		}
		if _, err := db.Exec(`UPDATE model_prices SET mode=?, input_price_per_m=?, output_price_per_m=?, cached_price_per_m=?, price_per_call=?, updated_at=? WHERE model=?`,
			row.Mode, row.InputPricePerM, row.OutputPricePerM, row.CachedPricePerM, row.PricePerCall, now, row.Model); err != nil {
			log.Debugf("model prices updater: update %s failed: %v", model, err)
			skipped++
			continue
		}
		updated++
	}
	return added, updated, skipped
}

// litellmEntryToPriceRow converts a per-token LiteLLM entry to the per-million
// ModelPriceRow used by the DB. Image-generation models become "call" mode.
func litellmEntryToPriceRow(model string, e *litellmModelEntry, now string) ModelPriceRow {
	row := ModelPriceRow{
		Model:     model,
		Mode:      "token",
		UpdatedAt: now,
	}
	if e.Mode == "image_generation" {
		row.Mode = "call"
		if e.InputCostPerImage != nil {
			row.PricePerCall = *e.InputCostPerImage
		}
		return row
	}
	if e.InputCostPerToken != nil {
		row.InputPricePerM = *e.InputCostPerToken * 1_000_000
	}
	if e.OutputCostPerToken != nil {
		row.OutputPricePerM = *e.OutputCostPerToken * 1_000_000
	}
	// Prefer the dedicated reasoning token cost when present (e.g. o-series
	// models bill reasoning separately); otherwise reasoning is folded into
	// output cost and OutputPricePerM already covers it.
	if e.OutputCostPerReasoningToken != nil && *e.OutputCostPerReasoningToken > 0 {
		row.OutputPricePerM += *e.OutputCostPerReasoningToken * 1_000_000
	}
	if e.CacheReadInputTokenCost != nil {
		row.CachedPricePerM = *e.CacheReadInputTokenCost * 1_000_000
	} else if e.CacheCreationInputTokenCost != nil {
		row.CachedPricePerM = *e.CacheCreationInputTokenCost * 1_000_000
	}
	return row
}
