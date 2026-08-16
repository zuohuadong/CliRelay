package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestAmpCodePanelHandlersPersistAndHideUpstreamKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	h := NewHandler(&config.Config{}, configPath, nil)

	request := func(method, path, body string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if body != "" {
			ctx.Request.Header.Set("Content-Type", "application/json")
		}
		handler(ctx)
		return rec
	}

	if rec := request(http.MethodPut, "/ampcode/upstream-url", `{"value":" https://amp.example.test "}`, h.PutAmpCodeUpstreamURL); rec.Code != http.StatusOK {
		t.Fatalf("put upstream url status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodPut, "/ampcode/upstream-api-key", `{"value":"secret-upstream-key"}`, h.PutAmpCodeUpstreamAPIKey); rec.Code != http.StatusOK {
		t.Fatalf("put upstream key status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodPut, "/ampcode/force-model-mappings", `{"value":true}`, h.PutAmpCodeForceModelMappings); rec.Code != http.StatusOK {
		t.Fatalf("put force mappings status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodPatch, "/ampcode/model-mappings", `{"value":[{"from":"amp-model","to":"target-model"}]}`, h.PatchAmpCodeModelMappings); rec.Code != http.StatusOK {
		t.Fatalf("patch mappings status = %d body=%s", rec.Code, rec.Body.String())
	}

	rec := request(http.MethodGet, "/ampcode", "", h.GetAmpCode)
	if rec.Code != http.StatusOK {
		t.Fatalf("get ampcode status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode ampcode payload: %v", err)
	}
	if payload["upstream-url"] != "https://amp.example.test" || payload["force-model-mappings"] != true {
		t.Fatalf("unexpected ampcode payload: %#v", payload)
	}
	if _, ok := payload["upstream-api-key"]; ok {
		t.Fatalf("ampcode payload exposes upstream api key: %#v", payload)
	}

	if rec := request(http.MethodDelete, "/ampcode/model-mappings", "", h.DeleteAmpCodeModelMappings); rec.Code != http.StatusOK {
		t.Fatalf("clear mappings status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(h.cfg.AmpCode.ModelMappings) != 0 {
		t.Fatalf("model mappings = %#v, want empty", h.cfg.AmpCode.ModelMappings)
	}
}
