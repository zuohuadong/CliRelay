package usage

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func newTestPricesDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test_usage.db")
	defaultSink.SetPath(dbPath)
	db := getDB()
	if db == nil {
		t.Fatal("expected non-nil db")
	}
	EnsureModelPricesTable(db)
	return db
}

func TestParseLitellmPricesSkipsPrefixedAndSample(t *testing.T) {
	raw := litellmPricesFile{
		"sample_spec":              []byte(`{"input_cost_per_token":0}`),
		"gpt-4o":                   []byte(`{"input_cost_per_token":0.0000025,"output_cost_per_token":0.00001,"mode":"chat"}`),
		"vertex_ai/gemini-2.5-pro": []byte(`{"input_cost_per_token":0.00000125,"output_cost_per_token":0.00001,"mode":"chat"}`),
		"dall-e-3":                 []byte(`{"mode":"image_generation","input_cost_per_image":0.04}`),
		"empty-model":              []byte(`{"mode":"chat"}`),
	}
	parsed := parseLitellmPrices(raw)
	if _, ok := parsed["sample_spec"]; ok {
		t.Fatal("sample_spec should be skipped")
	}
	if _, ok := parsed["vertex_ai/gemini-2.5-pro"]; ok {
		t.Fatal("provider-prefixed models should be skipped")
	}
	if _, ok := parsed["empty-model"]; ok {
		t.Fatal("entry without prices should be skipped")
	}
	if _, ok := parsed["gpt-4o"]; !ok {
		t.Fatal("gpt-4o should be parsed")
	}
	if _, ok := parsed["dall-e-3"]; !ok {
		t.Fatal("dall-e-3 should be parsed")
	}
}

func TestLitellmEntryToPriceRowToken(t *testing.T) {
	in := 0.0000025
	out := 0.00001
	cache := 0.00000125
	e := &litellmModelEntry{
		InputCostPerToken:       &in,
		OutputCostPerToken:      &out,
		CacheReadInputTokenCost: &cache,
		Mode:                    "chat",
	}
	row := litellmEntryToPriceRow("gpt-4o", e, "2026-01-01T00:00:00Z")
	if row.Mode != "token" {
		t.Fatalf("mode = %q, want token", row.Mode)
	}
	if row.InputPricePerM != 2.5 {
		t.Fatalf("input per M = %f, want 2.5", row.InputPricePerM)
	}
	if row.OutputPricePerM != 10 {
		t.Fatalf("output per M = %f, want 10", row.OutputPricePerM)
	}
	if row.CachedPricePerM != 1.25 {
		t.Fatalf("cached per M = %f, want 1.25", row.CachedPricePerM)
	}
	if row.PricePerCall != 0 {
		t.Fatalf("price per call = %f, want 0", row.PricePerCall)
	}
}

func TestLitellmEntryToPriceRowImage(t *testing.T) {
	img := 0.04
	e := &litellmModelEntry{
		Mode:              "image_generation",
		InputCostPerImage: &img,
	}
	row := litellmEntryToPriceRow("dall-e-3", e, "2026-01-01T00:00:00Z")
	if row.Mode != "call" {
		t.Fatalf("mode = %q, want call", row.Mode)
	}
	if row.PricePerCall != 0.04 {
		t.Fatalf("price per call = %f, want 0.04", row.PricePerCall)
	}
}

func TestUpsertLitellmPricesInsertsNewModels(t *testing.T) {
	db := newTestPricesDB(t)
	// Use model ids that are NOT in the official seed so they are genuinely new.
	entries := map[string]litellmModelEntry{
		"test-litellm-a": {InputCostPerToken: p(0.0000025), OutputCostPerToken: p(0.00001), Mode: "chat"},
		"test-litellm-b": {InputCostPerToken: p(0.00000015), OutputCostPerToken: p(0.0000006), Mode: "chat"},
	}
	added, updated, skipped := upsertLitellmPrices(db, entries)
	if added != 2 || updated != 0 || skipped != 0 {
		t.Fatalf("added=%d updated=%d skipped=%d, want 2/0/0", added, updated, skipped)
	}
	cost := CalculateModelCostWithDB(db, "test-litellm-a", 1_000_000, 0, 0, 0)
	if cost < 2.4 || cost > 2.6 {
		t.Fatalf("cost for 1M input test-litellm-a = %f, want ~2.5", cost)
	}
}

