package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
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
