package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func probeTextResponse() map[string]any {
	return map[string]any{
		"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": "OK"},
		}},
	}
}

func TestAutoModelFallsBackAfterQuotaError(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		calls[body.Model]++
		mu.Unlock()
		if body.Model == "exhausted" {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"insufficient_quota"}}`))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "chatcmpl_test", "object": "chat.completion", "model": body.Model,
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "fallback"}, "finish_reason": "stop"}},
		})
	}))
	defer upstream.Close()

	first := ProviderConfig{ID: "first", Name: "First", Type: "openai", Enabled: true, BaseURL: upstream.URL + "/v1", APIKey: "key", Models: []string{"exhausted"}, EnabledModels: []string{"exhausted"}}
	second := ProviderConfig{ID: "second", Name: "Second", Type: "openai", Enabled: true, BaseURL: upstream.URL + "/v1", APIKey: "key", Models: []string{"fallback"}, EnabledModels: []string{"fallback"}}
	s := &Server{
		config: Config{
			AccessKey: "gateway",
			AutoModel: AutoModelConfig{Enabled: true, Models: []string{"first/exhausted", "second/fallback"}},
			Providers: []ProviderConfig{first, second},
		},
		client:  upstream.Client(),
		dataDir: t.TempDir(),
	}

	callAuto := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hello"}]}`))
		req.Header.Set("Authorization", "Bearer gateway")
		rr := httptest.NewRecorder()
		s.handleChatCompletions(rr, req)
		return rr
	}

	firstResponse := callAuto()
	if firstResponse.Code != http.StatusOK || !strings.Contains(firstResponse.Body.String(), `"fallback"`) {
		t.Fatalf("first auto response = %d %s", firstResponse.Code, firstResponse.Body.String())
	}
	secondResponse := callAuto()
	if secondResponse.Code != http.StatusOK {
		t.Fatalf("second auto response = %d %s", secondResponse.Code, secondResponse.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if calls["exhausted"] != 1 || calls["fallback"] != 2 {
		t.Fatalf("upstream calls = %#v", calls)
	}
}

func TestProviderRotatesAcrossKeysForQuotaAndRateLimit(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		calls = append(calls, key)
		mu.Unlock()
		switch key {
		case "key1":
			writeJSON(w, http.StatusPaymentRequired, map[string]any{"error": map[string]any{"message": "Insufficient balance"}})
		case "key2":
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": map[string]any{"message": "Rate limit exceeded"}})
		default:
			writeJSON(w, http.StatusOK, probeTextResponse())
		}
	}))
	defer upstream.Close()
	p := ProviderConfig{
		ID: "provider", Type: "openai", Enabled: true, BaseURL: upstream.URL + "/v1",
		APIKey: "key1", APIKeys: []string{"key1", "key2", "key3"}, Models: []string{"model"}, EnabledModels: []string{"model"},
	}
	s := &Server{config: Config{AccessKey: "gateway", Providers: []ProviderConfig{p}}, client: upstream.Client(), dataDir: t.TempDir()}
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"provider/model","messages":[{"role":"user","content":"hello"}]}`))
		req.Header.Set("Authorization", "Bearer gateway")
		rr := httptest.NewRecorder()
		s.handleChatCompletions(rr, req)
		return rr
	}
	if rr := call(); rr.Code != http.StatusOK {
		t.Fatalf("rotated response = %d %s", rr.Code, rr.Body.String())
	}
	updated, _ := s.providerByID("provider")
	failed := failedKeyIndexesForModel(updated, "model")
	if updated.ProviderSpecificData["active_key_index"] != "2" || !failed[0] || failed[1] {
		t.Fatalf("unexpected key state: %#v", updated.ProviderSpecificData)
	}
	if rr := call(); rr.Code != http.StatusOK {
		t.Fatalf("active-key response = %d %s", rr.Code, rr.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"key1", "key2", "key3", "key3"}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Fatalf("key call order = %#v, want %#v", calls, want)
	}
}

func TestProviderKeyErrorClassification(t *testing.T) {
	if !isQuotaKeyError(http.StatusPaymentRequired, []byte(`{"error":"payment required"}`)) {
		t.Fatal("generic 402 was not recognized as a quota failure")
	}
	if !isRateLimitKeyError(http.StatusTooManyRequests, []byte(`{"error":"too many requests"}`)) {
		t.Fatal("429 was not recognized as a temporary key limit")
	}
	if isCredentialKeyError(http.StatusForbidden, []byte(`{"error":"forbidden"}`)) {
		t.Fatal("generic 403 was incorrectly recognized as a credential failure")
	}
	if isCredentialKeyError(http.StatusForbidden, []byte(`{"error":"cline-free/glm-5.2 is only available via Cline product surfaces"}`)) {
		t.Fatal("model-specific 403 was incorrectly recognized as a credential failure")
	}
	if !isCredentialKeyError(http.StatusForbidden, []byte(`{"error":"invalid api key"}`)) {
		t.Fatal("explicit invalid-key 403 was not recognized as a credential failure")
	}
	if isCredentialKeyError(http.StatusForbidden, []byte(`{"error":"rate limit exceeded"}`)) {
		t.Fatal("rate-limited 403 was incorrectly persisted as a credential failure")
	}
}

func TestResponsesProviderRotatesAfterRateLimit(t *testing.T) {
	var calls []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		calls = append(calls, key)
		if key == "key1" {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate limit exceeded"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"output": []any{}})
	}))
	defer upstream.Close()
	p := ProviderConfig{ID: "responses", Type: "openai", Enabled: true, BaseURL: upstream.URL, APIKey: "key1", APIKeys: []string{"key1", "key2"}, Models: []string{"model"}}
	s := &Server{config: Config{Providers: []ProviderConfig{p}}, client: upstream.Client(), dataDir: t.TempDir()}
	resp, err := s.doResponsesRequest(t.Context(), p, []byte(`{"model":"model"}`), "model", false)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || strings.Join(calls, ",") != "key1,key2" {
		t.Fatalf("Responses key rotation failed: status=%d calls=%#v", resp.StatusCode, calls)
	}
}

func TestAnthropicProviderRotatesAfterRateLimit(t *testing.T) {
	var calls []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("x-api-key")
		calls = append(calls, key)
		if key == "key1" {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": map[string]any{"message": "rate limit exceeded"}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"content": []any{}})
	}))
	defer upstream.Close()
	p := ProviderConfig{ID: "anthropic", Type: "anthropic", Enabled: true, BaseURL: upstream.URL, APIKey: "key1", APIKeys: []string{"key1", "key2"}, Models: []string{"model"}}
	s := &Server{config: Config{Providers: []ProviderConfig{p}}, client: upstream.Client(), dataDir: t.TempDir()}
	resp, err := s.doAnthropicRequest(t.Context(), nil, p, []byte(`{"model":"model"}`), "model", false)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || strings.Join(calls, ",") != "key1,key2" {
		t.Fatalf("Anthropic key rotation failed: status=%d calls=%#v", resp.StatusCode, calls)
	}
}

func TestAutoModelFallsBackAfterExplicitErrorInSuccessfulResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Model == "broken" {
			writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "upstream unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "fallback"}}},
		})
	}))
	defer upstream.Close()
	first := ProviderConfig{ID: "first", Type: "openai", Enabled: true, BaseURL: upstream.URL + "/v1", APIKey: "key", Models: []string{"broken"}, EnabledModels: []string{"broken"}}
	second := ProviderConfig{ID: "second", Type: "openai", Enabled: true, BaseURL: upstream.URL + "/v1", APIKey: "key", Models: []string{"fallback"}, EnabledModels: []string{"fallback"}}
	s := &Server{config: Config{AccessKey: "gateway", AutoModel: AutoModelConfig{Enabled: true, Models: []string{"first/broken", "second/fallback"}}, Providers: []ProviderConfig{first, second}}, client: upstream.Client(), dataDir: t.TempDir()}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer gateway")
	rr := httptest.NewRecorder()
	s.handleChatCompletions(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "fallback") {
		t.Fatalf("auto did not fall back after explicit 2xx error: %d %s", rr.Code, rr.Body.String())
	}
}

func TestAutoFallbackErrorRecognition(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{name: "model removed", status: http.StatusBadRequest, body: `{"error":{"message":"model has been deleted"}}`, want: true},
		{name: "ip blocked", status: http.StatusForbidden, body: `{"error":{"message":"ip has been blocked"}}`, want: true},
		{name: "upstream unavailable", status: http.StatusServiceUnavailable, body: `{}`, want: true},
		{name: "invalid request", status: http.StatusBadRequest, body: `{"error":{"message":"unsupported response_format"}}`, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isAutoFallbackError(test.status, []byte(test.body)); got != test.want {
				t.Fatalf("isAutoFallbackError(%d, %s) = %v, want %v", test.status, test.body, got, test.want)
			}
		})
	}
}

func TestDirectQuotaErrorRemovesSingleKeyModelFromAvailability(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":{"message":"Insufficient balance"}}`))
	}))
	defer upstream.Close()

	p := ProviderConfig{
		ID: "custom13", Name: "Openrouter", Type: "openai", Enabled: true,
		BaseURL: upstream.URL + "/v1", APIKey: "key", Models: []string{"x-ai/grok-4.5"},
		EnabledModels: []string{"x-ai/grok-4.5"}, AvailableModels: []string{"x-ai/grok-4.5"}, AvailabilityCheckedAt: 1,
	}
	s := &Server{config: Config{AccessKey: "gateway", Providers: []ProviderConfig{p}}, client: upstream.Client(), dataDir: t.TempDir()}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"Openrouter/x-ai/grok-4.5","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer gateway")
	rr := httptest.NewRecorder()
	s.handleChatCompletions(rr, req)
	if rr.Code != http.StatusPaymentRequired {
		t.Fatalf("response = %d %s", rr.Code, rr.Body.String())
	}
	updated, _ := s.providerByID("custom13")
	if len(updated.AvailableModels) != 0 || updated.ModelErrors["x-ai/grok-4.5"] == "" || !modelQuotaBlocked(updated, "x-ai/grok-4.5") {
		t.Fatalf("quota failure did not remove model: %#v", updated)
	}
	if candidates := s.autoChatCandidatesForOpenAI(t.Context(), false); len(candidates) != 0 {
		t.Fatalf("quota-failed model still appears in auto candidates: %#v", candidates)
	}
	recovered := updateProbeResult(updated, "x-ai/grok-4.5", nil, 10, false)
	if len(recovered.AvailableModels) != 1 || modelQuotaBlocked(recovered, "x-ai/grok-4.5") {
		t.Fatalf("successful probe did not clear quota block: %#v", recovered)
	}
}

