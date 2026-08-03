package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestAutoAgentAffinityAndLoadDistribution(t *testing.T) {
	providers := []ProviderConfig{
		{ID: "text1", Enabled: true, Models: []string{"model"}},
		{ID: "vision1", Enabled: true, Models: []string{"model"}, ModelMultimodal: map[string]bool{"model": true}},
		{ID: "text2", Enabled: true, Models: []string{"model"}},
	}
	s := &Server{config: Config{AutoModel: AutoModelConfig{Enabled: true, Models: []string{"text1/model", "vision1/model", "text2/model"}}, Providers: providers}}
	base := s.autoChatCandidatesForOpenAI(t.Context(), false)
	if len(base) != 3 || base[0] != "text1/model" {
		t.Fatalf("unexpected Auto order: %#v", base)
	}
	if got := s.orderAutoCandidatesForAgent("agent-1", base, false); got[0] != "text1/model" {
		t.Fatalf("agent-1 initial model = %#v", got)
	}
	if got := s.orderAutoCandidatesForAgent("agent-1", base, false); got[0] != "text1/model" {
		t.Fatalf("agent-1 was not sticky: %#v", got)
	}
	if got := s.orderAutoCandidatesForAgent("agent-2", base, false); got[0] != "vision1/model" {
		t.Fatalf("agent-2 was not distributed to the next model: %#v", got)
	}
	vision := s.autoChatCandidatesForOpenAI(t.Context(), true)
	if got := s.orderAutoCandidatesForAgent("agent-1", vision, true); len(got) != 1 || got[0] != "vision1/model" {
		t.Fatalf("agent-1 vision binding = %#v", got)
	}
	if got := s.orderAutoCandidatesForAgent("agent-1", vision, true); got[0] != "vision1/model" {
		t.Fatalf("agent-1 vision binding was not sticky: %#v", got)
	}
}

func TestAutoAgentIdentityIncludesGroupAndUserAgent(t *testing.T) {
	group := ModelGroup{ID: "agents", Name: "Agents", APIKey: "group-key", Enabled: true}
	scope := accessScope{Group: &group}
	first := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	first.Header.Set("Authorization", "Bearer group-key")
	first.Header.Set("User-Agent", "agent-a")
	second := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	second.Header.Set("Authorization", "Bearer group-key")
	second.Header.Set("User-Agent", "agent-b")
	keyA, labelA := autoAgentIdentity(first, scope)
	keyB, _ := autoAgentIdentity(second, scope)
	if keyA == keyB || !strings.Contains(labelA, "Agents") || !strings.Contains(labelA, "agent-a") {
		t.Fatalf("group and User-Agent were not included in identity: %q %q", keyA, labelA)
	}
	if strings.Contains(keyA, "group-key") {
		t.Fatal("raw API key leaked into Agent identity")
	}
}

func TestAutoSessionIdentitySeparatesConcurrentConversations(t *testing.T) {
	request := func(body, sessionID string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer gateway")
		req.Header.Set("User-Agent", "shared-agent")
		if sessionID != "" {
			req.Header.Set("X-9Router-Session-ID", sessionID)
		}
		return req
	}
	firstBody := []byte(`{"messages":[{"role":"user","content":"first task"}]}`)
	grownBody := []byte(`{"messages":[{"role":"user","content":"first task"},{"role":"assistant","content":"done"},{"role":"user","content":"continue"}]}`)
	otherBody := []byte(`{"messages":[{"role":"user","content":"second task"}]}`)
	firstKey, _ := autoSessionIdentity(request(string(firstBody), ""), accessScope{Full: true}, firstBody)
	grownKey, _ := autoSessionIdentity(request(string(grownBody), ""), accessScope{Full: true}, grownBody)
	otherKey, _ := autoSessionIdentity(request(string(otherBody), ""), accessScope{Full: true}, otherBody)
	if firstKey != grownKey || firstKey == otherKey {
		t.Fatalf("automatic conversation fingerprints are incorrect: first=%q grown=%q other=%q", firstKey, grownKey, otherKey)
	}
	explicitA, _ := autoSessionIdentity(request(string(firstBody), "session-a"), accessScope{Full: true}, firstBody)
	explicitB, _ := autoSessionIdentity(request(string(otherBody), "session-a"), accessScope{Full: true}, otherBody)
	if explicitA != explicitB || strings.Contains(explicitA, "session-a") {
		t.Fatalf("explicit session identity is unstable or leaked: %q %q", explicitA, explicitB)
	}
}

func TestAutoVisionFollowupDoesNotReplacePrimaryBinding(t *testing.T) {
	s := &Server{}
	primary := []string{"text/model", "vision/model"}
	vision := []string{"vision/model"}
	if got := s.orderAutoCandidatesForAgent("session", primary, false); got[0] != "text/model" {
		t.Fatalf("primary assignment = %#v", got)
	}
	s.commitAutoAgentSuccess("session", "text/model", false, false)
	useVision, followup := s.autoSessionRequestMode("session", true, true)
	if !useVision || followup {
		t.Fatalf("new image mode = vision:%v followup:%v", useVision, followup)
	}
	if got := s.orderAutoCandidatesForAgent("session", vision, true); got[0] != "vision/model" {
		t.Fatalf("vision assignment = %#v", got)
	}
	s.commitAutoAgentSuccess("session", "vision/model", true, true)
	useVision, followup = s.autoSessionRequestMode("session", false, true)
	if !useVision || !followup {
		t.Fatalf("first image follow-up mode = vision:%v followup:%v", useVision, followup)
	}
	s.commitAutoAgentSuccess("session", "vision/model", true, false)
	useVision, followup = s.autoSessionRequestMode("session", false, true)
	if useVision || followup {
		t.Fatalf("vision follow-up was not consumed: vision:%v followup:%v", useVision, followup)
	}
	if got := s.orderAutoCandidatesForAgent("session", primary, false); got[0] != "text/model" {
		t.Fatalf("primary binding was replaced by vision: %#v", got)
	}
}

func TestAutoVisionFollowupReservationIsConcurrentSafe(t *testing.T) {
	s := &Server{}
	s.commitAutoAgentSuccess("session", "vision/model", true, true)
	var visionCalls atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			vision, _ := s.autoSessionRequestMode("session", false, true)
			if vision {
				visionCalls.Add(1)
			}
		}()
	}
	wg.Wait()
	if visionCalls.Load() != 1 {
		t.Fatalf("vision follow-up was reserved %d times, want 1", visionCalls.Load())
	}
}
