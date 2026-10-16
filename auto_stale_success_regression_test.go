package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAutoStaleSuccessCannotUndoConcurrentFailover(t *testing.T) {
	for _, separateSessions := range []bool{false, true} {
		name := "same-session"
		if separateSessions {
			name = "different-sessions"
		}
		t.Run(name, func(t *testing.T) { autoStaleSuccessRegression(t, separateSessions) })
	}
}

func autoStaleSuccessRegression(t *testing.T, separateSessions bool) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseOld := func() { releaseOnce.Do(func() { close(release) }) }
	var firstCalls atomic.Int32
	var secondCalls atomic.Int32
	var clientCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(400)
			return
		}
		if body.Model == "first" {
			switch firstCalls.Add(1) {
			case 1:
				close(entered)
				<-release
			case 2:
				w.WriteHeader(503)
				_, _ = w.Write([]byte(`{"error":{"message":"upstream temporarily unavailable"}}`))
				return
			}
		} else {
			secondCalls.Add(1)
		}
		writeJSON(w, 200, map[string]any{"model": body.Model, "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": body.Model}}}})
	}))
	defer upstream.Close()
	defer releaseOld()
	p := ProviderConfig{ID: "provider", Type: "openai", Enabled: true, BaseURL: upstream.URL + "/v1", APIKey: "fake-key", Models: []string{"first", "second"}, EnabledModels: []string{"first", "second"}}
	s := &Server{config: Config{AccessKey: "fake-gateway", AutoModel: AutoModelConfig{Enabled: true, Models: []string{"provider/first", "provider/second"}}, Providers: []ProviderConfig{p}}, usage: newUsageStore(), client: upstream.Client(), dataDir: t.TempDir()}
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"same task"}]}`))
		req.Header.Set("Authorization", "Bearer fake-gateway")
		req.Header.Set("User-Agent", "fake-agent")
		req.Header.Set("X-9Router-Session-ID", "old-session")
		if separateSessions && clientCalls.Add(1) == 2 {
			req.Header.Set("X-9Router-Session-ID", "new-session")
			// Both sessions can already be pinned to A from earlier traffic.
			key, _ := autoSessionIdentityForModel(req, accessScope{Full: true}, []byte(`{"model":"auto","messages":[{"role":"user","content":"same task"}]}`), "auto")
			s.commitAutoAgentSuccess(key, "provider/first", false, false)
		}
		rr := httptest.NewRecorder()
		s.handleChatCompletions(rr, req)
		return rr
	}
	oldDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { oldDone <- call() }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("old request did not reach A")
	}
	newer := call()
	if newer.Code != 200 || !strings.Contains(newer.Body.String(), `"second"`) {
		releaseOld()
		t.Fatalf("new request did not failover to B: %d %s", newer.Code, newer.Body.String())
	}
	if !s.autoModelFailed("provider", "first") {
		releaseOld()
		t.Fatal("failed A was not cooled down")
	}
	releaseOld()
	select {
	case older := <-oldDone:
		if older.Code != 200 {
			t.Fatalf("old request = %d", older.Code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("old request did not complete")
	}
	if !s.autoModelFailed("provider", "first") {
		t.Fatal("older success cleared a newer model failure cooldown")
	}
	final := call()
	if final.Code != 200 || !strings.Contains(final.Body.String(), `"second"`) {
		t.Fatalf("repro changed: late A success must not replace the newer B binding, got %d %s", final.Code, final.Body.String())
	}
	if firstCalls.Load() != 2 || secondCalls.Load() != 2 {
		t.Fatalf("unexpected calls: A=%d B=%d", firstCalls.Load(), secondCalls.Load())
	}
	t.Log("A late success kept the newer failover binding and model failure cooldown")
}
