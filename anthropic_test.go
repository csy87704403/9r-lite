package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIRequestToAnthropicTools(t *testing.T) {
	raw := []byte(`{
  "model":"Claude/claude-test",
  "max_tokens":123,
  "messages":[
    {"role":"system","content":"Be concise."},
    {"role":"user","content":"weather"},
    {"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"SZ\"}"}}]},
    {"role":"tool","tool_call_id":"call_1","content":"sunny"}
  ],
  "tools":[{"type":"function","function":{"name":"weather","description":"Weather","parameters":{"type":"object"}}}]
}`)
	converted, err := openAIRequestToAnthropic(raw, "claude-test")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(converted, &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "claude-test" || numberValue(body["max_tokens"]) != 123 {
		t.Fatalf("unexpected request: %s", converted)
	}
	if len(anySlice(body["system"])) != 1 || len(anySlice(body["tools"])) != 1 {
		t.Fatalf("system/tools were not converted: %s", converted)
	}
	messages := anySlice(body["messages"])
	if len(messages) != 3 {
		t.Fatalf("messages = %d, want 3: %s", len(messages), converted)
	}
	toolUse := anyMap(anySlice(anyMap(messages[1])["content"])[0])
	if toolUse["type"] != "tool_use" || toolUse["name"] != "weather" {
		t.Fatalf("tool call was not converted: %#v", toolUse)
	}
	toolResult := anyMap(anySlice(anyMap(messages[2])["content"])[0])
	if toolResult["type"] != "tool_result" || toolResult["tool_use_id"] != "call_1" {
		t.Fatalf("tool result was not converted: %#v", toolResult)
	}
}

func TestAnthropicRequestToOpenAITools(t *testing.T) {
	raw := []byte(`{
  "model":"Open/gpt-test","max_tokens":100,"stream":true,
  "system":"Be concise.",
  "messages":[
    {"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"weather","input":{"city":"SZ"}}]},
    {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"sunny"}]}
  ],
  "tools":[{"name":"weather","input_schema":{"type":"object"}}]
}`)
	converted, err := anthropicRequestToOpenAI(raw)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(converted, &body); err != nil {
		t.Fatal(err)
	}
	messages := anySlice(body["messages"])
	if len(messages) != 3 || anyString(anyMap(messages[0])["role"]) != "system" || anyString(anyMap(messages[2])["role"]) != "tool" {
		t.Fatalf("messages were not converted: %s", converted)
	}
	if len(anySlice(anyMap(messages[1])["tool_calls"])) != 1 || len(anySlice(body["tools"])) != 1 {
		t.Fatalf("tools were not converted: %s", converted)
	}
	if len(anyMap(body["stream_options"])) == 0 {
		t.Fatalf("stream usage was not requested: %s", converted)
	}
}

func TestOpenAIEndpointCallsAnthropicProvider(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "upstream-key" || r.Header.Get("anthropic-version") != anthropicAPIVersion {
			t.Fatalf("missing Anthropic headers: %#v", r.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "claude-test" || len(anySlice(body["tools"])) != 1 {
			t.Fatalf("unexpected upstream body: %#v", body)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-test",
			"content":     []any{map[string]any{"type": "tool_use", "id": "toolu_1", "name": "weather", "input": map[string]any{"city": "SZ"}}},
			"stop_reason": "tool_use", "usage": map[string]any{"input_tokens": 10, "output_tokens": 4},
		})
	}))
	defer upstream.Close()

	p := ProviderConfig{ID: "custom", Name: "Claude", Type: "anthropic", Enabled: true, BaseURL: upstream.URL + "/v1", APIKey: "upstream-key", Models: []string{"claude-test"}}
	s := &Server{config: Config{AccessKey: "gateway-key", Providers: []ProviderConfig{p}}, client: upstream.Client(), dataDir: t.TempDir()}
	raw := []byte(`{"model":"Claude/claude-test","messages":[{"role":"user","content":"weather"}],"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object"}}}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer gateway-key")
	rr := httptest.NewRecorder()
	s.handleChatCompletions(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &response)
	choice := anyMap(anySlice(response["choices"])[0])
	message := anyMap(choice["message"])
	if choice["finish_reason"] != "tool_calls" || len(anySlice(message["tool_calls"])) != 1 {
		t.Fatalf("Anthropic response was not converted: %s", rr.Body.String())
	}
}

func TestClaudeCodeCompatibleProviderAddsFingerprint(t *testing.T) {
	p := ProviderConfig{
		ID: "custom", Name: "Claude", Type: "anthropic", Enabled: true, BaseURL: "https://example.com/v1", APIKey: "upstream-key", Models: []string{"claude-test"},
		ProviderSpecificData: map[string]string{anthropicRequestModeKey: claudeCodeCompatibleMode},
	}
	converted, err := openAIRequestToAnthropicForProvider([]byte(`{"model":"Claude/claude-test","messages":[{"role":"user","content":"test"}]}`), "claude-test", p)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(converted, &body); err != nil {
		t.Fatal(err)
	}
	if !anthropicBlocksContainText(anySlice(body["system"]), claudeCodeSystemPrompt) || !anthropicBlocksContainText(anySlice(body["system"]), "x-anthropic-billing-header:") {
		t.Fatalf("Claude Code system fingerprint is missing: %#v", body["system"])
	}
	headers := make(http.Header)
	applyAnthropicUpstreamHeaders(headers, nil, p, "key", true)
	if headers.Get("anthropic-beta") != claudeCodeBetaFeatures || headers.Get("x-app") != "cli" || !strings.HasPrefix(headers.Get("User-Agent"), "claude-cli/") || len(headers.Get("x-claude-code-session-id")) < 32 {
		t.Fatalf("Claude CLI fingerprint is incomplete: %#v", headers)
	}
	if target := anthropicRequestTarget(p, "/messages"); target != "https://example.com/v1/messages?beta=true" {
		t.Fatalf("target = %s", target)
	}
}

func TestAnthropicEndpointCallsOpenAIProvider(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "gpt-test" || len(anySlice(body["tools"])) != 1 {
			t.Fatalf("unexpected upstream body: %#v", body)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "chatcmpl_1", "object": "chat.completion", "model": "gpt-test",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "hello"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 8, "completion_tokens": 2, "total_tokens": 10},
		})
	}))
	defer upstream.Close()

	p := ProviderConfig{ID: "custom", Name: "Open", Type: "openai", Enabled: true, BaseURL: upstream.URL + "/v1", APIKey: "upstream-key", Models: []string{"gpt-test"}}
	s := &Server{config: Config{AccessKey: "gateway-key", Providers: []ProviderConfig{p}}, client: upstream.Client(), dataDir: t.TempDir()}
	raw := []byte(`{"model":"Open/gpt-test","max_tokens":100,"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"weather","input_schema":{"type":"object"}}]}`)
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", bytes.NewReader(raw))
	req.Header.Set("x-api-key", "gateway-key")
	rr := httptest.NewRecorder()
	s.handleAnthropicMessages(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &response)
	if response["type"] != "message" || response["role"] != "assistant" || response["stop_reason"] != "end_turn" {
		t.Fatalf("OpenAI response was not converted: %s", rr.Body.String())
	}
	usage := anyMap(response["usage"])
	if numberValue(usage["input_tokens"]) != 8 || numberValue(usage["output_tokens"]) != 2 {
		t.Fatalf("usage was not converted: %#v", usage)
	}
}

func TestAnthropicAndOpenAIStreamConversion(t *testing.T) {
	anthropicSSE := strings.Join([]string{
		`event: message_start`, `data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":3}}}`, ``,
		`event: content_block_start`, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`, ``,
		`event: content_block_delta`, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`, ``,
		`event: message_delta`, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`, ``,
		`event: message_stop`, `data: {"type":"message_stop"}`, ``,
	}, "\n")
	rr := httptest.NewRecorder()
	anthropicStreamToOpenAI(rr, strings.NewReader(anthropicSSE), "Claude/test")
	if !strings.Contains(rr.Body.String(), `"content":"hi"`) || !strings.Contains(rr.Body.String(), `"total_tokens":4`) || !strings.Contains(rr.Body.String(), "[DONE]") {
		t.Fatalf("Anthropic stream conversion failed: %s", rr.Body.String())
	}

	openAISSE := "data: {\"id\":\"chat_1\",\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chat_1\",\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chat_1\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\n" +
		"data: [DONE]\n\n"
	rr = httptest.NewRecorder()
	openAIStreamToAnthropic(rr, strings.NewReader(openAISSE), "Open/test")
	body, _ := io.ReadAll(rr.Result().Body)
	if !strings.Contains(string(body), `"text":"hi"`) || !strings.Contains(string(body), `"input_tokens":3`) || !strings.Contains(string(body), `event: message_stop`) {
		t.Fatalf("OpenAI stream conversion failed: %s", body)
	}
}

func TestAnthropicModelsRespectsGroup(t *testing.T) {
	p := ProviderConfig{ID: "custom", Name: "Claude", Type: "anthropic", Enabled: true, BaseURL: "https://example.invalid/v1", APIKey: "key", Models: []string{"one", "two"}}
	s := &Server{config: Config{AccessKey: "master", Providers: []ProviderConfig{p}, ModelGroups: []ModelGroup{{ID: "g", Name: "g", APIKey: "group", Enabled: true, Models: []string{"Claude/two"}}}}, client: http.DefaultClient}
	req := httptest.NewRequest(http.MethodGet, "/anthropic/v1/models", nil)
	req.Header.Set("x-api-key", "group")
	rr := httptest.NewRecorder()
	s.handleAnthropicModels(rr, req)
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), `"Claude/one"`) || !strings.Contains(rr.Body.String(), `"Claude/two"`) {
		t.Fatalf("group model filtering failed: %d %s", rr.Code, rr.Body.String())
	}
}

func TestClaudeCodeProbeUsesModelList(t *testing.T) {
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" || r.URL.Query().Get("beta") != "true" {
			t.Fatalf("unexpected probe request: %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("x-app") != "cli" || r.Header.Get("x-claude-code-session-id") == "" {
			t.Fatalf("Claude Code model probe headers are missing: %#v", r.Header)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{map[string]any{"id": "claude-opus-4-8"}}})
	}))
	defer upstream.Close()
	p := ProviderConfig{ID: "custom", Name: "Claude", Type: "anthropic", Enabled: true, BaseURL: upstream.URL + "/v1", APIKey: "key", Models: []string{"claude-opus-4-8"}, ProviderSpecificData: map[string]string{anthropicRequestModeKey: claudeCodeCompatibleMode}}
	s := &Server{config: Config{Providers: []ProviderConfig{p}}, client: upstream.Client()}
	available, failures, _ := s.probeProviderModels(t.Context(), p, p.Models)
	if requests != 1 || len(available) != 1 || len(failures) != 0 {
		t.Fatalf("model-list probe failed: requests=%d available=%#v failures=%#v", requests, available, failures)
	}
}

func TestClaudeCodeProviderIsAnthropicOnly(t *testing.T) {
	cc := ProviderConfig{ID: "custom", Name: "ClaudeCode", Type: "anthropic", Enabled: true, BaseURL: "https://example.invalid/v1", APIKey: "key", Models: []string{"claude-opus-4-8"}, ProviderSpecificData: map[string]string{anthropicRequestModeKey: claudeCodeCompatibleMode}}
	open := ProviderConfig{ID: "custom2", Name: "Open", Type: "openai", Enabled: true, BaseURL: "https://example.invalid/v1", APIKey: "key", Models: []string{"gpt-test"}}
	s := &Server{config: Config{AccessKey: "gateway", Providers: []ProviderConfig{cc, open}, AutoModel: AutoModelConfig{Enabled: true, Models: []string{"ClaudeCode/claude-opus-4-8", "Open/gpt-test"}}}, client: http.DefaultClient}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer gateway")
	rr := httptest.NewRecorder()
	s.handleModels(rr, req)
	if strings.Contains(rr.Body.String(), "ClaudeCode/claude-opus-4-8") || !strings.Contains(rr.Body.String(), "Open/gpt-test") {
		t.Fatalf("OpenAI model list leaked Claude Code-only model: %s", rr.Body.String())
	}
	target, ok := s.resolveAutoModelForOpenAI(t.Context())
	if !ok || target != "Open/gpt-test" {
		t.Fatalf("OpenAI auto target = %q, %v", target, ok)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"ClaudeCode/claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer gateway")
	rr = httptest.NewRecorder()
	s.handleChatCompletions(rr, req)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "Anthropic Base URL") {
		t.Fatalf("OpenAI request was not rejected clearly: %d %s", rr.Code, rr.Body.String())
	}
}

func TestClaudeCodeModelsUsesSeparateListAndRejectsGroupKeys(t *testing.T) {
	cc := ProviderConfig{ID: "custom", Name: "Freemodel", Type: "anthropic", Enabled: true, BaseURL: "https://example.invalid/v1", APIKey: "key", Models: []string{"claude-opus-4-8"}, ProviderSpecificData: map[string]string{anthropicRequestModeKey: claudeCodeCompatibleMode}}
	open := ProviderConfig{ID: "custom2", Name: "Open", Type: "openai", Enabled: true, BaseURL: "https://example.invalid/v1", APIKey: "key", Models: []string{"gpt-test"}}
	s := &Server{config: Config{AccessKey: "gateway", Providers: []ProviderConfig{cc, open}, ModelGroups: []ModelGroup{{ID: "g", Name: "g", APIKey: "group", Enabled: true, Models: []string{"Open/gpt-test"}}}}, client: http.DefaultClient}

	req := httptest.NewRequest(http.MethodGet, "/v2/models", nil)
	req.Header.Set("Authorization", "Bearer gateway")
	rr := httptest.NewRecorder()
	s.handleClaudeCodeModels(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Freemodel/claude-opus-4-8") || strings.Contains(rr.Body.String(), "Open/gpt-test") {
		t.Fatalf("separate Claude Code model list failed: %d %s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v2/models", nil)
	req.Header.Set("Authorization", "Bearer group")
	rr = httptest.NewRecorder()
	s.handleClaudeCodeModels(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("group key should not access Claude Code models: %d %s", rr.Code, rr.Body.String())
	}
}

func TestClaudeCodeNativeRequestIsPreserved(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.URL.Query().Get("beta") != "true" {
			t.Fatalf("target = %s", r.URL.String())
		}
		if r.Header.Get("x-claude-code-session-id") != "session-from-client" || r.Header.Get("x-stainless-package-version") != "0.94.0" {
			t.Fatalf("native Claude Code headers were not preserved: %#v", r.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(anyMap(body["context_management"])) == 0 || body["model"] != "claude-opus-4-8" {
			t.Fatalf("native Claude Code body was changed: %#v", body)
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-4-8", "content": []any{map[string]any{"type": "text", "text": "OK"}}, "stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}})
	}))
	defer upstream.Close()
	p := ProviderConfig{ID: "custom", Name: "ClaudeCode", Type: "anthropic", Enabled: true, BaseURL: upstream.URL + "/v1", APIKey: "upstream", Models: []string{"claude-opus-4-8"}, ProviderSpecificData: map[string]string{anthropicRequestModeKey: claudeCodeCompatibleMode}}
	s := &Server{config: Config{AccessKey: "gateway", Providers: []ProviderConfig{p}}, client: upstream.Client(), dataDir: t.TempDir()}
	raw := `{"model":"ClaudeCode/claude-opus-4-8","max_tokens":4,"messages":[{"role":"user","content":"hi"}],"context_management":{"edits":[]}}`
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages?beta=true", strings.NewReader(raw))
	req.Header.Set("x-api-key", "gateway")
	req.Header.Set("x-claude-code-session-id", "session-from-client")
	req.Header.Set("x-stainless-package-version", "0.94.0")
	rr := httptest.NewRecorder()
	s.handleAnthropicMessages(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"text":"OK"`) {
		t.Fatalf("native Claude Code forwarding failed: %d %s", rr.Code, rr.Body.String())
	}
}

func TestAnthropicEndpointStreamsOpenAIProvider(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chat_1\",\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chat_1\",\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chat_1\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	p := ProviderConfig{ID: "custom", Name: "Open", Type: "openai", Enabled: true, BaseURL: upstream.URL + "/v1", APIKey: "upstream", Models: []string{"gpt-test"}}
	s := &Server{config: Config{AccessKey: "gateway", Providers: []ProviderConfig{p}}, client: upstream.Client(), dataDir: t.TempDir()}
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(`{"model":"Open/gpt-test","max_tokens":20,"stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("x-api-key", "gateway")
	rr := httptest.NewRecorder()
	s.handleAnthropicMessages(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"text":"hello"`) || !strings.Contains(rr.Body.String(), "event: message_stop") {
		t.Fatalf("stream bridge failed: %d %s", rr.Code, rr.Body.String())
	}
}

func TestAnthropicCountTokensAndAdminAddress(t *testing.T) {
	p := ProviderConfig{ID: "custom", Name: "Open", Type: "openai", Enabled: true, Models: []string{"gpt-test"}}
	s := &Server{config: Config{AccessKey: "gateway", Providers: []ProviderConfig{p}}}
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages/count_tokens", strings.NewReader(`{"model":"Open/gpt-test","system":"简洁回答","messages":[{"role":"user","content":"hello world"}]}`))
	req.Header.Set("x-api-key", "gateway")
	rr := httptest.NewRecorder()
	s.handleAnthropicCountTokens(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "input_tokens") {
		t.Fatalf("count tokens failed: %d %s", rr.Code, rr.Body.String())
	}
	html := adminHTMLLiteV2(`{"providers":[]}`)
	if !strings.Contains(html, "Anthropic Base URL") || !strings.Contains(html, "programAnthropicBaseUrl") || !strings.Contains(html, "Anthropic Compatible") || !strings.Contains(html, "Claude Code 兼容") || !strings.Contains(html, "模型探测只校验上游模型列表") {
		t.Fatal("admin page does not expose Anthropic controls")
	}
}

func TestMergeConfigKeepsCustomAnthropicProvider(t *testing.T) {
	cfg := mergeDefaultConfig(Config{Providers: []ProviderConfig{{ID: "custom2", Name: "Claude Proxy", Type: "anthropic", BaseURL: "https://example.com/v1"}}})
	p, ok := func() (ProviderConfig, bool) {
		for _, provider := range cfg.Providers {
			if provider.ID == "custom2" {
				return provider, true
			}
		}
		return ProviderConfig{}, false
	}()
	if !ok || p.Type != "anthropic" || p.Name != "Claude Proxy" {
		t.Fatalf("custom Anthropic provider was not preserved: %#v", p)
	}
}
