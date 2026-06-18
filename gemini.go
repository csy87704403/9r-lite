package main

import (
	"bufio"
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	geminiAuthorizeURL = "https://accounts.google.com/o/oauth2/v2/auth"
	geminiTokenURL     = "https://oauth2.googleapis.com/token"
	geminiUserInfoURL  = "https://www.googleapis.com/oauth2/v1/userinfo?alt=json"

	geminiAPIBaseURL        = "https://cloudcode-pa.googleapis.com/v1internal"
	geminiLoadCodeAssistURL = "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"
	geminiModelsURL         = "https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels"

	geminiCLIUserAgentVersion = "0.34.0"
	geminiCLIApiClient        = "google-genai-sdk/1.41.0 gl-node/v22.19.0"
)

func geminiOAuthClientID() string {
	return strings.TrimSpace(os.Getenv("GEMINI_OAUTH_CLIENT_ID"))
}

func geminiOAuthClientSecret() string {
	return strings.TrimSpace(os.Getenv("GEMINI_OAUTH_CLIENT_SECRET"))
}

func geminiOAuthCredentials() (string, string, error) {
	clientID := geminiOAuthClientID()
	clientSecret := geminiOAuthClientSecret()
	if clientID == "" || clientSecret == "" {
		return "", "", fmt.Errorf("Gemini OAuth requires GEMINI_OAUTH_CLIENT_ID and GEMINI_OAUTH_CLIENT_SECRET")
	}
	return clientID, clientSecret, nil
}

var geminiScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
}

var geminiModelsCache = struct {
	sync.Mutex
	entries map[string]struct {
		expiresAt time.Time
		models    []string
	}
}{entries: map[string]struct {
	expiresAt time.Time
	models    []string
}{}}

func (s *Server) handleGeminiAuthorize(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	clientID, _, err := geminiOAuthCredentials()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	redirectURI := publicURL(r, "/api/oauth/gemini/callback")
	state := randomHex(16)
	params := url.Values{}
	params.Set("client_id", clientID)
	params.Set("response_type", "code")
	params.Set("redirect_uri", redirectURI)
	params.Set("scope", strings.Join(geminiScopes, " "))
	params.Set("state", state)
	params.Set("access_type", "offline")
	params.Set("prompt", "consent")

	writeJSON(w, http.StatusOK, map[string]any{
		"authUrl":     geminiAuthorizeURL + "?" + params.Encode(),
		"redirectUri": redirectURI,
		"state":       state,
	})
}

func (s *Server) handleGeminiCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if errText := strings.TrimSpace(r.URL.Query().Get("error")); errText != "" {
		http.Error(w, errText, http.StatusBadRequest)
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}
	redirectURI := publicURL(r, "/api/oauth/gemini/callback")
	tokens, err := s.exchangeGeminiToken(r.Context(), code, redirectURI)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	userInfo, err := s.geminiUserInfo(r.Context(), tokens.AccessToken)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	projectID, err := s.geminiLoadProjectID(r.Context(), tokens.AccessToken)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	p, _ := s.providerByID("gemini")
	if p.ID == "" {
		p = ProviderConfig{
			ID:     "gemini",
			Name:   "Gemini CLI",
			Type:   "gemini-cli",
			Models: []string{"gemini-3-flash-preview", "gemini-3-pro-preview"},
		}
	}
	p.Type = "gemini-cli"
	p.Enabled = true
	p.AccessToken = tokens.AccessToken
	p.RefreshToken = tokens.RefreshToken
	p.ExpiresIn = tokens.ExpiresIn
	p.Email = userInfo.Email
	p.DisplayName = firstNonEmpty(userInfo.Name, userInfo.Email)
	p.ProviderSpecificData = map[string]string{
		"projectId": projectID,
		"scope":     tokens.Scope,
	}
	if len(p.Models) == 0 {
		p.Models = []string{"gemini-3-flash-preview", "gemini-3-pro-preview"}
	}
	if err := s.updateProvider(p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte("<!doctype html><meta charset=\"utf-8\"><title>Gemini Connected</title><p>Gemini connected. You can close this window.</p><script>setTimeout(()=>window.close(),1200)</script>"))
}

type geminiTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

type geminiUser struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

