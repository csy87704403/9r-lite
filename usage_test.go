package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestUsageTracksGroupAndActualAutoAttempts(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request chatRequest
		if err := decodeJSONBody(r, &request); err != nil {
			t.Fatal(err)
		}
		if request.Model == "first" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"message": "model unavailable"}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"model":   request.Model,
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "OK"}}},
			"usage":   map[string]any{"prompt_tokens": 11, "completion_tokens": 4, "total_tokens": 15},
		})
	}))
	defer upstream.Close()

	first := ProviderConfig{ID: "first-provider", Name: "First", Type: "openai", Enabled: true, BaseURL: upstream.URL + "/v1", APIKey: "key", Models: []string{"first"}, EnabledModels: []string{"first"}}
	second := ProviderConfig{ID: "second-provider", Name: "Second", Type: "openai", Enabled: true, BaseURL: upstream.URL + "/v1", APIKey: "key", Models: []string{"second"}, EnabledModels: []string{"second"}}
	s := &Server{
		config: Config{
			ModelGroups: []ModelGroup{{ID: "agents", Name: "Agent 分组", APIKey: "group-key", Enabled: true, Models: []string{"auto"}}},
			AutoModel:   AutoModelConfig{Enabled: true, Models: []string{"first-provider/first", "second-provider/second"}},
			Providers:   []ProviderConfig{first, second},
		},
		usage: newUsageStore(), client: upstream.Client(), dataDir: t.TempDir(),
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer group-key")
	rr := httptest.NewRecorder()
	s.handleChatCompletions(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("response = %d %s", rr.Code, rr.Body.String())
	}

	group := s.usageSnapshot().Groups["agents"]
	if group == nil || group.Name != "Agent 分组" {
		t.Fatalf("group stats = %#v", group)
	}
	if group.Total.ClientRequests != 1 || group.Total.ClientSuccess != 1 || group.Total.UpstreamCalls != 2 || group.Total.UpstreamFailed != 1 || group.Total.TotalTokens != 15 {
		t.Fatalf("group counters = %#v", group.Total)
	}
	failed := group.Models["first-provider/first"]
	success := group.Models["second-provider/second"]
	if failed == nil || failed.Total.UpstreamFailed != 1 || failed.LastFailure == "" || failed.LastFailureStatus != http.StatusServiceUnavailable {
		t.Fatalf("failed model stats = %#v", failed)
	}
	if success == nil || success.Total.TotalTokens != 15 || success.Total.PromptTokens != 11 || success.Total.CompletionTokens != 4 {
		t.Fatalf("successful model stats = %#v", success)
	}
}

func TestUsageParsesSplitStreamingUsage(t *testing.T) {
	rr := httptest.NewRecorder()
	w := &usageResponseWriter{target: rr, stream: true}
	_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,"))
	_, _ = w.Write([]byte("\"completion_tokens\":3,\"total_tokens\":10}}\n\n"))
	w.finishStream()
	if !w.usage.Found || w.usage.Prompt != 7 || w.usage.Completion != 3 || w.usage.Total != 10 {
		t.Fatalf("stream usage = %#v", w.usage)
	}
}

func TestInternalProbeDoesNotCreateUsageGroup(t *testing.T) {
	s := &Server{usage: newUsageStore()}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(withInternalBypass(req))
	w, _, finish := s.beginClientUsage(httptest.NewRecorder(), req, accessScope{Full: true})
	w.WriteHeader(http.StatusOK)
	finish()
	if len(s.usageSnapshot().Groups) != 0 {
		t.Fatal("internal probe was counted")
	}
}

func TestAdminIncludesUsageView(t *testing.T) {
	html := adminHTMLLiteV2(`{"providers":[]}`)
	for _, marker := range []string{"用量统计", "panel_usage", "loadUsageStats", "/api/admin/usage", "全部分组模型汇总", "成功调用", "失败尝试", "最后失败原因", "usageLogModal", "查看日志", "复制日志"} {
		if !strings.Contains(html, marker) {
			t.Fatalf("admin usage view missing %q", marker)
		}
	}
}

func TestUsageConcurrentAccounting(t *testing.T) {
	s := &Server{usage: newUsageStore()}
	info := usageRequestInfo{GroupID: "agents", GroupName: "Agents"}
	p := ProviderConfig{ID: "provider", Name: "Provider"}
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.recordClientUsage(info, http.StatusOK)
			s.recordUpstreamUsage(info, p, "model", http.StatusOK, tokenUsage{Prompt: 2, Completion: 1, Total: 3, Found: true}, "", 0)
		}()
	}
	wg.Wait()
	group := s.usageSnapshot().Groups["agents"]
	if group.Total.ClientRequests != 100 || group.Total.UpstreamCalls != 100 || group.Total.TotalTokens != 300 {
		t.Fatalf("concurrent counters = %#v", group.Total)
	}
}

func TestUsageCountsExplicitErrorResponseAsFailure(t *testing.T) {
	s := &Server{usage: newUsageStore()}
	info := usageRequestInfo{GroupID: "agents", GroupName: "Agents"}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(context.WithValue(req.Context(), usageRequestContextKey{}, &info))
	w, finish := s.beginUpstreamUsage(httptest.NewRecorder(), req, ProviderConfig{ID: "provider", Name: "Provider"}, "model", false)
	errorMessage := "invalid request: " + strings.Repeat("details-", 20) + "reasoning_content must be passed back"
	writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": errorMessage})
	finish()
	stats := s.usageSnapshot().Groups["agents"].Models["provider/model"]
	if stats.Total.UpstreamSuccess != 0 || stats.Total.UpstreamFailed != 1 || stats.LastFailureStatus != http.StatusOK || stats.LastFailure != errorMessage {
		t.Fatalf("explicit error usage stats = %#v", stats)
	}
}

func decodeJSONBody(r *http.Request, target any) error {
	return json.NewDecoder(r.Body).Decode(target)
}

func withInternalBypass(r *http.Request) context.Context {
	return context.WithValue(r.Context(), internalBypassKey{}, true)
}