func TestAutoProbeClearsRecoveredQuotaBlock(t *testing.T) {
	s := &Server{}
	p := ProviderConfig{
		ID: "test", Models: []string{"model"}, EnabledModels: []string{"model"},
		QuotaBlockedModels: []string{"model"}, ModelErrors: map[string]string{"model": "免费额度已结束"},
	}
	p = s.applyAutoProbeResults(p, []string{"model"}, []string{"model"}, nil, map[string]int64{"model": 100})
	if len(p.AvailableModels) != 1 || modelQuotaBlocked(p, "model") || p.ModelErrors["model"] != "" {
		t.Fatalf("automatic probe did not clear recovered quota block: %#v", p)
	}
}

func TestAutoProbeRemovesModelOnlyAfterConsecutiveFailures(t *testing.T) {
	s := &Server{}
	p := ProviderConfig{
		ID:                    "test",
		Models:                []string{"model"},
		EnabledModels:         []string{"model"},
		AvailableModels:       []string{"model"},
		AvailabilityCheckedAt: 1,
	}
	for attempt := 1; attempt < autoProbeFailureThreshold; attempt++ {
		p = s.applyAutoProbeResults(p, []string{"model"}, nil, map[string]string{"model": "temporary upstream error"}, map[string]int64{"model": 100})
		if len(p.AvailableModels) != 1 || p.ModelFailureCounts["model"] != attempt {
			t.Fatalf("attempt %d removed model too early: %#v", attempt, p)
		}
	}
	p = s.applyAutoProbeResults(p, []string{"model"}, nil, map[string]string{"model": "temporary upstream error"}, map[string]int64{"model": 100})
	if len(p.AvailableModels) != 0 || p.ModelFailureCounts["model"] != autoProbeFailureThreshold {
		t.Fatalf("model should be removed after %d failures: %#v", autoProbeFailureThreshold, p)
	}
}