func (s *Server) exchangeGeminiToken(ctx context.Context, code, redirectURI string) (geminiTokenResponse, error) {
	clientID, clientSecret, err := geminiOAuthCredentials()
	if err != nil {
		return geminiTokenResponse{}, err
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return geminiTokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return geminiTokenResponse{}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return geminiTokenResponse{}, fmt.Errorf("Gemini token exchange failed: %d %s", resp.StatusCode, truncateString(string(b), 240))
	}
	var out geminiTokenResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return geminiTokenResponse{}, err
	}
	if out.AccessToken == "" {
		return geminiTokenResponse{}, fmt.Errorf("Gemini token exchange returned no access token")
	}
	return out, nil
}

func (s *Server) geminiUserInfo(ctx context.Context, accessToken string) (geminiUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, geminiUserInfoURL, nil)
	if err != nil {
		return geminiUser{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return geminiUser{}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return geminiUser{}, fmt.Errorf("Gemini user info failed: %d %s", resp.StatusCode, truncateString(string(b), 240))
	}
	var out geminiUser
	if err := json.Unmarshal(b, &out); err != nil {
		return geminiUser{}, err
	}
	return out, nil
}

func (s *Server) geminiLoadProjectID(ctx context.Context, accessToken string) (string, error) {
	body := mustJSON(map[string]any{
		"metadata": geminiOAuthClientMetadata(),
		"mode":     1,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiLoadCodeAssistURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	for k, v := range geminiCommonHeaders(accessToken, "gemini-3-flash-preview", false) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Client-Metadata", string(mustJSON(geminiOAuthClientMetadata())))

	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("Gemini project lookup failed: %d %s", resp.StatusCode, truncateString(string(b), 240))
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return "", err
	}
	projectID := strings.TrimSpace(stringValue(raw["cloudaicompanionProject"]))
	if projectID == "" {
		if m, ok := raw["cloudaicompanionProject"].(map[string]any); ok {
			projectID = strings.TrimSpace(stringValue(m["id"]))
		}
	}
	if projectID == "" {
		return "", fmt.Errorf("Gemini returned no project id")
	}
	return projectID, nil
}

func (s *Server) fetchGeminiCLIModels(ctx context.Context, p ProviderConfig) ([]string, error) {
	if strings.TrimSpace(p.AccessToken) == "" {
		return p.Models, nil
	}
	cacheKey := firstNonEmpty(p.ProviderSpecificData["projectId"], p.Email, p.AccessToken)
	geminiModelsCache.Lock()
	if cached, ok := geminiModelsCache.entries[cacheKey]; ok && time.Now().Before(cached.expiresAt) {
		models := append([]string(nil), cached.models...)
		geminiModelsCache.Unlock()
		return models, nil
	}
	geminiModelsCache.Unlock()

	ids, err := s.fetchGeminiCLIModelsOnce(ctx, p)
	if err != nil && p.RefreshToken != "" {
		if refreshed, rerr := s.refreshGeminiProvider(ctx, p); rerr == nil {
			p = refreshed
			ids, err = s.fetchGeminiCLIModelsOnce(ctx, p)
		}
	}
	if err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		geminiModelsCache.Lock()
		geminiModelsCache.entries[cacheKey] = struct {
			expiresAt time.Time
			models    []string
		}{expiresAt: time.Now().Add(time.Hour), models: append([]string(nil), ids...)}
		geminiModelsCache.Unlock()
	}
	return ids, nil
}

func (s *Server) fetchGeminiCLIModelsOnce(ctx context.Context, p ProviderConfig) ([]string, error) {
	payload := map[string]any{}
	if projectID := strings.TrimSpace(p.ProviderSpecificData["projectId"]); projectID != "" {
		payload["project"] = projectID
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiModelsURL, bytes.NewReader(mustJSON(payload)))
	if err != nil {
		return nil, err
	}
	for k, v := range geminiCommonHeaders(p.AccessToken, "gemini-3-flash-preview", false) {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("gemini models unauthorized: %d %s", resp.StatusCode, truncateString(string(b), 240))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("gemini models status %d %s", resp.StatusCode, truncateString(string(b), 240))
	}
	return parseGeminiModelIDs(b)
}

