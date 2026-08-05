package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLegacyAutoConfigMigratesWithoutLosingModels(t *testing.T) {
	cfg := Config{AutoModel: AutoModelConfig{Enabled: true, Models: []string{"provider/model"}}}
	normalizeConfigModelRefs(&cfg)
	if len(cfg.AutoModels) != 1 || cfg.AutoModels[0].ID != "auto" || !cfg.AutoModels[0].Enabled || strings.Join(cfg.AutoModels[0].Models, ",") != "provider/model" {
		t.Fatalf("legacy Auto migration = %#v", cfg.AutoModels)
	}
	if !cfg.AutoModel.Enabled || strings.Join(cfg.AutoModel.Models, ",") != "provider/model" || cfg.AutoModel.ID != "" {
		t.Fatalf("legacy Auto compatibility value = %#v", cfg.AutoModel)
	}
}

func TestMultipleAutoModelsRouteAndPublishIndependently(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request chatRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"model":   request.Model,
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": request.Model}}},
		})
	}))
	defer upstream.Close()

	provider := ProviderConfig{
		ID: "provider", Name: "Provider", Type: "openai", Enabled: true, BaseURL: upstream.URL + "/v1", APIKey: "key",
		Models: []string{"general", "code"}, EnabledModels: []string{"general", "code"},
	}
	s := &Server{
		config: Config{
			AccessKey: "gateway",
			AutoModels: []AutoModelConfig{
				{ID: "auto", Enabled: true, Models: []string{"provider/general"}},
				{ID: "auto-code", Enabled: true, Models: []string{"provider/code"}},
			},
			Providers: []ProviderConfig{provider},
		},
		usage: newUsageStore(), client: upstream.Client(), dataDir: t.TempDir(),
	}
	call := func(model string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"same conversation"}]}`))
		req.Header.Set("Authorization", "Bearer gateway")
		req.Header.Set("User-Agent", "test-agent")
		rr := httptest.NewRecorder()
		s.handleChatCompletions(rr, req)
		return rr
	}
	if rr := call("auto"); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"general"`) {
		t.Fatalf("default Auto response = %d %s", rr.Code, rr.Body.String())
	}
	if rr := call("auto-code"); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"code"`) {
		t.Fatalf("named Auto response = %d %s", rr.Code, rr.Body.String())
	}
	s.autoAgentMu.Lock()
	bindings := len(s.autoAgents)
	s.autoAgentMu.Unlock()
	if bindings != 2 {
		t.Fatalf("Auto session bindings = %d, want separate bindings for both Auto models", bindings)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer gateway")
	req.Header.Set("Accept", "application/json")
	rr := httptest.NewRecorder()
	s.handleModels(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"id":"auto"`) || !strings.Contains(rr.Body.String(), `"id":"auto-code"`) {
		t.Fatalf("model list = %d %s", rr.Code, rr.Body.String())
	}
}

func TestMultipleAutoModelValidationAndAdminControls(t *testing.T) {
	if err := validateConfig(Config{AutoModels: []AutoModelConfig{{ID: "auto"}, {ID: "auto-code"}}}); err != nil {
		t.Fatal(err)
	}
	if err := validateConfig(Config{AutoModels: []AutoModelConfig{{ID: "auto"}, {ID: "bad/name"}}}); err == nil {
		t.Fatal("invalid Auto model id was accepted")
	}
	html := adminHTMLLiteV2(`{"providers":[]}`)
	for _, marker := range []string{"autoModelSelector", "auto-model-tab", "createAutoModel", "deleteCurrentAutoModel", "auto_models", "autoMemberships"} {
		if !strings.Contains(html, marker) {
			t.Fatalf("multi-Auto admin view missing %q", marker)
		}
	}
}
