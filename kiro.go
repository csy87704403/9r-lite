//go:build kiro

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	kiroDefaultRegion   = "us-east-1"
	kiroDefaultStartURL = "https://view.awsapps.com/start"

	kiroRegisterClientURLFormat = "https://oidc.%s.amazonaws.com/client/register"
	kiroDeviceAuthURLFormat     = "https://oidc.%s.amazonaws.com/device_authorization"
	kiroTokenURLFormat          = "https://oidc.%s.amazonaws.com/token"

	kiroRuntimeURL = "https://runtime.us-east-1.kiro.dev/generateAssistantResponse"
	kiroCWURL      = "https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse"
	kiroQURL       = "https://q.us-east-1.amazonaws.com/generateAssistantResponse"

	kiroListProfilesURL = "https://codewhisperer.us-east-1.amazonaws.com/ListAvailableProfiles"

	kiroClientName = "kiro-oauth-client"
	kiroClientType = "public"

	kiroRuntimeSDKVersion = "1.0.0"
	kiroAgentOS           = "windows"
	kiroAgentOSVersion    = "10.0.26200"
	kiroNodeVersion       = "22.21.1"
	kiroVersion           = "0.10.32"
)

func kiroDefaultModels() []string {
	return []string{
		"claude-sonnet-4.5",
		"claude-haiku-4.5",
		"deepseek-3.2",
		"qwen3-coder-next",
		"glm-5",
		"MiniMax-M2.5",
		"claude-sonnet-4.5-thinking",
		"claude-haiku-4.5-thinking",
		"claude-sonnet-4.5-agentic",
		"claude-haiku-4.5-agentic",
		"claude-sonnet-4.5-thinking-agentic",
		"claude-haiku-4.5-thinking-agentic",
	}
}

var (
	kiroScopes = []string{
		"codewhisperer:completions",
		"codewhisperer:analysis",
		"codewhisperer:conversations",
	}
	kiroGrantTypes = []string{
		"urn:ietf:params:oauth:grant-type:device_code",
		"refresh_token",
	}
	kiroDefaultProfileArns = map[string]string{
		"builder-id": "arn:aws:codewhisperer:us-east-1:638616132270:profile/AAAACCCCXXXX",
		"social":     "arn:aws:codewhisperer:us-east-1:699475941385:profile/EHGA3GRVQMUK",
	}
	kiroAgenticSystemPrompt = strings.TrimSpace(`
# CRITICAL: CHUNKED WRITE PROTOCOL (MANDATORY)

You MUST follow these rules for ALL file operations. Violation causes server timeouts and task failure.

## ABSOLUTE LIMITS
- MAXIMUM 350 LINES per single write/edit operation
- RECOMMENDED 300 LINES or less
- NEVER write entire files in one operation if >300 lines

## MANDATORY CHUNKED WRITE STRATEGY

For large files or large edits, split writes into multiple chunks.
When in doubt, write less per operation.
`)
)

var kiroModelCache = struct {
	sync.Mutex
	entries map[string]struct {
		expiresAt time.Time
		models    []string
	}
}{entries: map[string]struct {
	expiresAt time.Time
	models    []string
}{}}

type kiroPollRequest struct {
	DeviceCode string            `json:"deviceCode"`
	ExtraData  map[string]string `json:"extraData"`
}

func (s *Server) handleKiroDeviceCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	region := kiroDefaultRegion
	startURL := kiroDefaultStartURL
	authMethod := "builder-id"

	clientInfo, err := s.kiroRegisterClient(r.Context(), region)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	deviceInfo, err := s.kiroStartDeviceAuthorization(r.Context(), region, clientInfo["clientId"], clientInfo["clientSecret"], startURL)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	deviceInfo["_clientId"] = clientInfo["clientId"]
	deviceInfo["_clientSecret"] = clientInfo["clientSecret"]
	deviceInfo["_region"] = region
	deviceInfo["_authMethod"] = authMethod
	deviceInfo["_startUrl"] = startURL
	writeJSON(w, http.StatusOK, deviceInfo)
}