func TestAutoProbeRechecksPreviouslyUnavailableModel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, probeTextResponse())
	}))
	defer upstream.Close()
	p := ProviderConfig{ID: "test", Type: "openai", Enabled: true, BaseURL: upstream.URL + "/v1", APIKey: "key", Models: []string{"recovered"}, EnabledModels: []string{"recovered"}, AvailabilityCheckedAt: 1}
	s := &Server{config: Config{Providers: []ProviderConfig{p}}, client: upstream.Client(), dataDir: t.TempDir()}
	s.probeAllProviders(t.Context(), true)
	updated, ok := s.providerByID("test")
	if !ok || len(updated.AvailableModels) != 1 || updated.AvailableModels[0] != "recovered" {
		t.Fatalf("previously unavailable model was not rechecked: %#v", updated)
	}
}

func TestAutoProbePreservesRotatingKeyState(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer bad" {
			writeJSON(w, http.StatusPaymentRequired, map[string]any{"error": map[string]any{"message": "Insufficient balance"}})
			return
		}
		writeJSON(w, http.StatusOK, probeTextResponse())
	}))
	defer upstream.Close()
	p := ProviderConfig{ID: "keys", Type: "openai", Enabled: true, BaseURL: upstream.URL + "/v1", APIKey: "bad", APIKeys: []string{"bad", "good"}, Models: []string{"model"}, EnabledModels: []string{"model"}}
	s := &Server{config: Config{Providers: []ProviderConfig{p}}, client: upstream.Client(), dataDir: t.TempDir()}
	s.probeAllProviders(t.Context(), true)
	updated, _ := s.providerByID("keys")
	if updated.ProviderSpecificData["active_key_index"] != "1" || !failedKeyIndexesForModel(updated, "model")[0] {
		t.Fatalf("rotating key state was overwritten by probe result: %#v", updated.ProviderSpecificData)
	}
}

