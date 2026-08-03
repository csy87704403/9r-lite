package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)

const (
	autoAgentIdleTTL    = 30 * time.Minute
	autoFailureCooldown = 2 * time.Minute
)

type autoAgentBinding struct {
	TextModel   string
	VisionModel string
	VisionLock  bool
	LastSeen    time.Time
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

func (s *Server) autoAgentVisionMode(agentKey string, requestHasImage bool) bool {
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
	if requestHasImage {
		binding.VisionLock = true
	}
	binding.LastSeen = now
	return binding.VisionLock
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
	if vision {
		binding.VisionLock = true
	}

	selected := binding.TextModel
	if vision {
		selected = binding.VisionModel
		if selected == "" && containsString(candidates, binding.TextModel) {
			selected = binding.TextModel
		}
	}
	if !containsString(candidates, selected) {
		selected = s.leastAssignedAutoCandidateLocked(candidates)
	}
	if vision {
		binding.VisionModel = selected
	} else {
		binding.TextModel = selected
	}
	return moveCandidateFirst(candidates, selected)
}

func (s *Server) commitAutoAgentSuccess(agentKey, model string, vision bool) {
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
		binding.VisionLock = true
		binding.VisionModel = model
	} else {
		binding.TextModel = model
	}
	binding.LastSeen = time.Now()
}

func (s *Server) leastAssignedAutoCandidateLocked(candidates []string) string {
	counts := make(map[string]int, len(candidates))
	for _, binding := range s.autoAgents {
		model := binding.TextModel
		if binding.VisionLock && binding.VisionModel != "" {
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
