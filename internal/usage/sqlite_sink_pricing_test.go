package usage

import (
	"context"
	"path/filepath"
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// TestSqliteSinkComputesCostFromActualModel verifies that the sink writes a
// non-zero cost derived from the upstream model name in the usage record, not
// from the client alias. This is the core "bill by actual model" guarantee.
func TestSqliteSinkComputesCostFromActualModel(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test_usage.db")
	defaultSink.SetPath(dbPath)
	db := getDB()
	if db == nil {
		t.Fatal("expected non-nil db")
	}
	EnsureModelPricesTable(db)

	// Configure pricing for the upstream model (the one that actually ran).
	if err := SetModelPrice(ModelPriceRow{
		Model:           "glm-5.1",
		Mode:            "token",
		InputPricePerM:  0.5,
		OutputPricePerM: 1.5,
	}); err != nil {
		t.Fatalf("SetModelPrice: %v", err)
	}

	// A request came in as alias "gpt-5.3-codex" but was routed to glm-5.1.
	// The sink must bill glm-5.1, not gpt-5.3-codex.
	defaultSink.HandleUsage(context.Background(), coreusage.Record{
		Model:  "glm-5.1",
		Alias:  "gpt-5.3-codex",
		Detail: coreusage.Detail{InputTokens: 1000, OutputTokens: 500, TotalTokens: 1500},
	})

	var model string
	var cost float64
	err := db.QueryRow(`SELECT model, cost FROM request_logs ORDER BY id DESC LIMIT 1`).Scan(&model, &cost)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if model != "glm-5.1" {
		t.Fatalf("model = %q, want glm-5.1", model)
	}
	// input: 1000/1M * 0.5 = 0.0005, output: 500/1M * 1.5 = 0.00075 => 0.00125
	expected := 0.00125
	if cost < expected-0.0000001 || cost > expected+0.0000001 {
		t.Fatalf("cost = %f, want %f", cost, expected)
	}
}

func TestSqliteSinkFailedRequestHasZeroCost(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test_usage.db")
	defaultSink.SetPath(dbPath)
	db := getDB()
	if db == nil {
		t.Fatal("expected non-nil db")
	}
	EnsureModelPricesTable(db)
	if err := SetModelPrice(ModelPriceRow{Model: "glm-5.1", Mode: "token", InputPricePerM: 0.5}); err != nil {
		t.Fatalf("SetModelPrice: %v", err)
	}

	// Failed requests consume resources but we bill them as 0 by convention.
	defaultSink.HandleUsage(context.Background(), coreusage.Record{
		Model:  "glm-5.1",
		Failed: true,
		Detail: coreusage.Detail{InputTokens: 1000, OutputTokens: 500, TotalTokens: 1500},
	})

	var cost float64
	if err := db.QueryRow(`SELECT cost FROM request_logs ORDER BY id DESC LIMIT 1`).Scan(&cost); err != nil {
		t.Fatalf("query: %v", err)
	}
	if cost != 0 {
		t.Fatalf("failed request cost = %f, want 0", cost)
	}
}

func TestSqliteSinkUnpricedModelHasZeroCost(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test_usage.db")
	defaultSink.SetPath(dbPath)
	db := getDB()
	if db == nil {
		t.Fatal("expected non-nil db")
	}
	EnsureModelPricesTable(db)

	defaultSink.HandleUsage(context.Background(), coreusage.Record{
		Model:  "totally-unknown-model",
		Detail: coreusage.Detail{InputTokens: 1000, OutputTokens: 500, TotalTokens: 1500},
	})

	var cost float64
	if err := db.QueryRow(`SELECT cost FROM request_logs ORDER BY id DESC LIMIT 1`).Scan(&cost); err != nil {
		t.Fatalf("query: %v", err)
	}
	if cost != 0 {
		t.Fatalf("unpriced model cost = %f, want 0", cost)
	}
}