func (s *Server) handleKiroPoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body kiroPollRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	code := strings.TrimSpace(body.DeviceCode)
	clientID := strings.TrimSpace(body.ExtraData["_clientId"])
	clientSecret := strings.TrimSpace(body.ExtraData["_clientSecret"])
	region := firstNonEmpty(body.ExtraData["_region"], kiroDefaultRegion)
	authMethod := firstNonEmpty(body.ExtraData["_authMethod"], "builder-id")
	startURL := firstNonEmpty(body.ExtraData["_startUrl"], kiroDefaultStartURL)
	if code == "" || clientID == "" || clientSecret == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "invalid_request", "errorDescription": "Missing deviceCode/client credentials"})
		return
	}

	result, err := s.kiroPollToken(r.Context(), region, clientID, clientSecret, code)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"success": false, "error": "poll_failed", "errorDescription": err.Error()})
		return
	}
	if !result["success"].(bool) {
		if result["pending"].(bool) {
			writeJSON(w, http.StatusOK, map[string]any{"success": false, "pending": true, "error": result["error"], "errorDescription": result["errorDescription"]})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": result["error"], "errorDescription": result["errorDescription"]})
		return
	}

	accessToken := stringValue(result["accessToken"])
	refreshToken := stringValue(result["refreshToken"])
	profileArn := strings.TrimSpace(stringValue(result["profileArn"]))
	if profileArn == "" {
		profileArn = strings.TrimSpace(s.fetchKiroProfileArn(r.Context(), accessToken))
	}
	if profileArn == "" {
		profileArn = kiroDefaultProfileArn(authMethod)
	}

	p, _ := s.providerByID("kiro")
	if p.ID == "" {
		p = ProviderConfig{
			ID:      "kiro",
			Name:    "Kiro AI",
			Type:    "kiro",
			BaseURL: kiroRuntimeURL,
		}
	}
	p.Type = "kiro"
	p.Enabled = true
	p.BaseURL = firstNonEmpty(p.BaseURL, kiroRuntimeURL)
	p.AccessToken = accessToken
	p.RefreshToken = refreshToken
	p.ExpiresIn = int64(intValue(result["expiresIn"]))
	p.Email = firstNonEmpty(extractEmailFromJWT(accessToken), "kiro-user")
	p.ProviderSpecificData = map[string]string{
		"profileArn":   profileArn,
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"region":       region,
		"authMethod":   authMethod,
		"startUrl":     startURL,
	}
	if len(p.Models) == 0 {
		p.Models = kiroDefaultModels()
	}
	if err := s.updateProvider(p); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "provider": p})
}

