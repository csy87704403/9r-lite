package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCustomProviderUsesNameAsPublicModelPrefix(t *testing.T) {
	p := ProviderConfig{
		ID:            "custom2",
		Name:          "Cline",
		Type:          "openai",
		Enabled:       true,
		Models:        []string{"deepseek/deepseek-v4-flash"},
		EnabledModels: []string{"deepseek/deepseek-v4-flash"},
	}
	s := &Server{config: Config{AccessKey: "master-key", Providers: []ProviderConfig{p}}}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer master-key")
	rr := httptest.NewRecorder()

	s.handleModels(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := "Cline/deepseek/deepseek-v4-flash"
	if len(body.Data) != 1 || body.Data[0].ID != want {
		t.Fatalf("models = %#v, want %s", body.Data, want)
	}
	if got, ok := s.providerByRouteID("Cline"); !ok || got.ID != "custom2" {
		t.Fatalf("public route did not resolve: %#v, %v", got, ok)
	}
	if got, ok := s.providerByRouteID("custom2"); !ok || got.ID != "custom2" {
		t.Fatalf("legacy route did not resolve: %#v, %v", got, ok)
	}
}

func TestCustomProviderPublicPrefixRoutesChatRequest(t *testing.T) {
	received := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		received <- body.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"OK"}}]}`))
	}))
	defer upstream.Close()

	p := ProviderConfig{
		ID:            "custom2",
		Name:          "Cline",
		Type:          "openai",
		Enabled:       true,
		BaseURL:       upstream.URL + "/v1",
		APIKey:        "upstream-key",
		Models:        []string{"deepseek/deepseek-v4-flash"},
		EnabledModels: []string{"deepseek/deepseek-v4-flash"},
	}
	s := &Server{config: Config{AccessKey: "master-key", Providers: []ProviderConfig{p}}, client: upstream.Client()}
	raw := []byte(`{"model":"Cline/deepseek/deepseek-v4-flash","messages":[{"role":"user","content":"hello"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer master-key")
	rr := httptest.NewRecorder()

	s.handleChatCompletions(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if got := <-received; got != "deepseek/deepseek-v4-flash" {
		t.Fatalf("upstream model = %q", got)
	}
}

func TestMergeConfigMigratesCustomProviderModelReferences(t *testing.T) {
	cfg := mergeDefaultConfig(Config{
		AutoModel:   AutoModelConfig{Models: []string{"custom2/deepseek/deepseek-v4-flash"}},
		ModelGroups: []ModelGroup{{ID: "group", Models: []string{"custom2/deepseek/deepseek-v4-flash"}}},
		Providers: []ProviderConfig{{
			ID:      "custom2",
			Name:    "Cline",
			Type:    "openai",
			Enabled: true,
			Models:  []string{"deepseek/deepseek-v4-flash"},
		}},
	})
	want := "Cline/deepseek/deepseek-v4-flash"
	if len(cfg.AutoModel.Models) != 1 || cfg.AutoModel.Models[0] != want {
		t.Fatalf("auto models = %#v", cfg.AutoModel.Models)
	}
	if len(cfg.ModelGroups[0].Models) != 1 || cfg.ModelGroups[0].Models[0] != want {
		t.Fatalf("group models = %#v", cfg.ModelGroups[0].Models)
	}
}

func TestCustomProviderNameCannotContainSlash(t *testing.T) {
	err := validateConfig(Config{Providers: []ProviderConfig{{ID: "custom2", Name: "Cline/Test", Type: "openai", Enabled: true}}})
	if err == nil {
		t.Fatal("expected invalid custom provider name")
	}
}
