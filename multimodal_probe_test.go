package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func requestHasProbeImage(request map[string]any) bool {
	messages := anySlice(request["messages"])
	if len(messages) != 1 {
		return false
	}
	for _, part := range anySlice(anyMap(messages[0])["content"]) {
		if anyString(anyMap(part)["type"]) == "image_url" {
			return true
		}
	}
	return false
}

func TestMultimodalProbeDetectsAndCachesSupport(t *testing.T) {
	var textCalls, imageCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		if requestHasProbeImage(request) {
			imageCalls++
			writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "`9R7Q`"}}}})
			return
		}
		textCalls++
		writeJSON(w, http.StatusOK, probeTextResponse())
	}))
	defer upstream.Close()

	p := ProviderConfig{ID: "vision", Type: "openai", Enabled: true, BaseURL: upstream.URL + "/v1", APIKey: "key", Models: []string{"model"}, EnabledModels: []string{"model"}}
	s := &Server{config: Config{Providers: []ProviderConfig{p}}, client: upstream.Client(), dataDir: t.TempDir()}
	if err := s.probeSingleModel(t.Context(), p.ID, "model"); err != nil {
		t.Fatal(err)
	}
	if err := s.probeSingleModel(t.Context(), p.ID, "model"); err != nil {
		t.Fatal(err)
	}
	updated, _ := s.providerByID(p.ID)
	if !updated.ModelMultimodal["model"] {
		t.Fatalf("multimodal support was not recorded: %#v", updated.ModelMultimodal)
	}
	if textCalls != 2 || imageCalls != 1 {
		t.Fatalf("calls text=%d image=%d, want text=2 image=1", textCalls, imageCalls)
	}
}

func TestTemporaryMultimodalFailureIsRetried(t *testing.T) {
	var imageCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		if requestHasProbeImage(request) {
			imageCalls++
			if imageCalls == 1 {
				writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": map[string]any{"message": "rate limit exceeded"}})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "9R7Q"}}}})
			return
		}
		writeJSON(w, http.StatusOK, probeTextResponse())
	}))
	defer upstream.Close()

	p := ProviderConfig{ID: "retry", Type: "openai", Enabled: true, BaseURL: upstream.URL + "/v1", APIKey: "key", Models: []string{"model"}, EnabledModels: []string{"model"}}
	s := &Server{config: Config{Providers: []ProviderConfig{p}}, client: upstream.Client(), dataDir: t.TempDir()}
	if err := s.probeSingleModel(t.Context(), p.ID, "model"); err != nil {
		t.Fatal(err)
	}
	first, _ := s.providerByID(p.ID)
	if _, known := first.ModelMultimodal["model"]; known {
		t.Fatalf("temporary failure should not be cached: %#v", first.ModelMultimodal)
	}
	if err := s.probeSingleModel(t.Context(), p.ID, "model"); err != nil {
		t.Fatal(err)
	}
	second, _ := s.providerByID(p.ID)
	if !second.ModelMultimodal["model"] || imageCalls != 2 {
		t.Fatalf("temporary failure was not retried: calls=%d state=%#v", imageCalls, second.ModelMultimodal)
	}
}

func TestMultimodalProbeTimeIsExcludedFromReportedLatency(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		if requestHasProbeImage(request) {
			time.Sleep(200 * time.Millisecond)
			writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "9R7Q"}}}})
			return
		}
		writeJSON(w, http.StatusOK, probeTextResponse())
	}))
	defer upstream.Close()

	p := ProviderConfig{ID: "latency", Type: "openai", Enabled: true, BaseURL: upstream.URL + "/v1", APIKey: "key", Models: []string{"model"}}
	s := &Server{config: Config{Providers: []ProviderConfig{p}}, client: upstream.Client(), dataDir: t.TempDir()}
	start := time.Now()
	err, latency := s.probeSingleModelWithLatency(t.Context(), p.ID, "model")
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		t.Fatal(err)
	}
	if elapsed-latency < 150 {
		t.Fatalf("multimodal probe time leaked into latency: reported=%dms elapsed=%dms", latency, elapsed)
	}
}

