package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func streamRouteFixture(t *testing.T, kind string, duration, oldTimeout time.Duration, stall bool) *Server {
	t.Helper()
	start, delta, done := "", healthPartial, healthStop+"data: [DONE]\n\n"
	switch kind {
	case "responses":
		delta = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"part\"}\n\n"
		done = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"
	case "anthropic", "native-anthropic":
		start = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"test\",\"usage\":{\"input_tokens\":1}}}\n\n"
		delta = "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"part\"}}\n\n"
		done = "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	}
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, start+delta)
		w.(http.Flusher).Flush()
		if stall {
			<-r.Context().Done()
			return
		}
		interval := 10 * time.Millisecond
		if duration > time.Second {
			interval = time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		end := time.NewTimer(duration)
		defer end.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if _, err := fmt.Fprint(w, delta); err != nil {
					return
				}
				w.(http.Flusher).Flush()
			case <-end.C:
				fmt.Fprint(w, done)
				return
			}
		}
	}))
	t.Cleanup(u.Close)
	p := ProviderConfig{ID: "p", Name: "Mock", Type: "openai", Enabled: true, BaseURL: u.URL + "/v1", APIKey: "mock", Models: []string{"m"}, EnabledModels: []string{"m"}}
	switch kind {
	case "multi-key":
		p.APIKeys = []string{"mock-2"}
	case "cline":
		p.Type = "cline"
		p.BaseURL = u.URL + "/v1/chat/completions"
		p.ClineAccounts = []ClineAccount{{Email: "test@example.invalid", AccessToken: "mock", RefreshToken: "mock", ExpiresIn: 3600}}
	case "responses":
		p.ProviderSpecificData = map[string]string{openAIRequestModeKey: openAIResponsesMode}
	case "anthropic", "native-anthropic":
		p.Type = "anthropic"
	}
	c := u.Client()
	c.Timeout = oldTimeout
	return &Server{config: Config{AccessKey: "test", Providers: []ProviderConfig{p}}, client: c, usage: newUsageStore(), dataDir: t.TempDir()}
}

func runStreamRoute(t *testing.T, s *Server, kind string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"p/m","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`))
	r.Header.Set("Authorization", "Bearer test")
	w := httptest.NewRecorder()
	if kind == "native-anthropic" {
		s.handleAnthropicMessages(w, r)
	} else {
		s.handleChatCompletions(w, r)
	}
	return w
}

func TestStreamingIgnoresTotalTimeoutAcrossRoutes(t *testing.T) {
	for _, kind := range []string{"single-key", "multi-key", "cline", "responses", "anthropic", "native-anthropic"} {
		t.Run(kind, func(t *testing.T) {
			s := streamRouteFixture(t, kind, 200*time.Millisecond, 40*time.Millisecond, false)
			w := runStreamRoute(t, s, kind)
			group := s.usageSnapshot().Groups["_master"]
			if w.Code != 200 || group == nil || group.Total.UpstreamSuccess != 1 || group.Total.UpstreamFailed != 0 {
				t.Fatalf("total timeout still truncates route: status=%d group=%+v body=%s", w.Code, group, w.Body)
			}
			if s.client.Timeout != 40*time.Millisecond {
				t.Fatal("shared nonstreaming client was mutated")
			}
			if rows := streamHealthRanking(s.usageSnapshot(), 24, time.Now()); len(rows) != 0 {
				t.Fatalf("healthy stream ranked as interrupted: %+v", rows)
			}
		})
	}
}

// Opt-in real 65-second test: six local protocols run concurrently, no account
// credentials or upstream network requests. Keep ordinary regression runs fast.
func TestStreamingOver60Seconds(t *testing.T) {
	if os.Getenv("NINEROUTER_LONG_STREAM_TEST") != "1" {
		t.Skip("opt-in 65-second local stream check")
	}
	for _, kind := range []string{"single-key", "multi-key", "cline", "responses", "anthropic", "native-anthropic"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			s := streamRouteFixture(t, kind, 65*time.Second, 60*time.Second, false)
			started := time.Now()
			w := runStreamRoute(t, s, kind)
			group := s.usageSnapshot().Groups["_master"]
			if time.Since(started) < 64*time.Second || w.Code != 200 || group == nil || group.Total.UpstreamSuccess != 1 || group.Total.UpstreamFailed != 0 {
				t.Fatalf("65s stream failed after %s: status=%d group=%+v", time.Since(started), w.Code, group)
			}
			t.Logf("%s: full stream completed after %s", kind, time.Since(started))
		})
	}
}

func TestGatewayIdleTimeoutNeverPollutesRanking(t *testing.T) {
	t.Setenv("UPSTREAM_STREAM_IDLE_TIMEOUT", "40ms")
	for _, kind := range []string{"single-key", "multi-key", "cline", "responses", "anthropic", "native-anthropic"} {
		t.Run(kind, func(t *testing.T) {
			s := streamRouteFixture(t, kind, 0, 60*time.Second, true)
			w := runStreamRoute(t, s, kind)
			stats := s.usageSnapshot().Groups["_master"].Models["p/m"]
			if stats.Total.UpstreamFailed != 1 || !strings.Contains(stats.LastFailure, "网关超时") {
				t.Fatalf("gateway timeout lost origin: stats=%+v body=%s", stats, w.Body)
			}
			if len(s.usageSnapshot().StreamHealth) != 0 {
				t.Fatalf("gateway timeout polluted history: %+v", s.usageSnapshot().StreamHealth)
			}
		})
	}
}