func (s *Server) kiroRegisterClient(ctx context.Context, region string) (map[string]string, error) {
	reqBody := map[string]any{
		"clientName": kiroClientName,
		"clientType": kiroClientType,
		"scopes":     kiroScopes,
		"grantTypes": kiroGrantTypes,
		"issuerUrl":  "https://identitycenter.amazonaws.com/ssoins-722374e8c3c8e6c6",
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf(kiroRegisterClientURLFormat, region), bytes.NewReader(mustJSON(reqBody)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("Kiro client registration failed: %d %s", resp.StatusCode, truncateString(string(b), 240))
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	return map[string]string{
		"clientId":     stringValue(raw["clientId"]),
		"clientSecret": stringValue(raw["clientSecret"]),
	}, nil
}

func (s *Server) kiroStartDeviceAuthorization(ctx context.Context, region, clientID, clientSecret, startURL string) (map[string]any, error) {
	reqBody := map[string]any{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"startUrl":     startURL,
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf(kiroDeviceAuthURLFormat, region), bytes.NewReader(mustJSON(reqBody)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("Kiro device authorization failed: %d %s", resp.StatusCode, truncateString(string(b), 240))
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	return map[string]any{
		"device_code":               stringValue(raw["deviceCode"]),
		"user_code":                 stringValue(raw["userCode"]),
		"verification_uri":          stringValue(raw["verificationUri"]),
		"verification_uri_complete": stringValue(raw["verificationUriComplete"]),
		"expires_in":                intValue(raw["expiresIn"]),
		"interval":                  intValue(raw["interval"]),
	}, nil
}

func (s *Server) kiroPollToken(ctx context.Context, region, clientID, clientSecret, deviceCode string) (map[string]any, error) {
	reqBody := map[string]any{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"deviceCode":   deviceCode,
		"grantType":    "urn:ietf:params:oauth:grant-type:device_code",
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf(kiroTokenURLFormat, region), bytes.NewReader(mustJSON(reqBody)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var raw map[string]any
	_ = json.Unmarshal(b, &raw)
	if token := stringValue(raw["accessToken"]); token != "" {
		return map[string]any{
			"success":      true,
			"pending":      false,
			"accessToken":  token,
			"refreshToken": stringValue(raw["refreshToken"]),
			"expiresIn":    intValue(raw["expiresIn"]),
			"profileArn":   stringValue(raw["profileArn"]),
		}, nil
	}
	errName := firstNonEmpty(stringValue(raw["error"]), "authorization_pending")
	errDesc := firstNonEmpty(stringValue(raw["error_description"]), stringValue(raw["message"]))
	return map[string]any{
		"success":          false,
		"pending":          errName == "authorization_pending" || errName == "slow_down",
		"error":            errName,
		"errorDescription": errDesc,
	}, nil
}

func (s *Server) fetchKiroProfileArn(ctx context.Context, accessToken string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, kiroListProfilesURL, bytes.NewReader(mustJSON(map[string]any{"maxResults": 10})))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := s.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return ""
	}
	var raw struct {
		Profiles []struct {
			Arn string `json:"arn"`
		} `json:"profiles"`
	}
	if json.NewDecoder(resp.Body).Decode(&raw) != nil {
		return ""
	}
	for _, p := range raw.Profiles {
		if strings.TrimSpace(p.Arn) != "" {
			return strings.TrimSpace(p.Arn)
		}
	}
	return ""
}

func (s *Server) fetchKiroModels(ctx context.Context, p ProviderConfig) ([]string, error) {
	if strings.TrimSpace(p.AccessToken) == "" {
		return p.Models, nil
	}
	key := kiroCacheKey(p)
	kiroModelCache.Lock()
	if cached, ok := kiroModelCache.entries[key]; ok && time.Now().Before(cached.expiresAt) {
		models := append([]string(nil), cached.models...)
		kiroModelCache.Unlock()
		return models, nil
	}
	kiroModelCache.Unlock()

	models, err := s.fetchKiroModelsOnce(ctx, p)
	if err != nil && p.RefreshToken != "" {
		if refreshed, rerr := s.refreshKiroProvider(ctx, p); rerr == nil {
			p = refreshed
			models, err = s.fetchKiroModelsOnce(ctx, p)
		}
	}
	if err != nil {
		return nil, err
	}
	if len(models) > 0 {
		kiroModelCache.Lock()
		kiroModelCache.entries[key] = struct {
			expiresAt time.Time
			models    []string
		}{expiresAt: time.Now().Add(5 * time.Minute), models: append([]string(nil), models...)}
		kiroModelCache.Unlock()
	}
	return models, nil
}

func (s *Server) fetchKiroModelsOnce(ctx context.Context, p ProviderConfig) ([]string, error) {
	profileArn := strings.TrimSpace(firstNonEmpty(p.ProviderSpecificData["profileArn"], kiroDefaultProfileArn(p.ProviderSpecificData["authMethod"])))
	region := kiroRegionFromProfileArn(profileArn)
	params := url.Values{}
	params.Set("origin", "AI_EDITOR")
	if profileArn != "" {
		params.Set("profileArn", profileArn)
	}
	u := "https://q." + region + ".amazonaws.com/ListAvailableModels?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range buildKiroFingerprintHeaders(p) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Authorization", "Bearer "+p.AccessToken)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("Kiro models failed: %d %s", resp.StatusCode, truncateString(string(b), 240))
	}
	var raw struct {
		Models []struct {
			ModelID        string `json:"modelId"`
			ModelName      string `json:"modelName"`
			RateMultiplier any    `json:"rateMultiplier"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	var out []string
	for _, m := range raw.Models {
		id := strings.TrimSpace(m.ModelID)
		if id == "" {
			continue
		}
		out = append(out, id, id+"-thinking")
		if id != "auto" {
			out = append(out, id+"-agentic", id+"-thinking-agentic")
		}
	}
	out = uniqueStrings(out)
	sort.Strings(out)
	return out, nil
}

func (s *Server) proxyKiro(w http.ResponseWriter, r *http.Request, p ProviderConfig, req chatRequest, upstreamModel string) {
	if strings.TrimSpace(p.AccessToken) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "kiro is not logged in"})
		return
	}
	payload, resolvedModel, err := buildKiroPayload(req.Raw, upstreamModel, p)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	body := mustJSON(payload)

	resp, usedProvider, err := s.doKiroRequest(r.Context(), p, body)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	_ = usedProvider
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}
	if req.Stream {
		kiroEventStreamToOpenAI(w, resp.Body, "kiro/"+resolvedModel)
		return
	}
	kiroEventJSONToOpenAI(w, resp.Body, "kiro/"+resolvedModel)
}

func (s *Server) doKiroRequest(ctx context.Context, p ProviderConfig, body []byte) (*http.Response, ProviderConfig, error) {
	targets := []string{kiroRuntimeURL, kiroCWURL, kiroQURL}
	tryRequest := func(provider ProviderConfig) (*http.Response, error) {
		var lastResp *http.Response
		var lastErr error
		for _, target := range targets {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			for k, v := range map[string]string{
				"Content-Type":          "application/json",
				"Accept":                "application/vnd.amazon.eventstream",
				"X-Amz-Target":          "AmazonCodeWhispererStreamingService.GenerateAssistantResponse",
				"User-Agent":            "AWS-SDK-JS/3.0.0 kiro-ide/1.0.0",
				"X-Amz-User-Agent":      "aws-sdk-js/3.0.0 kiro-ide/1.0.0",
				"Amz-Sdk-Request":       "attempt=1; max=3",
				"Amz-Sdk-Invocation-Id": uuidV4(),
				"Authorization":         "Bearer " + provider.AccessToken,
			} {
				req.Header.Set(k, v)
			}
			resp, err := s.client.Do(req)
			if err != nil {
				lastErr = err
				continue
			}
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				lastResp = resp
				resp.Body.Close()
				continue
			}
			return resp, nil
		}
		if lastResp != nil {
			return lastResp, nil
		}
		return nil, lastErr
	}

	resp, err := tryRequest(p)
	if err != nil {
		return nil, p, err
	}
	if (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) && strings.TrimSpace(p.RefreshToken) != "" {
		resp.Body.Close()
		refreshed, rerr := s.refreshKiroProvider(ctx, p)
		if rerr != nil {
			return nil, p, rerr
		}
		resp, err = tryRequest(refreshed)
		if err != nil {
			return nil, refreshed, err
		}
		return resp, refreshed, nil
	}
	return resp, p, nil
}

func (s *Server) refreshKiroProvider(ctx context.Context, p ProviderConfig) (ProviderConfig, error) {
	clientID := strings.TrimSpace(p.ProviderSpecificData["clientId"])
	clientSecret := strings.TrimSpace(p.ProviderSpecificData["clientSecret"])
	region := firstNonEmpty(p.ProviderSpecificData["region"], kiroDefaultRegion)
	if clientID == "" || clientSecret == "" || strings.TrimSpace(p.RefreshToken) == "" {
		return p, fmt.Errorf("Kiro refresh token/client credentials are missing")
	}
	reqBody := map[string]any{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"refreshToken": p.RefreshToken,
		"grantType":    "refresh_token",
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf(kiroTokenURLFormat, region), bytes.NewReader(mustJSON(reqBody)))
	if err != nil {
		return p, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return p, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return p, fmt.Errorf("Kiro token refresh failed: %d %s", resp.StatusCode, truncateString(string(b), 240))
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return p, err
	}
	p.AccessToken = stringValue(raw["accessToken"])
	p.RefreshToken = firstNonEmpty(stringValue(raw["refreshToken"]), p.RefreshToken)
	p.ExpiresIn = int64(intValue(raw["expiresIn"]))
	if p.ProviderSpecificData == nil {
		p.ProviderSpecificData = map[string]string{}
	}
	if strings.TrimSpace(p.ProviderSpecificData["profileArn"]) == "" {
		if arn := strings.TrimSpace(firstNonEmpty(stringValue(raw["profileArn"]), s.fetchKiroProfileArn(ctx, p.AccessToken), kiroDefaultProfileArn(p.ProviderSpecificData["authMethod"]))); arn != "" {
			p.ProviderSpecificData["profileArn"] = arn
		}
	}
	if err := s.updateProvider(p); err != nil {
		return p, err
	}
	return p, nil
}

func buildKiroPayload(raw []byte, model string, p ProviderConfig) (map[string]any, string, error) {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, "", err
	}
	messages, _ := body["messages"].([]any)
	tools, _ := body["tools"].([]any)
	upstreamModel, thinking, agentic := resolveKiroModel(model)
	if isKiroThinkingRequested(body) {
		thinking = true
	}
	history, currentMessage := convertKiroMessages(messages, tools, upstreamModel)
	content := stringValue(currentMessage["content"])
	var prefix []string
	if thinking {
		prefix = append(prefix, "<thinking_mode>enabled</thinking_mode>\n<max_thinking_length>16000</max_thinking_length>")
	}
	prefix = append(prefix, "[Context: Current time is "+time.Now().UTC().Format(time.RFC3339)+"]")
	if agentic {
		prefix = append(prefix, kiroAgenticSystemPrompt)
	}
	content = strings.TrimSpace(strings.Join(append(prefix, content), "\n\n"))
	currentMessage["content"] = content
	currentMessage["modelId"] = upstreamModel

	payload := map[string]any{
		"conversationState": map[string]any{
			"chatTriggerType": "MANUAL",
			"conversationId":  uuidV4() + fmt.Sprint(time.Now().UnixMilli()),
			"currentMessage": map[string]any{
				"userInputMessage": currentMessage,
			},
			"history": history,
		},
	}
	if profileArn := strings.TrimSpace(firstNonEmpty(p.ProviderSpecificData["profileArn"], kiroDefaultProfileArn(p.ProviderSpecificData["authMethod"]))); profileArn != "" {
		payload["profileArn"] = profileArn
	}
	inference := map[string]any{"maxTokens": 32000}
	if body["temperature"] != nil {
		inference["temperature"] = body["temperature"]
	}
	if body["top_p"] != nil {
		inference["topP"] = body["top_p"]
	}
	payload["inferenceConfig"] = inference
	return payload, upstreamModel, nil
}

func convertKiroMessages(messages []any, tools []any, model string) ([]any, map[string]any) {
	clientProvidedTools := len(tools) > 0
	if !clientProvidedTools {
		messages = flattenKiroToolInteractions(messages)
	}
	var history []any
	var currentMessage map[string]any
	var pendingUser []string
	var pendingAssistant []string
	var pendingToolResults []any
	var currentRole string
	toolsInjected := false
	flush := func() {
		switch currentRole {
		case "user":
			content := strings.TrimSpace(strings.Join(pendingUser, "\n\n"))
			if content == "" {
				content = "continue"
			}
			userMsg := map[string]any{
				"content": content,
				"modelId": model,
			}
			if len(pendingToolResults) > 0 {
				userMsg["userInputMessageContext"] = map[string]any{
					"toolResults": pendingToolResults,
				}
			}
			if clientProvidedTools && !toolsInjected {
				ctx, _ := userMsg["userInputMessageContext"].(map[string]any)
				if ctx == nil {
					ctx = map[string]any{}
				}
				ctx["tools"] = convertKiroToolSpecs(tools)
				userMsg["userInputMessageContext"] = ctx
				toolsInjected = true
			}
			history = append(history, map[string]any{"userInputMessage": userMsg})
			currentMessage = userMsg
			pendingUser = nil
			pendingToolResults = nil
		case "assistant":
			content := strings.TrimSpace(strings.Join(pendingAssistant, "\n\n"))
			if content == "" {
				content = "..."
			}
			history = append(history, map[string]any{"assistantResponseMessage": map[string]any{"content": content}})
			pendingAssistant = nil
		}
	}

	for _, item := range messages {
		msg, _ := item.(map[string]any)
		role := stringValue(msg["role"])
		if role == "system" || role == "tool" {
			role = "user"
		}
		if currentRole != "" && role != currentRole {
			flush()
		}
		currentRole = role
		switch role {
		case "user":
			content := kiroMessageText(msg["content"])
			if stringValue(msg["role"]) == "tool" {
				pendingToolResults = append(pendingToolResults, map[string]any{
					"toolUseId": stringValue(msg["tool_call_id"]),
					"status":    "success",
					"content":   []any{map[string]any{"text": content}},
				})
			} else if content != "" {
				pendingUser = append(pendingUser, content)
			}
		case "assistant":
			textContent := kiroMessageText(msg["content"])
			if textContent != "" {
				pendingAssistant = append(pendingAssistant, textContent)
			}
			toolUses := convertKiroToolUses(msg)
			if len(toolUses) > 0 {
				flush()
				if len(history) > 0 {
					last, _ := history[len(history)-1].(map[string]any)
					if assistant, ok := last["assistantResponseMessage"].(map[string]any); ok {
						assistant["toolUses"] = toolUses
					}
				}
				currentRole = ""
			}
		}
	}
	if currentRole != "" {
		flush()
	}
	for i := len(history) - 1; i >= 0; i-- {
		item, _ := history[i].(map[string]any)
		if uim, ok := item["userInputMessage"].(map[string]any); ok {
			currentMessage = uim
			history = append(history[:i], history[i+1:]...)
			break
		}
	}
	if currentMessage == nil {
		currentMessage = map[string]any{"content": "", "modelId": model}
	}
	for _, item := range history {
		entry, _ := item.(map[string]any)
		if uim, ok := entry["userInputMessage"].(map[string]any); ok {
			if ctx, ok := uim["userInputMessageContext"].(map[string]any); ok {
				delete(ctx, "tools")
				if len(ctx) == 0 {
					delete(uim, "userInputMessageContext")
				}
			}
			if strings.TrimSpace(stringValue(uim["modelId"])) == "" {
				uim["modelId"] = model
			}
		}
	}
	firstTools := extractFirstKiroTools(history)
	if len(firstTools) > 0 {
		ctx, _ := currentMessage["userInputMessageContext"].(map[string]any)
		if ctx == nil {
			ctx = map[string]any{}
		}
		if _, exists := ctx["tools"]; !exists {
			ctx["tools"] = firstTools
			currentMessage["userInputMessageContext"] = ctx
		}
	}
	return mergeKiroConsecutiveUsers(history), currentMessage
}

func flattenKiroToolInteractions(messages []any) []any {
	var out []any
	for _, item := range messages {
		msg, _ := item.(map[string]any)
		role := stringValue(msg["role"])
		if role == "tool" {
			out = append(out, map[string]any{"role": "user", "content": "[Tool result: " + kiroMessageText(msg["content"]) + "]"})
			continue
		}
		if role == "assistant" {
			parts := []string{}
			if text := kiroMessageText(msg["content"]); text != "" {
				parts = append(parts, text)
			}
			for _, item := range convertKiroToolUses(msg) {
				tc, _ := item.(map[string]any)
				if tc == nil {
					continue
				}
				parts = append(parts, "[Tool call: "+stringValue(tc["name"])+"("+string(mustJSON(tc["input"]))+")]")
			}
			cloned := cloneMap(msg)
			cloned["content"] = strings.TrimSpace(strings.Join(parts, "\n"))
			delete(cloned, "tool_calls")
			out = append(out, cloned)
			continue
		}
		out = append(out, msg)
	}
	return out
}

func kiroMessageText(content any) string {
	switch v := content.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		var parts []string
		for _, item := range v {
			m, _ := item.(map[string]any)
			switch stringValue(m["type"]) {
			case "text":
				if text := strings.TrimSpace(stringValue(m["text"])); text != "" {
					parts = append(parts, text)
				}
			case "tool_result":
				if text := strings.TrimSpace(kiroMessageText(m["content"])); text != "" {
					parts = append(parts, "[Tool result: "+text+"]")
				}
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	default:
		return strings.TrimSpace(stringValue(content))
	}
}

func convertKiroToolUses(msg map[string]any) []any {
	var out []any
	if toolCalls, ok := msg["tool_calls"].([]any); ok {
		for _, item := range toolCalls {
			tc, _ := item.(map[string]any)
			fn, _ := tc["function"].(map[string]any)
			out = append(out, map[string]any{
				"toolUseId": firstNonEmpty(stringValue(tc["id"]), uuidV4()),
				"name":      stringValue(fn["name"]),
				"input":     parseJSONObject(stringValue(fn["arguments"])),
			})
		}
	}
	return out
}

func convertKiroToolSpecs(tools []any) []any {
	var out []any
	for _, item := range tools {
		t, _ := item.(map[string]any)
		fn, _ := t["function"].(map[string]any)
		name := firstNonEmpty(stringValue(fn["name"]), stringValue(t["name"]))
		if name == "" {
			continue
		}
		description := firstNonEmpty(stringValue(fn["description"]), stringValue(t["description"]), "Tool: "+name)
		schema := fn["parameters"]
		if schema == nil {
			schema = t["input_schema"]
		}
		normalized := normalizeKiroToolSchema(normalizeSchemaRoot(schema))
		out = append(out, map[string]any{
			"toolSpecification": map[string]any{
				"name":        name,
				"description": description,
				"inputSchema": map[string]any{"json": normalized},
			},
		})
	}
	return out
}

func normalizeKiroToolSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}, "required": []any{}}
	}
	if strings.TrimSpace(stringValue(schema["type"])) == "" && schema["properties"] != nil {
		schema["type"] = "object"
	}
	if schema["required"] == nil {
		schema["required"] = []any{}
	}
	return schema
}

func normalizeSchemaRoot(v any) map[string]any {
	if m, ok := v.(map[string]any); ok && m != nil {
		return m
	}
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func extractFirstKiroTools(history []any) []any {
	for _, item := range history {
		entry, _ := item.(map[string]any)
		uim, _ := entry["userInputMessage"].(map[string]any)
		ctx, _ := uim["userInputMessageContext"].(map[string]any)
		if tools, ok := ctx["tools"].([]any); ok && len(tools) > 0 {
			return tools
		}
	}
	return nil
}

func mergeKiroConsecutiveUsers(history []any) []any {
	var out []any
	for _, item := range history {
		cur, _ := item.(map[string]any)
		uim, _ := cur["userInputMessage"].(map[string]any)
		if uim == nil || len(out) == 0 {
			out = append(out, item)
			continue
		}
		last, _ := out[len(out)-1].(map[string]any)
		prev, _ := last["userInputMessage"].(map[string]any)
		if prev == nil {
			out = append(out, item)
			continue
		}
		prev["content"] = strings.TrimSpace(stringValue(prev["content"]) + "\n\n" + stringValue(uim["content"]))
		prevCtx, _ := prev["userInputMessageContext"].(map[string]any)
		curCtx, _ := uim["userInputMessageContext"].(map[string]any)
		if curCtx != nil {
			if prevCtx == nil {
				prev["userInputMessageContext"] = curCtx
			} else {
				if tr, ok := curCtx["toolResults"].([]any); ok && len(tr) > 0 {
					prevCtx["toolResults"] = append(anySlice(prevCtx["toolResults"]), tr...)
				}
				if tl, ok := curCtx["tools"].([]any); ok && len(tl) > 0 {
					prevCtx["tools"] = append(anySlice(prevCtx["tools"]), tl...)
				}
			}
		}
	}
	return out
}

func anySlice(v any) []any {
	if arr, ok := v.([]any); ok {
		return arr
	}
	return nil
}

func resolveKiroModel(model string) (upstream string, thinking bool, agentic bool) {
	upstream = model
	if strings.HasSuffix(upstream, "-agentic") {
		agentic = true
		upstream = strings.TrimSuffix(upstream, "-agentic")
	}
	if strings.HasSuffix(upstream, "-thinking") {
		thinking = true
		upstream = strings.TrimSuffix(upstream, "-thinking")
	}
	return upstream, thinking, agentic
}

func isKiroThinkingRequested(body map[string]any) bool {
	if thinking, ok := body["thinking"].(map[string]any); ok && stringValue(thinking["type"]) == "enabled" {
		return true
	}
	if effort := strings.ToLower(strings.TrimSpace(stringValue(body["reasoning_effort"]))); effort == "low" || effort == "medium" || effort == "high" || effort == "auto" {
		return true
	}
	if reasoning, ok := body["reasoning"].(map[string]any); ok {
		if effort := strings.ToLower(strings.TrimSpace(stringValue(reasoning["effort"]))); effort == "low" || effort == "medium" || effort == "high" || effort == "auto" {
			return true
		}
	}
	return false
}

func kiroEventStreamToOpenAI(w http.ResponseWriter, r io.Reader, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	responseID := "chatcmpl-" + uuidV4()
	created := time.Now().Unix()
	chunkIndex := 0
	hasToolCalls := false
	var usage map[string]any
	emit := func(v any) {
		_, _ = w.Write([]byte("data: " + string(mustJSON(v)) + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	_ = parseKiroEventStream(r, func(eventType string, payload map[string]any) {
		switch eventType {
		case "assistantResponseEvent", "codeEvent":
			content := strings.TrimSpace(stringValue(payload["content"]))
			if content == "" {
				return
			}
			delta := map[string]any{"content": content}
			if chunkIndex == 0 {
				delta["role"] = "assistant"
			}
			emit(map[string]any{
				"id":      responseID,
				"object":  "chat.completion.chunk",
				"created": created,
				"model":   model,
				"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}},
			})
			chunkIndex++
		case "reasoningContentEvent":
			content := strings.TrimSpace(firstNonEmpty(stringValue(payload["text"]), stringValue(payload["content"])))
			if content == "" {
				return
			}
			delta := map[string]any{"reasoning_content": content}
			if chunkIndex == 0 {
				delta["role"] = "assistant"
			}
			emit(map[string]any{
				"id":      responseID,
				"object":  "chat.completion.chunk",
				"created": created,
				"model":   model,
				"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}},
			})
			chunkIndex++
		case "toolUseEvent":
			hasToolCalls = true
			toolEvents := []map[string]any{payload}
			if arr, ok := payload["items"].([]any); ok {
				toolEvents = nil
				for _, item := range arr {
					m, _ := item.(map[string]any)
					if m != nil {
						toolEvents = append(toolEvents, m)
					}
				}
			}
			for i, item := range toolEvents {
				emit(map[string]any{
					"id":      responseID,
					"object":  "chat.completion.chunk",
					"created": created,
					"model":   model,
					"choices": []any{map[string]any{
						"index": 0,
						"delta": map[string]any{
							"tool_calls": []any{map[string]any{
								"index": i,
								"id":    firstNonEmpty(stringValue(item["toolUseId"]), "call_"+uuidV4()),
								"type":  "function",
								"function": map[string]any{
									"name":      stringValue(item["name"]),
									"arguments": string(mustJSON(item["input"])),
								},
							}},
						},
						"finish_reason": nil,
					}},
				})
			}
		case "metricsEvent":
			in := intValue(payload["inputTokens"])
			out := intValue(payload["outputTokens"])
			if in > 0 || out > 0 {
				usage = map[string]any{
					"prompt_tokens":     in,
					"completion_tokens": out,
					"total_tokens":      in + out,
				}
			}
		case "messageStopEvent":
			final := map[string]any{
				"id":      responseID,
				"object":  "chat.completion.chunk",
				"created": created,
				"model":   model,
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": ternaryKiroFinish(hasToolCalls)}},
			}
			if usage != nil {
				final["usage"] = usage
			}
			emit(final)
		}
	})
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
}

func kiroEventJSONToOpenAI(w http.ResponseWriter, r io.Reader, model string) {
	var content strings.Builder
	var reasoning strings.Builder
	var toolCalls []any
	var usage map[string]any
	_ = parseKiroEventStream(r, func(eventType string, payload map[string]any) {
		switch eventType {
		case "assistantResponseEvent", "codeEvent":
			content.WriteString(stringValue(payload["content"]))
		case "reasoningContentEvent":
			reasoning.WriteString(firstNonEmpty(stringValue(payload["text"]), stringValue(payload["content"])))
		case "toolUseEvent":
			toolCalls = append(toolCalls, map[string]any{
				"id":   firstNonEmpty(stringValue(payload["toolUseId"]), "call_"+uuidV4()),
				"type": "function",
				"function": map[string]any{
					"name":      stringValue(payload["name"]),
					"arguments": string(mustJSON(payload["input"])),
				},
			})
		case "metricsEvent":
			in := intValue(payload["inputTokens"])
			out := intValue(payload["outputTokens"])
			if in > 0 || out > 0 {
				usage = map[string]any{
					"prompt_tokens":     in,
					"completion_tokens": out,
					"total_tokens":      in + out,
				}
			}
		}
	})
	message := map[string]any{"role": "assistant", "content": content.String()}
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String()
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      "chatcmpl-" + uuidV4(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": ternaryKiroFinish(len(toolCalls) > 0),
		}},
		"usage": usage,
	})
}

func parseKiroEventStream(r io.Reader, onEvent func(eventType string, payload map[string]any)) error {
	buf := make([]byte, 0, 64*1024)
	tmp := make([]byte, 32*1024)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			for {
				if len(buf) < 16 {
					break
				}
				totalLength := int(binary.BigEndian.Uint32(buf[0:4]))
				if totalLength < 16 || totalLength > len(buf) {
					break
				}
				frame := make([]byte, totalLength)
				copy(frame, buf[:totalLength])
				buf = buf[totalLength:]
				eventType, payload, ok := parseKiroFrame(frame)
				if ok {
					onEvent(eventType, payload)
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func parseKiroFrame(data []byte) (string, map[string]any, bool) {
	if len(data) < 16 {
		return "", nil, false
	}
	headersLen := int(binary.BigEndian.Uint32(data[4:8]))
	offset := 12
	headerEnd := 12 + headersLen
	headers := map[string]string{}
	for offset < headerEnd && offset < len(data) {
		nameLen := int(data[offset])
		offset++
		if offset+nameLen > len(data) {
			return "", nil, false
		}
		name := string(data[offset : offset+nameLen])
		offset += nameLen
		if offset >= len(data) {
			return "", nil, false
		}
		headerType := data[offset]
		offset++
		if headerType != 7 || offset+2 > len(data) {
			return "", nil, false
		}
		valLen := int(binary.BigEndian.Uint16(data[offset : offset+2]))
		offset += 2
		if offset+valLen > len(data) {
			return "", nil, false
		}
		headers[name] = string(data[offset : offset+valLen])
		offset += valLen
	}
	payloadStart := 12 + headersLen
	payloadEnd := len(data) - 4
	if payloadStart >= payloadEnd || payloadStart >= len(data) {
		return headers[":event-type"], nil, false
	}
	text := strings.TrimSpace(string(data[payloadStart:payloadEnd]))
	if text == "" {
		return headers[":event-type"], nil, false
	}
	var payload map[string]any
	if json.Unmarshal([]byte(text), &payload) != nil {
		return headers[":event-type"], map[string]any{"content": text}, true
	}
	return headers[":event-type"], normalizeKiroEventPayload(headers[":event-type"], payload), true
}

func normalizeKiroEventPayload(eventType string, payload map[string]any) map[string]any {
	switch eventType {
	case "assistantResponseEvent", "codeEvent", "contextUsageEvent", "messageStopEvent", "meteringEvent", "metricsEvent":
		if inner, ok := payload[eventType].(map[string]any); ok {
			return inner
		}
	case "reasoningContentEvent":
		if inner, ok := payload["reasoningContentEvent"].(map[string]any); ok {
			return inner
		}
	case "toolUseEvent":
		if inner, ok := payload["toolUseEvent"].(map[string]any); ok {
			return inner
		}
	}
	return payload
}

func buildKiroFingerprintHeaders(p ProviderConfig) map[string]string {
	seed := firstNonEmpty(p.ProviderSpecificData["clientId"], p.RefreshToken, p.ProviderSpecificData["profileArn"], p.AccessToken, "kiro-anonymous")
	sum := sha256.Sum256([]byte(seed))
	machineID := hex.EncodeToString(sum[:])
	userAgent := fmt.Sprintf("aws-sdk-js/%s ua/2.1 os/%s#%s lang/js md/nodejs#%s api/codewhispererruntime#%s m/N,E KiroIDE-%s-%s", kiroRuntimeSDKVersion, kiroAgentOS, kiroAgentOSVersion, kiroNodeVersion, kiroRuntimeSDKVersion, kiroVersion, machineID)
	return map[string]string{
		"User-Agent":                  userAgent,
		"x-amz-user-agent":            fmt.Sprintf("aws-sdk-js/%s KiroIDE-%s-%s", kiroRuntimeSDKVersion, kiroVersion, machineID),
		"x-amzn-kiro-agent-mode":      "vibe",
		"x-amzn-codewhisperer-optout": "true",
		"amz-sdk-request":             "attempt=1; max=1",
		"amz-sdk-invocation-id":       uuidV4(),
		"Accept":                      "application/json",
	}
}

func kiroCacheKey(p ProviderConfig) string {
	seed := firstNonEmpty(p.ProviderSpecificData["profileArn"], p.ProviderSpecificData["clientId"], p.RefreshToken, p.AccessToken, "anonymous")
	sum := sha256.Sum256([]byte("kiro:" + seed))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func kiroRegionFromProfileArn(profileArn string) string {
	parts := strings.Split(profileArn, ":")
	if len(parts) >= 4 && strings.TrimSpace(parts[3]) != "" {
		return strings.TrimSpace(parts[3])
	}
	return kiroDefaultRegion
}

func kiroDefaultProfileArn(authMethod string) string {
	if authMethod == "google" || authMethod == "github" {
		return kiroDefaultProfileArns["social"]
	}
	return kiroDefaultProfileArns["builder-id"]
}

func extractEmailFromJWT(token string) string {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var raw map[string]any
	if json.Unmarshal(payload, &raw) != nil {
		return ""
	}
	return firstNonEmpty(stringValue(raw["email"]), stringValue(raw["preferred_username"]), stringValue(raw["sub"]))
}

func ternaryKiroFinish(hasToolCalls bool) string {
	if hasToolCalls {
		return "tool_calls"
	}
	return "stop"
}
