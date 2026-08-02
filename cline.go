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
	"strings"
	"time"
)

const (
	clineAuthorizeURL     = "https://api.cline.bot/api/v1/auth/authorize"
	clineTokenExchangeURL = "https://api.cline.bot/api/v1/auth/token"
	clineRefreshURL       = "https://api.cline.bot/api/v1/auth/refresh"
	clineChatURL          = "https://api.cline.bot/api/v1/chat/completions"
)

var clineFreeModels = []string{
	"deepseek/deepseek-v4-flash",
	"cline-free/glm-5.2",
	"stepfun/step-3.7-flash",
}

func withClineFreeModels(models []string) []string {
	merged := append([]string(nil), clineFreeModels...)
	merged = append(merged, models...)
	return uniqueStrings(merged)
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
	redirectURI := publicURL(r, "/api/oauth/cline/callback")
	params := url.Values{}
	params.Set("client_type", "extension")
	params.Set("callback_url", redirectURI)
	params.Set("redirect_uri", redirectURI)
	writeJSON(w, http.StatusOK, map[string]any{
		"authUrl":     clineAuthorizeURL + "?" + params.Encode(),
		"redirectUri": redirectURI,
	})
}

func (s *Server) handleClineCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}
	redirectURI := publicURL(r, "/api/oauth/cline/callback")
	tokens, err := s.exchangeClineToken(r, code, redirectURI)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	p, _ := s.providerByID("cline")
	if p.ID == "" {
		p = ProviderConfig{ID: "cline", Name: "Cline OAuth", Type: "cline", BaseURL: clineChatURL}
	}
	p.Type = "cline"
	p.Enabled = true
	p.BaseURL = firstNonEmpty(p.BaseURL, clineChatURL)
	account := ClineAccount{
		AccessToken:  firstNonEmpty(stringValue(tokens["access_token"]), stringValue(tokens["accessToken"])),
		RefreshToken: firstNonEmpty(stringValue(tokens["refresh_token"]), stringValue(tokens["refreshToken"])),
		Email:        strings.TrimSpace(stringValue(tokens["email"])),
	}
	if account.AccessToken == "" {
		http.Error(w, "cline token exchange returned no access token", http.StatusBadGateway)
		return
	}
	if p.ProviderSpecificData == nil {
		p.ProviderSpecificData = map[string]string{}
	}
	p.ProviderSpecificData["firstName"] = stringValue(tokens["firstName"])
	p.ProviderSpecificData["lastName"] = stringValue(tokens["lastName"])
	if expiresAt := firstNonEmpty(stringValue(tokens["expires_at"]), stringValue(tokens["expiresAt"])); expiresAt != "" {
		if t, err := time.Parse(time.RFC3339, expiresAt); err == nil {
			account.ExpiresIn = int64(time.Until(t).Seconds())
		}
	}
	if account.ExpiresIn <= 0 {
		account.ExpiresIn = 3600
	}
	p, _ = upsertClineAccount(p, account)
	if len(p.Models) == 0 {
		p.Models = []string{
			"anthropic/claude-opus-4.7",
			"anthropic/claude-sonnet-4.6",
			"anthropic/claude-opus-4.6",
			"openai/gpt-5.3-codex",
			"openai/gpt-5.4",
			"google/gemini-3.1-pro-preview",
			"google/gemini-3.1-flash-lite-preview",
			"kwaipilot/kat-coder-pro",
		}
	}
	p.Models = withClineFreeModels(p.Models)
	if err := s.updateProvider(p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	message := fmt.Sprintf("Cline connected: %s. %d account(s) saved.", htmlEscape(account.Email), len(p.ClineAccounts))
	_, _ = w.Write([]byte("<!doctype html><meta charset=\"utf-8\"><title>Cline Connected</title><p>" + message + "</p><script>setTimeout(()=>window.close(),1200)</script>"))
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
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "cline is not logged in"})
		return
	}
	body, err := replaceModel(req.Raw, upstreamModel)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	resp, _, err := s.doClineRequest(r.Context(), p, req.Stream, body, true)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		s.markProviderAuthState(p.ID, "needs_login", fmt.Sprintf("Cline returned %d; please login again", resp.StatusCode))
	} else if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		s.markProviderAuthState(p.ID, "ok", "")
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
	order := clineAccountOrder(p)
	if len(order) == 0 {
		return nil, p, errors.New("Cline has no logged-in accounts")
	}
	for position, accountIndex := range order {
		resp, updated, authFailure, err := s.doClineAccountRequest(ctx, p, accountIndex, stream, body, allowRefresh)
		p = updated
		if err != nil {
			if authFailure && position < len(order)-1 {
				continue
			}
			return nil, p, err
		}
		if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
			p = s.activateClineAccount(p, accountIndex)
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
		return s.doClineAccountRequest(ctx, refreshed, accountIndex, stream, body, false)
	}
	return resp, p, resp.StatusCode == http.StatusUnauthorized, nil
}

func (s *Server) activateClineAccount(p ProviderConfig, accountIndex int) ProviderConfig {
	accounts := clineProviderAccounts(p)
	if accountIndex < 0 || accountIndex >= len(accounts) {
		return p
	}
	changed := p.ActiveClineAccount != accountIndex || len(p.ClineAccounts) != len(accounts)
	p.ClineAccounts = accounts
	p.ActiveClineAccount = accountIndex
	syncClineLegacyAccount(&p)
	if changed {
		_ = s.updateProvider(p)
	}
	return p
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
	if strings.TrimSpace(account.RefreshToken) == "" {
		return p, errors.New("Cline refresh token is empty; please login again")
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
	accounts[accountIndex] = account
	p.ClineAccounts = accounts
	syncClineLegacyAccount(&p)
	if p.ProviderSpecificData == nil {
		p.ProviderSpecificData = map[string]string{}
	}
	p.ProviderSpecificData["authStatus"] = "ok"
	delete(p.ProviderSpecificData, "lastAuthError")
	if err := s.updateProvider(p); err != nil {
		return p, err
	}
	return p, nil
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