func TestStreamingHeaderDeadlineAndNonstreamTotalDeadline(t *testing.T) {
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer u.Close()
	r, _ := http.NewRequest(http.MethodGet, u.URL, nil)
	c := u.Client()
	c.Timeout = 25 * time.Millisecond
	_, err := doStreamingRequest(c, r, 40*time.Millisecond, time.Second)
	if !errors.Is(err, errGatewayTimeout) || !strings.Contains(err.Error(), "响应头") {
		t.Fatalf("missing header deadline: %v", err)
	}
	_, err = doUpstreamRequest(c, r, false)
	if err == nil || errors.Is(err, errGatewayTimeout) {
		t.Fatalf("nonstreaming total timeout changed: %v", err)
	}
}

func TestStreamingHeartbeatDoesNotExtendProgressDeadline(t *testing.T) {
	for _, heartbeat := range []string{": keepalive\n\n", "event: ping\ndata: {\"timestamp\":1}\n\n", "data: {\"type\":\"ping\"}\n\n", "data: ping\n\n"} {
		t.Run(heartbeat, func(t *testing.T) {
			u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				ticker := time.NewTicker(5 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-r.Context().Done():
						return
					case <-ticker.C:
						fmt.Fprint(w, heartbeat)
						w.(http.Flusher).Flush()
					}
				}
			}))
			defer u.Close()
			r, _ := http.NewRequest(http.MethodGet, u.URL, nil)
			resp, err := doStreamingRequest(u.Client(), r, time.Second, 40*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			_, err = io.Copy(io.Discard, resp.Body)
			if !errors.Is(err, errGatewayTimeout) {
				t.Fatalf("heartbeat prevented deadline: %v", err)
			}
		})
	}
}

func TestStreamingSlowDownstreamIsNotUpstreamIdle(t *testing.T) {
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, healthPartial)
		w.(http.Flusher).Flush()
		select {
		case <-time.After(80 * time.Millisecond):
			fmt.Fprint(w, healthStop)
		case <-r.Context().Done():
		}
	}))
	defer u.Close()
	r, _ := http.NewRequest(http.MethodGet, u.URL, nil)
	resp, err := doStreamingRequest(u.Client(), r, time.Second, 30*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 4096)
	if _, err = resp.Body.Read(buf); err != nil {
		t.Fatal(err)
	}
	time.Sleep(110 * time.Millisecond) // Simulate slow client writes; not waiting on upstream.
	if _, err = io.ReadAll(resp.Body); err != nil {
		t.Fatalf("slow downstream triggered upstream timeout: %v", err)
	}
}

func TestStreamingClientCancelPreserved(t *testing.T) {
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer u.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.URL, nil)
	resp, err := doStreamingRequest(u.Client(), r, time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	cancel()
	_, err = io.ReadAll(resp.Body)
	if err == nil || errors.Is(err, errGatewayTimeout) {
		t.Fatalf("client cancel mistaken for gateway timeout: %v", err)
	}
}

func TestStreamIdleTimeoutConfig(t *testing.T) {
	for _, value := range []string{"", "invalid", "0s", "-1s"} {
		t.Setenv("UPSTREAM_STREAM_IDLE_TIMEOUT", value)
		if streamIdleTimeout() != 180*time.Second {
			t.Fatalf("bad default for %q", value)
		}
	}
	t.Setenv("UPSTREAM_STREAM_IDLE_TIMEOUT", "5m")
	if streamIdleTimeout() != 5*time.Minute {
		t.Fatal("custom timeout ignored")
	}
}

func TestUpstreamTimeoutErrorIsStillRanked(t *testing.T) {
	s := &Server{usage: newUsageStore()}
	ctx := context.WithValue(context.Background(), usageRequestContextKey{}, &usageRequestInfo{GroupID: "g"})
	r := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
	w, finish := s.beginUpstreamUsage(httptest.NewRecorder(), r, ProviderConfig{ID: "p"}, "m", true)
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, healthPartial+"data: {\"error\":{\"code\":\"gateway_timeout\",\"message\":\"upstream vendor gateway timed out\"}}\n\n")
	finish()
	rows := streamHealthRanking(s.usageSnapshot(), 24, time.Now())
	if len(rows) != 1 || rows[0].Interruptions != 1 {
		t.Fatalf("upstream's timeout was incorrectly excluded: %+v", rows)
	}
}

func TestQoderTimeoutOriginSurvivesConversion(t *testing.T) {
	s := &Server{usage: newUsageStore()}
	ctx := context.WithValue(context.Background(), usageRequestContextKey{}, &usageRequestInfo{GroupID: "g"})
	r := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
	w, finish := s.beginUpstreamUsage(httptest.NewRecorder(), r, ProviderConfig{ID: "qoder"}, "m", true)
	read := io.MultiReader(strings.NewReader("data: {\"body\":\"{\\\"choices\\\":[{\\\"delta\\\":{\\\"content\\\":\\\"part\\\"}}]}\"}\n\n"), timeoutFixtureReader{})
	qoderStreamToOpenAI(w, read)
	finish()
	stats := s.usageSnapshot().Groups["g"].Models["qoder/m"]
	if !strings.Contains(stats.LastFailure, "网关超时") || len(s.usageSnapshot().StreamHealth) != 0 {
		t.Fatalf("Qoder timeout lost origin: %+v", stats)
	}
}

type timeoutFixtureReader struct{}

func (timeoutFixtureReader) Read([]byte) (int, error) {
	return 0, &gatewayTimeoutError{stage: "test timeout"}
}
