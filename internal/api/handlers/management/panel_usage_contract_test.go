package management

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "modernc.org/sqlite"
)

func newUsageContractTestHandler(t *testing.T) *Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data dir: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "usage.db"))
	if err != nil {
		t.Fatalf("open usage db: %v", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.Exec(`create table api_keys (
		key text not null primary key,
		name text not null default ''
	);
	create table request_logs (
		id integer primary key,
		timestamp text not null,
		api_key text,
		api_key_name text,
		model text,
		source text,
		channel_name text,
		auth_index text,
		failed integer,
		latency_ms integer,
		first_token_ms integer,
		input_tokens integer,
		output_tokens integer,
		reasoning_tokens integer,
		cached_tokens integer,
		total_tokens integer,
		cost real,
		input_content text default '',
		output_content text default ''
	);
	create table request_log_content (
		log_id integer primary key,
		input_content text,
		output_content text,
		detail_content text
	);`)
	if err != nil {
		t.Fatalf("create usage schema: %v", err)
	}
	_, err = db.Exec(`insert into api_keys (key, name) values ('sk-a', 'Primary'), ('sk-b', 'Secondary');
	insert into request_logs (
		id, timestamp, api_key, api_key_name, model, source, channel_name, auth_index, failed,
		latency_ms, first_token_ms, input_tokens, output_tokens, reasoning_tokens, cached_tokens,
		total_tokens, cost, input_content, output_content
	) values
		(1, datetime('now', '-1 day'), 'sk-a', '', 'gpt-5', 'codex', 'Codex', 'auth-a', 0, 1200, 100, 10, 20, 3, 4, 37, 0.12, 'legacy input', 'legacy output'),
		(2, datetime('now', '-1 day'), 'sk-a', 'Stale Primary', 'gpt-5', 'codex', 'Codex', 'auth-a', 1, 500, 50, 1, 2, 0, 0, 3, 0.01, '', ''),
		(3, datetime('now', '-20 day'), 'sk-b', '', 'old-model', 'web', 'Web', 'auth-b', 0, 300, 30, 5, 6, 0, 0, 11, 0.02, '', '');
	insert into request_log_content (log_id, input_content, output_content, detail_content)
	values (1, 'client body', 'server body', 'debug details');`)
	if err != nil {
		t.Fatalf("seed usage data: %v", err)
	}

	return &Handler{
		cfg:            &config.Config{},
		configFilePath: filepath.Join(dir, "config.yaml"),
	}
}

func performUsageContractRequest(t *testing.T, method, target string, body []byte, params gin.Params, handler gin.HandlerFunc) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	ctx.Request = req
	ctx.Params = params
	handler(ctx)
	if rec.Body.Len() == 0 {
		return rec.Code, map[string]any{}
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response body %q: %v", rec.Body.String(), err)
	}
	return rec.Code, payload
}

func TestUsageChartDataContract(t *testing.T) {
	h := newUsageContractTestHandler(t)
	status, payload := performUsageContractRequest(t, http.MethodGet, "/v0/management/usage/chart-data?days=7&api_key=sk-a", nil, nil, h.GetUsageChartData)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	stats := payload["stats"].(map[string]any)
	if stats["total"].(float64) != 2 || stats["total_tokens"].(float64) != 40 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
	daily := payload["daily_series"].([]any)
	if len(daily) != 1 {
		t.Fatalf("daily len = %d, want 1", len(daily))
	}
	point := daily[0].(map[string]any)
	if point["requests"].(float64) != 2 || point["failed_requests"].(float64) != 1 || point["input_tokens"].(float64) != 11 {
		t.Fatalf("unexpected daily point: %#v", point)
	}
	models := payload["model_distribution"].([]any)
	if len(models) != 1 || models[0].(map[string]any)["requests"].(float64) != 2 {
		t.Fatalf("unexpected model distribution: %#v", models)
	}
	apiKeys := payload["apikey_distribution"].([]any)
	if len(apiKeys) != 1 || apiKeys[0].(map[string]any)["api_key"] != "sk-a" || apiKeys[0].(map[string]any)["name"] != "Primary" {
		t.Fatalf("unexpected api key distribution: %#v", apiKeys)
	}
}

func TestUsageLogsContractFiltersAndContentFlag(t *testing.T) {
	h := newUsageContractTestHandler(t)

	status, failedPayload := performUsageContractRequest(t, http.MethodGet, "/v0/management/usage/logs?days=7&status=failed", nil, nil, h.GetUsageLogs)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	failedItems := failedPayload["items"].([]any)
	if len(failedItems) != 1 {
		t.Fatalf("failed items len = %d, want 1", len(failedItems))
	}
	failedItem := failedItems[0].(map[string]any)
	if failedItem["id"].(float64) != 2 || failedItem["api_key_name"] != "Primary" || failedItem["failed"] != true || failedItem["has_content"] != false {
		t.Fatalf("unexpected failed item: %#v", failedItem)
	}

	status, successPayload := performUsageContractRequest(t, http.MethodGet, "/v0/management/usage/logs?days=7&status=success", nil, nil, h.GetUsageLogs)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	successItems := successPayload["items"].([]any)
	if len(successItems) != 1 {
		t.Fatalf("success items len = %d, want 1", len(successItems))
	}
	successItem := successItems[0].(map[string]any)
	if successItem["id"].(float64) != 1 || successItem["api_key_name"] != "Primary" || successItem["has_content"] != true {
		t.Fatalf("unexpected success item: %#v", successItem)
	}
	filters := successPayload["filters"].(map[string]any)
	names := filters["api_key_names"].(map[string]any)
	if names["sk-a"] != "Primary" {
		t.Fatalf("api key names = %#v", names)
	}
}

