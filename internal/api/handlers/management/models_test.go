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

	decodeIDs := func(rec *httptest.ResponseRecorder) map[string]bool {
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
		ids := make(map[string]bool)
		for _, item := range payload.Data {
			ids[item.ID] = true
			if item.ID == "custom-active" && item.Source != "user" {
				t.Fatalf("custom-active source = %q, want user", item.Source)
			}
		}
		return ids
	}

	activeIDs := decodeIDs(performModelsRequest(http.MethodGet, "/model-configs", nil, h.GetModelConfigs))
	if !activeIDs["custom-active"] {
		t.Fatal("expected custom-active in default active scope")
	}
	if activeIDs["gpt-image-2"] {
		t.Fatal("did not expect seed-only gpt-image-2 in default active scope")
	}

	libraryIDs := decodeIDs(performModelsRequest(http.MethodGet, "/model-configs?scope=library", nil, h.GetModelConfigs))
	if !libraryIDs["gpt-image-2"] {
		t.Fatal("expected gpt-image-2 in library scope")
	}
	if libraryIDs["custom-active"] {
		t.Fatal("did not expect user custom-active in library scope")
	}

	allIDs := decodeIDs(performModelsRequest(http.MethodGet, "/model-configs?scope=all", nil, h.GetModelConfigs))
	if !allIDs["gpt-image-2"] || !allIDs["custom-active"] {
		t.Fatalf("expected all scope to include seed and user models, got gpt-image-2=%v custom-active=%v", allIDs["gpt-image-2"], allIDs["custom-active"])
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
