package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestImageHistoryDetection(t *testing.T) {
	openAI := []byte(`{"messages":[
  {"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}},{"type":"text","text":"这是什么"}]},
  {"role":"assistant","content":"一张图片"},
  {"role":"user","content":"继续分析左边"}
]}`)
	if !openAIChatHasImage(openAI) {
		t.Fatal("OpenAI image history was not detected")
	}
	anthropic := []byte(`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}},{"type":"text","text":"这是什么"}]}]}`)
	if !anthropicMessagesHaveImage(anthropic) {
		t.Fatal("Anthropic image history was not detected")
	}
	toolSchema := []byte(`{"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"save","parameters":{"properties":{"image":{"type":"string"}}}}}]}`)
	if openAIChatHasImage(toolSchema) {
		t.Fatal("tool schema image field must not trigger multimodal routing")
	}
}

func TestAutoImageConversationUsesVisionCandidates(t *testing.T) {
	var normalCalls atomic.Int32
	var visionCalls atomic.Int32
	newUpstream := func(counter *atomic.Int32, reply string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			counter.Add(1)
			writeJSON(w, http.StatusOK, map[string]any{
				"id": "chat_1", "object": "chat.completion", "model": reply,
				"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": reply}, "finish_reason": "stop"}},
			})
		}))
	}
	normalUpstream := newUpstream(&normalCalls, "normal")
	defer normalUpstream.Close()
	visionUpstream := newUpstream(&visionCalls, "vision")
	defer visionUpstream.Close()

	normal := ProviderConfig{ID: "custom", Name: "Normal", Type: "openai", Enabled: true, BaseURL: normalUpstream.URL + "/v1", APIKey: "key", Models: []string{"deepseek"}, EnabledModels: []string{"deepseek"}}
	vision := ProviderConfig{ID: "custom2", Name: "Vision", Type: "openai", Enabled: true, BaseURL: visionUpstream.URL + "/v1", APIKey: "key", Models: []string{"vision-model"}, EnabledModels: []string{"vision-model"}}
	s := &Server{config: Config{AccessKey: "gateway", Providers: []ProviderConfig{normal, vision}, AutoModel: AutoModelConfig{Enabled: true, Models: []string{"Normal/deepseek"}, VisionModels: []string{"Vision/vision-model"}}}, client: normalUpstream.Client(), dataDir: t.TempDir()}

	call := func(raw string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(raw))
		req.Header.Set("Authorization", "Bearer gateway")
		rr := httptest.NewRecorder()
		s.handleChatCompletions(rr, req)
		return rr
	}
	pureText := call(`{"model":"auto","messages":[{"role":"user","content":"hello"}]}`)
	if pureText.Code != http.StatusOK || !strings.Contains(pureText.Body.String(), `"content":"normal"`) {
		t.Fatalf("text Auto route failed: %d %s", pureText.Code, pureText.Body.String())
	}
	imageHistory := call(`{"model":"auto","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}},{"type":"text","text":"describe"}]},{"role":"assistant","content":"previous"},{"role":"user","content":"continue"}]}`)
	if imageHistory.Code != http.StatusOK || !strings.Contains(imageHistory.Body.String(), `"content":"vision"`) {
		t.Fatalf("vision Auto route failed: %d %s", imageHistory.Code, imageHistory.Body.String())
	}
	if normalCalls.Load() != 1 || visionCalls.Load() != 1 {
		t.Fatalf("unexpected route counts: normal=%d vision=%d", normalCalls.Load(), visionCalls.Load())
	}
}

func TestAutoImageConversationRequiresVisionCandidate(t *testing.T) {
	p := ProviderConfig{ID: "custom", Name: "Normal", Type: "openai", Enabled: true, BaseURL: "https://example.invalid/v1", APIKey: "key", Models: []string{"deepseek"}, EnabledModels: []string{"deepseek"}}
	s := &Server{config: Config{AccessKey: "gateway", Providers: []ProviderConfig{p}, AutoModel: AutoModelConfig{Enabled: true, Models: []string{"Normal/deepseek"}}}, client: http.DefaultClient}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]}`))
	req.Header.Set("Authorization", "Bearer gateway")
	rr := httptest.NewRecorder()
	s.handleChatCompletions(rr, req)
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "multimodal") {
		t.Fatalf("missing multimodal target error: %d %s", rr.Code, rr.Body.String())
	}
}

func TestAutoVisionConfigNormalizationAndAdmin(t *testing.T) {
	cfg := Config{Providers: []ProviderConfig{{ID: "custom", Name: "Vision", Type: "openai"}}, AutoModel: AutoModelConfig{VisionModels: []string{"custom/vision-model"}}}
	normalizeConfigModelRefs(&cfg)
	if len(cfg.AutoModel.VisionModels) != 1 || cfg.AutoModel.VisionModels[0] != "Vision/vision-model" {
		t.Fatalf("vision model reference was not normalized: %#v", cfg.AutoModel.VisionModels)
	}
	html := adminHTMLLiteV2(`{"providers":[],"auto_model":{"enabled":true,"models":[],"vision_models":[]}}`)
	if !strings.Contains(html, "多模态候选模型") || !strings.Contains(html, "addAutoVisionModelValue") || !strings.Contains(html, "vision_models:autoVisionModels") {
		t.Fatal("Admin multimodal Auto controls are missing")
	}
}