func parseGeminiModelIDs(raw []byte) ([]string, error) {
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	var ids []string
	switch models := data["models"].(type) {
	case []any:
		for _, item := range models {
			if m, ok := item.(map[string]any); ok {
				id := firstNonEmpty(stringValue(m["id"]), stringValue(m["model"]), stringValue(m["name"]))
				if id != "" {
					ids = append(ids, id)
				}
			}
		}
	case map[string]any:
		for id, item := range models {
			if m, ok := item.(map[string]any); ok {
				if boolValue(m["isInternal"]) {
					continue
				}
			}
			if strings.TrimSpace(id) != "" {
				ids = append(ids, id)
			}
		}
	}
	ids = uniqueStrings(ids)
	sort.Strings(ids)
	return ids, nil
}

func (s *Server) proxyGeminiCLI(w http.ResponseWriter, r *http.Request, p ProviderConfig, req chatRequest, upstreamModel string) {
	if strings.TrimSpace(p.AccessToken) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "gemini is not logged in"})
		return
	}
	body, err := buildGeminiCLIEnvelope(p, req.Raw, upstreamModel)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	resp, usedProvider, err := s.doGeminiCLIRequest(r.Context(), p, upstreamModel, req.Stream, body, true)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	if usedProvider.AccessToken != p.AccessToken {
		p = usedProvider
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		s.markProviderAuthState(p.ID, "needs_login", fmt.Sprintf("Gemini returned %d; please login again", resp.StatusCode))
	} else if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		s.markProviderAuthState(p.ID, "ok", "")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}
	if req.Stream {
		geminiCLIStreamToOpenAI(w, resp.Body, "gemini/"+upstreamModel)
		return
	}
	geminiCLIJSONToOpenAI(w, resp.Body, "gemini/"+upstreamModel)
}

func (s *Server) doGeminiCLIRequest(ctx context.Context, p ProviderConfig, model string, stream bool, body []byte, allowRefresh bool) (*http.Response, ProviderConfig, error) {
	action := "generateContent"
	if stream {
		action = "streamGenerateContent?alt=sse"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiAPIBaseURL+":"+action, bytes.NewReader(body))
	if err != nil {
		return nil, p, err
	}
	for k, v := range geminiCommonHeaders(p.AccessToken, model, stream) {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, p, err
	}
	if allowRefresh && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) && p.RefreshToken != "" {
		resp.Body.Close()
		refreshed, rerr := s.refreshGeminiProvider(ctx, p)
		if rerr != nil {
			s.markProviderAuthState(p.ID, "needs_login", rerr.Error())
			return nil, p, rerr
		}
		s.markProviderAuthState(p.ID, "ok", "")
		return s.doGeminiCLIRequest(ctx, refreshed, model, stream, body, false)
	}
	return resp, p, nil
}

func (s *Server) refreshGeminiProvider(ctx context.Context, p ProviderConfig) (ProviderConfig, error) {
	clientID, clientSecret, err := geminiOAuthCredentials()
	if err != nil {
		return p, err
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", p.RefreshToken)
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return p, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return p, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return p, fmt.Errorf("Gemini token refresh failed: %d %s", resp.StatusCode, truncateString(string(b), 240))
	}
	var data geminiTokenResponse
	if err := json.Unmarshal(b, &data); err != nil {
		return p, err
	}
	if strings.TrimSpace(data.AccessToken) == "" {
		return p, fmt.Errorf("Gemini token refresh returned no access token")
	}
	p.AccessToken = data.AccessToken
	if strings.TrimSpace(data.RefreshToken) != "" {
		p.RefreshToken = data.RefreshToken
	}
	if data.ExpiresIn > 0 {
		p.ExpiresIn = data.ExpiresIn
	}
	if p.ProviderSpecificData == nil {
		p.ProviderSpecificData = map[string]string{}
	}
	p.ProviderSpecificData["authStatus"] = "ok"
	delete(p.ProviderSpecificData, "lastAuthError")
	if strings.TrimSpace(data.Scope) != "" {
		p.ProviderSpecificData["scope"] = data.Scope
	}
	if err := s.updateProvider(p); err != nil {
		return p, err
	}
	return p, nil
}

func buildGeminiCLIEnvelope(p ProviderConfig, raw []byte, model string) ([]byte, error) {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	contents, systemInstruction := openAIToGeminiContents(body["messages"])
	request := map[string]any{
		"sessionId":         uuidV4(),
		"contents":          contents,
		"systemInstruction": systemInstruction,
		"generationConfig":  geminiGenerationConfig(body),
	}
	if tools := openAIToGeminiTools(body["tools"]); len(tools) > 0 {
		request["tools"] = []any{map[string]any{"functionDeclarations": tools}}
	}
	envelope := map[string]any{
		"project":   firstNonEmpty(p.ProviderSpecificData["projectId"], "default-project"),
		"model":     model,
		"userAgent": "gemini-cli",
		"requestId": "req-" + uuidV4(),
		"request":   request,
	}
	return json.Marshal(envelope)
}