func TestUpsertLitellmPricesRespectsLockedRows(t *testing.T) {
	db := newTestPricesDB(t)
	// Operator locks a custom price.
	if err := SetModelPrice(ModelPriceRow{Model: "gpt-4o", Mode: "locked", InputPricePerM: 999}); err != nil {
		t.Fatalf("SetModelPrice: %v", err)
	}
	entries := map[string]litellmModelEntry{
		"gpt-4o": {InputCostPerToken: p(0.0000025), OutputCostPerToken: p(0.00001), Mode: "chat"},
	}
	added, updated, skipped := upsertLitellmPrices(db, entries)
	if added != 0 || updated != 0 || skipped != 1 {
		t.Fatalf("added=%d updated=%d skipped=%d, want 0/0/1", added, updated, skipped)
	}
	price := GetModelPrice("gpt-4o")
	if price == nil || price.InputPricePerM != 999 {
		t.Fatalf("locked row was overwritten: %#v", price)
	}
}

func TestUpsertLitellmPricesUpdatesExistingRows(t *testing.T) {
	db := newTestPricesDB(t)
	// Seed an old price.
	if err := SetModelPrice(ModelPriceRow{Model: "gpt-4o", Mode: "token", InputPricePerM: 1.0, OutputPricePerM: 2.0}); err != nil {
		t.Fatalf("SetModelPrice: %v", err)
	}
	entries := map[string]litellmModelEntry{
		"gpt-4o": {InputCostPerToken: p(0.0000025), OutputCostPerToken: p(0.00001), Mode: "chat"},
	}
	added, updated, skipped := upsertLitellmPrices(db, entries)
	if added != 0 || updated != 1 || skipped != 0 {
		t.Fatalf("added=%d updated=%d skipped=%d, want 0/1/0", added, updated, skipped)
	}
	price := GetModelPrice("gpt-4o")
	if price == nil || price.InputPricePerM != 2.5 {
		t.Fatalf("existing row not updated: input=%f, want 2.5", price.InputPricePerM)
	}
}

func TestSeedOfficialModelPricesIsIdempotent(t *testing.T) {
	db := newTestPricesDB(t)
	// Reset the process-level seed flag so this test controls seeding.
	modelPricesSeedingMu.Lock()
	modelPricesSeededDone = false
	modelPricesSeedingMu.Unlock()

	SeedOfficialModelPrices(db)
	first := len(ListModelPrices())
	if first == 0 {
		t.Fatal("seed produced no entries")
	}
	// Seed again; count must not change (idempotent).
	modelPricesSeedingMu.Lock()
	modelPricesSeededDone = false
	modelPricesSeedingMu.Unlock()
	SeedOfficialModelPrices(db)
	second := len(ListModelPrices())
	if second != first {
		t.Fatalf("seed not idempotent: first=%d second=%d", first, second)
	}
}

func TestSeedOfficialModelPricesPreservesOperatorEdits(t *testing.T) {
	db := newTestPricesDB(t)
	modelPricesSeedingMu.Lock()
	modelPricesSeededDone = false
	modelPricesSeedingMu.Unlock()

	// Operator sets a custom price for a seed model before seeding.
	customModel := officialModelPrices[0].Model
	if err := SetModelPrice(ModelPriceRow{Model: customModel, Mode: "token", InputPricePerM: 123.45}); err != nil {
		t.Fatalf("SetModelPrice: %v", err)
	}
	SeedOfficialModelPrices(db)
	price := GetModelPrice(customModel)
	if price == nil || price.InputPricePerM != 123.45 {
		t.Fatalf("operator edit overwritten by seed: %#v", price)
	}
}

