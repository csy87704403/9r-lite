package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenCodeUsesConfiguredKeysAndRotatesOnRateLimit(t *testing.T) {
	var calls []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if calls[len(calls)-1] == "zen-key-1" {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate limit exceeded"})
			return
		}
		writeJSON(w, http.StatusOK, probeTextResponse())
	}))
	defer upstream.Close()

	p := ProviderConfig{
		ID: "oc", Name: "OpenCode Free", Type: "opencode-free", Enabled: true,
		BaseURL: upstream.URL + "/zen/v1", APIKey: "zen-key-1", APIKeys: []string{"zen-key-1", "zen-key-2"},
		Models: []string{"deepseek-v4-flash-free"}, EnabledModels: []string{"deepseek-v4-flash-free"},
	}
	s := &Server{config: Config{AccessKey: "gateway", Providers: []ProviderConfig{p}}, client: upstream.Client(), dataDir: t.TempDir()}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"oc/deepseek-v4-flash-free","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer gateway")
	rr := httptest.NewRecorder()
	s.handleChatCompletions(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("response = %d %s", rr.Code, rr.Body.String())
	}
	if got := strings.Join(calls, ","); got != "zen-key-1,zen-key-2" {
		t.Fatalf("OpenCode key order = %s", got)
	}
	updated, _ := s.providerByID("oc")
	if updated.ProviderSpecificData["active_key_index"] != "1" {
		t.Fatalf("active OpenCode key = %#v", updated.ProviderSpecificData)
	}
}

func TestOpenCodeKeepsAnonymousPublicFallback(t *testing.T) {
	gotAuthorization := ""
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		writeJSON(w, http.StatusOK, probeTextResponse())
	}))
	defer upstream.Close()

	p := ProviderConfig{ID: "oc", Type: "opencode-free", Enabled: true, BaseURL: upstream.URL + "/zen/v1", Models: []string{"model"}, EnabledModels: []string{"model"}}
	s := &Server{config: Config{AccessKey: "gateway", Providers: []ProviderConfig{p}}, client: upstream.Client(), dataDir: t.TempDir()}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"oc/model","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer gateway")
	rr := httptest.NewRecorder()
	s.handleChatCompletions(rr, req)

	if rr.Code != http.StatusOK || gotAuthorization != "Bearer public" {
		t.Fatalf("anonymous response=%d authorization=%q", rr.Code, gotAuthorization)
	}
}

func TestOpenCodeSingleKeyProbeUsesZenEndpoint(t *testing.T) {
	gotAuthorization := ""
	gotClient := ""
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		gotClient = r.Header.Get("x-opencode-client")
		writeJSON(w, http.StatusOK, probeTextResponse())
	}))
	defer upstream.Close()

	p := ProviderConfig{ID: "oc", Type: "opencode-free", BaseURL: upstream.URL + "/zen/v1"}
	s := &Server{client: upstream.Client()}
	status, _, err := s.probeSingleCompatibleModelWithKey(context.Background(), p, "model", "zen-key")
	if err != nil || status != http.StatusOK {
		t.Fatalf("probe status=%d err=%v", status, err)
	}
	if gotAuthorization != "Bearer zen-key" || gotClient != "desktop" {
		t.Fatalf("probe headers authorization=%q client=%q", gotAuthorization, gotClient)
	}
}

func TestOpenCodePublishCardRendersZenKeyEditor(t *testing.T) {
	html := adminHTMLLiteV2(`{"providers":[{"id":"oc","name":"OpenCode Free","type":"opencode-free","enabled":true,"models":["model"]}]}`)
	for _, marker := range []string{"OpenCode Zen API Keys（可选）", "saveOpenCodeKeys", "全部留空则使用匿名 public 模式", "?'apiStatus_'+id:'publishStatus_'+id"} {
		if !strings.Contains(html, marker) {
			t.Fatalf("admin page is missing %q", marker)
		}
	}
}