func TestUsageLogContentSupportsPartAndPublicAPIKeyScope(t *testing.T) {
	h := newUsageContractTestHandler(t)
	params := gin.Params{{Key: "id", Value: "1"}}

	status, privatePayload := performUsageContractRequest(t, http.MethodGet, "/v0/management/usage/logs/1/content?part=input", nil, params, h.GetUsageLogContent)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if privatePayload["part"] != "input" || privatePayload["content"] != "client body" {
		t.Fatalf("unexpected private content payload: %#v", privatePayload)
	}

	status, publicPayload := performUsageContractRequest(t, http.MethodPost, "/v0/management/public/usage/logs/1/content", []byte(`{"api_key":"sk-a","part":"output"}`), params, h.GetPublicUsageLogContent)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if publicPayload["part"] != "output" || publicPayload["content"] != "server body" {
		t.Fatalf("unexpected public content payload: %#v", publicPayload)
	}

	status, _ = performUsageContractRequest(t, http.MethodPost, "/v0/management/public/usage/logs/1/content", []byte(`{"api_key":"sk-b","part":"output"}`), params, h.GetPublicUsageLogContent)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want %d for mismatched api key", status, http.StatusNotFound)
	}
}

func TestDeleteUsageLogsClearsContentThenRecords(t *testing.T) {
	h := newUsageContractTestHandler(t)

	status, clearPayload := performUsageContractRequest(t, http.MethodDelete, "/v0/management/usage/logs", []byte(`{"clear_body_content":true,"clear_detail_content":true,"clear_request_records":false}`), nil, h.DeleteUsageLogs)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if clearPayload["cleared_body_rows"].(float64) != 1 || clearPayload["cleared_detail_rows"].(float64) != 1 || clearPayload["deleted_logs"].(float64) != 0 {
		t.Fatalf("unexpected clear payload: %#v", clearPayload)
	}

	status, contentPayload := performUsageContractRequest(t, http.MethodGet, "/v0/management/usage/logs/1/content?part=details", nil, gin.Params{{Key: "id", Value: "1"}}, h.GetUsageLogContent)
	if status != http.StatusOK || contentPayload["content"] != "" {
		t.Fatalf("content after clear status=%d payload=%#v", status, contentPayload)
	}

	status, deletePayload := performUsageContractRequest(t, http.MethodDelete, "/v0/management/usage/logs", []byte(`{"clear_body_content":true,"clear_detail_content":true,"clear_request_records":true}`), nil, h.DeleteUsageLogs)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if deletePayload["deleted_logs"].(float64) != 3 || deletePayload["deleted_contents"].(float64) != 1 {
		t.Fatalf("unexpected delete payload: %#v", deletePayload)
	}
}

func TestDashboardSummaryUsesUsageAggregation(t *testing.T) {
	h := newUsageContractTestHandler(t)
	status, payload := performUsageContractRequest(t, http.MethodGet, "/v0/management/dashboard-summary?days=7", nil, nil, h.GetDashboardSummary)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	kpi := payload["kpi"].(map[string]any)
	if kpi["total_requests"].(float64) != 2 || kpi["success_requests"].(float64) != 1 || kpi["failed_requests"].(float64) != 1 || kpi["total_tokens"].(float64) != 40 {
		t.Fatalf("unexpected dashboard kpi: %#v", kpi)
	}
	trends := payload["trends"].(map[string]any)
	if len(trends["request_volume"].([]any)) != 1 || len(trends["throughput_series"].([]any)) != 1 {
		t.Fatalf("unexpected dashboard trends: %#v", trends)
	}
}

func TestPublicUsageSummaryContract(t *testing.T) {
	h := newUsageContractTestHandler(t)
	status, payload := performUsageContractRequest(t, http.MethodPost, "/v0/management/public/usage", []byte(`{"api_key":"sk-a","days":7}`), nil, h.GetPublicUsageSummary)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if payload["found"] != true {
		t.Fatalf("found = %#v, want true", payload["found"])
	}
	usage := payload["usage"].(map[string]any)
	if usage["total_requests"].(float64) != 2 || usage["total_tokens"].(float64) != 40 {
		t.Fatalf("unexpected public usage payload: %#v", usage)
	}
}
