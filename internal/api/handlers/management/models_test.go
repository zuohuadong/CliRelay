package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

func initManagementModelsTestDB(t *testing.T) {
	t.Helper()
	usage.CloseDB()
	dbPath := filepath.Join(t.TempDir(), "usage.db")
	if err := usage.InitDB(dbPath, config.RequestLogStorageConfig{}, time.UTC); err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	t.Cleanup(usage.CloseDB)
}

func performModelsRequest(method string, path string, body []byte, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	if body == nil {
		c.Request = httptest.NewRequest(method, path, nil)
	} else {
		c.Request = httptest.NewRequest(method, path, bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
	}
	handler(c)
	return rec
}

func TestModelConfigHandlersCreateListAndDelete(t *testing.T) {
	initManagementModelsTestDB(t)
	h := NewHandler(&config.Config{}, "", nil)

	createBody := []byte(`{
		"id": "custom-image",
		"owned_by": "acme-ai",
		"description": "Custom image model",
		"enabled": true,
		"pricing": {
			"mode": "call",
			"price_per_call": 0.12
		}
	}`)
	createRec := performModelsRequest(http.MethodPost, "/model-configs", createBody, h.PostModelConfig)
	if createRec.Code != http.StatusOK {
		t.Fatalf("PostModelConfig status = %d body = %s", createRec.Code, createRec.Body.String())
	}

	listRec := performModelsRequest(http.MethodGet, "/model-configs", nil, h.GetModelConfigs)
	if listRec.Code != http.StatusOK {
		t.Fatalf("GetModelConfigs status = %d body = %s", listRec.Code, listRec.Body.String())
	}
	var listPayload struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
			Pricing struct {
				Mode         string  `json:"mode"`
				PricePerCall float64 `json:"price_per_call"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listPayload); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	found := false
	for _, item := range listPayload.Data {
		if item.ID == "custom-image" {
			found = true
			if item.OwnedBy != "acme-ai" || item.Pricing.Mode != "call" || item.Pricing.PricePerCall != 0.12 {
				t.Fatalf("unexpected custom-image payload: %+v", item)
			}
		}
	}
	if !found {
		t.Fatal("expected custom-image in list response")
	}

	deleteRec := performModelsRequest(http.MethodDelete, "/model-configs/custom-image", nil, func(c *gin.Context) {
		c.Params = gin.Params{{Key: "id", Value: "custom-image"}}
		h.DeleteModelConfig(c)
	})
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("DeleteModelConfig status = %d body = %s", deleteRec.Code, deleteRec.Body.String())
	}
	if _, ok := usage.GetModelConfig("custom-image"); ok {
		t.Fatal("expected custom-image to be deleted")
	}
}

func TestModelConfigHandlersScopeFiltering(t *testing.T) {
	initManagementModelsTestDB(t)
	h := NewHandler(&config.Config{}, "", nil)

	createBody := []byte(`{
		"id": "custom-active",
		"owned_by": "acme-ai",
		"description": "Custom active model",
		"enabled": true,
		"pricing": {
			"mode": "token",
			"input_price_per_million": 1,
			"output_price_per_million": 2
		}
	}`)
	createRec := performModelsRequest(http.MethodPost, "/model-configs", createBody, h.PostModelConfig)
	if createRec.Code != http.StatusOK {
		t.Fatalf("PostModelConfig status = %d body = %s", createRec.Code, createRec.Body.String())
	}

	createLibraryBody := []byte(`{
		"id": "custom-library",
		"owned_by": "acme-ai",
		"description": "Custom library model",
		"enabled": true,
		"pricing": {
			"mode": "token",
			"input_price_per_million": 3,
			"output_price_per_million": 4
		}
	}`)
	createLibraryRec := performModelsRequest(http.MethodPost, "/model-configs?scope=library", createLibraryBody, h.PostModelConfig)
	if createLibraryRec.Code != http.StatusOK {
		t.Fatalf("PostModelConfig library status = %d body = %s", createLibraryRec.Code, createLibraryRec.Body.String())
	}

	decodeSources := func(rec *httptest.ResponseRecorder) map[string]string {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("list status = %d body = %s", rec.Code, rec.Body.String())
		}
		var payload struct {
			Data []struct {
				ID     string `json:"id"`
				Source string `json:"source"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal list response: %v", err)
		}
		sources := make(map[string]string)
		for _, item := range payload.Data {
			sources[item.ID] = item.Source
		}
		return sources
	}

	activeSources := decodeSources(performModelsRequest(http.MethodGet, "/model-configs", nil, h.GetModelConfigs))
	if activeSources["custom-active"] != "user" {
		t.Fatal("expected custom-active in default active scope")
	}
	if _, ok := activeSources["gpt-image-2"]; ok {
		t.Fatal("did not expect seed-only gpt-image-2 in default active scope")
	}
	if _, ok := activeSources["custom-library"]; ok {
		t.Fatal("did not expect custom-library in default active scope")
	}

	librarySources := decodeSources(performModelsRequest(http.MethodGet, "/model-configs?scope=library", nil, h.GetModelConfigs))
	if _, ok := librarySources["gpt-image-2"]; !ok {
		t.Fatal("expected gpt-image-2 in library scope")
	}
	if _, ok := librarySources["custom-active"]; ok {
		t.Fatal("did not expect user custom-active in library scope")
	}
	if librarySources["custom-library"] != "seed" {
		t.Fatalf("custom-library source = %q, want seed", librarySources["custom-library"])
	}

	allSources := decodeSources(performModelsRequest(http.MethodGet, "/model-configs?scope=all", nil, h.GetModelConfigs))
	if _, ok := allSources["gpt-image-2"]; !ok {
		t.Fatal("expected all scope to include gpt-image-2")
	}
	if allSources["custom-active"] != "user" || allSources["custom-library"] != "seed" {
		t.Fatalf("expected all scope to include user and seed models, got custom-active=%q custom-library=%q", allSources["custom-active"], allSources["custom-library"])
	}
}

func TestModelOwnerPresetHandlersReplacePresets(t *testing.T) {
	initManagementModelsTestDB(t)
	h := NewHandler(&config.Config{}, "", nil)

	body := []byte(`{
		"items": [
			{"value": "openai", "label": "OpenAI", "description": "OpenAI models", "enabled": true},
			{"value": "acme-ai", "label": "Acme AI", "description": "Internal models", "enabled": true}
		]
	}`)
	putRec := performModelsRequest(http.MethodPut, "/model-owner-presets", body, h.PutModelOwnerPresets)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PutModelOwnerPresets status = %d body = %s", putRec.Code, putRec.Body.String())
	}

	getRec := performModelsRequest(http.MethodGet, "/model-owner-presets", nil, h.GetModelOwnerPresets)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GetModelOwnerPresets status = %d body = %s", getRec.Code, getRec.Body.String())
	}
	if _, ok := usage.GetModelOwnerPreset("acme-ai"); !ok {
		t.Fatal("expected acme-ai owner preset")
	}
}
