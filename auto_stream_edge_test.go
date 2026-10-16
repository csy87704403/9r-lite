package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAutoStreamFragmentedPreludeAndError(t *testing.T) {
	rr := httptest.NewRecorder()
	w := newAutoRetryWriter(rr, false)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	payload := ": ping\r\n\r\ndata: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\r\n\r\ndata: {\"error\":{\"message\":\"quota exhausted\"}}\r\n\r\n"
	var lastErr error
	for _, b := range []byte(payload) {
		_, lastErr = w.Write([]byte{b})
		if lastErr != nil {
			break
		}
	}
	w.finishResponse()
	if !errors.Is(lastErr, errAutoStreamRejected) || w.committed || rr.Body.Len() != 0 || w.status != http.StatusBadGateway || !strings.Contains(w.body.String(), "quota exhausted") {
		t.Fatalf("initial fragmented error was committed/lost: committed=%v status=%d err=%v body=%s", w.committed, w.status, lastErr, w.body.String())
	}
}

func TestAutoPartialStreamErrorDoesNotAppendAnotherModel(t *testing.T) {
	var fallbackCalls atomic.Int32
	s, _ := regressionFailoverServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Model == "good" {
			fallbackCalls.Add(1)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial answer\"}}]}\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(10 * time.Millisecond)
		fmt.Fprint(w, "data: {\"error\":{\"message\":\"rate limit exceeded\"}}\n\n")
	}, false)
	s.usage = newUsageStore()
	rr := regressionAutoRequest(t, s, `{"model":"auto","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	if fallbackCalls.Load() != 0 || !strings.Contains(rr.Body.String(), "partial answer") || s.autoRuntimeSnapshot()[0].LastResult != "failed" {
		t.Fatalf("partial response was retried or marked success: fallback=%d runtime=%+v body=%s", fallbackCalls.Load(), s.autoRuntimeSnapshot(), rr.Body.String())
	}
	group := s.usageSnapshot().Groups["_master"]
	if group == nil || group.Total.UpstreamFailed != 1 || group.Total.UpstreamSuccess != 0 || group.Total.ClientFailed != 1 {
		t.Fatalf("stream failure not represented in usage: %+v", group)
	}
}

func TestAutoInitialStreamErrorDoesNotWaitForConnectionClose(t *testing.T) {
	s, _ := regressionFailoverServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		if req.Model == "bad" {
			fmt.Fprint(w, "data: {\"error\":{\"message\":\"rate limit exceeded\"}}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"fallback\"}}]}\n\ndata: [DONE]\n\n")
	}, false)
	s.client.Timeout = time.Second
	started := time.Now()
	rr := regressionAutoRequest(t, s, `{"model":"auto","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond || !strings.Contains(rr.Body.String(), "fallback") {
		t.Fatalf("initial error waited for EOF: elapsed=%v body=%s", elapsed, rr.Body.String())
	}
}

