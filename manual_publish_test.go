package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestManualPublishOverrideBypassesAvailability(t *testing.T) {
	p := ProviderConfig{
		ID:                    "test",
		Enabled:               true,
		Models:                []string{"available", "forced"},
		EnabledModels:         []string{"forced"},
		AvailableModels:       []string{"available"},
		AvailabilityCheckedAt: 1,
		ProviderSpecificData:  map[string]string{"manualPublishOverride": "true"},
	}
	s := &Server{config: Config{Providers: []ProviderConfig{p}}}
	visible := s.visibleModelsForProvider(nil, p)
	if len(visible) != 1 || visible[0] != "forced" {
		t.Fatalf("visible models = %#v, want forced model", visible)
	}
}

func TestModelsEndpointIncludesForcedTextAndExcludesMedia(t *testing.T) {
	p := ProviderConfig{
		ID:                    "custom",
		Name:                  "SenseNova",
		Type:                  "openai",
		Enabled:               true,
		Models:                []string{"forced-chat", "sensenova-u1-fast"},
		EnabledModels:         []string{"forced-chat", "sensenova-u1-fast"},
		AvailabilityCheckedAt: 1,
		ProviderSpecificData:  map[string]string{"manualPublishOverride": "true"},
		ModelKinds:            map[string]string{"sensenova-u1-fast": "image"},
	}
	s := &Server{config: Config{AccessKey: "master", Providers: []ProviderConfig{p}}}
	r := httptest.NewRequest(http.MethodGet, "/v1/models?key=master", nil)
	w := httptest.NewRecorder()
	s.handleModels(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 1 || response.Data[0].ID != "SenseNova/forced-chat" {
		t.Fatalf("models response = %#v", response.Data)
	}
}
