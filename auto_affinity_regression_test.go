package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestAutoFallbackIdentityIncludesUserAndCanonicalContent(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer gateway")
	req.Header.Set("User-Agent", "same-agent")
	identity := func(body string) string {
		key, _ := autoSessionIdentityForModel(req, accessScope{Full: true}, []byte(body), "auto")
		return key
	}
	first := identity(`{"user":"agent-a","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	formatted := identity(`{"user":"agent-a","messages":[{"role":"user","content":[ { "text":"\u0068ello", "type":"text" } ]}]}`)
	other := identity(`{"user":"agent-b","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	if first != formatted {
		t.Fatal("equivalent JSON formatting changed the conversation fingerprint")
	}
	if first == other {
		t.Fatal("different explicit request.user values shared a conversation fingerprint")
	}
}

func TestAutoExplicitIdentitySurvivesContextCompaction(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer gateway")
	req.Header.Set("X-9Router-Session-ID", "stable-session")
	before, _ := autoSessionIdentity(req, accessScope{Full: true}, []byte(`{"messages":[{"role":"user","content":"first task"},{"role":"user","content":"continue"}]}`))
	after, _ := autoSessionIdentity(req, accessScope{Full: true}, []byte(`{"messages":[{"role":"system","content":"summary"},{"role":"user","content":"continue"}]}`))
	if before != after {
		t.Fatal("explicit session ID did not survive context compaction")
	}
}

func TestAutoNewestRequestOwnsSuccessfulBinding(t *testing.T) {
	s := &Server{}
	old := s.beginAutoAgentRequest("session")
	newest := s.beginAutoAgentRequest("session")
	if !s.commitAutoAgentRequestSuccess("session", "provider/new", false, false, newest) {
		t.Fatal("newest successful request was rejected")
	}
	if s.commitAutoAgentRequestSuccess("session", "provider/old", false, false, old) {
		t.Fatal("old success replaced the newer primary binding")
	}
	if s.commitAutoAgentRequestSuccess("session", "provider/old-vision", true, true, old) {
		t.Fatal("old image request replaced newer vision state")
	}
	s.autoAgentMu.Lock()
	binding := *s.autoAgents["session"]
	s.autoAgentMu.Unlock()
	if binding.PrimaryModel != "provider/new" || binding.VisionModel != "" || binding.VisionFollowups != 0 {
		t.Fatalf("obsolete request changed state: %#v", binding)
	}
}

func TestAutoOldRequestCannotResurrectPrunedSession(t *testing.T) {
	s := &Server{}
	old := s.beginAutoAgentRequest("session")
	s.autoAgentMu.Lock()
	delete(s.autoAgents, "session")
	s.autoAgentMu.Unlock()
	newest := s.beginAutoAgentRequest("session")
	if newest == old || s.commitAutoAgentRequestSuccess("session", "provider/old", false, false, old) {
		t.Fatal("pruned session reused an old request ticket")
	}
}

func TestAutoVisionFollowupTracksOpenAIUserTurn(t *testing.T) {
	s := &Server{}
	image := `{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}},{"type":"text","text":"look"}]}`
	first := []byte(`{"messages":[` + image + `]}`)
	if vision, followup := s.autoSessionRequestModeForRequest("session", first, false); !vision || followup {
		t.Fatalf("image turn = %v %v", vision, followup)
	}
	s.commitAutoAgentSuccess("session", "provider/vision", true, true)
	followup := []byte(`{"messages":[` + image + `,{"role":"assistant","content":"description"},{"role":"user","content":"check this"}]}`)
	for range 2 { // Includes retry after a failed request, with no success commit.
		if vision, following := s.autoSessionRequestModeForRequest("session", followup, false); !vision || !following {
			t.Fatalf("follow-up retry lost the visual model: %v %v", vision, following)
		}
	}
	toolResult := []byte(strings.TrimSuffix(string(followup), "]}") + `,{"role":"assistant","content":null,"tool_calls":[{"id":"call","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call","content":"result"}]}`)
	for range 3 {
		if vision, following := s.autoSessionRequestModeForRequest("session", toolResult, false); !vision || !following {
			t.Fatalf("tool loop changed the follow-up turn: %v %v", vision, following)
		}
	}
	next := []byte(strings.TrimSuffix(string(toolResult), "]}") + `,{"role":"user","content":"next question"}]}`)
	if vision, following := s.autoSessionRequestModeForRequest("session", next, false); vision || following {
		t.Fatalf("new user turn did not return to primary routing: %v %v", vision, following)
	}
}

func TestAutoVisionFollowupTracksAnthropicToolResults(t *testing.T) {
	s := &Server{}
	image := `{"role":"user","content":[{"type":"text","text":"look"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}]}`
	if vision, _ := s.autoSessionRequestModeForRequest("session", []byte(`{"messages":[`+image+`]}`), true); !vision {
		t.Fatal("initial image was not visual")
	}
	s.commitAutoAgentSuccess("session", "provider/vision", true, true)
	followup := `{"messages":[` + image + `,{"role":"assistant","content":[{"type":"text","text":"description"}]},{"role":"user","content":[{"type":"text","text":"check"}]}]}`
	if vision, following := s.autoSessionRequestModeForRequest("session", []byte(followup), true); !vision || !following {
		t.Fatal("follow-up turn was not visual")
	}
	tools := strings.TrimSuffix(followup, "]}") + `,{"role":"assistant","content":[{"type":"tool_use","id":"call","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call","content":"result"}]}]}`
	if autoRequestTurn([]byte(followup), true) != autoRequestTurn([]byte(tools), true) {
		t.Fatal("Anthropic tool_result created a new user turn")
	}
	if vision, following := s.autoSessionRequestModeForRequest("session", []byte(tools), true); !vision || !following {
		t.Fatal("Anthropic tool_result switched the follow-up to text")
	}
	next := strings.TrimSuffix(tools, "]}") + `,{"role":"assistant","content":[{"type":"text","text":"done"}]},{"role":"user","content":"next question"}]}`
	if vision, following := s.autoSessionRequestModeForRequest("session", []byte(next), true); vision || following {
		t.Fatal("next Anthropic user turn did not return to text")
	}
}

func TestAutoSameFollowupTurnIsStableAcrossConcurrentRetries(t *testing.T) {
	s := &Server{}
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]},{"role":"assistant","content":"description"},{"role":"user","content":"check"}]}`)
	s.commitAutoAgentSuccess("one", "provider/vision", true, true)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if vision, followup := s.autoSessionRequestModeForRequest("one", body, false); !vision || !followup {
				t.Errorf("concurrent retry lost follow-up: %v %v", vision, followup)
			}
		}()
	}
	wg.Wait()
	if vision, followup := s.autoSessionRequestModeForRequest("other", body, false); vision || followup {
		t.Fatal("a different session inherited the visual follow-up")
	}
}

func TestAutoRepeatedUserTextStillStartsAnotherTurn(t *testing.T) {
	s := &Server{}
	image := `{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}`
	s.autoSessionRequestModeForRequest("session", []byte(`{"messages":[`+image+`]}`), false)
	s.commitAutoAgentSuccess("session", "provider/vision", true, true)
	followup := `{"messages":[` + image + `,{"role":"assistant","content":"description"},{"role":"user","content":"continue"}]}`
	if vision, following := s.autoSessionRequestModeForRequest("session", []byte(followup), false); !vision || !following {
		t.Fatal("first text follow-up was not visual")
	}
	next := strings.TrimSuffix(followup, "]}") + `,{"role":"assistant","content":"done"},{"role":"user","content":"continue"}]}`
	if vision, following := s.autoSessionRequestModeForRequest("session", []byte(next), false); vision || following {
		t.Fatal("identical text in a new user message reused an older follow-up turn")
	}
}

func TestAutoNewToolImageRenewsExactlyOneFollowingUserTurn(t *testing.T) {
	s := &Server{}
	image := `{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}]}`
	s.autoSessionRequestModeForRequest("session", []byte(`{"messages":[`+image+`]}`), true)
	s.commitAutoAgentSuccess("session", "provider/vision", true, true)
	followup := `{"messages":[` + image + `,{"role":"assistant","content":[{"type":"text","text":"description"}]},{"role":"user","content":"check screenshot"}]}`
	if vision, following := s.autoSessionRequestModeForRequest("session", []byte(followup), true); !vision || !following {
		t.Fatal("initial follow-up was not visual")
	}
	newScreenshot := strings.TrimSuffix(followup, "]}") + `,{"role":"assistant","content":[{"type":"tool_use","id":"call","name":"screenshot","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AQ=="}}]}]}]}`
	if vision, following := s.autoSessionRequestModeForRequest("session", []byte(newScreenshot), true); !vision || following {
		t.Fatal("new screenshot did not become the current image turn")
	}
	s.commitAutoAgentSuccess("session", "provider/vision", true, true)
	tools := strings.TrimSuffix(newScreenshot, "]}") + `,{"role":"assistant","content":[{"type":"tool_use","id":"call2","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call2","content":"done"}]}]}`
	if vision, following := s.autoSessionRequestModeForRequest("session", []byte(tools), true); !vision || following {
		t.Fatal("tool chain after screenshot consumed the next user follow-up")
	}
	s.commitAutoAgentSuccess("session", "provider/vision", true, true)
	next := strings.TrimSuffix(tools, "]}") + `,{"role":"assistant","content":[{"type":"text","text":"done"}]},{"role":"user","content":"explain more"}]}`
	if vision, following := s.autoSessionRequestModeForRequest("session", []byte(next), true); !vision || !following {
		t.Fatal("new screenshot did not renew a single follow-up user turn")
	}
	final := strings.TrimSuffix(next, "]}") + `,{"role":"assistant","content":[{"type":"text","text":"explained"}]},{"role":"user","content":"now write code"}]}`
	if vision, following := s.autoSessionRequestModeForRequest("session", []byte(final), true); vision || following {
		t.Fatal("screenshot tool loop renewed more than one new user follow-up")
	}
}

func TestAutoOldCandidateSnapshotCannotOverwriteNewerBinding(t *testing.T) {
	s := &Server{}
	old := s.beginAutoAgentRequest("session")
	oldCandidates := []string{"provider/old"}
	// The older request pauses after reading its candidate snapshot.
	newest := s.beginAutoAgentRequest("session")
	s.orderAutoCandidatesForRequest("session", []string{"provider/new"}, false, newest)
	if !s.commitAutoAgentRequestSuccess("session", "provider/new", false, false, newest) {
		t.Fatal("newer request did not save its successful binding")
	}
	got := s.orderAutoCandidatesForRequest("session", oldCandidates, false, old)
	if len(got) != 1 || got[0] != "provider/old" {
		t.Fatalf("old request lost its own candidate list: %#v", got)
	}
	if s.commitAutoAgentRequestSuccess("session", "provider/old", false, false, old) {
		t.Fatal("older request was accepted as the newest request")
	}
	s.autoAgentMu.Lock()
	binding := *s.autoAgents["session"]
	s.autoAgentMu.Unlock()
	if binding.PrimaryModel != "provider/new" || binding.LastRequestID != newest {
		t.Fatalf("candidate ordering overwrote the successful newer binding: %#v", binding)
	}
}

func TestAutoOldModeCannotOverwriteNewerVisualTurn(t *testing.T) {
	s := &Server{}
	old := s.beginAutoAgentRequest("session")
	newest := s.beginAutoAgentRequest("session")
	image := []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}]}`)
	s.autoSessionRequestModeForTicket("session", image, false, newest)
	s.commitAutoAgentRequestSuccess("session", "provider/new-vision", true, true, newest)
	s.autoAgentMu.Lock()
	before := *s.autoAgents["session"]
	s.autoAgentMu.Unlock()
	text := []byte(`{"messages":[{"role":"user","content":"old text request"}]}`)
	s.autoSessionRequestModeForTicket("session", text, false, old)
	if vision, _ := s.autoSessionRequestModeForTicket("session", image, false, old); !vision {
		t.Fatal("an old request with a real image must still use visual candidates")
	}
	s.orderAutoCandidatesForRequest("session", []string{"provider/old-vision"}, true, old)
	s.autoAgentMu.Lock()
	after := *s.autoAgents["session"]
	s.autoAgentMu.Unlock()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("old request changed a newer visual binding or budget: before=%#v after=%#v", before, after)
	}
}