func TestProbeUsesShortTextAndOnlyOneMultimodalRequest(t *testing.T) {
	var requests []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		requests = append(requests, request)
		writeJSON(w, http.StatusOK, map[string]any{
			"choices": []any{map[string]any{
				"finish_reason": "length",
				"message":       map[string]any{"role": "assistant", "content": "", "reasoning_content": "OK"},
			}},
		})
	}))
	defer upstream.Close()
	p := ProviderConfig{ID: "text", Name: "Text", Type: "openai", Enabled: true, BaseURL: upstream.URL + "/v1", APIKey: "key", Models: []string{"glm-5.2"}, EnabledModels: []string{"glm-5.2"}}
	s := &Server{config: Config{Providers: []ProviderConfig{p}}, client: upstream.Client(), dataDir: t.TempDir()}
	s.probeAllProviders(t.Context(), true)
	s.probeAllProviders(t.Context(), true)
	if len(requests) != 3 {
		t.Fatalf("probe request count = %d, want 3 (two text and one cached multimodal probe)", len(requests))
	}
	if len(anySlice(requests[0]["tools"])) != 0 || requests[0]["tool_choice"] != nil {
		t.Fatalf("probe unexpectedly included tools: %#v", requests[0])
	}
	messages := anySlice(requests[0]["messages"])
	if len(messages) != 1 || anyString(anyMap(messages[0])["content"]) != "Reply with OK." {
		t.Fatalf("probe is not a short text request: %#v", requests[0])
	}
	if maxTokens, _ := requests[0]["max_tokens"].(float64); int(maxTokens) != 16 {
		t.Fatalf("normal probe max_tokens = %v, want 16", requests[0]["max_tokens"])
	}
	visionMessages := anySlice(requests[1]["messages"])
	if len(visionMessages) != 1 || len(anySlice(anyMap(visionMessages[0])["content"])) != 2 {
		t.Fatalf("first successful probe did not include one image capability check: %#v", requests[1])
	}
	secondTextMessages := anySlice(requests[2]["messages"])
	if len(secondTextMessages) != 1 || anyString(anyMap(secondTextMessages[0])["content"]) != "Reply with OK." {
		t.Fatalf("later probe should only use short text: %#v", requests[2])
	}
	updated, _ := s.providerByID("text")
	if len(updated.AvailableModels) != 1 || updated.AvailableModels[0] != "glm-5.2" {
		t.Fatalf("reasoning-only 2xx response was rejected: %#v", updated)
	}
	if supported, known := updated.ModelMultimodal["glm-5.2"]; !known || supported {
		t.Fatalf("non-multimodal result was not cached: %#v", updated.ModelMultimodal)
	}
}

func TestOpenRouterProbeOmitsMaxTokensToCatchQuotaFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request["max_tokens"] == nil {
			writeJSON(w, http.StatusPaymentRequired, map[string]any{"error": map[string]any{"message": "Insufficient balance"}})
			return
		}
		writeJSON(w, http.StatusOK, probeTextResponse())
	}))
	defer upstream.Close()
	p := ProviderConfig{ID: "custom13", Name: "Openrouter", Type: "openai", Enabled: true, BaseURL: upstream.URL + "/v1", APIKey: "key", Models: []string{"x-ai/grok-4.5"}, EnabledModels: []string{"x-ai/grok-4.5"}}
	s := &Server{config: Config{Providers: []ProviderConfig{p}}, client: upstream.Client(), dataDir: t.TempDir()}
	s.probeAllProviders(t.Context(), true)
	updated, _ := s.providerByID("custom13")
	if len(updated.AvailableModels) != 0 || !modelQuotaBlocked(updated, "x-ai/grok-4.5") {
		t.Fatalf("OpenRouter probe did not catch quota failure: %#v", updated)
	}
}