func TestRetainKnownModelMultimodal(t *testing.T) {
	p := ProviderConfig{
		Models:          []string{"keep", "new"},
		ModelMultimodal: map[string]bool{"keep": true, "removed": false},
	}
	retainKnownModelMultimodal(&p)
	if !p.ModelMultimodal["keep"] {
		t.Fatalf("existing model capability was removed: %#v", p.ModelMultimodal)
	}
	if _, ok := p.ModelMultimodal["removed"]; ok {
		t.Fatalf("removed model capability was retained: %#v", p.ModelMultimodal)
	}
	if _, ok := p.ModelMultimodal["new"]; ok {
		t.Fatalf("new model should remain unprobed: %#v", p.ModelMultimodal)
	}
}

func TestMultimodalProbeUsesDedicatedTokenLimit(t *testing.T) {
	request := multimodalProbeRequest("model", multimodalProbeMaxTokens)
	if got := int(request["max_tokens"].(int)); got != 128 {
		t.Fatalf("max_tokens = %d, want 128", got)
	}
}

func TestMigrateMultimodalProbeCacheClearsOldFalseOnlyOnce(t *testing.T) {
	p := ProviderConfig{ModelMultimodal: map[string]bool{"vision": true, "wrong-false": false}}
	migrateMultimodalProbeCache(&p)
	if !p.ModelMultimodal["vision"] {
		t.Fatal("known multimodal model was removed")
	}
	if _, exists := p.ModelMultimodal["wrong-false"]; exists {
		t.Fatal("old false cache was not cleared")
	}
	p.ModelMultimodal["confirmed-text-only"] = false
	migrateMultimodalProbeCache(&p)
	if _, exists := p.ModelMultimodal["confirmed-text-only"]; !exists {
		t.Fatal("current-version false cache was cleared again")
	}
}

func TestMultimodalProbeResponseTextSupportsWrappedAndReasoningResponses(t *testing.T) {
	raw, err := json.Marshal(map[string]any{"data": map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{
			"content":           nil,
			"reasoning":         "The image code is 9R7Q.",
			"reasoning_details": []any{map[string]any{"type": "reasoning.text", "text": "9R7Q"}},
		}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := multimodalProbeResponseText(raw); !strings.Contains(got, "9R7Q") {
		t.Fatalf("wrapped reasoning response was not parsed: %q", got)
	}
}

func TestManualMultimodalRetryClearsOnlySelectedFalseCache(t *testing.T) {
	p := ProviderConfig{
		ID:              "provider",
		Models:          []string{"retry", "keep-false", "keep-true"},
		ModelMultimodal: map[string]bool{"retry": false, "keep-false": false, "keep-true": true},
	}
	s := &Server{config: Config{Providers: []ProviderConfig{p}}, dataDir: t.TempDir()}
	s.clearNegativeModelMultimodal(p.ID, []string{"retry", "keep-true"})
	updated, _ := s.providerByID(p.ID)
	if _, exists := updated.ModelMultimodal["retry"]; exists {
		t.Fatal("selected false cache was not cleared")
	}
	if supported, exists := updated.ModelMultimodal["keep-true"]; !exists || !supported {
		t.Fatal("known multimodal support was removed")
	}
	if supported, exists := updated.ModelMultimodal["keep-false"]; !exists || supported {
		t.Fatal("unselected false cache was changed")
	}
}

func TestAdminShowsDetectedMultimodalBadge(t *testing.T) {
	html := adminHTMLLiteV2(`{"providers":[]}`)
	if !strings.Contains(html, "model_multimodal") || !strings.Contains(html, ">多模态</span>") {
		t.Fatal("admin page is missing the detected multimodal badge")
	}
}

func TestAdminConnectedCardsShowPublishedModels(t *testing.T) {
	html := adminHTMLLiteV2(`{"providers":[]}`)
	for _, marker := range []string{"published-model-title", "published-model-list", "published-model-tag", "已发布模型", "暂无已发布模型"} {
		if !strings.Contains(html, marker) {
			t.Fatalf("admin page is missing published model card marker %q", marker)
		}
	}
}
