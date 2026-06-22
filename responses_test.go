package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func responsesTestProvider(baseURL string) ProviderConfig {
	return ProviderConfig{
		ID: "custom", Name: "OpenModel", Type: "openai", Enabled: true, BaseURL: baseURL, APIKey: "upstream-key",
		Models: []string{"gpt-test"}, EnabledModels: []string{"gpt-test"},
		ProviderSpecificData: map[string]string{openAIRequestModeKey: openAIResponsesMode},
	}
}

func TestChatCompletionsToResponses(t *testing.T) {
	raw := []byte(`{
  "model":"OpenModel/gpt-test",
  "messages":[
    {"role":"system","content":"Be concise."},
    {"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]},
    {"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},
    {"role":"tool","tool_call_id":"call_1","content":"result"}
  ],
  "tools":[{"type":"function","function":{"name":"lookup","description":"Lookup","parameters":{"type":"object"}}}],
  "max_tokens":32,
  "stream":true
}`)
	converted, err := chatCompletionsToResponses(raw, "gpt-test")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(converted, &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "gpt-test" || body["instructions"] != "Be concise." || numberValue(body["max_output_tokens"]) != 32 || body["stream"] != true {
		t.Fatalf("unexpected Responses request: %#v", body)
	}
	input := anySlice(body["input"])
	if len(input) != 3 || anyString(anyMap(input[1])["type"]) != "function_call" || anyString(anyMap(input[2])["type"]) != "function_call_output" {
		t.Fatalf("message/tool conversion failed: %#v", input)
	}
	tools := anySlice(body["tools"])
	if len(tools) != 1 || anyString(anyMap(tools[0])["name"]) != "lookup" {
		t.Fatalf("tool definition conversion failed: %#v", tools)
	}
}

func TestResponsesResponseToChat(t *testing.T) {
	raw := []byte(`{
  "id":"resp_1","created_at":123,"status":"completed",
  "output":[
    {"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]},
    {"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"}
  ],
  "usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}
}`)
	converted, err := responsesResponseToChat(raw, "OpenModel/gpt-test")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(converted, &body); err != nil {
		t.Fatal(err)
	}
	choice := anyMap(anySlice(body["choices"])[0])
	message := anyMap(choice["message"])
	if message["content"] != "hello" || choice["finish_reason"] != "tool_calls" || len(anySlice(message["tool_calls"])) != 1 {
		t.Fatalf("unexpected Chat Completions response: %#v", body)
	}
	if numberValue(anyMap(body["usage"])["total_tokens"]) != 5 {
		t.Fatalf("usage conversion failed: %#v", body["usage"])
	}
}

func TestResponsesProviderRoutesChatCompletions(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected upstream target: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer upstream-key" {
			t.Fatalf("missing upstream authorization")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "gpt-test" || body["input"] == nil || body["messages"] != nil {
			t.Fatalf("unexpected upstream body: %#v", body)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "resp_1", "created_at": 123, "status": "completed",
			"output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "OK"}}}},
			"usage":  map[string]any{"input_tokens": 2, "output_tokens": 1, "total_tokens": 3},
		})
	}))
	defer upstream.Close()
	p := responsesTestProvider(upstream.URL + "/v1")
	s := &Server{config: Config{AccessKey: "gateway", Providers: []ProviderConfig{p}}, client: upstream.Client(), dataDir: t.TempDir()}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"OpenModel/gpt-test","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer gateway")
	rr := httptest.NewRecorder()
	s.handleChatCompletions(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"content":"OK"`) || !strings.Contains(rr.Body.String(), `"model":"OpenModel/gpt-test"`) {
		t.Fatalf("Responses route failed: %d %s", rr.Code, rr.Body.String())
	}
}

func TestResponsesStreamToChat(t *testing.T) {
	stream := "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"created_at\":123}}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3}}}\n\n"
	rr := httptest.NewRecorder()
	responsesStreamToChat(rr, strings.NewReader(stream), "OpenModel/gpt-test")
	if !strings.Contains(rr.Body.String(), `"content":"hello"`) || !strings.Contains(rr.Body.String(), `"total_tokens":3`) || !strings.Contains(rr.Body.String(), "data: [DONE]") {
		t.Fatalf("stream conversion failed: %s", rr.Body.String())
	}
}

func TestResponsesModelsFilterSupportedAPIs(t *testing.T) {
	models := `{"data":[
  {"id":"gpt-test","supported_apis":["responses"]},
  {"id":"claude-test","supported_apis":["messages"]},
  {"id":"qwen-test","supported_protocols":["responses"]},
  {"id":"gemini-test","supported_protocols":["gemini"]},
  {"id":"legacy-without-metadata"}
]}`
	ids, err := parseResponsesModelIDs(strings.NewReader(models))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(ids, ",")
	if !strings.Contains(joined, "gpt-test") || !strings.Contains(joined, "qwen-test") || !strings.Contains(joined, "legacy-without-metadata") || strings.Contains(joined, "claude-test") || strings.Contains(joined, "gemini-test") {
		t.Fatalf("Responses model filtering failed: %#v", ids)
	}
}

func TestFetchResponsesModelsUsesProtocolMetadata(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected model endpoint: %s", r.URL.Path)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{
			map[string]any{"id": "gpt-test", "supported_protocols": []any{"responses"}},
			map[string]any{"id": "claude-test", "supported_protocols": []any{"messages"}},
		}})
	}))
	defer upstream.Close()
	p := responsesTestProvider(upstream.URL + "/v1")
	ids, err := fetchCompatibleModels(t.Context(), upstream.Client(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "gpt-test" {
		t.Fatalf("unexpected fetched Responses models: %#v", ids)
	}
}

func TestFetchAnthropicModelsUsesMessagesMetadata(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected model endpoint: %s", r.URL.Path)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{
			map[string]any{"id": "gpt-test", "supported_protocols": []any{"responses"}},
			map[string]any{"id": "claude-test", "supported_protocols": []any{"messages"}},
		}})
	}))
	defer upstream.Close()
	p := ProviderConfig{ID: "custom", Name: "OpenModel Messages", Type: "anthropic", Enabled: true, BaseURL: upstream.URL + "/v1", APIKey: "key"}
	s := &Server{client: upstream.Client()}
	ids, err := s.fetchProviderModels(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "claude-test" {
		t.Fatalf("unexpected fetched Messages models: %#v", ids)
	}
}

func TestResponsesProviderAdminOption(t *testing.T) {
	cfg := `{"providers":[{"id":"custom","name":"OpenModel","type":"openai","enabled":true,"base_url":"https://example.com/v1","provider_specific_data":{"customProvider":"true","openaiRequestMode":"responses"}}]}`
	html := adminHTMLLiteV2(cfg)
	if !strings.Contains(html, `option value="responses"`) || !strings.Contains(html, `OpenAI Responses`) || !strings.Contains(html, `psd.openaiRequestMode='responses'`) || !strings.Contains(html, `stopProviderProbe`) || !strings.Contains(html, `new AbortController()`) {
		t.Fatalf("Admin does not expose Responses mode")
	}
}

func TestAnthropicEndpointCanUseResponsesProvider(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected target: %s", r.URL.Path)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "resp_1", "created_at": 123, "status": "completed",
			"output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "bridged"}}}},
			"usage":  map[string]any{"input_tokens": 2, "output_tokens": 1, "total_tokens": 3},
		})
	}))
	defer upstream.Close()
	p := responsesTestProvider(upstream.URL + "/v1")
	s := &Server{config: Config{AccessKey: "gateway", Providers: []ProviderConfig{p}}, client: upstream.Client(), dataDir: t.TempDir()}
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(`{"model":"OpenModel/gpt-test","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", "gateway")
	rr := httptest.NewRecorder()
	s.handleAnthropicMessages(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"text":"bridged"`) {
		t.Fatalf("Anthropic to Responses bridge failed: %d %s", rr.Code, rr.Body.String())
	}
}
