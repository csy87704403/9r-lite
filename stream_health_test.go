package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const healthPartial = "data: {\"choices\":[{\"delta\":{\"content\":\"partial answer\"}}]}\n\n"
const healthStop = "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"

type healthBrokenClient struct{ *httptest.ResponseRecorder }

func (w healthBrokenClient) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestStreamHealthClassification(t *testing.T) {
	for _, tc := range []struct {
		name, body, contentType                  string
		status                                   int
		stream, canceled, brokenClient, internal bool
		readErr                                  error
		calls, interruptions                     int64
	}{
		{name: "clean EOF during output", body: healthPartial, stream: true, calls: 1, interruptions: 1},
		{name: "socket read failed", body: healthPartial, stream: true, readErr: io.ErrUnexpectedEOF, calls: 1, interruptions: 1},
		{name: "no output before EOF", body: ": keepalive\n\n", stream: true, calls: 1, interruptions: 1},
		{name: "complete DONE", body: healthPartial + "data: [DONE]\n\n", stream: true, calls: 1},
		{name: "finish reason without DONE", body: healthPartial + healthStop, stream: true, calls: 1},
		{name: "finish then late read error", body: healthPartial + strings.TrimSpace(healthStop), stream: true, readErr: io.ErrUnexpectedEOF, calls: 1},
		{name: "normal length cap", body: healthPartial + strings.ReplaceAll(healthStop, "stop", "length"), stream: true, calls: 1},
		{name: "midstream error", body: healthPartial + "data: {\"error\":{\"message\":\"provider disconnected\"}}\n\n", stream: true, calls: 1, interruptions: 1},
		{name: "initial rate limit", body: "data: {\"error\":{\"message\":\"rate limit exceeded\"}}\n\n", stream: true, calls: 1},
		{name: "initial quota exhausted", body: "data: {\"error\":{\"message\":\"insufficient credits\"}}\n\n", stream: true, calls: 1},
		{name: "HTTP 429", body: `{"error":"too frequent"}`, status: 429, stream: true, contentType: "application/json"},
		{name: "HTTP 502 SSE rejection", body: "data: {\"error\":\"bad gateway\"}\n\n", status: 502, stream: true},
		{name: "JSON instead of stream", body: `{"error":"insufficient credits"}`, stream: true, contentType: "application/json"},
		{name: "nonstream response", body: `{"choices":[{"message":{"content":"ok"}}]}`, contentType: "application/json"},
		{name: "user canceled", body: healthPartial, stream: true, canceled: true, readErr: context.Canceled},
		{name: "client disconnected", body: healthPartial, stream: true, brokenClient: true},
		{name: "internal probe", body: healthPartial, stream: true, internal: true},
		{name: "Anthropic complete", body: "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"ok\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", stream: true, calls: 1},
		{name: "Anthropic interrupted", body: "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"partial\"}}\n\n", stream: true, calls: 1, interruptions: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{usage: newUsageStore()}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if !tc.internal {
				ctx = context.WithValue(ctx, usageRequestContextKey{}, &usageRequestInfo{GroupID: "agents"})
			}
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
			var target http.ResponseWriter = httptest.NewRecorder()
			if tc.brokenClient {
				target = healthBrokenClient{httptest.NewRecorder()}
			}
			w, finish := s.beginUpstreamUsage(target, r, ProviderConfig{ID: "p", Name: "Provider"}, "m", tc.stream)
			ct := tc.contentType
			if ct == "" {
				ct = "text/event-stream"
			}
			w.Header().Set("Content-Type", ct)
			status := tc.status
			if status == 0 {
				status = 200
			}
			w.WriteHeader(status)
			// Split across every byte to exercise TCP chunk boundaries too.
			for _, b := range []byte(tc.body) {
				if _, err := w.Write([]byte{b}); err != nil {
					break
				}
			}
			reportUpstreamReadError(w, tc.readErr)
			if tc.canceled {
				cancel()
			}
			finish()
			stats := s.usageSnapshot().StreamHealth["p/m"]
			var calls, interruptions int64
			if stats != nil {
				for _, b := range stats.Buckets {
					calls += b.Calls
					interruptions += b.Interruptions
				}
			}
			if calls != tc.calls || interruptions != tc.interruptions {
				t.Fatalf("calls/interruption = %d/%d, want %d/%d; stats=%+v", calls, interruptions, tc.calls, tc.interruptions, stats)
			}
			if interruptions > 0 && stats.LastInterruption == "" {
				t.Fatal("missing interruption log")
			}
		})
	}
}

