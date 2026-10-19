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

func guardFixture(t *testing.T) (*Server, ProviderConfig, time.Time) {
	t.Helper()
	p := ProviderConfig{ID: "p", Name: "Mock", Type: "openai", Enabled: true, Models: []string{"m"}, EnabledModels: []string{"m"}}
	s := &Server{usage: newUsageStore(), dataDir: t.TempDir(), config: Config{AutoModel: AutoModelConfig{Enabled: true, Models: []string{"p/m"}}, Providers: []ProviderConfig{p}}}
	return s, p, time.Now().Truncate(time.Second)
}

func guardObserve(s *Server, p ProviderConfig, failed bool, now time.Time) {
	s.recordStreamHealth(p, "m", failed, "upstream dropped", now)
}

func guardTrialResponse(success bool) *autoRetryWriter {
	w := newAutoRetryWriter(httptest.NewRecorder(), false)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	body := healthPartial
	if success {
		body += healthStop + "data: [DONE]\n\n"
	}
	w.Write([]byte(body))
	w.finishResponse()
	return w
}

func TestAutoStreamGuardThresholds(t *testing.T) {
	t.Run("consecutive", func(t *testing.T) {
		s, p, now := guardFixture(t)
		guardObserve(s, p, true, now)
		if h := s.autoStreamHealthSnapshot("p", "m", now); h.State != "recent" {
			t.Fatalf("first failure state=%+v", h)
		}
		guardObserve(s, p, true, now.Add(time.Second))
		h := s.autoStreamHealthSnapshot("p", "m", now.Add(time.Second))
		if h.State != "isolated" || h.Until != now.Add(time.Second+15*time.Minute).Unix() {
			t.Fatalf("not isolated: %+v", h)
		}
	})
	t.Run("rate and minimum sample", func(t *testing.T) {
		s, p, now := guardFixture(t)
		for i := 0; i < 9; i++ {
			guardObserve(s, p, i == 2, now.Add(time.Duration(i)*time.Second))
		}
		if h := s.autoStreamHealthSnapshot("p", "m", now.Add(9*time.Second)); h.State == "isolated" {
			t.Fatal("small sample isolated")
		}
		guardObserve(s, p, true, now.Add(10*time.Second))
		if h := s.autoStreamHealthSnapshot("p", "m", now.Add(10*time.Second)); h.State != "isolated" || h.Calls != 10 || h.Interruptions != 2 {
			t.Fatalf("20%% threshold failed: %+v", h)
		}
	})
	t.Run("healthy stream resets consecutive", func(t *testing.T) {
		s, p, now := guardFixture(t)
		for i, failed := range []bool{true, false, true} {
			guardObserve(s, p, failed, now.Add(time.Duration(i)*time.Second))
		}
		if h := s.autoStreamHealthSnapshot("p", "m", now.Add(3*time.Second)); h.State != "recent" {
			t.Fatalf("interleaved success ignored: %+v", h)
		}
	})
	t.Run("old ranking not used", func(t *testing.T) {
		s, p, now := guardFixture(t)
		s.usage.StreamHealth = map[string]*StreamHealthStats{"p/m": {ProviderID: "p", Model: "m", Buckets: []StreamHealthBucket{{Minute: now.Truncate(time.Minute).Unix(), Calls: 20, Interruptions: 20}}, LastInterruptedAt: now.Unix()}}
		guardObserve(s, p, false, now)
		if h := s.autoStreamHealthSnapshot("p", "m", now); h.State != "" || h.Calls != 1 {
			t.Fatalf("legacy ranking poisoned guard: %+v", h)
		}
	})
	t.Run("old window expires", func(t *testing.T) {
		s, p, now := guardFixture(t)
		guardObserve(s, p, true, now.Add(-16*time.Minute))
		guardObserve(s, p, true, now)
		if h := s.autoStreamHealthSnapshot("p", "m", now); h.State != "recent" || h.Interruptions != 1 {
			t.Fatalf("expired failure counted: %+v", h)
		}
	})
}

