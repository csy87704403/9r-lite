package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const (
	autoAgentIdleTTL    = 30 * time.Minute
	autoFailureCooldown = 2 * time.Minute
)

type autoAgentBinding struct {
	PrimaryModel    string
	VisionModel     string
	VisionFollowups int
	VisionTurn      string
	FollowupTurn    string
	LastRequestID   uint64
	LastSeen        time.Time
}

var autoAgentRequestSequence atomic.Uint64

func autoAgentIdentity(r *http.Request, scope accessScope) (string, string) {
	userAgent := strings.TrimSpace(r.UserAgent())
	if userAgent == "" {
		userAgent = "unknown-client"
	}
	if len(userAgent) > 256 {
		userAgent = userAgent[:256]
	}

	tokenHash := sha256.Sum256([]byte(requestAccessToken(r)))
	fingerprint := hex.EncodeToString(tokenHash[:8])
	namespace := "master"
	label := "主密钥"
	if scope.Group != nil {
		namespace = "group:" + strings.TrimSpace(scope.Group.ID)
		label = strings.TrimSpace(scope.Group.Name)
		if label == "" {
			label = strings.TrimSpace(scope.Group.ID)
		}
	}
	return namespace + ":" + fingerprint + "|ua:" + userAgent, label + " · " + trimAutoUserAgent(userAgent)
}

func autoSessionIdentity(r *http.Request, scope accessScope, raw []byte) (string, string) {
	agentKey, label := autoAgentIdentity(r, scope)
	hint := autoSessionHint(r, raw)
	if hint == "" {
		hint = "default"
	}
	hash := sha256.Sum256([]byte(hint))
	fingerprint := hex.EncodeToString(hash[:8])
	return agentKey + "|session:" + fingerprint, label + " · 会话 " + fingerprint[:6]
}

func autoSessionIdentityForModel(r *http.Request, scope accessScope, raw []byte, autoID string) (string, string) {
	key, label := autoSessionIdentity(r, scope, raw)
	autoID = strings.TrimSpace(autoID)
	return key + "|auto:" + autoID, label + " · " + autoID
}

func autoSessionHint(r *http.Request, raw []byte) string {
	for _, header := range []string{"X-9Router-Session-ID", "X-Session-ID", "X-Conversation-ID"} {
		if value := strings.TrimSpace(r.Header.Get(header)); value != "" {
			return strings.ToLower(header) + ":" + value
		}
	}
	var request map[string]json.RawMessage
	if json.Unmarshal(raw, &request) != nil {
		return ""
	}
	for _, key := range []string{"conversation_id", "session_id", "thread_id"} {
		if value := rawJSONString(request[key]); value != "" {
			return key + ":" + value
		}
	}
	var metadata map[string]json.RawMessage
	if json.Unmarshal(request["metadata"], &metadata) == nil {
		for _, key := range []string{"conversation_id", "session_id", "thread_id"} {
			if value := rawJSONString(metadata[key]); value != "" {
				return "metadata." + key + ":" + value
			}
		}
	}
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(request["messages"], &messages) == nil {
		for _, message := range messages {
			if !strings.EqualFold(strings.TrimSpace(message.Role), "user") || len(message.Content) == 0 {
				continue
			}
			// This is only a fallback: equal opening messages can still belong
			// to different sessions. A client-supplied session ID is preferred.
			hash := sha256.Sum256(canonicalAutoContent(message.Content))
			return "user:" + rawJSONString(request["user"]) + "|first-user:" + hex.EncodeToString(hash[:])
		}
	}
	if value := rawJSONString(request["user"]); value != "" {
		return "user:" + value
	}
	return ""
}

func canonicalAutoContent(raw json.RawMessage) []byte {
	var value any
	if json.Unmarshal(raw, &value) == nil {
		if normalized, err := json.Marshal(value); err == nil {
			return normalized
		}
	}
	return raw
}

