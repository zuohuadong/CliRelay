package usage

import (
	"database/sql"
	"math"
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

func TestParseLitellmPricesSkipsResellersAndSample(t *testing.T) {
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
	if _, ok := parsed["gemini-2.5-pro"]; ok {
		t.Fatal("Vertex rates must not replace AI Studio's bare model rates")
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

func TestParseLitellmPricesNormalizesNativeProviders(t *testing.T) {
	models := map[string]string{
		"openai/gpt-6-astra":        "gpt-6-astra",
		"anthropic/claude-opus-5-5": "claude-opus-5-5",
		"gemini/gemini-3.8-flash":   "gemini-3.8-flash",
		"xai/grok-4.7":              "grok-4.7",
		"moonshot/kimi-k2.5":        "kimi-k2.5",
		"deepseek/deepseek-v3.2":    "deepseek-v3.2",
		"zai/glm-5.3":               "glm-5.3",
		"dashscope/qwen3.8-max":     "qwen3.8-max",
		"minimax/MiniMax-M2.5":      "MiniMax-M2.5",
		"meta/muse-spark-1.3":       "muse-spark-1.3",
	}
	raw := litellmPricesFile{}
	for key := range models {
		raw[key] = []byte(`{"input_cost_per_token":0.000002,"output_cost_per_token":0.000006,"mode":"chat"}`)
	}
	for _, key := range []string{"azure/grok-4.7", "dashscope/deepseek-v3.2", "openai/gpt-6-astra:batch", "vertex_ai/us/gemini-3.8-flash", "xai/grok-4.7@fast"} {
		raw[key] = []byte(`{"input_cost_per_token":0.000099,"mode":"chat"}`)
	}
	parsed := parseLitellmPrices(raw)
	if len(parsed) != len(models) {
		t.Fatalf("parsed %d entries, want %d: %#v", len(parsed), len(models), parsed)
	}
	for _, model := range models {
		entry, ok := parsed[model]
		if !ok || entry.InputCostPerToken == nil || *entry.InputCostPerToken != 0.000002 {
			t.Fatalf("native price missing or replaced for %s: %#v", model, entry)
		}
	}
}

func TestParseLitellmPricesBareModelsAlwaysWin(t *testing.T) {
	raw := litellmPricesFile{
		"gpt-6-sol":         []byte(`{"input_cost_per_token":0.000002,"mode":"chat"}`),
		"openai/gpt-6-sol":  []byte(`{"input_cost_per_token":0.000099,"mode":"chat"}`),
		"zai/glm-4.7-flash": []byte(`{"input_cost_per_token":0.000001,"mode":"chat"}`),
	}
	for range 50 {
		parsed := parseLitellmPrices(raw)
		if len(parsed) != 2 || *parsed["gpt-6-sol"].InputCostPerToken != 0.000002 ||
			*parsed["glm-4.7-flash"].InputCostPerToken != 0.000001 {
			t.Fatalf("bare prices must win regardless of map iteration: %#v", parsed)
		}
	}
}

func TestLitellmEntryHasPriceRejectsInvalidRates(t *testing.T) {
	for _, entry := range []litellmModelEntry{
		{},
		{InputCostPerToken: p(-1)},
		{OutputCostPerToken: p(math.NaN())},
		{OutputCostPerToken: p(math.Inf(1))},
		{InputCostPerToken: p(1), CacheReadInputTokenCost: p(-1)},
	} {
		if litellmEntryHasPrice(&entry) {
			t.Fatalf("invalid rate accepted: %#v", entry)
		}
	}
}

func TestLitellmEntryToPriceRowReasoningAndCacheFallbacks(t *testing.T) {
	tests := []struct {
		name  string
		entry litellmModelEntry
		out   float64
		cache float64
	}{
		{"separate reasoning is not added", litellmModelEntry{InputCostPerToken: p(0.000002), OutputCostPerToken: p(0.00001), OutputCostPerReasoningToken: p(0.00001), CacheCreationInputTokenCost: p(0.000004)}, 10, 2},
		{"reasoning-only output", litellmModelEntry{InputCostPerToken: p(0.000002), OutputCostPerReasoningToken: p(0.00001)}, 10, 2},
		{"zero cache read is preserved", litellmModelEntry{InputCostPerToken: p(0.000002), OutputCostPerToken: p(0.00001), CacheReadInputTokenCost: p(0)}, 10, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := litellmEntryToPriceRow("test-model", &tt.entry, "")
			if row.OutputPricePerM != tt.out || row.CachedPricePerM != tt.cache {
				t.Fatalf("converted price = %#v, want output=%f cache=%f", row, tt.out, tt.cache)
			}
		})
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

func TestSeedOfficialModelPricesRefreshesPreviousBuiltInPrices(t *testing.T) {
	db := newTestPricesDB(t)
	modelPricesSeedingMu.Lock()
	modelPricesSeededDone = false
	modelPricesSeedingMu.Unlock()

	if err := SetModelPrice(ModelPriceRow{Model: "claude-fable-5", Mode: "token", InputPricePerM: 5, OutputPricePerM: 25, CachedPricePerM: 0.5}); err != nil {
		t.Fatalf("SetModelPrice claude-fable-5: %v", err)
	}
	if err := SetModelPrice(ModelPriceRow{Model: "grok-4.6", Mode: "token", InputPricePerM: 99, OutputPricePerM: 99, CachedPricePerM: 99}); err != nil {
		t.Fatalf("SetModelPrice grok-4.6: %v", err)
	}
	if err := SetModelPrice(ModelPriceRow{Model: "grok-4.3", Mode: "locked", InputPricePerM: 3, OutputPricePerM: 15, CachedPricePerM: 0.3}); err != nil {
		t.Fatalf("SetModelPrice grok-4.3: %v", err)
	}

	SeedOfficialModelPrices(db)

	fable := GetModelPrice("claude-fable-5")
	if fable == nil || fable.InputPricePerM != 10 || fable.OutputPricePerM != 50 || fable.CachedPricePerM != 1 {
		t.Fatalf("claude-fable-5 price = %#v", fable)
	}
	custom := GetModelPrice("grok-4.6")
	if custom == nil || custom.InputPricePerM != 99 {
		t.Fatalf("custom price overwritten: %#v", custom)
	}
	locked := GetModelPrice("grok-4.3")
	if locked == nil || locked.Mode != "locked" || locked.InputPricePerM != 3 {
		t.Fatalf("locked price overwritten: %#v", locked)
	}
}

func TestSeedOfficialModelPricesRefreshesRecentAndFreePrices(t *testing.T) {
	db := newTestPricesDB(t)
	initial := []ModelPriceRow{
		{Model: "gemini-3.7-flash", Mode: "token", InputPricePerM: 0.75, OutputPricePerM: 7.5, CachedPricePerM: 0.075},
		{Model: "deepseek-v4-flash", Mode: "token", InputPricePerM: 0.14, OutputPricePerM: 0.28, CachedPricePerM: 0.0028},
		{Model: "glm-4.7-flash", Mode: "token", InputPricePerM: 0.5, OutputPricePerM: 1.5, CachedPricePerM: 0.05},
		{Model: "kimi-k2.5", Mode: "token", InputPricePerM: 99},
		{Model: "gpt-6-astra", Mode: "LOCKED"},
	}
	for _, row := range initial {
		if err := SetModelPrice(row); err != nil {
			t.Fatalf("SetModelPrice %s: %v", row.Model, err)
		}
	}
	modelPricesSeedingMu.Lock()
	modelPricesSeededDone = false
	modelPricesSeedingMu.Unlock()
	SeedOfficialModelPrices(db)
	for _, want := range []ModelPriceRow{
		{Model: "gemini-3.7-flash", Mode: "token", InputPricePerM: 0.75, OutputPricePerM: 3.75, CachedPricePerM: 0.075},
		{Model: "deepseek-v4-flash", Mode: "token", InputPricePerM: 0.3, OutputPricePerM: 1.2, CachedPricePerM: 0.006},
		{Model: "glm-4.7-flash", Mode: "token", InputPricePerM: 0.5, OutputPricePerM: 1.5, CachedPricePerM: 0.05},
		initial[3],
		initial[4],
	} {
		got := GetModelPrice(want.Model)
		if got == nil {
			t.Fatalf("missing model price %s", want.Model)
		}
		got.UpdatedAt = ""
		if *got != want {
			t.Fatalf("price = %#v, want %#v", *got, want)
		}
	}
}

func TestOfficialModelPricesCoverOctoberCatalog(t *testing.T) {
	want := map[string]ModelPriceRow{
		"gpt-6-astra":                {Mode: "token", InputPricePerM: 10, OutputPricePerM: 50, CachedPricePerM: 1},
		"gpt-6-sol":                  {Mode: "token", InputPricePerM: 2, OutputPricePerM: 10, CachedPricePerM: 0.2},
		"gpt-6-luna":                 {Mode: "token", InputPricePerM: 0.1, OutputPricePerM: 0.5, CachedPricePerM: 0.01},
		"gpt-6.1-sol":                {Mode: "token", InputPricePerM: 2, OutputPricePerM: 10, CachedPricePerM: 0.1},
		"claude-fable-5-1":           {Mode: "token", InputPricePerM: 10, OutputPricePerM: 50, CachedPricePerM: 0.25},
		"claude-opus-5-5":            {Mode: "token", InputPricePerM: 4, OutputPricePerM: 20, CachedPricePerM: 0.2},
		"claude-sonnet-5-5":          {Mode: "token", InputPricePerM: 2, OutputPricePerM: 10, CachedPricePerM: 0.1},
		"gemini-3.8-flash":           {Mode: "token", InputPricePerM: 0.75, OutputPricePerM: 3.75, CachedPricePerM: 0.075},
		"gemini-3.8-flash-high":      {Mode: "token", InputPricePerM: 0.75, OutputPricePerM: 3.75, CachedPricePerM: 0.075},
		"grok-4.7":                   {Mode: "token", InputPricePerM: 2, OutputPricePerM: 6, CachedPricePerM: 0.5},
		"muse-spark-1.1":             {Mode: "token", InputPricePerM: 1.25, OutputPricePerM: 4.25, CachedPricePerM: 0.15},
		"muse-spark-1.2":             {Mode: "token", InputPricePerM: 1.25, OutputPricePerM: 4.25, CachedPricePerM: 0.15},
		"muse-spark-1.2-contributor": {Mode: "token", InputPricePerM: 0.1, OutputPricePerM: 0.2, CachedPricePerM: 0.002},
		"muse-spark-1.3":             {Mode: "token", InputPricePerM: 1.25, OutputPricePerM: 4.25, CachedPricePerM: 0.15},
		"muse-spark-1.3-contributor": {Mode: "token", InputPricePerM: 0.1, OutputPricePerM: 0.2, CachedPricePerM: 0.002},
		"kimi-k2.5":                  {Mode: "token", InputPricePerM: 0.6, OutputPricePerM: 3, CachedPricePerM: 0.1},
		"deepseek-v3.2":              {Mode: "token", InputPricePerM: 0.28, OutputPricePerM: 0.4, CachedPricePerM: 0.028},
		"MiniMax-M2.5":               {Mode: "token", InputPricePerM: 0.3, OutputPricePerM: 1.2, CachedPricePerM: 0.03},
		"glm-5":                      {Mode: "token", InputPricePerM: 1, OutputPricePerM: 3.2, CachedPricePerM: 0.2},
		"glm-5.1":                    {Mode: "token", InputPricePerM: 1.4, OutputPricePerM: 4.4, CachedPricePerM: 0.26},
	}
	seen := make(map[string]bool, len(officialModelPrices))
	for _, row := range officialModelPrices {
		if seen[row.Model] {
			t.Fatalf("duplicate price seed %s", row.Model)
		}
		seen[row.Model] = true
		if expected, ok := want[row.Model]; ok {
			expected.Model = row.Model
			if row != expected {
				t.Fatalf("price = %#v, want %#v", row, expected)
			}
			delete(want, row.Model)
		}
	}
	if len(want) > 0 {
		t.Fatalf("missing October price seeds: %#v", want)
	}
	for _, model := range []string{"kimi-k2.8", "kimi-k2.8-code", "grok-4.7-build-fast"} {
		if seen[model] {
			t.Fatalf("unpublished price must not be inferred for %s", model)
		}
	}
}

func TestOfficialModelPricesCoverVisibleUnpricedModels(t *testing.T) {
	want := map[string]ModelPriceRow{
		"agnes-1.5-flash":               {Mode: "token", InputPricePerM: 0.005, OutputPricePerM: 0.015, CachedPricePerM: 0.0005},
		"agnes-2.0-flash":               {Mode: "token", InputPricePerM: 0.005, OutputPricePerM: 0.015, CachedPricePerM: 0.0005},
		"agnes-image-2.0-flash":         {Mode: "token", InputPricePerM: 0.01, OutputPricePerM: 0.03, CachedPricePerM: 0.001},
		"agnes-image-2.1-flash":         {Mode: "token", InputPricePerM: 0.01, OutputPricePerM: 0.03, CachedPricePerM: 0.001},
		"agnes-video-v2.0":              {Mode: "token", InputPricePerM: 0.02, OutputPricePerM: 0.06, CachedPricePerM: 0.002},
		"codex-auto-review":             {Mode: "token", InputPricePerM: 1.25, OutputPricePerM: 10, CachedPricePerM: 0.125},
		"claude-fable-5":                {Mode: "token", InputPricePerM: 10, OutputPricePerM: 50, CachedPricePerM: 1},
		"claude-haiku-4-5":              {Mode: "token", InputPricePerM: 1, OutputPricePerM: 5, CachedPricePerM: 0.1},
		"claude-opus-5":                 {Mode: "token", InputPricePerM: 5, OutputPricePerM: 25, CachedPricePerM: 0.5},
		"claude-sonnet-5":               {Mode: "token", InputPricePerM: 2, OutputPricePerM: 10, CachedPricePerM: 0.2},
		"deepseek-v4-flash":             {Mode: "token", InputPricePerM: 0.3, OutputPricePerM: 1.2, CachedPricePerM: 0.006},
		"deepseek-v4-pro":               {Mode: "token", InputPricePerM: 1.32, OutputPricePerM: 3.96, CachedPricePerM: 0.044},
		"gemini-3.1-pro":                {Mode: "token", InputPricePerM: 2, OutputPricePerM: 12, CachedPricePerM: 0.2},
		"gemini-3.1-pro-high":           {Mode: "token", InputPricePerM: 2, OutputPricePerM: 12, CachedPricePerM: 0.2},
		"gemini-3.5-flash":              {Mode: "token", InputPricePerM: 1.5, OutputPricePerM: 9, CachedPricePerM: 0.15},
		"gemini-3.5-flash-extra-low":    {Mode: "token", InputPricePerM: 1.5, OutputPricePerM: 9, CachedPricePerM: 0.15},
		"gemini-3.5-flash-lite":         {Mode: "token", InputPricePerM: 0.3, OutputPricePerM: 2.5, CachedPricePerM: 0.03},
		"gemini-3.6-flash":              {Mode: "token", InputPricePerM: 0.75, OutputPricePerM: 3.75, CachedPricePerM: 0.075},
		"gemini-3.7-flash":              {Mode: "token", InputPricePerM: 0.75, OutputPricePerM: 3.75, CachedPricePerM: 0.075},
		"gemini-3.7-flash-high":         {Mode: "token", InputPricePerM: 0.75, OutputPricePerM: 3.75, CachedPricePerM: 0.075},
		"gemini-3-pro-image":            {Mode: "call", PricePerCall: 0.0011},
		"gpt-5.6-luna":                  {Mode: "token", InputPricePerM: 0.2, OutputPricePerM: 1.2, CachedPricePerM: 0.02},
		"gpt-5.6-sol":                   {Mode: "token", InputPricePerM: 4, OutputPricePerM: 20, CachedPricePerM: 0.4},
		"gpt-5.6-terra":                 {Mode: "token", InputPricePerM: 2, OutputPricePerM: 12, CachedPricePerM: 0.2},
		"gpt-image-1.5":                 {Mode: "call", PricePerCall: 0.04},
		"gpt-image-2-vip":               {Mode: "call", PricePerCall: 0.04},
		"glm-4.7-flash":                 {Mode: "token", InputPricePerM: 0.5, OutputPricePerM: 1.5, CachedPricePerM: 0.05},
		"glm-5.2":                       {Mode: "token", InputPricePerM: 1.4, OutputPricePerM: 4.4, CachedPricePerM: 0.26},
		"grok-4.5":                      {Mode: "token", InputPricePerM: 2, OutputPricePerM: 6, CachedPricePerM: 0.3},
		"grok-4.6":                      {Mode: "token", InputPricePerM: 2, OutputPricePerM: 6, CachedPricePerM: 0.5},
		"imagen-4.0-fast-generate-001":  {Mode: "call", PricePerCall: 0.02},
		"imagen-4.0-generate-001":       {Mode: "call", PricePerCall: 0.04},
		"imagen-4.0-ultra-generate-001": {Mode: "call", PricePerCall: 0.06},
		"kimi-k2.7-code":                {Mode: "token", InputPricePerM: 0.95, OutputPricePerM: 4, CachedPricePerM: 0.19},
		"kimi-k2.7-code-highspeed":      {Mode: "token", InputPricePerM: 1.9, OutputPricePerM: 8, CachedPricePerM: 0.38},
		"kimi-k3":                       {Mode: "token", InputPricePerM: 3, OutputPricePerM: 15, CachedPricePerM: 0.3},
		"kimi-k3-256k":                  {Mode: "token", InputPricePerM: 3, OutputPricePerM: 15, CachedPricePerM: 0.3},
		"kimi-k2.6":                     {Mode: "token", InputPricePerM: 0.95, OutputPricePerM: 4, CachedPricePerM: 0.16},
		"spark-x2-flash":                {Mode: "call", PricePerCall: 0.02},
		"xopdeepseekv4pro":              {Mode: "token", InputPricePerM: 1.32, OutputPricePerM: 3.96, CachedPricePerM: 0.044},
		"xopglm52":                      {Mode: "token", InputPricePerM: 1.4, OutputPricePerM: 4.4, CachedPricePerM: 0.26},
		"xopkimik26":                    {Mode: "token", InputPricePerM: 0.95, OutputPricePerM: 4, CachedPricePerM: 0.16},
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
