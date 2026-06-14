package usage

import (
	"database/sql"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// ModelPriceRow stores per-model pricing used for cost calculation.
type ModelPriceRow struct {
	Model           string  `json:"model"`
	Mode            string  `json:"mode"` // "token" or "call"
	InputPricePerM  float64 `json:"input_price_per_million"`
	OutputPricePerM float64 `json:"output_price_per_million"`
	CachedPricePerM float64 `json:"cached_price_per_million"`
	PricePerCall    float64 `json:"price_per_call"`
	UpdatedAt       string  `json:"updated_at,omitempty"`
}

var (
	modelPricesCache   map[string]ModelPriceRow
	modelPricesCacheAt time.Time
	modelPricesMu      sync.RWMutex
)

const modelPricesCacheTTL = 5 * time.Minute

// EnsureModelPricesTable creates the model_prices table if it does not exist.
func EnsureModelPricesTable(db *sql.DB) {
	if db == nil {
		return
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS model_prices (
		model               TEXT NOT NULL PRIMARY KEY,
		mode                TEXT NOT NULL DEFAULT 'token',
		input_price_per_m   REAL NOT NULL DEFAULT 0,
		output_price_per_m  REAL NOT NULL DEFAULT 0,
		cached_price_per_m  REAL NOT NULL DEFAULT 0,
		price_per_call      REAL NOT NULL DEFAULT 0,
		updated_at          TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		log.Errorf("usage: create model_prices table: %v", err)
	}
}

// SetModelPrice inserts or updates a model pricing entry.
func SetModelPrice(row ModelPriceRow) error {
	db := getDB()
	if db == nil {
		return sql.ErrConnDone
	}
	EnsureModelPricesTable(db)
	row.Model = strings.TrimSpace(row.Model)
	if row.Model == "" {
		return nil
	}
	if row.Mode == "" {
		row.Mode = "token"
	}
	row.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`INSERT INTO model_prices (model, mode, input_price_per_m, output_price_per_m, cached_price_per_m, price_per_call, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(model) DO UPDATE SET mode=excluded.mode, input_price_per_m=excluded.input_price_per_m, output_price_per_m=excluded.output_price_per_m, cached_price_per_m=excluded.cached_price_per_m, price_per_call=excluded.price_per_call, updated_at=excluded.updated_at`,
		row.Model, row.Mode, row.InputPricePerM, row.OutputPricePerM, row.CachedPricePerM, row.PricePerCall, row.UpdatedAt,
	)
	if err != nil {
		log.Errorf("usage: set model price for %s: %v", row.Model, err)
	}
	invalidateModelPricesCache()
	return err
}

// DeleteModelPrice removes a model pricing entry.
func DeleteModelPrice(model string) error {
	db := getDB()
	if db == nil {
		return sql.ErrConnDone
	}
	_, err := db.Exec(`DELETE FROM model_prices WHERE model = ?`, strings.TrimSpace(model))
	if err != nil {
		log.Errorf("usage: delete model price for %s: %v", model, err)
	}
	invalidateModelPricesCache()
	return err
}

// ListModelPrices returns all configured model pricing entries.
func ListModelPrices() []ModelPriceRow {
	db := getDB()
	if db == nil {
		return nil
	}
	EnsureModelPricesTable(db)
	rows, err := db.Query(`SELECT model, mode, input_price_per_m, output_price_per_m, cached_price_per_m, price_per_call, updated_at FROM model_prices ORDER BY model`)
	if err != nil {
		log.Errorf("usage: list model prices: %v", err)
		return nil
	}
	defer func() { _ = rows.Close() }()
	var result []ModelPriceRow
	for rows.Next() {
		var r ModelPriceRow
		if errScan := rows.Scan(&r.Model, &r.Mode, &r.InputPricePerM, &r.OutputPricePerM, &r.CachedPricePerM, &r.PricePerCall, &r.UpdatedAt); errScan != nil {
			continue
		}
		result = append(result, r)
	}
	return result
}

// GetModelPrice returns the pricing for a specific model, or nil if not configured.
func GetModelPrice(model string) *ModelPriceRow {
	prices := getCachedModelPrices()
	if p, ok := prices[model]; ok {
		return &p
	}
	return nil
}

// CalculateModelCost computes the cost for a single request based on model pricing.
// Returns 0 if the model has no pricing configured.
// Thread-safe; uses a TTL cache to avoid querying the DB on every request.
func CalculateModelCost(model string, inputTokens, outputTokens, reasoningTokens, cachedTokens int64) float64 {
	price := GetModelPrice(model)
	return calculateCostFromPrice(price, inputTokens, outputTokens, reasoningTokens, cachedTokens)
}

// CalculateModelCostWithDB computes cost using a specific DB connection (avoids deadlock when called inside a locked section).
func CalculateModelCostWithDB(db *sql.DB, model string, inputTokens, outputTokens, reasoningTokens, cachedTokens int64) float64 {
	if db == nil {
		return 0
	}
	price := lookupModelPriceLocked(db, model)
	return calculateCostFromPrice(price, inputTokens, outputTokens, reasoningTokens, cachedTokens)
}

// calculateCostFromPrice is the pure cost computation shared between both entry points.
func calculateCostFromPrice(price *ModelPriceRow, inputTokens, outputTokens, reasoningTokens, cachedTokens int64) float64 {
	if price == nil {
		return 0
	}

	if price.Mode == "call" && price.PricePerCall > 0 {
		return price.PricePerCall
	}

	// Token-based pricing: cost = (tokens / 1_000_000) * price_per_million
	netInput := inputTokens - cachedTokens
	if netInput < 0 {
		netInput = inputTokens
	}
	inputCost := float64(netInput) / 1_000_000 * price.InputPricePerM
	outputCost := float64(outputTokens+reasoningTokens) / 1_000_000 * price.OutputPricePerM
	cachedCost := float64(cachedTokens) / 1_000_000 * price.CachedPricePerM

	total := inputCost + outputCost + cachedCost
	if total < 0 {
		return 0
	}
	return total
}

// LookupAPIKeyNameWithDB resolves the display name for an API key using a specific DB connection.
func LookupAPIKeyNameWithDB(db *sql.DB, apiKey string) string {
	if db == nil {
		return ""
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return ""
	}
	var name string
	err := db.QueryRow(`SELECT name FROM api_keys WHERE key = ?`, apiKey).Scan(&name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(name)
}

// lookupModelPriceLocked queries model pricing directly from DB (no locking, caller must ensure thread safety).
func lookupModelPriceLocked(db *sql.DB, model string) *ModelPriceRow {
	if db == nil {
		return nil
	}
	EnsureModelPricesTable(db)
	var r ModelPriceRow
	err := db.QueryRow(`SELECT model, mode, input_price_per_m, output_price_per_m, cached_price_per_m, price_per_call, updated_at FROM model_prices WHERE model = ?`, model).Scan(
		&r.Model, &r.Mode, &r.InputPricePerM, &r.OutputPricePerM, &r.CachedPricePerM, &r.PricePerCall, &r.UpdatedAt,
	)
	if err != nil {
		return nil
	}
	return &r
}

// LookupAPIKeyName resolves the display name for an API key from the management DB.
// Thread-safe but acquires the global DB lock; use LookupAPIKeyNameWithDB inside locked sections.
func LookupAPIKeyName(apiKey string) string {
	return LookupAPIKeyNameWithDB(getDB(), apiKey)
}

// getCachedModelPrices returns a snapshot of model prices with a short TTL cache.
func getCachedModelPrices() map[string]ModelPriceRow {
	modelPricesMu.RLock()
	if modelPricesCache != nil && time.Since(modelPricesCacheAt) < modelPricesCacheTTL {
		cache := modelPricesCache
		modelPricesMu.RUnlock()
		return cache
	}
	modelPricesMu.RUnlock()

	db := getDB()
	if db == nil {
		return nil
	}
	EnsureModelPricesTable(db)
	rows, err := db.Query(`SELECT model, mode, input_price_per_m, output_price_per_m, cached_price_per_m, price_per_call, updated_at FROM model_prices`)
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	cache := make(map[string]ModelPriceRow)
	for rows.Next() {
		var r ModelPriceRow
		if errScan := rows.Scan(&r.Model, &r.Mode, &r.InputPricePerM, &r.OutputPricePerM, &r.CachedPricePerM, &r.PricePerCall, &r.UpdatedAt); errScan != nil {
			continue
		}
		cache[r.Model] = r
	}

	modelPricesMu.Lock()
	modelPricesCache = cache
	modelPricesCacheAt = time.Now()
	modelPricesMu.Unlock()

	return cache
}

func invalidateModelPricesCache() {
	modelPricesMu.Lock()
	modelPricesCache = nil
	modelPricesMu.Unlock()
}