// A turn ends when another real user message arrives, not when an HTTP
// request finishes. Tool results and retries keep the same turn fingerprint.
func autoRequestTurn(raw []byte, anthropic bool) string {
	var request struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(raw, &request) != nil {
		return ""
	}
	var last json.RawMessage
	count := 0
	for _, message := range request.Messages {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "user") {
			continue
		}
		if anthropic {
			var content any
			_ = json.Unmarshal(message.Content, &content)
			if anthropicToolResultOnlyContent(content) {
				continue
			}
		}
		count++
		last = message.Content
	}
	if count == 0 {
		return ""
	}
	normalized, _ := json.Marshal([]any{count, string(canonicalAutoContent(last))})
	hash := sha256.Sum256(normalized)
	return hex.EncodeToString(hash[:])
}

func rawJSONString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func (s *Server) autoSessionRequestMode(sessionKey string, latestHasImage, anyImage bool) (vision, followup bool) {
	now := time.Now()
	s.autoAgentMu.Lock()
	defer s.autoAgentMu.Unlock()
	s.pruneAutoAgentsLocked(now)
	if s.autoAgents == nil {
		s.autoAgents = map[string]*autoAgentBinding{}
	}
	binding := s.autoAgents[sessionKey]
	if binding == nil {
		binding = &autoAgentBinding{}
		s.autoAgents[sessionKey] = binding
	}
	binding.LastSeen = now
	if latestHasImage {
		return true, false
	}
	if anyImage && binding.VisionFollowups > 0 {
		binding.VisionFollowups--
		return true, true
	}
	if !anyImage {
		binding.VisionFollowups = 0
	}
	return false, false
}

func (s *Server) autoSessionRequestModeForRequest(sessionKey string, raw []byte, anthropic bool) (vision, followup bool) {
	return s.autoSessionRequestModeForTicket(sessionKey, raw, anthropic, 0)
}

func (s *Server) autoSessionRequestModeForTicket(sessionKey string, raw []byte, anthropic bool, ticket uint64) (vision, followup bool) {
	var latestHasImage, anyImage bool
	if anthropic {
		latestHasImage, anyImage = anthropicMessagesLatestUserHasImage(raw), anthropicMessagesHaveImage(raw)
	} else {
		latestHasImage, anyImage = openAIChatLatestUserHasImage(raw), openAIChatHasImage(raw)
	}
	turn := autoRequestTurn(raw, anthropic)
	now := time.Now()
	s.autoAgentMu.Lock()
	defer s.autoAgentMu.Unlock()
	s.pruneAutoAgentsLocked(now)
	if s.autoAgents == nil {
		s.autoAgents = map[string]*autoAgentBinding{}
	}
	binding := s.autoAgents[sessionKey]
	if ticket != 0 && (binding == nil || binding.LastRequestID != ticket) {
		// An older request may still need an image-capable model, but must
		// not change the newer request's turn or follow-up allowance.
		copy := autoAgentBinding{}
		if binding != nil {
			copy = *binding
		}
		binding = &copy
	} else if binding == nil {
		binding = &autoAgentBinding{}
		s.autoAgents[sessionKey] = binding
	}
	binding.LastSeen = now
	if latestHasImage {
		if binding.VisionTurn != turn {
			binding.VisionFollowups = 0
			binding.FollowupTurn = ""
		}
		binding.VisionTurn = turn
		return true, false
	}
	if turn != "" && binding.FollowupTurn == turn {
		return true, true
	}
	if turn != "" && binding.VisionTurn == turn {
		return true, false
	}
	if !anyImage || (binding.FollowupTurn != "" && binding.FollowupTurn != turn) {
		binding.VisionFollowups = 0
		binding.FollowupTurn = ""
		return false, false
	}
	if turn != "" && binding.VisionFollowups > 0 {
		binding.FollowupTurn = turn
		return true, true
	}
	return false, false
}

func (s *Server) orderAutoCandidatesForAgent(agentKey string, candidates []string, vision bool) []string {
	return s.orderAutoCandidatesForRequest(agentKey, candidates, vision, 0)
}