func TestStreamHealthWindowsPersistenceAndRollover(t *testing.T) {
	now := time.Now().Truncate(time.Minute).Add(30 * time.Second)
	s := &Server{usage: newUsageStore(), dataDir: t.TempDir()}
	p := ProviderConfig{ID: "first", Name: "First"}
	q := ProviderConfig{ID: "second", Name: "Second"}
	s.recordStreamHealth(p, "m", true, "expired", now.Add(-8*24*time.Hour))
	s.recordStreamHealth(p, "m", true, "two days ago", now.Add(-48*time.Hour))
	s.recordStreamHealth(p, "m", false, "", now.Add(-time.Hour))
	s.recordStreamHealth(q, "m", true, "latest", now)
	s.recordStreamHealth(q, "m", true, "older", now.Add(-time.Hour))
	// Same model through another access group is not duplicated or split.
	s.recordStreamHealth(q, "m", false, "", now)
	s.usage.Date = "2000-01-01"
	day := streamHealthRanking(s.usageSnapshot(), 24, now)
	if len(day) != 1 || day[0].ProviderID != "second" || day[0].Interruptions != 2 || day[0].Calls != 3 || day[0].LastInterruption != "latest" {
		t.Fatalf("24h ranking=%+v", day)
	}
	week := streamHealthRanking(s.usageSnapshot(), 168, now)
	if len(week) != 2 || week[0].Interruptions != 2 || week[1].Interruptions != 1 || week[1].Calls != 2 {
		t.Fatalf("7d ranking=%+v", week)
	}
	if err := s.saveUsage(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadUsage(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := streamHealthRanking(reloaded, 168, now); len(got) != 2 || got[0] != week[0] {
		t.Fatalf("lost history on restart: %+v", got)
	}
	pruneStreamHealth(&reloaded, now.Add(8*24*time.Hour))
	if len(reloaded.StreamHealth) != 0 {
		t.Fatal("expired history not pruned")
	}
}

func TestStreamHealthConcurrentAccounting(t *testing.T) {
	s := &Server{usage: newUsageStore()}
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.recordStreamHealth(ProviderConfig{ID: "p"}, "m", i%2 == 0, "closed", time.Now())
		}()
	}
	wg.Wait()
	rows := streamHealthRanking(s.usageSnapshot(), 24, time.Now())
	if len(rows) != 1 || rows[0].Calls != 100 || rows[0].Interruptions != 50 || rows[0].InterruptionRate != 0.5 {
		t.Fatalf("concurrent ranking=%+v", rows)
	}
}

func TestStreamHealthAdminAPI(t *testing.T) {
	s := &Server{usage: newUsageStore(), adminSecret: "test-only"}
	s.recordStreamHealth(ProviderConfig{ID: "p"}, "m", true, "lost stream", time.Now().Add(-48*time.Hour))
	for _, tc := range []struct {
		query        string
		auth         bool
		status, rows int
	}{
		{"", false, 401, 0}, {"?stream_hours=24", true, 200, 0}, {"?stream_hours=168", true, 200, 1}, {"?stream_hours=99999", true, 400, 0},
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/admin/usage"+tc.query, nil)
		if tc.auth {
			r.AddCookie(&http.Cookie{Name: "nr_admin", Value: s.adminCookieValue()})
		}
		w := httptest.NewRecorder()
		s.handleUsage(w, r)
		if w.Code != tc.status {
			t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body)
		}
		if tc.status == 200 {
			var body struct {
				Rows []StreamHealthRank `json:"stream_health"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Rows) != tc.rows || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("unexpected response %s", w.Body)
			}
			if strings.Contains(w.Body.String(), "buckets") {
				t.Fatal("raw history exposed by admin summary API")
			}
		}
	}
}

func TestStreamHealthActualAutoAndMultiKeyStreams(t *testing.T) {
	for _, multiKey := range []bool{false, true} {
		t.Run(fmt.Sprint(multiKey), func(t *testing.T) {
			s, _ := regressionFailoverServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, healthPartial)
				w.(http.Flusher).Flush()
				// A long response aborts after its output is already delivered.
				if multiKey {
					panic(http.ErrAbortHandler)
				}
			}, false)
			if multiKey {
				s.config.Providers[0].APIKeys = []string{"mock-a", "mock-b"}
			}
			regressionAutoRequest(t, s, `{"model":"auto","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
			rows := streamHealthRanking(s.usageSnapshot(), 24, time.Now())
			if len(rows) != 1 || rows[0].ProviderID != "first" || rows[0].Model != "bad" || rows[0].Interruptions != 1 {
				t.Fatalf("actual model not counted: %+v", rows)
			}
		})
	}
}