func openAIToGeminiContents(raw any) ([]any, any) {
	msgs, _ := raw.([]any)
	toolNames := map[string]string{}
	for _, item := range msgs {
		msg, _ := item.(map[string]any)
		if stringValue(msg["role"]) != "assistant" {
			continue
		}
		if toolCalls, ok := msg["tool_calls"].([]any); ok {
			for _, item := range toolCalls {
				tc, _ := item.(map[string]any)
				fn, _ := tc["function"].(map[string]any)
				id := strings.TrimSpace(stringValue(tc["id"]))
				name := sanitizeGeminiFunctionName(stringValue(fn["name"]))
				if id != "" && name != "" {
					toolNames[id] = name
				}
			}
		}
	}

	var contents []any
	var systemParts []any
	for _, item := range msgs {
		msg, _ := item.(map[string]any)
		role := stringValue(msg["role"])
		switch role {
		case "system":
			if text := openAIText(msg["content"]); text != "" {
				systemParts = append(systemParts, map[string]any{"text": text})
			}
		case "user":
			if parts := openAIContentToGeminiParts(msg["content"]); len(parts) > 0 {
				contents = append(contents, map[string]any{"role": "user", "parts": parts})
			}
		case "assistant":
			var parts []any
			if text := openAIText(msg["content"]); text != "" {
				parts = append(parts, map[string]any{"text": text})
			}
			if toolCalls, ok := msg["tool_calls"].([]any); ok {
				for _, item := range toolCalls {
					tc, _ := item.(map[string]any)
					fn, _ := tc["function"].(map[string]any)
					name := sanitizeGeminiFunctionName(stringValue(fn["name"]))
					if name == "" {
						continue
					}
					args := parseJSONObject(stringValue(fn["arguments"]))
					call := map[string]any{
						"name": name,
						"args": args,
					}
					if id := strings.TrimSpace(stringValue(tc["id"])); id != "" {
						call["id"] = id
						toolNames[id] = name
					}
					parts = append(parts, map[string]any{"functionCall": call})
				}
			}
			if len(parts) > 0 {
				contents = append(contents, map[string]any{"role": "model", "parts": parts})
			}
		case "tool":
			name := toolNames[strings.TrimSpace(stringValue(msg["tool_call_id"]))]
			if name == "" {
				name = "tool"
			}
			contents = append(contents, map[string]any{
				"role": "user",
				"parts": []any{map[string]any{
					"functionResponse": map[string]any{
						"id":   strings.TrimSpace(stringValue(msg["tool_call_id"])),
						"name": name,
						"response": map[string]any{
							"result": parseToolResult(msg["content"]),
						},
					},
				}},
			})
		}
	}

	if len(contents) == 0 {
		contents = []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": ""}}}}
	}
	if len(systemParts) == 0 {
		return contents, nil
	}
	return contents, map[string]any{"role": "user", "parts": systemParts}
}

func openAIContentToGeminiParts(content any) []any {
	if text := openAIText(content); text != "" {
		return []any{map[string]any{"text": text}}
	}
	return nil
}

func openAIText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			m, _ := item.(map[string]any)
			if text := strings.TrimSpace(stringValue(m["text"])); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return strings.TrimSpace(stringValue(content))
	}
}