func TestAutoMultiKeyRotatesOnInitialStreamQuotaError(t *testing.T) {
	var calls atomic.Int32
	s, _ := regressionFailoverServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if r.Header.Get("Authorization") == "Bearer mock" {
			fmt.Fprint(w, "data: {\"error\":{\"message\":\"insufficient_quota\"}}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"second key works\"}}]}\n\ndata: [DONE]\n\n")
	}, false)
	s.config.Providers[0].APIKeys = []string{"second-key"}
	s.client.Timeout = time.Second
	rr := regressionAutoRequest(t, s, `{"model":"auto","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	if calls.Load() != 2 || !strings.Contains(rr.Body.String(), "second key works") || s.autoModelFailed("first", "bad") || s.autoRuntimeSnapshot()[0].LastResult != "success" {
		t.Fatalf("key rotation failed: calls=%d runtime=%+v body=%s", calls.Load(), s.autoRuntimeSnapshot(), rr.Body.String())
	}
}

func TestAutoBufferedResponseHasSizeLimit(t *testing.T) {
	w := newAutoRetryWriter(httptest.NewRecorder(), false)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	chunk := bytes.Repeat([]byte{'x'}, 64<<10)
	var err error
	for n := 0; n <= maxBufferedResponseBytes/len(chunk); n++ {
		_, err = w.Write(chunk)
		if err != nil {
			break
		}
	}
	if !errors.Is(err, errResponseTooLarge) || w.status != http.StatusBadGateway || w.body.Len() > 1024 {
		t.Fatalf("unbounded JSON buffering: status=%d body=%d err=%v", w.status, w.body.Len(), err)
	}
}

type regressionReadAfterTerminal struct{}

func TestAutoStreamKeepsUsageAfterFinishReason(t *testing.T) {
	rr := httptest.NewRecorder()
	gate := newAutoRetryWriter(rr, false)
	gate.Header().Set("Content-Type", "text/event-stream")
	gate.WriteHeader(http.StatusOK)
	first := "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n"
	if _, err := gate.Write([]byte(first)); err != nil {
		t.Fatalf("finish_reason stopped stream before usage: %v", err)
	}
	usage := "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2,\"total_tokens\":9}}\n\n"
	if _, err := gate.Write([]byte(usage)); err != nil {
		t.Fatalf("usage chunk rejected: %v", err)
	}
	if _, err := gate.Write([]byte("data: [DONE]\n\n")); !errors.Is(err, errAutoStreamComplete) {
		t.Fatalf("wire terminal did not end stream: %v", err)
	}
	gate.finishResponse()
	if len(gate.stream.failure) != 0 || !strings.Contains(rr.Body.String(), `"total_tokens":9`) || !strings.Contains(rr.Body.String(), "[DONE]") {
		t.Fatalf("valid usage/terminal lost: failure=%s body=%s", gate.stream.failure, rr.Body.String())
	}
}

func TestAutoMultiKeyStreamUsageIsPreserved(t *testing.T) {
	s, _ := regressionFailoverServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}`,
			`[DONE]`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", event)
			w.(http.Flusher).Flush()
			time.Sleep(5 * time.Millisecond)
		}
	}, false)
	s.config.Providers[0].APIKeys = []string{"second-key"}
	s.usage = newUsageStore()
	rr := regressionAutoRequest(t, s, `{"model":"auto","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	group := s.usageSnapshot().Groups["_master"]
	if group == nil || group.Total.TotalTokens != 9 || group.Total.UpstreamSuccess != 1 || !strings.Contains(rr.Body.String(), "[DONE]") {
		t.Fatalf("stream usage truncated: group=%+v body=%s", group, rr.Body.String())
	}
}

func (regressionReadAfterTerminal) Read([]byte) (int, error) {
	return 0, errors.New("read past terminal event")
}

func TestResponsesStreamTerminalEventsAndFailure(t *testing.T) {
	cases := []struct {
		name, input, want string
		failed            bool
	}{
		{"failure", "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"insufficient_quota\"}}}\n\n", "insufficient_quota", true},
		{"incomplete", "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\nevent: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\"}}\n\n", `"finish_reason":"length"`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			gate := newAutoRetryWriter(rr, false)
			responsesStreamToChat(gate, io.MultiReader(strings.NewReader(tc.input), regressionReadAfterTerminal{}), "model")
			gate.finishResponse()
			if tc.failed {
				if gate.committed || !strings.Contains(gate.body.String(), tc.want) || strings.Contains(gate.body.String(), "read past terminal") {
					t.Fatalf("initial Responses failure lost: committed=%v body=%s", gate.committed, gate.body.String())
				}
			} else if !gate.committed || len(gate.stream.failure) != 0 || !strings.Contains(rr.Body.String(), tc.want) {
				t.Fatalf("valid terminal event rejected: failure=%s body=%s", gate.stream.failure, rr.Body.String())
			}
		})
	}
}
