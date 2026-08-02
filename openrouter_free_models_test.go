package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestFetchCompatibleModelsFiltersOpenRouterFreeModelsByDefault(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{
			map[string]any{"id": "openrouter/free"},
			map[string]any{"id": "nvidia/nemotron:free"},
			map[string]any{"id": "google/lyria-preview"},
			map[string]any{"id": "tencent/hy3-preview"},
		}})
	}))
	defer upstream.Close()

	p := ProviderConfig{Name: "Openrouter", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "key"}
	got, err := fetchCompatibleModels(t.Context(), upstream.Client(), p)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"openrouter/free", "nvidia/nemotron:free"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %#v, want %#v", got, want)
	}
}

func TestFetchCompatibleModelsCanDisableOpenRouterFreeFilter(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{
			map[string]any{"id": "openrouter/free"},
			map[string]any{"id": "tencent/hy3-preview"},
		}})
	}))
	defer upstream.Close()

	p := ProviderConfig{
		Name: "Openrouter", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "key",
		ProviderSpecificData: map[string]string{"openrouterFreeModelsOnly": "false"},
	}
	got, err := fetchCompatibleModels(t.Context(), upstream.Client(), p)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"openrouter/free", "tencent/hy3-preview"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %#v, want %#v", got, want)
	}
}

func TestRetainOpenRouterFreeModelsRemovesPaidModelState(t *testing.T) {
	p := ProviderConfig{
		Models:             []string{"paid", "openrouter/free", "model:free"},
		EnabledModels:      []string{"paid", "model:free"},
		AvailableModels:    []string{"paid", "openrouter/free"},
		LockedModels:       []string{"paid"},
		QuotaBlockedModels: []string{"paid"},
		ModelLatencyMS:     map[string]int64{"paid": 1, "model:free": 2},
		ModelErrors:        map[string]string{"paid": "quota"},
	}

	retainOpenRouterFreeModels(&p)

	if !reflect.DeepEqual(p.Models, []string{"openrouter/free", "model:free"}) {
		t.Fatalf("models = %#v", p.Models)
	}
	if !reflect.DeepEqual(p.EnabledModels, []string{"model:free"}) || !reflect.DeepEqual(p.AvailableModels, []string{"openrouter/free"}) {
		t.Fatalf("enabled = %#v, available = %#v", p.EnabledModels, p.AvailableModels)
	}
	if len(p.LockedModels) != 0 || len(p.QuotaBlockedModels) != 0 || p.ModelLatencyMS["paid"] != 0 || p.ModelErrors["paid"] != "" {
		t.Fatalf("paid model state was retained: %#v", p)
	}
}