func TestProbeRejectsExplicitErrorInSuccessfulResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "empty response content"})
	}))
	defer upstream.Close()
	p := ProviderConfig{ID: "broken", Type: "openai", Enabled: true, BaseURL: upstream.URL + "/v1", APIKey: "key", Models: []string{"model"}, EnabledModels: []string{"model"}}
	s := &Server{config: Config{Providers: []ProviderConfig{p}}, client: upstream.Client(), dataDir: t.TempDir()}
	s.probeAllProviders(t.Context(), true)
	updated, _ := s.providerByID("broken")
	if updated.ModelErrors["model"] == "" || updated.ModelFailureCounts["model"] != 1 {
		t.Fatalf("explicit 2xx error was accepted: %#v", updated)
	}
}

func TestOpenRouterProbeDetectionDoesNotMatchKiloPath(t *testing.T) {
	openRouter := ProviderConfig{Name: "Openrouter", Type: "openai", BaseURL: "https://openrouter.ai/api/v1"}
	if got := probeMaxTokens(openRouter); got != 0 {
		t.Fatalf("OpenRouter max_tokens = %d, want omitted", got)
	}
	kilo := ProviderConfig{Name: "Kilo Code OAuth", Type: "kilocode", BaseURL: "https://api.kilo.ai/api/openrouter/chat/completions"}
	if got := probeMaxTokens(kilo); got != 16 {
		t.Fatalf("Kilo max_tokens = %d, want 16", got)
	}
}

func TestAutoCandidatesSkipPersistedFailures(t *testing.T) {
	base := ProviderConfig{ID: "provider", Type: "openai", Enabled: true, APIKey: "key1", APIKeys: []string{"key1", "key2"}, Models: []string{"model"}, EnabledModels: []string{"model"}, AvailableModels: []string{"model"}, AvailabilityCheckedAt: 1}
	newServer := func(p ProviderConfig) *Server {
		return &Server{config: Config{AutoModel: AutoModelConfig{Enabled: true, Models: []string{"provider/model"}}, Providers: []ProviderConfig{p}}}
	}
	quotaBlocked := base
	quotaBlocked.QuotaBlockedModels = []string{"model"}
	if got := newServer(quotaBlocked).autoChatCandidatesForOpenAI(t.Context(), false); len(got) != 0 {
		t.Fatalf("quota-blocked model remained an Auto candidate: %#v", got)
	}
	allKeysFailed := base
	allKeysFailed.ProviderSpecificData = map[string]string{modelFailedKeyIndexesDataKey("model"): "0,1"}
	if got := newServer(allKeysFailed).autoChatCandidatesForOpenAI(t.Context(), false); len(got) != 0 {
		t.Fatalf("model with all keys failed remained an Auto candidate: %#v", got)
	}
}

func TestAutoCandidatesUseAvailabilityInsteadOfPublishSelection(t *testing.T) {
	newServer := func(p ProviderConfig) *Server {
		return &Server{config: Config{AutoModel: AutoModelConfig{Enabled: true, Models: []string{"provider/model"}}, Providers: []ProviderConfig{p}}}
	}
	availableButUnpublished := ProviderConfig{
		ID: "provider", Type: "openai", Enabled: true, Models: []string{"model"},
		AvailableModels: []string{"model"}, AvailabilityCheckedAt: 1,
		ProviderSpecificData: map[string]string{"manualPublishOverride": "true"},
	}
	if got := newServer(availableButUnpublished).autoChatCandidatesForOpenAI(t.Context(), false); len(got) != 0 {
		t.Fatalf("available unpublished model was excluded from Auto: %#v", got)
	}
	publishedButUnavailable := availableButUnpublished
	publishedButUnavailable.EnabledModels = []string{"model"}
	publishedButUnavailable.AvailableModels = nil
	if got := newServer(publishedButUnavailable).autoChatCandidatesForOpenAI(t.Context(), false); len(got) != 0 {
		t.Fatalf("published unavailable model remained in Auto: %#v", got)
	}
}
