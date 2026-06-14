package usage

import (
	"path/filepath"
	"testing"
)

func TestCalculateModelCostTokenMode(t *testing.T) {
	// Setup in-memory DB with pricing
	dbPath := filepath.Join(t.TempDir(), "test_usage.db")
	defaultSink.SetPath(dbPath)
	db := getDB()
	if db == nil {
		t.Fatal("expected non-nil db")
	}
	EnsureModelPricesTable(db)

	// Set pricing: $3/M input, $15/M output, $1.50/M cached
	err := SetModelPrice(ModelPriceRow{
		Model:           "gpt-4",
		Mode:            "token",
		InputPricePerM:  3.0,
		OutputPricePerM: 15.0,
		CachedPricePerM: 1.5,
	})
	if err != nil {
		t.Fatalf("SetModelPrice: %v", err)
	}

	cost := CalculateModelCost("gpt-4", 1000, 500, 200, 300)
	// input: (1000-300)/1M * 3 = 0.0021
	// output: (500+200)/1M * 15 = 0.0105
	// cached: 300/1M * 1.5 = 0.00045
	// total = 0.0021 + 0.0105 + 0.00045 = 0.01305
	expected := 0.01305
	if cost < expected-0.000001 || cost > expected+0.000001 {
		t.Fatalf("cost = %f, want %f", cost, expected)
	}
}

func TestCalculateModelCostCallMode(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test_usage.db")
	defaultSink.SetPath(dbPath)
	db := getDB()
	if db == nil {
		t.Fatal("expected non-nil db")
	}
	EnsureModelPricesTable(db)

	err := SetModelPrice(ModelPriceRow{
		Model:        "dall-e-3",
		Mode:         "call",
		PricePerCall: 0.04,
	})
	if err != nil {
		t.Fatalf("SetModelPrice: %v", err)
	}

	cost := CalculateModelCost("dall-e-3", 0, 0, 0, 0)
	if cost != 0.04 {
		t.Fatalf("cost = %f, want 0.04", cost)
	}
}

func TestCalculateModelCostNoPricing(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test_usage.db")
	defaultSink.SetPath(dbPath)
	db := getDB()
	if db == nil {
		t.Fatal("expected non-nil db")
	}
	EnsureModelPricesTable(db)

	cost := CalculateModelCost("unknown-model", 1000, 500, 0, 0)
	if cost != 0 {
		t.Fatalf("cost = %f, want 0 for unconfigured model", cost)
	}
}

func TestLookupAPIKeyName(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test_usage.db")
	defaultSink.SetPath(dbPath)
	db := getDB()
	if db == nil {
		t.Fatal("expected non-nil db")
	}
	EnsureAPIKeysTable(db)

	// Insert a test key
	_, err := db.Exec(`INSERT INTO api_keys (key, name) VALUES (?, ?)`, "sk-test123", "My Test Key")
	if err != nil {
		t.Fatalf("insert test key: %v", err)
	}

	name := LookupAPIKeyName("sk-test123")
	if name != "My Test Key" {
		t.Fatalf("name = %q, want %q", name, "My Test Key")
	}

	// Non-existent key
	name = LookupAPIKeyName("nonexistent")
	if name != "" {
		t.Fatalf("name = %q, want empty for unknown key", name)
	}
}

func TestListModelPrices(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test_usage.db")
	defaultSink.SetPath(dbPath)
	db := getDB()
	if db == nil {
		t.Fatal("expected non-nil db")
	}
	EnsureModelPricesTable(db)

	_ = SetModelPrice(ModelPriceRow{Model: "model-a", Mode: "token", InputPricePerM: 1.0, OutputPricePerM: 2.0})
	_ = SetModelPrice(ModelPriceRow{Model: "model-b", Mode: "call", PricePerCall: 0.05})

	prices := ListModelPrices()
	if len(prices) != 2 {
		t.Fatalf("ListModelPrices returned %d entries, want 2", len(prices))
	}

	found := map[string]bool{}
	for _, p := range prices {
		found[p.Model] = true
	}
	if !found["model-a"] || !found["model-b"] {
		t.Fatal("expected both model-a and model-b in results")
	}
}

func TestDeleteModelPrice(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test_usage.db")
	defaultSink.SetPath(dbPath)
	db := getDB()
	if db == nil {
		t.Fatal("expected non-nil db")
	}
	EnsureModelPricesTable(db)

	_ = SetModelPrice(ModelPriceRow{Model: "temp-model", Mode: "token", InputPricePerM: 5.0})

	err := DeleteModelPrice("temp-model")
	if err != nil {
		t.Fatalf("DeleteModelPrice: %v", err)
	}

	cost := CalculateModelCost("temp-model", 1000, 500, 0, 0)
	if cost != 0 {
		t.Fatalf("cost after delete = %f, want 0", cost)
	}
}