func TestSeedOfficialModelPricesRefreshesUnpricedRows(t *testing.T) {
	db := newTestPricesDB(t)
	modelPricesSeedingMu.Lock()
	modelPricesSeededDone = false
	modelPricesSeedingMu.Unlock()

	if err := SetModelPrice(ModelPriceRow{Model: "codex-auto-review", Mode: "token"}); err != nil {
		t.Fatalf("SetModelPrice codex-auto-review: %v", err)
	}
	if err := SetModelPrice(ModelPriceRow{Model: "agnes-2.0-flash", Mode: "token"}); err != nil {
		t.Fatalf("SetModelPrice agnes-2.0-flash: %v", err)
	}
	if err := SetModelPrice(ModelPriceRow{Model: "glm-4.7-flash", Mode: "token", InputPricePerM: 99}); err != nil {
		t.Fatalf("SetModelPrice glm-4.7-flash: %v", err)
	}

	SeedOfficialModelPrices(db)

	codex := GetModelPrice("codex-auto-review")
	if codex == nil || codex.InputPricePerM != 1.25 || codex.OutputPricePerM != 10 || codex.CachedPricePerM != 0.125 {
		t.Fatalf("codex-auto-review price = %#v", codex)
	}
	agnes := GetModelPrice("agnes-2.0-flash")
	if agnes == nil || agnes.InputPricePerM != 0.005 || agnes.OutputPricePerM != 0.015 || agnes.CachedPricePerM != 0.0005 {
		t.Fatalf("agnes-2.0-flash price = %#v", agnes)
	}
	custom := GetModelPrice("glm-4.7-flash")
	if custom == nil || custom.InputPricePerM != 99 {
		t.Fatalf("nonzero operator price overwritten: %#v", custom)
	}
}

func TestOfficialModelPricesCoverVisibleUnpricedModels(t *testing.T) {
	want := map[string]ModelPriceRow{
		"agnes-1.5-flash":            {Mode: "token", InputPricePerM: 0.005, OutputPricePerM: 0.015, CachedPricePerM: 0.0005},
		"agnes-2.0-flash":            {Mode: "token", InputPricePerM: 0.005, OutputPricePerM: 0.015, CachedPricePerM: 0.0005},
		"agnes-image-2.0-flash":      {Mode: "token", InputPricePerM: 0.01, OutputPricePerM: 0.03, CachedPricePerM: 0.001},
		"agnes-image-2.1-flash":      {Mode: "token", InputPricePerM: 0.01, OutputPricePerM: 0.03, CachedPricePerM: 0.001},
		"agnes-video-v2.0":           {Mode: "token", InputPricePerM: 0.02, OutputPricePerM: 0.06, CachedPricePerM: 0.002},
		"codex-auto-review":          {Mode: "token", InputPricePerM: 1.25, OutputPricePerM: 10, CachedPricePerM: 0.125},
		"gemini-3.1-pro":             {Mode: "token", InputPricePerM: 1.25, OutputPricePerM: 10, CachedPricePerM: 0.3125},
		"gemini-3.1-pro-high":        {Mode: "token", InputPricePerM: 1.25, OutputPricePerM: 10, CachedPricePerM: 0.3125},
		"gemini-3.5-flash-extra-low": {Mode: "token", InputPricePerM: 0.15, OutputPricePerM: 0.6, CachedPricePerM: 0.0375},
		"glm-4.7-flash":              {Mode: "token", InputPricePerM: 0.5, OutputPricePerM: 1.5, CachedPricePerM: 0.05},
	}

	got := make(map[string]ModelPriceRow, len(officialModelPrices))
	for _, row := range officialModelPrices {
		got[row.Model] = row
	}
	for model, expected := range want {
		row, ok := got[model]
		if !ok {
			t.Fatalf("missing price seed for visible unpriced model %s", model)
		}
		if seedMode(row) != expected.Mode ||
			row.InputPricePerM != expected.InputPricePerM ||
			row.OutputPricePerM != expected.OutputPricePerM ||
			row.CachedPricePerM != expected.CachedPricePerM ||
			row.PricePerCall != expected.PricePerCall {
			t.Fatalf("%s price = %#v, want %#v", model, row, expected)
		}
	}
}

// p is a tiny helper to take the address of a float64 literal.
func p(v float64) *float64 { return &v }