func TestAutoStreamGuardSingleConcurrentTrial(t *testing.T) {
	s, p, now := guardFixture(t)
	guardObserve(s, p, true, now)
	guardObserve(s, p, true, now.Add(time.Second))
	later := now.Add(16 * time.Minute)
	var admitted atomic.Int32
	var trial *autoStreamTrial
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, ok := s.admitAutoStreamCandidate("p", "m", true, later)
			if ok {
				admitted.Add(1)
				mu.Lock()
				trial = lease
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 1 || trial == nil {
		t.Fatalf("concurrent trials=%d", admitted.Load())
	}
	if !s.autoStreamBlocked("p", "m", later) {
		t.Fatal("pending trial not blocked")
	}
	if h := s.autoStreamHealthSnapshot("p", "m", later); h.State != "recovering" {
		t.Fatalf("pending state=%+v", h)
	}
	s.finishAutoStreamTrial(trial, guardTrialResponse(true), context.Background(), later)
	if h := s.autoStreamHealthSnapshot("p", "m", later); h.State != "" || h.Interruptions != 0 {
		t.Fatalf("success did not recover: %+v", h)
	}
	if _, ok := s.admitAutoStreamCandidate("p", "m", false, later); !ok {
		t.Fatal("recovered model unavailable")
	}
}

func TestAutoStreamGuardTrialFailureCancellationAndStaleCompletions(t *testing.T) {
	s, p, now := guardFixture(t)
	oldEpoch := s.autoStreamEpoch("p", "m")
	guardObserve(s, p, true, now)
	guardObserve(s, p, true, now.Add(time.Second))
	s.clearAutoModelFailure("p", "m") // A manual short probe must not lift isolation.
	if _, ok := s.admitAutoStreamCandidate("p", "m", true, now.Add(time.Minute)); ok {
		t.Fatal("probe cleared long isolation")
	}
	later := now.Add(16 * time.Minute)
	if _, ok := s.admitAutoStreamCandidate("p", "m", false, later); ok {
		t.Fatal("nonstream call used as recovery proof")
	}
	trial, ok := s.admitAutoStreamCandidate("p", "m", true, later)
	if !ok {
		t.Fatal("trial not admitted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.finishAutoStreamTrial(trial, guardTrialResponse(false), ctx, later)
	if h := s.autoStreamHealthSnapshot("p", "m", later); h.State != "awaiting_trial" {
		t.Fatalf("cancel falsely changed guard: %+v", h)
	}
	trial, ok = s.admitAutoStreamCandidate("p", "m", true, later)
	if !ok {
		t.Fatal("canceled trial lease leaked")
	}
	w := guardTrialResponse(false)
	w.readErr = &gatewayTimeoutError{stage: "local idle timeout"}
	s.finishAutoStreamTrial(trial, w, context.Background(), later)
	if h := s.autoStreamHealthSnapshot("p", "m", later); h.State != "awaiting_trial" {
		t.Fatalf("gateway timeout blamed on model: %+v", h)
	}
	trial, _ = s.admitAutoStreamCandidate("p", "m", true, later)
	s.finishAutoStreamTrial(trial, guardTrialResponse(false), context.Background(), later)
	h := s.autoStreamHealthSnapshot("p", "m", later)
	if h.State != "isolated" || h.Until != later.Add(15*time.Minute).Unix() {
		t.Fatalf("failed trial not re-isolated: %+v", h)
	}
	s.recordStreamHealthObservation(p, "m", false, true, "", oldEpoch, later.Add(time.Second))
	if h = s.autoStreamHealthSnapshot("p", "m", later.Add(time.Second)); h.State != "isolated" {
		t.Fatal("old success lifted new isolation")
	}
	trial, _ = s.admitAutoStreamCandidate("p", "m", true, later.Add(16*time.Minute))
	s.finishAutoStreamTrial(trial, guardTrialResponse(true), context.Background(), later.Add(16*time.Minute))
	s.recordStreamHealthObservation(p, "m", true, false, "old response", oldEpoch, later.Add(16*time.Minute+time.Second))
	if h = s.autoStreamHealthSnapshot("p", "m", later.Add(16*time.Minute+time.Second)); h.State != "" {
		t.Fatalf("stale in-flight failure reopened recovered model: %+v", h)
	}
}

func TestAutoStreamGuardPersistenceAndRuntimeIdentity(t *testing.T) {
	s, p, now := guardFixture(t)
	guardObserve(s, p, true, now)
	guardObserve(s, p, true, now)
	if err := s.saveUsage(); err != nil {
		t.Fatal(err)
	}
	store, err := loadUsage(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	restarted := &Server{usage: store, config: s.config}
	if _, ok := restarted.admitAutoStreamCandidate("p", "m", true, now); ok {
		t.Fatal("restart cleared isolation")
	}
	pruneStreamHealth(&restarted.usage, now.Add(8*24*time.Hour))
	if len(restarted.usage.StreamHealth["p/m"].Buckets) != 0 || restarted.usage.StreamHealth["p/m"].Guard == nil {
		t.Fatal("history expiry must not remove recovery gate")
	}
	trial, _ := restarted.admitAutoStreamCandidate("p", "m", true, now.Add(16*time.Minute))
	if trial == nil {
		t.Fatal("persisted isolation cannot half-open")
	}
	restarted.finishAutoStreamTrial(trial, nil, context.Background(), now.Add(16*time.Minute))
	// Public provider aliases must share the canonical model guard across Autos.
	s.config.Providers[0].ID = "custom13"
	s.config.Providers[0].Name = "Named"
	s.config.AutoModel.Models = []string{"Named/m"}
	s.usage.StreamHealth["custom13/m"] = s.usage.StreamHealth["p/m"]
	rows := s.autoRuntimeSnapshot()
	if len(rows) != 1 || rows[0].StreamHealth == nil || rows[0].StreamHealth.State != "isolated" {
		t.Fatalf("alias guard missing: %+v", rows)
	}
}

func TestAutoStreamGuardActualAutoSkipsWithoutChangingConfig(t *testing.T) {
	var badCalls atomic.Int32
	s, _ := regressionFailoverServer(t, func(w http.ResponseWriter, r *http.Request) {
		var in chatRequest
		json.NewDecoder(r.Body).Decode(&in)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, healthPartial)
		w.(http.Flusher).Flush()
		if in.Model == "bad" {
			badCalls.Add(1)
			return
		}
		fmt.Fprint(w, healthStop+"data: [DONE]\n\n")
	}, false)
	original := strings.Join(s.config.AutoModel.Models, ",")
	raw := `{"model":"auto","stream":true,"messages":[{"role":"user","content":"hello"}]}`
	regressionAutoRequest(t, s, raw)
	s.clearAutoModelFailure("first", "bad")
	regressionAutoRequest(t, s, raw)
	if h := s.autoStreamHealthSnapshot("first", "bad", time.Now()); h == nil || h.State != "isolated" {
		t.Fatalf("actual failures not isolated: %+v", h)
	}
	s.clearAutoModelFailure("first", "bad")
	w := regressionAutoRequest(t, s, raw)
	if !strings.Contains(w.Body.String(), "[DONE]") || badCalls.Load() != 2 {
		t.Fatalf("isolated model still called: calls=%d body=%s", badCalls.Load(), w.Body)
	}
	if strings.Join(s.config.AutoModel.Models, ",") != original || len(s.config.Providers[0].EnabledModels) != 1 {
		t.Fatal("guard changed ordering or publication")
	}
}

func TestAutoStreamGuardAllIsolatedFailsClosed(t *testing.T) {
	s, p, now := guardFixture(t)
	s.config.AccessKey = "test"
	guardObserve(s, p, true, now)
	guardObserve(s, p, true, now)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"auto","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	r.Header.Set("Authorization", "Bearer test")
	w := httptest.NewRecorder()
	s.handleChatCompletions(w, r)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "断流隔离") {
		t.Fatalf("all-isolated fallback unsafe: %d %s", w.Code, w.Body)
	}
}

func TestAutoStreamGuardIgnoresGatewayCancellationAndInitialQuota(t *testing.T) {
	t.Setenv("UPSTREAM_STREAM_IDLE_TIMEOUT", "30ms")
	s := streamRouteFixture(t, "single-key", 0, time.Minute, true)
	for range 2 {
		runStreamRoute(t, s, "single-key")
	}
	if h := s.autoStreamHealthSnapshot("p", "m", time.Now()); h != nil && h.State != "" {
		t.Fatalf("local gateway timeout isolated model: %+v", h)
	}
	s, p, now := guardFixture(t)
	for i := 0; i < 3; i++ {
		s.recordStreamHealthObservation(p, "m", false, false, "rate limited", 0, now.Add(time.Duration(i)*time.Second))
	}
	if h := s.autoStreamHealthSnapshot("p", "m", now); h != nil {
		t.Fatalf("quota errors entered guard: %+v", h)
	}
}

func TestAutoStreamGuardRealMultiAgentTrialAndAffinity(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var firstCalls, fallbackCalls atomic.Int32
	s, _ := regressionFailoverServer(t, func(w http.ResponseWriter, r *http.Request) {
		var in chatRequest
		json.NewDecoder(r.Body).Decode(&in)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, healthPartial)
		w.(http.Flusher).Flush()
		if in.Model == "bad" {
			if firstCalls.Add(1) == 1 {
				close(started)
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		} else {
			fallbackCalls.Add(1)
		}
		fmt.Fprint(w, healthStop+"data: [DONE]\n\n")
	}, false)
	s.config.AutoModels = []AutoModelConfig{{ID: "auto", Enabled: true, Models: []string{"first/bad", "second/good"}}, {ID: "auto-two", Enabled: true, Models: []string{"first/bad", "second/good"}}}
	first := s.config.Providers[0]
	now := time.Now()
	s.recordStreamHealth(first, "bad", true, "lost", now)
	s.recordStreamHealth(first, "bad", true, "lost", now)
	s.usageMu.Lock()
	s.usage.StreamHealth["first/bad"].Guard.Until = now.Add(-time.Second).Unix()
	s.usageMu.Unlock()
	call := func(session, auto string) int {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+auto+`","stream":true,"messages":[{"role":"user","content":"hello"}]}`)).WithContext(ctx)
		r.Header.Set("Authorization", "Bearer gateway")
		r.Header.Set("User-Agent", "MockAgents")
		r.Header.Set("X-Session-ID", session)
		w := httptest.NewRecorder()
		s.handleChatCompletions(w, r)
		if !strings.Contains(w.Body.String(), "[DONE]") {
			return 0
		}
		return w.Code
	}
	firstDone := make(chan int, 1)
	go func() { firstDone <- call("trial", "auto") }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("real trial not started")
	}
	if h := s.autoStreamHealthSnapshot("first", "bad", time.Now()); h.State != "recovering" {
		t.Fatalf("real trial state=%+v", h)
	}
	const agents = 20
	results := make(chan int, agents)
	for i := range agents {
		go func() { results <- call(fmt.Sprint("agent-", i), "auto-two") }()
	}
	for range agents {
		select {
		case status := <-results:
			if status != 200 {
				t.Fatalf("other agent failed during trial: %d", status)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("other agent waited behind trial")
		}
	}
	if firstCalls.Load() != 1 || fallbackCalls.Load() != agents {
		t.Fatalf("trial storm or routing error: first=%d fallback=%d", firstCalls.Load(), fallbackCalls.Load())
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case status := <-firstDone:
		if status != 200 {
			t.Fatalf("trial failed: %d", status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("trial did not finish")
	}
	if h := s.autoStreamHealthSnapshot("first", "bad", time.Now()); h.State != "" {
		t.Fatalf("successful real trial not restored: %+v", h)
	}
	if call("agent-0", "auto-two") != 200 || firstCalls.Load() != 1 || fallbackCalls.Load() != agents+1 {
		t.Fatal("healthy conversation was forced back to recovered model")
	}
}

func TestAutoStreamGuardNativeAnthropicRecovery(t *testing.T) {
	s := streamRouteFixture(t, "native-anthropic", time.Millisecond, time.Minute, false)
	p := s.config.Providers[0]
	now := time.Now()
	s.config.AutoModel = AutoModelConfig{Enabled: true, Models: []string{"p/m"}}
	s.recordStreamHealth(p, "m", true, "lost", now)
	s.recordStreamHealth(p, "m", true, "lost", now)
	s.usageMu.Lock()
	s.usage.StreamHealth["p/m"].Guard.Until = now.Add(-time.Second).Unix()
	s.usageMu.Unlock()
	r := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(`{"model":"auto","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`))
	r.Header.Set("Authorization", "Bearer test")
	w := httptest.NewRecorder()
	s.handleAnthropicMessages(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "message_stop") {
		t.Fatalf("native Auto recovery failed: %d %s", w.Code, w.Body)
	}
	if h := s.autoStreamHealthSnapshot("p", "m", time.Now()); h.State != "" {
		t.Fatalf("native trial did not recover: %+v", h)
	}
}

func TestAutoStreamGuardAdminEndpoint(t *testing.T) {
	s, p, now := guardFixture(t)
	s.adminSecret = "test-only"
	guardObserve(s, p, true, now)
	guardObserve(s, p, true, now)
	for _, auth := range []bool{false, true} {
		r := httptest.NewRequest(http.MethodGet, "/api/admin/auto-status", nil)
		if auth {
			r.AddCookie(&http.Cookie{Name: "nr_admin", Value: s.adminCookieValue()})
		}
		w := httptest.NewRecorder()
		s.handleAutoRuntimeStatus(w, r)
		if !auth {
			if w.Code != 401 {
				t.Fatal("guard status exposed without login")
			}
			continue
		}
		var data struct {
			Models []autoRuntimeModelStatus `json:"models"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || len(data.Models) != 1 || data.Models[0].StreamHealth.State != "isolated" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("guard status missing: %d %s", w.Code, w.Body)
		}
	}
}

func TestAutoStreamGuardBridgedGatewayTimeoutIsInconclusive(t *testing.T) {
	t.Setenv("UPSTREAM_STREAM_IDLE_TIMEOUT", "30ms")
	s := streamRouteFixture(t, "single-key", 0, time.Minute, true)
	p := s.config.Providers[0]
	now := time.Now()
	s.config.AutoModel = AutoModelConfig{Enabled: true, Models: []string{"p/m"}}
	s.recordStreamHealth(p, "m", true, "lost", now)
	s.recordStreamHealth(p, "m", true, "lost", now)
	s.usageMu.Lock()
	s.usage.StreamHealth["p/m"].Guard.Until = now.Add(-time.Second).Unix()
	s.usageMu.Unlock()
	r := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(`{"model":"auto","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`))
	r.Header.Set("Authorization", "Bearer test")
	w := httptest.NewRecorder()
	s.handleAnthropicMessages(w, r)
	if h := s.autoStreamHealthSnapshot("p", "m", time.Now()); h.State != "awaiting_trial" {
		t.Fatalf("bridged local timeout wrongly re-isolated model: %+v body=%s", h, w.Body)
	}
}

func TestAutoStreamGuardOwnHeaderTimeoutDoesNotReisolate(t *testing.T) {
	s, p, now := guardFixture(t)
	guardObserve(s, p, true, now)
	guardObserve(s, p, true, now)
	later := now.Add(16 * time.Minute)
	trial, _ := s.admitAutoStreamCandidate("p", "m", true, later)
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer u.Close()
	r, _ := http.NewRequestWithContext(autoStreamTrialContext(context.Background(), trial), http.MethodGet, u.URL, nil)
	if _, err := doStreamingRequest(u.Client(), r, 25*time.Millisecond, time.Second); err == nil {
		t.Fatal("header timeout not triggered")
	}
	w := newAutoRetryWriter(httptest.NewRecorder(), false)
	w.WriteHeader(502)
	s.finishAutoStreamTrial(trial, w, context.Background(), later)
	if h := s.autoStreamHealthSnapshot("p", "m", later); h.State != "awaiting_trial" {
		t.Fatalf("our header timeout re-isolated model: %+v", h)
	}
}

func TestAutoStreamGuardVendorTimeoutIsNotLocalTimeout(t *testing.T) {
	s, p, now := guardFixture(t)
	guardObserve(s, p, true, now)
	guardObserve(s, p, true, now)
	later := now.Add(16 * time.Minute)
	trial, _ := s.admitAutoStreamCandidate("p", "m", true, later)
	w := newAutoRetryWriter(httptest.NewRecorder(), false)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	w.Write([]byte(healthPartial + "data: {\"error\":{\"code\":\"gateway_timeout\",\"type\":\"gateway_error\",\"message\":\"vendor gateway timeout\"}}\n\n"))
	w.finishResponse()
	s.finishAutoStreamTrial(trial, w, context.Background(), later)
	if h := s.autoStreamHealthSnapshot("p", "m", later); h.State != "isolated" {
		t.Fatalf("vendor error incorrectly excused: %+v", h)
	}
}