func openAIToGeminiTools(raw any) []any {
	tools, _ := raw.([]any)
	var out []any
	for _, item := range tools {
		t, _ := item.(map[string]any)
		switch {
		case t["name"] != nil && t["input_schema"] != nil:
			out = append(out, map[string]any{
				"name":        sanitizeGeminiFunctionName(stringValue(t["name"])),
				"description": stringValue(t["description"]),
				"parameters":  cleanGeminiSchema(normalizeSchemaRoot(t["input_schema"])),
			})
		case stringValue(t["type"]) == "function":
			fn, _ := t["function"].(map[string]any)
			name := sanitizeGeminiFunctionName(stringValue(fn["name"]))
			if name == "" {
				continue
			}
			parameters := fn["parameters"]
			if parameters == nil {
				parameters = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			out = append(out, map[string]any{
				"name":        name,
				"description": stringValue(fn["description"]),
				"parameters":  cleanGeminiSchema(normalizeSchemaRoot(parameters)),
			})
		}
	}
	return out
}

func geminiGenerationConfig(body map[string]any) map[string]any {
	cfg := map[string]any{}
	if v, ok := body["temperature"]; ok {
		cfg["temperature"] = v
	}
	if v, ok := body["top_p"]; ok {
		cfg["topP"] = v
	}
	if v, ok := body["top_k"]; ok {
		cfg["topK"] = v
	}
	if n := intValue(body["max_tokens"]); n > 0 {
		cfg["maxOutputTokens"] = n
	} else if n := intValue(body["max_completion_tokens"]); n > 0 {
		cfg["maxOutputTokens"] = n
	}
	return cfg
}

func geminiCLIStreamToOpenAI(w http.ResponseWriter, r io.Reader, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	emitSSE := func(v any) {
		_, _ = w.Write([]byte("data: " + string(mustJSON(v)) + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}

	messageID := "chatcmpl-" + uuidV4()
	created := time.Now().Unix()
	toolCallCount := 0
	started := false

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimSuffix(scanner.Text(), "\r"))
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var raw map[string]any
		if json.Unmarshal([]byte(payload), &raw) != nil {
			continue
		}
		resp, _ := raw["response"].(map[string]any)
		if resp == nil {
			resp = raw
		}
		candidates, _ := resp["candidates"].([]any)
		if len(candidates) == 0 {
			continue
		}
		candidate, _ := candidates[0].(map[string]any)
		content, _ := candidate["content"].(map[string]any)
		parts, _ := content["parts"].([]any)
		if !started {
			started = true
			emitSSE(map[string]any{
				"id":      messageID,
				"object":  "chat.completion.chunk",
				"created": created,
				"model":   model,
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil}},
			})
		}
		for _, item := range parts {
			part, _ := item.(map[string]any)
			if text := stringValue(part["text"]); text != "" {
				delta := map[string]any{}
				if boolValue(part["thought"]) {
					delta["reasoning_content"] = text
				} else {
					delta["content"] = text
				}
				emitSSE(map[string]any{
					"id":      messageID,
					"object":  "chat.completion.chunk",
					"created": created,
					"model":   model,
					"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}},
				})
			}
			if fn, ok := part["functionCall"].(map[string]any); ok {
				toolCallCount++
				emitSSE(map[string]any{
					"id":      messageID,
					"object":  "chat.completion.chunk",
					"created": created,
					"model":   model,
					"choices": []any{map[string]any{
						"index": 0,
						"delta": map[string]any{
							"tool_calls": []any{map[string]any{
								"index": toolCallCount - 1,
								"id":    firstNonEmpty(stringValue(fn["id"]), sanitizeGeminiFunctionName(stringValue(fn["name"]))+"-"+randomHex(6)),
								"type":  "function",
								"function": map[string]any{
									"name":      stringValue(fn["name"]),
									"arguments": string(mustJSON(fn["args"])),
								},
							}},
						},
						"finish_reason": nil,
					}},
				})
			}
		}
		if reason := geminiFinishReason(stringValue(candidate["finishReason"]), toolCallCount > 0); reason != "" {
			final := map[string]any{
				"id":      messageID,
				"object":  "chat.completion.chunk",
				"created": created,
				"model":   firstNonEmpty(stringValue(resp["modelVersion"]), model),
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": reason}},
			}
			if usage := geminiUsage(resp["usageMetadata"]); usage != nil {
				final["usage"] = usage
			}
			emitSSE(final)
		}
	}
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
}

