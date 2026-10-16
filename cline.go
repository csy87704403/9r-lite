package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	clineAuthorizeURL     = "https://api.cline.bot/api/v1/auth/authorize"
	clineTokenExchangeURL = "https://api.cline.bot/api/v1/auth/token"
	clineRefreshURL       = "https://api.cline.bot/api/v1/auth/refresh"
	clineChatURL          = "https://api.cline.bot/api/v1/chat/completions"
)

const clineRecommendedModelsURL = "https://api.cline.bot/api/v1/ai/cline/recommended-models"

var errClineLoginRequired = errors.New("Cline 登录已失效，请重新登录")

type clineRefreshCall struct {
	done    chan struct{}
	err     error
	account ClineAccount
}

func fetchClineFreeModels(ctx context.Context, client *http.Client) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clineRecommendedModelsURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("拉取 Cline 免费模型失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("拉取 Cline 免费模型失败: HTTP %d", resp.StatusCode)
	}
	var feed struct {
		Free []struct {
			ID string `json:"id"`
		} `json:"free"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&feed); err != nil {
		return nil, fmt.Errorf("解析 Cline 免费模型失败: %w", err)
	}
	var ids []string
	for _, model := range feed.Free {
		if id := strings.TrimSpace(model.ID); id != "" {
			ids = append(ids, id)
		}
	}
	ids = uniqueStrings(ids)
	if len(ids) == 0 {
		return nil, errors.New("Cline 未返回免费模型，已保留原模型列表")
	}
	return ids, nil
}

func clineProviderAccounts(p ProviderConfig) []ClineAccount {
	accounts := make([]ClineAccount, 0, len(p.ClineAccounts)+1)
	for _, account := range p.ClineAccounts {
		account.Email = strings.TrimSpace(account.Email)
		account.AccessToken = strings.TrimSpace(account.AccessToken)
		account.RefreshToken = strings.TrimSpace(account.RefreshToken)
		if account.AccessToken != "" || account.RefreshToken != "" {
			accounts = append(accounts, account)
		}
	}
	if len(accounts) == 0 && (strings.TrimSpace(p.AccessToken) != "" || strings.TrimSpace(p.RefreshToken) != "") {
		accounts = append(accounts, ClineAccount{
			Email:        strings.TrimSpace(p.Email),
			AccessToken:  strings.TrimSpace(p.AccessToken),
			RefreshToken: strings.TrimSpace(p.RefreshToken),
			ExpiresIn:    p.ExpiresIn,
		})
	}
	return accounts
}

func syncClineLegacyAccount(p *ProviderConfig) {
	if p == nil {
		return
	}
	accounts := clineProviderAccounts(*p)
	if len(accounts) == 0 {
		p.ClineAccounts = nil
		p.ActiveClineAccount = 0
		return
	}
	if p.ActiveClineAccount < 0 || p.ActiveClineAccount >= len(accounts) {
		p.ActiveClineAccount = 0
	}
	p.ClineAccounts = accounts
	active := accounts[p.ActiveClineAccount]
	p.AccessToken = active.AccessToken
	p.RefreshToken = active.RefreshToken
	p.Email = active.Email
	p.ExpiresIn = active.ExpiresIn
}

func migrateClineProviderAccounts(p *ProviderConfig) {
	if p == nil || p.Type != "cline" {
		return
	}
	syncClineLegacyAccount(p)
}

func upsertClineAccount(p ProviderConfig, account ClineAccount) (ProviderConfig, int) {
	accounts := clineProviderAccounts(p)
	match := -1
	for i, existing := range accounts {
		if account.Email != "" && strings.EqualFold(existing.Email, account.Email) {
			match = i
			break
		}
	}
	if match < 0 {
		accounts = append(accounts, account)
		match = len(accounts) - 1
	} else {
		if account.AccessToken == "" {
			account.AccessToken = accounts[match].AccessToken
		}
		if account.RefreshToken == "" {
			account.RefreshToken = accounts[match].RefreshToken
		}
		if account.Email == "" {
			account.Email = accounts[match].Email
		}
		accounts[match] = account
	}
	p.ClineAccounts = accounts
	p.ActiveClineAccount = match
	syncClineLegacyAccount(&p)
	return p, match
}

func clineAccountOrder(p ProviderConfig) []int {
	accounts := clineProviderAccounts(p)
	if len(accounts) == 0 {
		return nil
	}
	active := p.ActiveClineAccount
	if active < 0 || active >= len(accounts) {
		active = 0
	}
	order := make([]int, 0, len(accounts))
	for offset := 0; offset < len(accounts); offset++ {
		order = append(order, (active+offset)%len(accounts))
	}
	return order
}

func (s *Server) handleClineAuthorize(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	state, redirectURI := s.beginClineLogin(r)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	params := url.Values{}
	params.Set("state", state)
	params.Set("client_type", "extension")
	params.Set("callback_url", redirectURI)
	params.Set("redirect_uri", redirectURI)
	writeJSON(w, http.StatusOK, map[string]any{
		"authUrl":     clineAuthorizeURL + "?" + params.Encode(),
		"redirectUri": redirectURI,
	})
}

func (s *Server) handleClineCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}
	redirectURI, err := s.consumeClineLogin(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	tokens, err := s.exchangeClineToken(r, code, redirectURI)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	account := ClineAccount{
		AccessToken:  firstNonEmpty(stringValue(tokens["access_token"]), stringValue(tokens["accessToken"])),
		RefreshToken: firstNonEmpty(stringValue(tokens["refresh_token"]), stringValue(tokens["refreshToken"])),
		Email:        strings.TrimSpace(stringValue(tokens["email"])),
	}
	if account.AccessToken == "" {
		http.Error(w, "cline token exchange returned no access token", http.StatusBadGateway)
		return
	}
	if expiresAt := firstNonEmpty(stringValue(tokens["expires_at"]), stringValue(tokens["expiresAt"])); expiresAt != "" {
		if t, err := time.Parse(time.RFC3339, expiresAt); err == nil {
			account.ExpiresIn = int64(time.Until(t).Seconds())
		}
	}
	if account.ExpiresIn <= 0 {
		account.ExpiresIn = 3600
	}
	p, err := s.saveClineLogin(account, tokens)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	message := fmt.Sprintf("Cline connected: %s. %d account(s) saved.", htmlEscape(account.Email), len(p.ClineAccounts))
	_, _ = w.Write([]byte("<!doctype html><meta charset=\"utf-8\"><title>Cline Connected</title><p>" + message + "</p><script>setTimeout(()=>window.close(),1200)</script>"))
}

func (s *Server) saveClineLogin(account ClineAccount, tokens map[string]any) (ProviderConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	p := ProviderConfig{ID: "cline", Name: "Cline OAuth", Type: "cline", BaseURL: clineChatURL}
	for i, stored := range s.config.Providers {
		if stored.ID == "cline" {
			index, p = i, cloneProviderConfig(stored)
			break
		}
	}
	p.Type, p.Enabled = "cline", true
	p.BaseURL = firstNonEmpty(p.BaseURL, clineChatURL)
	p, _ = upsertClineAccount(p, account)
	if p.ProviderSpecificData == nil {
		p.ProviderSpecificData = map[string]string{}
	}
	p.ProviderSpecificData["firstName"] = stringValue(tokens["firstName"])
	p.ProviderSpecificData["lastName"] = stringValue(tokens["lastName"])
	p.ProviderSpecificData["authStatus"] = "ok"
	delete(p.ProviderSpecificData, "lastAuthError")
	if len(p.Models) == 0 && (p.ClineModelSync == nil || p.ClineModelSync.LastRunAt == 0) {
		p.Models = []string{"anthropic/claude-opus-4.7", "anthropic/claude-sonnet-4.6", "anthropic/claude-opus-4.6", "openai/gpt-5.3-codex", "openai/gpt-5.4", "google/gemini-3.1-pro-preview", "google/gemini-3.1-flash-lite-preview", "kwaipilot/kat-coder-pro"}
	}
	if index < 0 {
		s.config.Providers = append(s.config.Providers, p)
	} else {
		s.config.Providers[index] = p
	}
	pruneAutoModelsForProviderLocked(&s.config, p)
	return cloneProviderConfig(p), saveConfig(s.dataDir, s.config)
}

func (s *Server) exchangeClineToken(r *http.Request, code, redirectURI string) (map[string]any, error) {
	if embedded, err := decodeClineEmbeddedToken(code); err == nil {
		return map[string]any{
			"access_token":  embedded["accessToken"],
			"refresh_token": embedded["refreshToken"],
			"email":         embedded["email"],
			"firstName":     embedded["firstName"],
			"lastName":      embedded["lastName"],
			"expires_at":    embedded["expiresAt"],
		}, nil
	}
	body := map[string]any{
		"grant_type":   "authorization_code",
		"code":         code,
		"client_type":  "extension",
		"redirect_uri": redirectURI,
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, clineTokenExchangeURL, bytes.NewReader(mustJSON(body)))
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
		return nil, fmt.Errorf("Cline token exchange failed: %d %s", resp.StatusCode, truncateString(string(b), 200))
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	data, _ := raw["data"].(map[string]any)
	if data == nil {
		data = raw
	}
	userInfo, _ := data["userInfo"].(map[string]any)
	return map[string]any{
		"access_token":  firstNonEmpty(stringValue(data["accessToken"]), stringValue(raw["accessToken"])),
		"refresh_token": firstNonEmpty(stringValue(data["refreshToken"]), stringValue(raw["refreshToken"])),
		"email":         firstNonEmpty(stringValue(userInfo["email"]), stringValue(data["email"]), stringValue(raw["email"])),
		"expires_at":    firstNonEmpty(stringValue(data["expiresAt"]), stringValue(raw["expiresAt"])),
	}, nil
}

func (s *Server) proxyCline(w http.ResponseWriter, r *http.Request, p ProviderConfig, req chatRequest, upstreamModel string) {
	if len(clineProviderAccounts(p)) == 0 {
		s.markProviderAuthState(p.ID, "needs_login", errClineLoginRequired.Error())
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": errClineLoginRequired.Error()})
		return
	}
	body, err := replaceModel(req.Raw, upstreamModel)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	resp, _, err := s.doClineRequest(r.Context(), p, req.Stream, body, true)
	if err != nil {
		if errors.Is(err, errClineLoginRequired) {
			s.markProviderAuthState(p.ID, "needs_login", errClineLoginRequired.Error())
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": errClineLoginRequired.Error()})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		s.markProviderAuthState(p.ID, "needs_login", errClineLoginRequired.Error())
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": errClineLoginRequired.Error()})
		return
	} else if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		s.markProviderAuthState(p.ID, "ok", "")
	}
	if !req.Stream && resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		responseBody, err := io.ReadAll(resp.Body)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		// Cline wraps JSON completions in data; SSE must remain untouched.
		var envelope struct {
			Data    json.RawMessage `json:"data"`
			Success *bool           `json:"success"`
		}
		if json.Unmarshal(responseBody, &envelope) == nil && (envelope.Success == nil || *envelope.Success) {
			var completion struct {
				Choices json.RawMessage `json:"choices"`
			}
			if json.Unmarshal(envelope.Data, &completion) == nil && len(completion.Choices) > 0 {
				responseBody = envelope.Data
				resp.Header.Del("Content-Length")
			}
		}
		resp.Body = io.NopCloser(bytes.NewReader(responseBody))
	}

	for k, values := range resp.Header {
		if shouldSkipHeader(k) {
			continue
		}
		for _, v := range values {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = copyFlush(w, resp.Body)
}

func (s *Server) doClineRequest(ctx context.Context, p ProviderConfig, stream bool, body []byte, allowRefresh bool) (*http.Response, ProviderConfig, error) {
	if latest, ok := s.providerByID(p.ID); ok {
		p = latest
	}
	order := clineAccountOrder(p)
	if len(order) == 0 {
		return nil, p, errors.New("Cline has no logged-in accounts")
	}
	// Keep the account identities stable while other requests change the active slot.
	attempts := p
	for position, accountIndex := range order {
		resp, updated, authFailure, err := s.doClineAccountRequest(ctx, attempts, accountIndex, stream, body, allowRefresh)
		p = updated
		if err != nil {
			if authFailure && position < len(order)-1 {
				continue
			}
			return nil, p, err
		}
		if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
			p = s.activateClineAccount(attempts, accountIndex)
			return resp, p, nil
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(responseBody))
		if readErr != nil {
			return nil, p, readErr
		}
		shouldRotate := authFailure || isQuotaKeyError(resp.StatusCode, responseBody) || isRateLimitKeyError(resp.StatusCode, responseBody)
		if shouldRotate && position < len(order)-1 {
			_ = resp.Body.Close()
			continue
		}
		return resp, p, nil
	}
	return nil, p, errors.New("all Cline accounts failed")
}

func (s *Server) doClineAccountRequest(ctx context.Context, p ProviderConfig, accountIndex int, stream bool, body []byte, allowRefresh bool) (*http.Response, ProviderConfig, bool, error) {
	p, accountIndex, err := s.latestClineAccount(p, accountIndex)
	if err != nil {
		return nil, p, true, err
	}
	accounts := clineProviderAccounts(p)
	if accountIndex < 0 || accountIndex >= len(accounts) {
		return nil, p, true, errors.New("Cline account index is invalid")
	}
	account := accounts[accountIndex]
	headers := map[string]string{
		"Content-Type":       "application/json",
		"Authorization":      clineAuthorization(account.AccessToken),
		"Accept":             acceptHeader(stream),
		"HTTP-Referer":       "https://cline.bot",
		"X-Title":            "Cline",
		"User-Agent":         "9RouterLite/0.1",
		"X-PLATFORM":         "windows",
		"X-PLATFORM-VERSION": "go",
		"X-CLIENT-TYPE":      "9router-lite",
		"X-CLIENT-VERSION":   "0.1",
		"X-CORE-VERSION":     "0.1",
		"X-IS-MULTIROOT":     "false",
		"X-Task-ID":          newSessionID(),
	}
	target := firstNonEmpty(p.BaseURL, clineChatURL)
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, p, false, err
	}
	for k, v := range headers {
		upReq.Header.Set(k, v)
	}
	resp, err := s.client.Do(upReq)
	if err != nil {
		return nil, p, false, err
	}
	if resp.StatusCode == http.StatusUnauthorized && allowRefresh {
		_ = resp.Body.Close()
		refreshed, rerr := s.refreshClineAccount(ctx, p, accountIndex)
		if rerr != nil {
			return nil, p, true, rerr
		}
		refreshedIndex := findClineAccount(clineProviderAccounts(refreshed), account, accountIndex)
		return s.doClineAccountRequest(ctx, refreshed, refreshedIndex, stream, body, false)
	}
	return resp, p, resp.StatusCode == http.StatusUnauthorized, nil
}

func (s *Server) activateClineAccount(p ProviderConfig, accountIndex int) ProviderConfig {
	accounts := clineProviderAccounts(p)
	if accountIndex < 0 || accountIndex >= len(accounts) {
		return p
	}
	selected := accounts[accountIndex]
	updated, err := s.mutateProvider(p.ID, func(latest *ProviderConfig) error {
		index := findClineAccount(clineProviderAccounts(*latest), selected, accountIndex)
		if index < 0 {
			return errors.New("Cline account was removed")
		}
		latest.ActiveClineAccount = index
		syncClineLegacyAccount(latest)
		return nil
	})
	if err != nil {
		return p
	}
	return updated
}

func findClineAccount(accounts []ClineAccount, wanted ClineAccount, fallback int) int {
	for i, account := range accounts {
		if wanted.Email != "" && strings.EqualFold(account.Email, wanted.Email) {
			return i
		}
		if wanted.Email == "" && ((wanted.RefreshToken != "" && account.RefreshToken == wanted.RefreshToken) || (wanted.AccessToken != "" && account.AccessToken == wanted.AccessToken)) {
			return i
		}
	}
	// Legacy imports may have no email; their stable array slot is the identity.
	if wanted.Email == "" && fallback >= 0 && fallback < len(accounts) && accounts[fallback].Email == "" {
		return fallback
	}
	return -1
}

func (s *Server) latestClineAccount(p ProviderConfig, accountIndex int) (ProviderConfig, int, error) {
	accounts := clineProviderAccounts(p)
	if accountIndex < 0 || accountIndex >= len(accounts) {
		return p, accountIndex, errors.New("Cline account index is invalid")
	}
	latest, exists := s.providerByID(p.ID)
	if !exists {
		if p.ID == "" { // Standalone, unpersisted provider used by protocol helpers.
			return p, accountIndex, nil
		}
		return p, accountIndex, errors.New("Cline provider was removed")
	}
	index := findClineAccount(clineProviderAccounts(latest), accounts[accountIndex], accountIndex)
	if index < 0 {
		return latest, accountIndex, errors.New("Cline account was removed")
	}
	return latest, index, nil
}

func (s *Server) refreshClineProvider(ctx context.Context, p ProviderConfig) (ProviderConfig, error) {
	accounts := clineProviderAccounts(p)
	if len(accounts) == 0 {
		return p, errors.New("Cline has no logged-in accounts")
	}
	index := p.ActiveClineAccount
	if index < 0 || index >= len(accounts) {
		index = 0
	}
	return s.refreshClineAccount(ctx, p, index)
}

func (s *Server) refreshClineAccount(ctx context.Context, p ProviderConfig, accountIndex int) (ProviderConfig, error) {
	accounts := clineProviderAccounts(p)
	if accountIndex < 0 || accountIndex >= len(accounts) {
		return p, errors.New("Cline account index is invalid")
	}
	account := accounts[accountIndex]
	identity := strings.ToLower(account.Email)
	if identity == "" {
		identity = "slot:" + strconv.Itoa(accountIndex)
	}
	key := p.ID + "|" + identity
	s.clineRefreshMu.Lock()
	if call := s.clineRefreshes[key]; call != nil {
		s.clineRefreshMu.Unlock()
		select {
		case <-ctx.Done():
			return p, ctx.Err()
		case <-call.done:
		}
		latest, index, err := s.latestClineAccount(p, accountIndex)
		if err != nil {
			return latest, err
		}
		current := clineProviderAccounts(latest)[index]
		if current.AccessToken != account.AccessToken || current.RefreshToken != account.RefreshToken {
			return latest, nil
		}
		if call.err == nil && (call.account.AccessToken != account.AccessToken || call.account.RefreshToken != account.RefreshToken) {
			return s.refreshClineAccount(ctx, latest, index)
		}
		if call.err != nil && ctx.Err() == nil && (errors.Is(call.err, context.Canceled) || errors.Is(call.err, context.DeadlineExceeded)) {
			return s.refreshClineAccount(ctx, p, accountIndex)
		}
		return latest, call.err
	}
	if s.clineRefreshes == nil {
		s.clineRefreshes = map[string]*clineRefreshCall{}
	}
	call := &clineRefreshCall{done: make(chan struct{}), account: account}
	s.clineRefreshes[key] = call
	s.clineRefreshMu.Unlock()
	updated, err := s.refreshClineAccountOnce(ctx, p, accountIndex)
	if err != nil {
		if latest, index, latestErr := s.latestClineAccount(p, accountIndex); latestErr == nil {
			current := clineProviderAccounts(latest)[index]
			if current.AccessToken != account.AccessToken || current.RefreshToken != account.RefreshToken {
				updated, err = latest, nil
			}
		}
	}
	s.clineRefreshMu.Lock()
	call.err = err
	delete(s.clineRefreshes, key)
	close(call.done)
	s.clineRefreshMu.Unlock()
	return updated, err
}

func (s *Server) refreshClineAccountOnce(ctx context.Context, p ProviderConfig, accountIndex int) (ProviderConfig, error) {
	original := clineProviderAccounts(p)[accountIndex]
	latest, index, err := s.latestClineAccount(p, accountIndex)
	if err != nil {
		return p, err
	}
	p, accountIndex = latest, index
	account := clineProviderAccounts(p)[accountIndex]
	if account.AccessToken != original.AccessToken || account.RefreshToken != original.RefreshToken {
		return p, nil // A queued request already refreshed this exact account.
	}
	if strings.TrimSpace(account.RefreshToken) == "" {
		return p, fmt.Errorf("%w: refresh token is empty", errClineLoginRequired)
	}
	reqBody := map[string]any{
		"refreshToken": account.RefreshToken,
		"grantType":    "refresh_token",
		"clientType":   "extension",
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, clineRefreshURL, bytes.NewReader(mustJSON(reqBody)))
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
		lower := strings.ToLower(string(b))
		if resp.StatusCode == http.StatusUnauthorized || (resp.StatusCode == http.StatusBadRequest && (strings.Contains(lower, "invalid_grant") || strings.Contains(lower, "invalid_refresh_token"))) {
			return p, fmt.Errorf("%w: refresh token rejected (HTTP %d)", errClineLoginRequired, resp.StatusCode)
		}
		return p, fmt.Errorf("Cline token refresh failed: %d %s", resp.StatusCode, truncateString(string(b), 240))
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return p, err
	}
	data, _ := raw["data"].(map[string]any)
	if data == nil {
		data = raw
	}
	accessToken := stringValue(data["accessToken"])
	if accessToken == "" {
		return p, errors.New("Cline token refresh returned no access token")
	}
	account.AccessToken = accessToken
	if refreshToken := stringValue(data["refreshToken"]); refreshToken != "" {
		account.RefreshToken = refreshToken
	}
	if expiresAt := stringValue(data["expiresAt"]); expiresAt != "" {
		if t, err := time.Parse(time.RFC3339, expiresAt); err == nil {
			account.ExpiresIn = int64(time.Until(t).Seconds())
		}
	}
	if account.ExpiresIn <= 0 {
		account.ExpiresIn = 3600
	}
	return s.mutateProvider(p.ID, func(current *ProviderConfig) error {
		accounts := clineProviderAccounts(*current)
		index := findClineAccount(accounts, original, accountIndex)
		if index < 0 {
			return errors.New("Cline account was removed during refresh")
		}
		// A new OAuth login must win over an older in-flight refresh response.
		if accounts[index].AccessToken != original.AccessToken || accounts[index].RefreshToken != original.RefreshToken {
			return nil
		}
		accounts[index] = account
		current.ClineAccounts = accounts
		syncClineLegacyAccount(current)
		if current.ProviderSpecificData == nil {
			current.ProviderSpecificData = map[string]string{}
		}
		current.ProviderSpecificData["authStatus"] = "ok"
		delete(current.ProviderSpecificData, "lastAuthError")
		return nil
	})
}

func decodeClineEmbeddedToken(code string) (map[string]any, error) {
	code = strings.TrimSpace(code)
	padding := 4 - (len(code) % 4)
	if padding != 4 {
		code += strings.Repeat("=", padding)
	}
	decoded, err := base64.StdEncoding.DecodeString(code)
	if err != nil {
		return nil, err
	}
	text := string(decoded)
	lastBrace := strings.LastIndex(text, "}")
	if lastBrace == -1 {
		return nil, errors.New("no JSON found in decoded Cline code")
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(text[:lastBrace+1]), &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func clineAuthorization(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if !strings.HasPrefix(token, "workos:") {
		token = "workos:" + token
	}
	return "Bearer " + token
}

func publicURL(r *http.Request, suffix string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}
	host := r.Host
	if forwarded := r.Header.Get("X-Forwarded-Host"); forwarded != "" {
		host = forwarded
	}
	return scheme + "://" + host + suffix
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
