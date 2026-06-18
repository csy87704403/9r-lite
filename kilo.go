package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	kiloAPIBaseURL  = "https://api.kilo.ai"
	kiloInitiateURL = "https://api.kilo.ai/api/device-auth/codes"
	kiloPollURLBase = "https://api.kilo.ai/api/device-auth/codes"
	kiloChatURL     = "https://api.kilo.ai/api/openrouter/chat/completions"
	kiloProfileURL  = "https://api.kilo.ai/api/profile"
	kiloModelsURL   = "https://api.kilo.ai/api/gateway/models"
)

var kiloFreeModelsCache = struct {
	sync.Mutex
	expiresAt time.Time
	models    []string
}{}

type kiloPollRequest struct {
	DeviceCode string `json:"deviceCode"`
}

func (s *Server) handleKiloDeviceCode(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, kiloInitiateURL, nil)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusTooManyRequests {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too_many_pending_authorization_requests"})
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": fmt.Sprintf("Kilo device auth initiation failed: %d %s", resp.StatusCode, truncateString(string(b), 200))})
		return
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	code := stringValue(raw["code"])
	verificationURL := stringValue(raw["verificationUrl"])
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":               code,
		"user_code":                 code,
		"verification_uri":          verificationURL,
		"verification_uri_complete": verificationURL,
		"expires_in":                intValue(raw["expiresIn"]),
		"interval":                  3,
	})
}

func (s *Server) handleKiloPoll(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body kiloPollRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	code := strings.TrimSpace(body.DeviceCode)
	if code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "invalid_request", "errorDescription": "Missing deviceCode"})
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, kiloPollURLBase+"/"+code, nil)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	resp, err := s.client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"success": false, "error": "poll_failed", "errorDescription": err.Error()})
		return
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusAccepted:
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "pending": true, "error": "authorization_pending"})
		return
	case http.StatusForbidden:
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "access_denied", "errorDescription": "Authorization denied by user"})
		return
	case http.StatusGone:
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "expired_token", "errorDescription": "Authorization code expired"})
		return
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		writeJSON(w, http.StatusBadGateway, map[string]any{"success": false, "error": "poll_failed", "errorDescription": fmt.Sprintf("Poll failed: %d %s", resp.StatusCode, truncateString(string(b), 200))})
		return
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"success": false, "error": err.Error()})
		return
	}
	if stringValue(raw["status"]) != "approved" || stringValue(raw["token"]) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "pending": true, "error": "authorization_pending"})
		return
	}
	token := stringValue(raw["token"])
	orgID := s.kiloProfileOrgID(r, token)
	p, _ := s.providerByID("kilo")
	if p.ID == "" {
		p = ProviderConfig{ID: "kilo", Name: "Kilo Code OAuth", Type: "kilocode", BaseURL: kiloChatURL}
	}
	p.Type = "kilocode"
	p.Enabled = true
	p.BaseURL = firstNonEmpty(p.BaseURL, kiloChatURL)
	p.AccessToken = token
	p.Email = stringValue(raw["userEmail"])
	p.ProviderSpecificData = map[string]string{"orgId": orgID}
	if models, err := fetchKiloFreeModels(r.Context(), s.client, p); err == nil && len(models) > 0 {
		p.Models = models
		p.EnabledModels = append([]string(nil), models...)
		p.AvailableModels = nil
		p.ModelLatencyMS = nil
		p.AvailabilityCheckedAt = 0
	}
	if err := s.updateProvider(p); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "provider": p})
}

func (s *Server) kiloProfileOrgID(r *http.Request, token string) string {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, kiloProfileURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return ""
	}
	var raw struct {
		Organizations []struct {
			ID string `json:"id"`
		} `json:"organizations"`
	}
	if json.NewDecoder(resp.Body).Decode(&raw) != nil || len(raw.Organizations) == 0 {
		return ""
	}
	return raw.Organizations[0].ID
}

func (s *Server) proxyKiloCode(w http.ResponseWriter, r *http.Request, p ProviderConfig, req chatRequest, upstreamModel string) {
	if p.AccessToken == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "kilo is not logged in"})
		return
	}
	body, err := replaceModel(req.Raw, upstreamModel)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + p.AccessToken,
		"Accept":        acceptHeader(req.Stream),
		"User-Agent":    "9router-lite/0.1",
	}
	if orgID := p.ProviderSpecificData["orgId"]; orgID != "" {
		headers["X-Kilocode-OrganizationID"] = orgID
	}
	target := firstNonEmpty(p.BaseURL, kiloChatURL)
	status := s.proxyRaw(w, r, target, body, headers)
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		s.markProviderAuthState(p.ID, "needs_login", fmt.Sprintf("Kilo returned %d; please login again", status))
	} else if status >= 200 && status <= 299 {
		s.markProviderAuthState(p.ID, "ok", "")
	}
}

func fetchKiloFreeModels(ctx context.Context, client *http.Client, p ProviderConfig) ([]string, error) {
	kiloFreeModelsCache.Lock()
	if time.Now().Before(kiloFreeModelsCache.expiresAt) && len(kiloFreeModelsCache.models) > 0 {
		models := append([]string(nil), kiloFreeModelsCache.models...)
		kiloFreeModelsCache.Unlock()
		return models, nil
	}
	kiloFreeModelsCache.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, kiloModelsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if token := strings.TrimSpace(p.AccessToken); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("kilo free models status %d", resp.StatusCode)
	}
	var raw struct {
		Data []struct {
			ID            string `json:"id"`
			IsFree        bool   `json:"isFree"`
			ContextLength int64  `json:"context_length"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	var models []string
	for _, item := range raw.Data {
		if item.IsFree && strings.TrimSpace(item.ID) != "" {
			models = append(models, item.ID)
		}
	}
	models = uniqueStrings(models)
	if len(models) > 0 {
		kiloFreeModelsCache.Lock()
		kiloFreeModelsCache.models = append([]string(nil), models...)
		kiloFreeModelsCache.expiresAt = time.Now().Add(time.Hour)
		kiloFreeModelsCache.Unlock()
	}
	return models, nil
}

func (s *Server) refreshKiloModelsIfStale(ctx context.Context) {
	p, ok := s.providerByID("kilo")
	if !ok || p.Type != "kilocode" || !p.Enabled {
		return
	}
	if len(p.Models) > 0 && !isKiloLegacyModelSet(p.Models) {
		return
	}
	models, err := fetchKiloFreeModels(ctx, s.client, p)
	if err != nil || len(models) == 0 {
		return
	}
	p.Models = models
	p.EnabledModels = append([]string(nil), models...)
	p.AvailableModels = nil
	p.ModelLatencyMS = nil
	p.AvailabilityCheckedAt = 0
	_ = s.updateProvider(p)
}

func isKiloLegacyModelSet(models []string) bool {
	legacy := []string{
		"anthropic/claude-sonnet-4-20250514",
		"anthropic/claude-opus-4-20250514",
		"google/gemini-2.5-pro",
		"google/gemini-2.5-flash",
		"openai/gpt-4.1",
		"openai/o3",
		"deepseek/deepseek-chat",
		"deepseek/deepseek-reasoner",
	}
	if len(models) != len(legacy) {
		return false
	}
	have := sliceSet(models)
	for _, id := range legacy {
		if !have[id] {
			return false
		}
	}
	return true
}