func geminiCLIJSONToOpenAI(w http.ResponseWriter, r io.Reader, model string) {
	var raw map[string]any
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	resp, _ := raw["response"].(map[string]any)
	if resp == nil {
		resp = raw
	}
	candidates, _ := resp["candidates"].([]any)
	if len(candidates) == 0 {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "gemini returned no candidates"})
		return
	}
	candidate, _ := candidates[0].(map[string]any)
	content, _ := candidate["content"].(map[string]any)
	parts, _ := content["parts"].([]any)

	var text strings.Builder
	var reasoning strings.Builder
	var toolCalls []any
	for _, item := range parts {
		part, _ := item.(map[string]any)
		if t := stringValue(part["text"]); t != "" {
			if boolValue(part["thought"]) {
				reasoning.WriteString(t)
			} else {
				text.WriteString(t)
			}
		}
		if fn, ok := part["functionCall"].(map[string]any); ok {
			toolCalls = append(toolCalls, map[string]any{
				"id":   firstNonEmpty(stringValue(fn["id"]), sanitizeGeminiFunctionName(stringValue(fn["name"]))+"-"+randomHex(6)),
				"type": "function",
				"function": map[string]any{
					"name":      stringValue(fn["name"]),
					"arguments": string(mustJSON(fn["args"])),
				},
			})
		}
	}

	message := map[string]any{"role": "assistant", "content": text.String()}
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String()
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	usage := geminiUsage(resp["usageMetadata"])
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      "chatcmpl-" + uuidV4(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   firstNonEmpty(stringValue(resp["modelVersion"]), model),
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": geminiFinishReason(stringValue(candidate["finishReason"]), len(toolCalls) > 0),
		}},
		"usage": usage,
	})
}

func geminiUsage(raw any) map[string]any {
	m, _ := raw.(map[string]any)
	if m == nil {
		return nil
	}
	prompt := intValue(m["promptTokenCount"])
	candidates := intValue(m["candidatesTokenCount"])
	thoughts := intValue(m["thoughtsTokenCount"])
	completion := candidates + thoughts
	total := intValue(m["totalTokenCount"])
	if total <= 0 {
		total = prompt + completion
	}
	usage := map[string]any{
		"prompt_tokens":     prompt,
		"completion_tokens": completion,
		"total_tokens":      total,
	}
	if thoughts > 0 {
		usage["completion_tokens_details"] = map[string]any{"reasoning_tokens": thoughts}
	}
	return usage
}

func geminiFinishReason(reason string, hasToolCalls bool) string {
	switch strings.ToUpper(strings.TrimSpace(reason)) {
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		return "content_filter"
	default:
		if hasToolCalls {
			return "tool_calls"
		}
		return "stop"
	}
}

func geminiCommonHeaders(accessToken, model string, stream bool) map[string]string {
	headers := map[string]string{
		"Content-Type":      "application/json",
		"Authorization":     "Bearer " + accessToken,
		"User-Agent":        geminiCLIUserAgent(model),
		"X-Goog-Api-Client": geminiCLIApiClient,
		"Accept":            "application/json",
	}
	if stream {
		headers["Accept"] = "text/event-stream"
	}
	return headers
}

func geminiCLIUserAgent(model string) string {
	return fmt.Sprintf("GeminiCLI/%s/%s (%s; %s; terminal)", geminiCLIUserAgentVersion, firstNonEmpty(model, "unknown"), geminiPlatformName(), geminiArchName())
}

func geminiPlatformName() string {
	switch runtime.GOOS {
	case "windows":
		return "win32"
	case "darwin":
		return "darwin"
	default:
		return runtime.GOOS
	}
}

func geminiArchName() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "386":
		return "x86"
	default:
		return runtime.GOARCH
	}
}

func geminiOAuthClientMetadata() map[string]int {
	return map[string]int{
		"ideType":    9,
		"platform":   geminiPlatformEnum(),
		"pluginType": 2,
	}
}

func geminiPlatformEnum() int {
	switch runtime.GOOS {
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return 2
		}
		return 1
	case "linux":
		if runtime.GOARCH == "arm64" {
			return 4
		}
		return 3
	case "windows":
		return 5
	default:
		return 0
	}
}

