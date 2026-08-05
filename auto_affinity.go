package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
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
	LastSeen        time.Time
}

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
			hash := sha256.Sum256(message.Content)
			return "first-user:" + hex.EncodeToString(hash[:])
		}
	}
	if value := rawJSONString(request["user"]); value != "" {
		return "user:" + value
	}
	return ""
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

func (s *Server) orderAutoCandidatesForAgent(agentKey string, candidates []string, vision bool) []string {
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
	if binding == nil {
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
	if strings.TrimSpace(agentKey) == "" || strings.TrimSpace(model) == "" {
		return
	}
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
	if vision {
		binding.VisionModel = model
		if newImage {
			binding.VisionFollowups = 1
		}
	} else {
		binding.PrimaryModel = model
	}
	binding.LastSeen = time.Now()
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
