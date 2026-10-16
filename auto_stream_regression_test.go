package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type regressionSignalRecorder struct {
	*httptest.ResponseRecorder
	first chan struct{}
	once  sync.Once
}

func (w *regressionSignalRecorder) Write(b []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(b)
	w.once.Do(func() { close(w.first) })
	return n, err
}

func regressionFailoverServer(t *testing.T, handler http.HandlerFunc, named bool) (*Server, *httptest.Server) {
	t.Helper()
	u := httptest.NewServer(handler)
	t.Cleanup(u.Close)
	firstID, firstName := "first", "First"
	if named {
		firstID, firstName = "custom13", "MyUpstream"
	}
	p := ProviderConfig{ID: firstID, Name: firstName, Type: "openai", Enabled: true, BaseURL: u.URL + "/v1", APIKey: "mock", Models: []string{"bad"}, EnabledModels: []string{"bad"}}
	q := ProviderConfig{ID: "second", Type: "openai", Enabled: true, BaseURL: u.URL + "/v1", APIKey: "mock", Models: []string{"good"}, EnabledModels: []string{"good"}}
	s := &Server{config: Config{AccessKey: "gateway", AutoModel: AutoModelConfig{Enabled: true, Models: []string{providerModelRef(p, "bad"), "second/good"}}, Providers: []ProviderConfig{p, q}}, client: u.Client(), dataDir: t.TempDir()}
	return s, u
}

func regressionAutoRequest(t *testing.T, s *Server, raw string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(raw))
	r.Header.Set("Authorization", "Bearer gateway")
	rr := httptest.NewRecorder()
	s.handleChatCompletions(rr, r)
	return rr
}

func TestAutoRegressionStreamInitialErrorFallsBack(t *testing.T) {
	var fallbackCalls atomic.Int32
	s, _ := regressionFailoverServer(t, func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		w.Header().Set("Content-Type", "text/event-stream")
		if in.Model == "bad" {
			fmt.Fprint(w, "data: {\"error\":{\"message\":\"rate limit exceeded\"}}\n\ndata: [DONE]\n\n")
			return
		}
		fallbackCalls.Add(1)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"fallback\"}}]}\n\ndata: [DONE]\n\n")
	}, false)
	rr := regressionAutoRequest(t, s, `{"model":"auto","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	if fallbackCalls.Load() != 1 {
		t.Fatalf("initial stream error did not fall back: fallbackCalls=%d http=%d runtime=%+v body=%s", fallbackCalls.Load(), rr.Code, s.autoRuntimeSnapshot(), rr.Body.String())
	}
}

func TestAutoRegressionCustomProviderCooldownUsesCanonicalID(t *testing.T) {
	s, _ := regressionFailoverServer(t, func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Model == "bad" {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate limit exceeded"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "fallback"}}}})
	}, true)
	rr := regressionAutoRequest(t, s, `{"model":"auto","messages":[{"role":"user","content":"hello"}]}`)
	candidates := s.autoChatCandidatesForOpenAI(t.Context(), false)
	if containsString(candidates, "MyUpstream/bad") {
		t.Fatalf("429 failed model still selectable: http=%d candidates=%v canonicalCooldown=%v publicNameCooldown=%v", rr.Code, candidates, s.autoModelFailed("custom13", "bad"), s.autoModelFailed("MyUpstream", "bad"))
	}
}

func TestAutoRegressionCanceledStreamIsNotSuccess(t *testing.T) {
	started := make(chan struct{})
	s, _ := regressionFailoverServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"auto","stream":true,"messages":[{"role":"user","content":"hello"}]}`)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer gateway")
	rr := &regressionSignalRecorder{ResponseRecorder: httptest.NewRecorder(), first: make(chan struct{})}
	done := make(chan struct{})
	go func() { s.handleChatCompletions(rr, r); close(done) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream not started")
	}
	select {
	case <-rr.first:
		cancel()
	case <-time.After(3 * time.Second):
		t.Fatal("first stream chunk not relayed")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not stop request")
	}
	runtime := s.autoRuntimeSnapshot()
	if runtime[0].LastResult != "canceled" {
		t.Fatalf("client-canceled streaming request reported as %q rather than canceled; runtime=%+v", runtime[0].LastResult, runtime)
	}
}

func TestAutoRegressionRequestSpecificLimitDoesNotCoolDownForEveryone(t *testing.T) {
	s, _ := regressionFailoverServer(t, func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Model string `json:"model"`
			Max   int    `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Model == "bad" && in.Max > 10 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "max_tokens exceeds the provider's output cap"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "ok"}}}})
	}, false)
	regressionAutoRequest(t, s, `{"model":"auto","max_tokens":100,"messages":[{"role":"user","content":"large output request"}]}`)
	candidates := s.autoChatCandidatesForOpenAI(t.Context(), false)
	if !containsString(candidates, "first/bad") {
		t.Fatalf("one agent's max_tokens incompatibility globally excluded otherwise healthy model: candidates=%v globalCooldown=%v", candidates, s.autoModelFailed("first", "bad"))
	}
}

func TestAutoRegressionMultiKeyPreservesStreaming(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	s, _ := regressionFailoverServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first token\"}}]}\n\n")
		w.(http.Flusher).Flush()
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}, false)
	s.config.Providers[0].APIKeys = []string{"mock1", "mock2"}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"auto","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	r.Header.Set("Authorization", "Bearer gateway")
	rr := &regressionSignalRecorder{ResponseRecorder: httptest.NewRecorder(), first: make(chan struct{})}
	done := make(chan struct{})
	go func() { s.handleChatCompletions(rr, r); close(done) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("upstream not started")
	}
	tokenArrived := false
	select {
	case <-rr.first:
		tokenArrived = true
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("response did not finish")
	}
	if !tokenArrived {
		t.Fatalf("provider has two keys: first token was not relayed until upstream completed, defeating stream=true; body=%s", rr.Body.String())
	}
}
