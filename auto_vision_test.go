package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	if openAIChatLatestUserHasImage(openAI) {
		t.Fatal("historical image must not keep Auto on a multimodal model")
	}
	stripped, err := stripOpenAIHistoricalImages(openAI)
	if err != nil || openAIChatHasImage(stripped) {
		t.Fatalf("historical image was not stripped: err=%v body=%s", err, stripped)
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

func TestAutoImageConversationReturnsToGeneralCandidates(t *testing.T) {
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
	vision := ProviderConfig{ID: "custom2", Name: "Vision", Type: "openai", Enabled: true, BaseURL: visionUpstream.URL + "/v1", APIKey: "key", Models: []string{"vision-model"}, EnabledModels: []string{"vision-model"}, ModelMultimodal: map[string]bool{"vision-model": true}}
	s := &Server{config: Config{AccessKey: "gateway", Providers: []ProviderConfig{normal, vision}, AutoModel: AutoModelConfig{Enabled: true, Models: []string{"Normal/deepseek", "Vision/vision-model"}}}, client: normalUpstream.Client(), dataDir: t.TempDir()}

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
	image := call(`{"model":"auto","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}},{"type":"text","text":"describe"}]}]}`)
	if image.Code != http.StatusOK || !strings.Contains(image.Body.String(), `"content":"vision"`) {
		t.Fatalf("vision Auto route failed: %d %s", image.Code, image.Body.String())
	}
	textFollowUp := call(`{"model":"auto","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}},{"type":"text","text":"describe"}]},{"role":"assistant","content":"previous"},{"role":"user","content":"continue with text only"}]}`)
	if textFollowUp.Code != http.StatusOK || !strings.Contains(textFollowUp.Body.String(), `"content":"vision"`) {
		t.Fatalf("text follow-up did not continue the full candidate rotation: %d %s", textFollowUp.Code, textFollowUp.Body.String())
	}
	if normalCalls.Load() != 1 || visionCalls.Load() != 2 {
		t.Fatalf("unexpected route counts: normal=%d vision=%d", normalCalls.Load(), visionCalls.Load())
	}
}

func TestAutoCandidatesRoundRobinByModality(t *testing.T) {
	providers := []ProviderConfig{
		{ID: "text1", Enabled: true, Models: []string{"model"}},
		{ID: "vision1", Enabled: true, Models: []string{"model"}, ModelMultimodal: map[string]bool{"model": true}},
		{ID: "text2", Enabled: true, Models: []string{"model"}},
		{ID: "vision2", Enabled: true, Models: []string{"model"}, ModelMultimodal: map[string]bool{"model": true}},
	}
	s := &Server{config: Config{AutoModel: AutoModelConfig{Enabled: true, Models: []string{"text1/model", "vision1/model", "text2/model", "vision2/model"}}, Providers: providers}}
	if got := s.autoChatCandidatesForOpenAI(t.Context(), false); len(got) != 4 || got[0] != "text1/model" {
		t.Fatalf("first text rotation = %#v", got)
	}
	if got := s.autoChatCandidatesForOpenAI(t.Context(), false); len(got) != 4 || got[0] != "vision1/model" {
		t.Fatalf("second text rotation = %#v", got)
	}
	if got := s.autoChatCandidatesForOpenAI(t.Context(), false); len(got) != 4 || got[0] != "text2/model" {
		t.Fatalf("third text rotation = %#v", got)
	}
	if got := s.autoChatCandidatesForOpenAI(t.Context(), true); len(got) != 2 || got[0] != "vision1/model" {
		t.Fatalf("first vision rotation = %#v", got)
	}
	if got := s.autoChatCandidatesForOpenAI(t.Context(), true); len(got) != 2 || got[0] != "vision2/model" {
		t.Fatalf("second vision rotation = %#v", got)
	}
}

func TestAutoCandidateRoundRobinIsConcurrentSafe(t *testing.T) {
	providers := []ProviderConfig{
		{ID: "text1", Enabled: true, Models: []string{"model"}},
		{ID: "text2", Enabled: true, Models: []string{"model"}},
	}
	s := &Server{config: Config{AutoModel: AutoModelConfig{Enabled: true, Models: []string{"text1/model", "text2/model"}}, Providers: providers}}
	var first, second atomic.Int32
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			candidates := s.autoChatCandidatesForOpenAI(t.Context(), false)
			if len(candidates) == 0 {
				return
			}
			switch candidates[0] {
			case "text1/model":
				first.Add(1)
			case "text2/model":
				second.Add(1)
			}
		}()
	}
	wg.Wait()
	if first.Load() != 50 || second.Load() != 50 {
		t.Fatalf("unbalanced concurrent rotation: first=%d second=%d", first.Load(), second.Load())
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

func TestAutoImageConversationFallsBackBetweenDetectedMultimodalModels(t *testing.T) {
	var textCalls atomic.Int32
	var firstVisionCalls atomic.Int32
	var secondVisionCalls atomic.Int32
	newUpstream := func(counter *atomic.Int32, status int, content string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			counter.Add(1)
			if status != http.StatusOK {
				writeJSON(w, status, map[string]any{"error": map[string]any{"message": "model unavailable"}})
				return
			}
			writeJSON(w, status, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}}}})
		}))
	}
	textUpstream := newUpstream(&textCalls, http.StatusOK, "text")
	defer textUpstream.Close()
	firstVisionUpstream := newUpstream(&firstVisionCalls, http.StatusServiceUnavailable, "")
	defer firstVisionUpstream.Close()
	secondVisionUpstream := newUpstream(&secondVisionCalls, http.StatusOK, "vision fallback")
	defer secondVisionUpstream.Close()

	textProvider := ProviderConfig{ID: "text", Type: "openai", Enabled: true, BaseURL: textUpstream.URL + "/v1", APIKey: "key", Models: []string{"text-model"}}
	firstVision := ProviderConfig{ID: "vision1", Type: "openai", Enabled: true, BaseURL: firstVisionUpstream.URL + "/v1", APIKey: "key", Models: []string{"vision-model"}, ModelMultimodal: map[string]bool{"vision-model": true}}
	secondVision := ProviderConfig{ID: "vision2", Type: "openai", Enabled: true, BaseURL: secondVisionUpstream.URL + "/v1", APIKey: "key", Models: []string{"vision-model"}, ModelMultimodal: map[string]bool{"vision-model": true}}
	s := &Server{
		config: Config{AccessKey: "gateway", Providers: []ProviderConfig{textProvider, firstVision, secondVision}, AutoModel: AutoModelConfig{Enabled: true, Models: []string{"text/text-model", "vision1/vision-model", "vision2/vision-model"}}},
		client: textUpstream.Client(), dataDir: t.TempDir(),
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}},{"type":"text","text":"describe"}]}]}`))
	req.Header.Set("Authorization", "Bearer gateway")
	rr := httptest.NewRecorder()
	s.handleChatCompletions(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "vision fallback") {
		t.Fatalf("vision fallback failed: %d %s", rr.Code, rr.Body.String())
	}
	if textCalls.Load() != 0 || firstVisionCalls.Load() != 1 || secondVisionCalls.Load() != 1 {
		t.Fatalf("unexpected route counts: text=%d first=%d second=%d", textCalls.Load(), firstVisionCalls.Load(), secondVisionCalls.Load())
	}
}

func TestAutoVisionConfigNormalizationAndAdmin(t *testing.T) {
	cfg := Config{Providers: []ProviderConfig{{ID: "custom", Name: "Vision", Type: "openai"}}, AutoModel: AutoModelConfig{Models: []string{"Vision/text-model"}, VisionModels: []string{"custom/vision-model"}}}
	normalizeConfigModelRefs(&cfg)
	if len(cfg.AutoModel.Models) != 2 || cfg.AutoModel.Models[0] != "Vision/text-model" || cfg.AutoModel.Models[1] != "Vision/vision-model" || len(cfg.AutoModel.VisionModels) != 0 {
		t.Fatalf("legacy vision model was not merged into the Auto list: %#v", cfg.AutoModel)
	}
	html := adminHTMLLiteV2(`{"providers":[],"auto_model":{"enabled":true,"models":[],"vision_models":[]}}`)
	if strings.Contains(html, "autoVisionModelInput") || strings.Contains(html, "addAutoVisionModelValue") || !strings.Contains(html, "请求包含图片时") {
		t.Fatal("Admin did not switch to the single Auto model list")
	}
	for _, marker := range []string{"auto-sort-item", "autoSortPointerDown", "长按后拖拽排序", "uiToast", "auto-member-badge", "auto-remove-badge", "removeAutoModelValue"} {
		if !strings.Contains(html, marker) {
			t.Fatalf("Admin is missing Auto interaction marker %q", marker)
		}
	}
}