func randomHex(n int) string {
	if n <= 0 {
		return ""
	}
	buf := make([]byte, n)
	if _, err := crand.Read(buf); err != nil {
		return fmt.Sprint(time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func sanitizeGeminiFunctionName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	var b strings.Builder
	for i, r := range name {
		valid := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || (i > 0 && ((r >= '0' && r <= '9') || r == '.' || r == ':' || r == '-'))
		if valid {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
		if b.Len() >= 64 {
			break
		}
	}
	out := b.String()
	if out == "" {
		return "_tool"
	}
	if first := out[0]; !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') || first == '_') {
		out = "_" + out
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

func parseJSONObject(text string) map[string]any {
	text = strings.TrimSpace(text)
	if text == "" {
		return map[string]any{}
	}
	var out map[string]any
	if json.Unmarshal([]byte(text), &out) == nil && out != nil {
		return out
	}
	var generic any
	if json.Unmarshal([]byte(text), &generic) == nil {
		return map[string]any{"value": generic}
	}
	return map[string]any{"raw": text}
}

func parseToolResult(content any) any {
	text := openAIText(content)
	if strings.TrimSpace(text) == "" {
		return ""
	}
	var parsed any
	if json.Unmarshal([]byte(text), &parsed) == nil {
		return parsed
	}
	return text
}

func normalizeSchemaRoot(v any) map[string]any {
	if m, ok := v.(map[string]any); ok && m != nil {
		return m
	}
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func cleanGeminiSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{"reason": map[string]any{"type": "string", "description": "Brief explanation of why you are calling this tool"}},
			"required":   []any{"reason"},
		}
	}
	convertGeminiConstToEnum(schema)
	convertGeminiEnumValues(schema)
	mergeGeminiAllOf(schema)
	flattenGeminiAnyOfOneOf(schema)
	flattenGeminiTypeArrays(schema)
	ensureGeminiObjectType(schema)
	removeGeminiUnsupportedKeywords(schema)
	cleanupGeminiRequired(schema)
	addGeminiPlaceholders(schema)
	return schema
}

var geminiUnsupportedSchemaKeywords = map[string]bool{
	"minLength":            true,
	"maxLength":            true,
	"exclusiveMinimum":     true,
	"exclusiveMaximum":     true,
	"pattern":              true,
	"minItems":             true,
	"maxItems":             true,
	"format":               true,
	"default":              true,
	"examples":             true,
	"$schema":              true,
	"$defs":                true,
	"definitions":          true,
	"const":                true,
	"$ref":                 true,
	"$comment":             true,
	"additionalProperties": true,
	"propertyNames":        true,
	"patternProperties":    true,
	"enumDescriptions":     true,
	"anyOf":                true,
	"oneOf":                true,
	"allOf":                true,
	"not":                  true,
	"dependencies":         true,
	"dependentSchemas":     true,
	"dependentRequired":    true,
	"title":                true,
	"if":                   true,
	"then":                 true,
	"else":                 true,
	"contentMediaType":     true,
	"contentEncoding":      true,
	"cornerRadius":         true,
	"fillColor":            true,
	"fontFamily":           true,
	"fontSize":             true,
	"fontWeight":           true,
	"gap":                  true,
	"padding":              true,
	"strokeColor":          true,
	"strokeThickness":      true,
	"textColor":            true,
}

func convertGeminiConstToEnum(v any) {
	switch x := v.(type) {
	case map[string]any:
		if constant, ok := x["const"]; ok {
			if _, hasEnum := x["enum"]; !hasEnum {
				x["enum"] = []any{constant}
			}
			delete(x, "const")
		}
		for _, child := range x {
			convertGeminiConstToEnum(child)
		}
	case []any:
		for _, child := range x {
			convertGeminiConstToEnum(child)
		}
	}
}

func convertGeminiEnumValues(v any) {
	switch x := v.(type) {
	case map[string]any:
		if enumValues, ok := x["enum"].([]any); ok {
			converted := make([]any, 0, len(enumValues))
			for _, item := range enumValues {
				converted = append(converted, fmt.Sprint(item))
			}
			x["enum"] = converted
			x["type"] = "string"
		}
		for _, child := range x {
			convertGeminiEnumValues(child)
		}
	case []any:
		for _, child := range x {
			convertGeminiEnumValues(child)
		}
	}
}

func mergeGeminiAllOf(v any) {
	switch x := v.(type) {
	case map[string]any:
		if allOf, ok := x["allOf"].([]any); ok && len(allOf) > 0 {
			mergedProps := map[string]any{}
			requiredSet := map[string]bool{}
			for _, item := range allOf {
				part, _ := item.(map[string]any)
				if props, ok := part["properties"].(map[string]any); ok {
					for k, v := range props {
						mergedProps[k] = v
					}
				}
				if required, ok := part["required"].([]any); ok {
					for _, r := range required {
						name := strings.TrimSpace(stringValue(r))
						if name != "" {
							requiredSet[name] = true
						}
					}
				}
			}
			delete(x, "allOf")
			if len(mergedProps) > 0 {
				props, _ := x["properties"].(map[string]any)
				if props == nil {
					props = map[string]any{}
				}
				for k, v := range mergedProps {
					props[k] = v
				}
				x["properties"] = props
			}
			if len(requiredSet) > 0 {
				var required []any
				for name := range requiredSet {
					required = append(required, name)
				}
				x["required"] = required
			}
		}
		for _, child := range x {
			mergeGeminiAllOf(child)
		}
	case []any:
		for _, child := range x {
			mergeGeminiAllOf(child)
		}
	}
}

func flattenGeminiAnyOfOneOf(v any) {
	switch x := v.(type) {
	case map[string]any:
		for _, key := range []string{"anyOf", "oneOf"} {
			if items, ok := x[key].([]any); ok && len(items) > 0 {
				selected := pickBestGeminiSchema(items)
				delete(x, key)
				for k, v := range selected {
					x[k] = v
				}
			}
		}
		for _, child := range x {
			flattenGeminiAnyOfOneOf(child)
		}
	case []any:
		for _, child := range x {
			flattenGeminiAnyOfOneOf(child)
		}
	}
}

func pickBestGeminiSchema(items []any) map[string]any {
	bestScore := -1
	var best map[string]any
	for _, item := range items {
		part, _ := item.(map[string]any)
		if part == nil {
			continue
		}
		if stringValue(part["type"]) == "null" {
			continue
		}
		score := 0
		switch {
		case stringValue(part["type"]) == "object" || part["properties"] != nil:
			score = 3
		case stringValue(part["type"]) == "array" || part["items"] != nil:
			score = 2
		case stringValue(part["type"]) != "":
			score = 1
		}
		if score > bestScore {
			bestScore = score
			best = part
		}
	}
	if best == nil {
		return map[string]any{"type": "string"}
	}
	return best
}

func flattenGeminiTypeArrays(v any) {
	switch x := v.(type) {
	case map[string]any:
		if types, ok := x["type"].([]any); ok && len(types) > 0 {
			chosen := "string"
			for _, item := range types {
				t := strings.TrimSpace(stringValue(item))
				if t != "" && t != "null" {
					chosen = t
					break
				}
			}
			x["type"] = chosen
		}
		for _, child := range x {
			flattenGeminiTypeArrays(child)
		}
	case []any:
		for _, child := range x {
			flattenGeminiTypeArrays(child)
		}
	}
}

func ensureGeminiObjectType(v any) {
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x["properties"].(map[string]any); ok && strings.TrimSpace(stringValue(x["type"])) == "" {
			x["type"] = "object"
		}
		for _, child := range x {
			ensureGeminiObjectType(child)
		}
	case []any:
		for _, child := range x {
			ensureGeminiObjectType(child)
		}
	}
}

func removeGeminiUnsupportedKeywords(v any) {
	switch x := v.(type) {
	case map[string]any:
		for key := range x {
			if geminiUnsupportedSchemaKeywords[key] || strings.HasPrefix(key, "x-") {
				delete(x, key)
				continue
			}
			removeGeminiUnsupportedKeywords(x[key])
		}
	case []any:
		for _, child := range x {
			removeGeminiUnsupportedKeywords(child)
		}
	}
}

func cleanupGeminiRequired(v any) {
	switch x := v.(type) {
	case map[string]any:
		if required, ok := x["required"].([]any); ok {
			props, _ := x["properties"].(map[string]any)
			if len(props) == 0 {
				delete(x, "required")
			} else {
				var valid []any
				for _, item := range required {
					name := strings.TrimSpace(stringValue(item))
					if name != "" {
						if _, ok := props[name]; ok {
							valid = append(valid, name)
						}
					}
				}
				if len(valid) == 0 {
					delete(x, "required")
				} else {
					x["required"] = valid
				}
			}
		}
		for _, child := range x {
			cleanupGeminiRequired(child)
		}
	case []any:
		for _, child := range x {
			cleanupGeminiRequired(child)
		}
	}
}

func addGeminiPlaceholders(v any) {
	switch x := v.(type) {
	case map[string]any:
		if stringValue(x["type"]) == "object" {
			props, _ := x["properties"].(map[string]any)
			if len(props) == 0 {
				x["properties"] = map[string]any{
					"reason": map[string]any{
						"type":        "string",
						"description": "Brief explanation of why you are calling this tool",
					},
				}
				x["required"] = []any{"reason"}
			}
		}
		for _, child := range x {
			addGeminiPlaceholders(child)
		}
	case []any:
		for _, child := range x {
			addGeminiPlaceholders(child)
		}
	}
}
