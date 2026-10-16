package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type auditVisionCall struct {
	model string
	body  string
}

func auditVisionServer(t *testing.T) (*Server, *[]auditVisionCall, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	calls := []auditVisionCall{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		model, _ := in["model"].(string)
		mu.Lock()
		calls = append(calls, auditVisionCall{model: model, body: string(body)})
		callNumber := len(calls)
		mu.Unlock()
		message := map[string]any{"role": "assistant", "content": "OK"}
		finishReason := "stop"
		if model == "vision" && callNumber == 3 {
			message = map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": "inspect", "arguments": "{}"}}}}
			finishReason = "tool_calls"
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": "audit", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finishReason}}})
	}))
	t.Cleanup(upstream.Close)
	p := ProviderConfig{ID: "audit", Name: "Audit", Type: "openai", Enabled: true, BaseURL: upstream.URL + "/v1", APIKey: "dummy-local-only", Models: []string{"text", "vision"}, EnabledModels: []string{"text", "vision"}, ModelMultimodal: map[string]bool{"vision": true}}
	s := &Server{config: Config{AccessKey: "dummy-gateway", Providers: []ProviderConfig{p}, AutoModel: AutoModelConfig{Enabled: true, Models: []string{"audit/text", "audit/vision"}}}, client: upstream.Client(), dataDir: t.TempDir()}
	return s, &calls, &mu
}

func auditVisionRequest(t *testing.T, s *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer dummy-gateway")
	req.Header.Set("User-Agent", "audit-agent")
	req.Header.Set("X-9Router-Session-ID", "audit-conversation")
	rr := httptest.NewRecorder()
	if path == "/v1/messages" {
		s.handleAnthropicMessages(rr, req)
	} else {
		s.handleChatCompletions(rr, req)
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("mock request failed: HTTP %d %s", rr.Code, rr.Body.String())
	}
	return rr
}

func TestAuditVisionFollowupToolLoopKeepsSameTurnModel(t *testing.T) {
	s, calls, mu := auditVisionServer(t)
	base := `{"role":"user","content":"start"}`
	image := `{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}},{"type":"text","text":"read the image"}]}`
	followup := `{"role":"user","content":"Check the left side with inspect tool"}`
	toolCall := `{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"inspect","arguments":"{}"}}]}`
	toolResult := `{"role":"tool","tool_call_id":"call_1","content":"inspection complete"}`
	assistant := `{"role":"assistant","content":"OK"}`
	body := func(messages ...string) string {
		return `{"model":"auto","messages":[` + strings.Join(messages, ",") + `],"tools":[{"type":"function","function":{"name":"inspect","parameters":{"type":"object","properties":{}}}}]}`
	}
	auditVisionRequest(t, s, "/v1/chat/completions", body(base))
	auditVisionRequest(t, s, "/v1/chat/completions", body(base, assistant, image))
	rr := auditVisionRequest(t, s, "/v1/chat/completions", body(base, assistant, image, assistant, followup))
	if !strings.Contains(rr.Body.String(), "tool_calls") {
		t.Fatalf("vision mock did not issue a tool call: %s", rr.Body.String())
	}
	auditVisionRequest(t, s, "/v1/chat/completions", body(base, assistant, image, assistant, followup, toolCall, toolResult))
	mu.Lock()
	defer mu.Unlock()
	models := []string{}
	for _, call := range *calls {
		models = append(models, call.model)
	}
	t.Logf("actual upstream models: %v", models)
	if len(*calls) != 4 || (*calls)[3].model != "vision" {
		t.Fatalf("same user turn changed away from vision after its tool call: models=%v; image retained=%v", models, strings.Contains((*calls)[3].body, "image_url"))
	}
}

func TestAuditAnthropicToolResultImageRoutesAsImage(t *testing.T) {
	s, calls, mu := auditVisionServer(t)
	body := `{"model":"auto","max_tokens":64,"messages":[{"role":"user","content":"inspect screenshot"},{"role":"assistant","content":[{"type":"tool_use","id":"screen_1","name":"screenshot","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"screen_1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}]}]}]}`
	t.Logf("image detection: any=%v latest=%v", anthropicMessagesHaveImage([]byte(body)), anthropicMessagesLatestUserHasImage([]byte(body)))
	auditVisionRequest(t, s, "/v1/messages", body)
	mu.Lock()
	defer mu.Unlock()
	if len(*calls) != 1 || (*calls)[0].model != "vision" {
		t.Fatalf("tool-result image did not select vision: calls=%v; converted body=%s", len(*calls), (*calls)[0].body)
	}
	if !openAIChatHasImage([]byte((*calls)[0].body)) {
		t.Fatalf("tool-result image was not forwarded as an actual image: %s", (*calls)[0].body)
	}
}