func TestStreamHealthDoesNotTreatGateSignalsAsClientDisconnects(t *testing.T) {
	s := &Server{usage: newUsageStore()}
	info := &usageRequestInfo{GroupID: "g"}
	r := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(context.WithValue(context.Background(), usageRequestContextKey{}, info))
	gate := newAutoRetryWriter(httptest.NewRecorder(), false)
	w, finish := s.beginUpstreamUsage(gate, r, ProviderConfig{ID: "p"}, "m", true)
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte(healthPartial))
	_, err := w.Write([]byte("data: {\"error\":{\"message\":\"upstream dropped\"}}\n\n"))
	if !errors.Is(err, errAutoStreamRejected) {
		t.Fatalf("expected internal rejection signal, got %v", err)
	}
	finish()
	rows := streamHealthRanking(s.usageSnapshot(), 24, time.Now())
	if len(rows) != 1 || rows[0].Interruptions != 1 {
		t.Fatalf("upstream error hidden by gate signal: %+v", rows)
	}
}

func TestStreamHealthKeepsOriginalModelAfterAutoFallback(t *testing.T) {
	s, _ := regressionFailoverServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		if req.Model == "bad" {
			// An output delta and error in the same chunk: Auto can still fall
			// back because nothing was committed, but the upstream did fail.
			fmt.Fprint(w, healthPartial+"data: {\"error\":{\"message\":\"provider stream failed\"}}\n\n")
		} else {
			fmt.Fprint(w, healthPartial+healthStop+"data: [DONE]\n\n")
		}
	}, false)
	w := regressionAutoRequest(t, s, `{"model":"auto","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatalf("Auto did not recover: %d %s", w.Code, w.Body)
	}
	rows := streamHealthRanking(s.usageSnapshot(), 24, time.Now())
	if len(rows) != 1 || rows[0].ProviderID != "first" || rows[0].Interruptions != 1 {
		t.Fatalf("fallback lost original failure: %+v", rows)
	}
	stats := s.usageSnapshot().StreamHealth["second/good"]
	if stats == nil || stats.Buckets[0].Calls != 1 || stats.Buckets[0].Interruptions != 0 {
		t.Fatalf("fallback success misclassified: %+v", stats)
	}
}

func TestStreamHealthRealClientCancelExcluded(t *testing.T) {
	sent := make(chan struct{})
	s, _ := regressionFailoverServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, healthPartial)
		w.(http.Flusher).Flush()
		close(sent)
		<-r.Context().Done()
	}, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"auto","stream":true,"messages":[{"role":"user","content":"hello"}]}`)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer gateway")
	w := &regressionSignalRecorder{ResponseRecorder: httptest.NewRecorder(), first: make(chan struct{})}
	done := make(chan struct{})
	go func() { defer close(done); s.handleChatCompletions(w, r) }()
	select {
	case <-sent:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream not started")
	}
	select {
	case <-w.first:
		cancel()
	case <-time.After(3 * time.Second):
		t.Fatal("output not delivered")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not cancel")
	}
	if len(s.usageSnapshot().StreamHealth) != 0 {
		t.Fatal("client cancellation recorded as upstream stream")
	}
}

func TestStreamHealthMinuteWindowBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 30, 0, time.UTC)
	s := &Server{usage: newUsageStore()}
	p := ProviderConfig{ID: "p"}
	s.recordStreamHealth(p, "m", true, "outside", now.Add(-24*time.Hour-time.Minute))
	s.recordStreamHealth(p, "m", true, "boundary", now.Add(-24*time.Hour))
	s.recordStreamHealth(p, "m", false, "", now)
	rows := streamHealthRanking(s.usage, 24, now)
	if len(rows) != 1 || rows[0].Calls != 2 || rows[0].Interruptions != 1 {
		t.Fatalf("minute boundary mismatch: %+v", rows)
	}
	// Keep the latest log for the final retained minute too.
	pruneStreamHealth(&s.usage, now.Add(6*24*time.Hour))
	if s.usage.StreamHealth["p/m"].LastInterruption != "boundary" {
		t.Fatal("boundary log prematurely expired")
	}
}

func TestStreamHealthAdminPanel(t *testing.T) {
	html := adminHTMLLiteV2(`{"providers":[]}`)
	for _, marker := range []string{"断流模型排行", "streamHealthHours", "最近 24 小时", "最近 7 天", "断流次数 ↓", "断流率", "最近断流时间", "renderStreamHealth", "stream_hours=", "usageLoadPending", "!document.hidden", "5000", "断流日志"} {
		if !strings.Contains(html, marker) {
			t.Fatalf("missing panel marker %q", marker)
		}
	}
}