func (s *Server) orderAutoCandidatesForRequest(agentKey string, candidates []string, vision bool, ticket uint64) []string {
	if len(candidates) == 0 {
		return nil
	}
	now := time.Now()
	s.autoAgentMu.Lock()
	defer s.autoAgentMu.Unlock()
	s.pruneAutoAgentsLocked(now)
	if s.autoAgents == nil {
		s.autoAgents = map[string]*autoAgentBinding{}
	}
	binding := s.autoAgents[agentKey]
	if ticket != 0 && (binding == nil || binding.LastRequestID != ticket) {
		copy := autoAgentBinding{}
		if binding != nil {
			copy = *binding
		}
		binding = &copy
	} else if binding == nil {
		binding = &autoAgentBinding{}
		s.autoAgents[agentKey] = binding
	}
	binding.LastSeen = now
	selected := binding.PrimaryModel
	if vision {
		selected = binding.VisionModel
		if selected == "" && containsString(candidates, binding.PrimaryModel) {
			selected = binding.PrimaryModel
		}
	}
	if !containsString(candidates, selected) {
		selected = s.leastAssignedAutoCandidateLocked(candidates, vision)
	}
	if vision {
		binding.VisionModel = selected
	} else {
		binding.PrimaryModel = selected
	}
	return moveCandidateFirst(candidates, selected)
}

func (s *Server) commitAutoAgentSuccess(agentKey, model string, vision, newImage bool) {
	s.commitAutoAgentRequestSuccess(agentKey, model, vision, newImage, 0)
}

func (s *Server) beginAutoAgentRequest(agentKey string) uint64 {
	s.autoAgentMu.Lock()
	defer s.autoAgentMu.Unlock()
	if s.autoAgents == nil {
		s.autoAgents = map[string]*autoAgentBinding{}
	}
	binding := s.autoAgents[agentKey]
	if binding == nil {
		binding = &autoAgentBinding{}
		s.autoAgents[agentKey] = binding
	}
	binding.LastRequestID = autoAgentRequestSequence.Add(1)
	binding.LastSeen = time.Now()
	return binding.LastRequestID
}

func (s *Server) commitAutoAgentRequestSuccess(agentKey, model string, vision, newImage bool, requestID uint64) bool {
	if strings.TrimSpace(agentKey) == "" || strings.TrimSpace(model) == "" {
		return false
	}
	s.autoAgentMu.Lock()
	defer s.autoAgentMu.Unlock()
	if s.autoAgents == nil {
		s.autoAgents = map[string]*autoAgentBinding{}
	}
	binding := s.autoAgents[agentKey]
	if requestID != 0 && (binding == nil || binding.LastRequestID != requestID) {
		return false
	}
	if binding == nil {
		binding = &autoAgentBinding{}
		s.autoAgents[agentKey] = binding
	}
	if vision {
		binding.VisionModel = model
		if newImage {
			binding.VisionFollowups = 1
		}
	} else {
		binding.PrimaryModel = model
	}
	binding.LastSeen = time.Now()
	return true
}

func (s *Server) leastAssignedAutoCandidateLocked(candidates []string, vision bool) string {
	counts := make(map[string]int, len(candidates))
	for _, binding := range s.autoAgents {
		model := binding.PrimaryModel
		if vision {
			model = binding.VisionModel
		}
		if containsString(candidates, model) {
			counts[model]++
		}
	}
	selected := candidates[0]
	for _, candidate := range candidates[1:] {
		if counts[candidate] < counts[selected] {
			selected = candidate
		}
	}
	return selected
}

func (s *Server) pruneAutoAgentsLocked(now time.Time) {
	for key, binding := range s.autoAgents {
		if binding == nil || now.Sub(binding.LastSeen) >= autoAgentIdleTTL {
			delete(s.autoAgents, key)
		}
	}
}

func containsString(values []string, target string) bool {
	if target == "" {
		return false
	}
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func moveCandidateFirst(candidates []string, selected string) []string {
	ordered := make([]string, 0, len(candidates))
	if selected != "" {
		ordered = append(ordered, selected)
	}
	for _, candidate := range candidates {
		if candidate != selected {
			ordered = append(ordered, candidate)
		}
	}
	return ordered
}
