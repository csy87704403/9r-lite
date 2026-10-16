package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func securityTestServer(t *testing.T) *Server {
	t.Helper()
	return &Server{dataDir: t.TempDir(), adminSecret: "test-admin-session", config: mergeDefaultConfig(Config{AccessKey: "gateway", Providers: []ProviderConfig{{
		ID: "cline", Type: "cline", Enabled: true, Models: []string{"model"}, EnabledModels: []string{"model"},
		ClineAccounts: []ClineAccount{{Email: "owner@example.invalid", AccessToken: "old", RefreshToken: "old-refresh"}},
	}}})}
}

func securityAuthorize(t *testing.T, s *Server) *url.URL {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/oauth/cline/authorize", nil)
	r.AddCookie(&http.Cookie{Name: "nr_admin", Value: s.adminCookieValue()})
	w := httptest.NewRecorder()
	s.handleClineAuthorize(w, r)
	var result struct {
		AuthURL string `json:"authUrl"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatalf("authorize: %d %s", w.Code, w.Body.String())
	}
	auth, err := url.Parse(result.AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	callback, err := url.Parse(auth.Query().Get("callback_url"))
	if err != nil || callback.Query().Get("login_state") == "" || auth.Query().Get("state") == "" {
		t.Fatal("authorization did not carry a callback state")
	}
	q := callback.Query()
	q.Set("code", base64.StdEncoding.EncodeToString(mustJSON(map[string]any{"email": "new@example.invalid", "accessToken": "new", "refreshToken": "new-refresh"})))
	callback.RawQuery = q.Encode()
	return callback
}

func TestClineCallbackRequiresAdminAndOneTimeState(t *testing.T) {
	s := securityTestServer(t)
	callback := securityAuthorize(t, s)
	call := func(withAdmin bool) int {
		r := httptest.NewRequest(http.MethodGet, callback.String(), nil)
		if withAdmin {
			r.AddCookie(&http.Cookie{Name: "nr_admin", Value: s.adminCookieValue()})
		}
		w := httptest.NewRecorder()
		s.handleClineCallback(w, r)
		return w.Code
	}
	if got := call(false); got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated callback=%d", got)
	}
	p, _ := s.providerByID("cline")
	if len(p.ClineAccounts) != 1 {
		t.Fatal("unauthenticated callback mutated accounts")
	}
	if got := call(true); got != http.StatusOK {
		t.Fatalf("legitimate callback=%d", got)
	}
	if got := call(true); got != http.StatusForbidden {
		t.Fatalf("replayed callback=%d", got)
	}
	p, _ = s.providerByID("cline")
	if len(p.ClineAccounts) != 2 || p.ActiveClineAccount != 1 {
		t.Fatal("legitimate multi-account login failed")
	}
}

func TestClineCallbackRejectsMissingExpiredAndDifferentSessionState(t *testing.T) {
	for _, mode := range []string{"missing", "expired", "different-session"} {
		t.Run(mode, func(t *testing.T) {
			s := securityTestServer(t)
			callback := securityAuthorize(t, s)
			state := callback.Query().Get("login_state")
			switch mode {
			case "missing":
				q := callback.Query()
				q.Del("login_state")
				callback.RawQuery = q.Encode()
			case "expired":
				attempt := s.clineLogins[state]
				attempt.ExpiresAt = time.Now().Add(-time.Minute)
				s.clineLogins[state] = attempt
			case "different-session":
				s.adminSecret = "changed-admin-session"
			}
			r := httptest.NewRequest(http.MethodGet, callback.String(), nil)
			r.AddCookie(&http.Cookie{Name: "nr_admin", Value: s.adminCookieValue()})
			w := httptest.NewRecorder()
			s.handleClineCallback(w, r)
			p, _ := s.providerByID("cline")
			if w.Code != http.StatusForbidden || len(p.ClineAccounts) != 1 {
				t.Fatalf("invalid state accepted: http=%d", w.Code)
			}
		})
	}
}

func TestClineParallelLoginAttemptsRemainIndependent(t *testing.T) {
	s := securityTestServer(t)
	first, second := securityAuthorize(t, s), securityAuthorize(t, s)
	if first.Query().Get("login_state") == second.Query().Get("login_state") {
		t.Fatal("login states reused")
	}
	for _, callback := range []*url.URL{first, second} {
		r := httptest.NewRequest(http.MethodGet, callback.String(), nil)
		r.AddCookie(&http.Cookie{Name: "nr_admin", Value: s.adminCookieValue()})
		w := httptest.NewRecorder()
		s.handleClineCallback(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("parallel login invalidated: %d", w.Code)
		}
	}
}

func TestConfigVersionProtectsFreshCredentialsAndAccounts(t *testing.T) {
	s := securityTestServer(t)
	stale, oldVersion := s.currentConfig(), configVersion(s.currentConfig())
	_, err := s.saveClineLogin(ClineAccount{Email: "owner@example.invalid", AccessToken: "fresh", RefreshToken: "fresh-refresh"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.saveClineLogin(ClineAccount{Email: "another@example.invalid", AccessToken: "another", RefreshToken: "another-refresh"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"", oldVersion, "*"} {
		r := httptest.NewRequest(http.MethodPost, "/api/config", bytes.NewReader(mustJSON(stale)))
		r.Header.Set("If-Match", version)
		r.AddCookie(&http.Cookie{Name: "nr_admin", Value: s.adminCookieValue()})
		w := httptest.NewRecorder()
		s.handleConfig(w, r)
		p, _ := s.providerByID("cline")
		if w.Code != http.StatusConflict || len(p.ClineAccounts) != 2 || p.ClineAccounts[0].AccessToken != "fresh" {
			t.Fatalf("stale/missing version accepted: %d", w.Code)
		}
	}
	fresh := s.currentConfig()
	fresh.AutoProbeEnabled = true
	r := httptest.NewRequest(http.MethodPost, "/api/config", bytes.NewReader(mustJSON(fresh)))
	r.Header.Set("If-Match", configVersion(s.currentConfig()))
	r.AddCookie(&http.Cookie{Name: "nr_admin", Value: s.adminCookieValue()})
	w := httptest.NewRecorder()
	s.handleConfig(w, r)
	if w.Code != http.StatusOK || w.Header().Get("ETag") != configVersion(s.currentConfig()) {
		t.Fatalf("fresh save failed: %d", w.Code)
	}
}

func TestExplicitConfigImportRemainsAvailable(t *testing.T) {
	s := securityTestServer(t)
	imported := s.currentConfig()
	imported.Providers = append(imported.Providers, ProviderConfig{ID: "custom-import", Name: "Imported", Type: "openai", Enabled: true, BaseURL: "https://example.invalid/v1", APIKey: "test", Models: []string{"new"}})
	r := httptest.NewRequest(http.MethodPost, "/api/config", bytes.NewReader(mustJSON(imported)))
	r.AddCookie(&http.Cookie{Name: "nr_admin", Value: s.adminCookieValue()})
	r.Header.Set("X-9Router-Config-Import", "true")
	w := httptest.NewRecorder()
	s.handleConfig(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("explicit import rejected: %d %s", w.Code, w.Body.String())
	}
	if _, ok := s.providerByID("custom-import"); !ok {
		t.Fatal("import did not save provider")
	}
	html := adminHTMLLiteV2(string(mustJSON(s.currentConfig())))
	for _, marker := range []string{"configRevision=", "'If-Match':expected", "saveConfigObject(cfg,{importConfig:true})", "saveConfigObject(parseConfig())"} {
		if !strings.Contains(html, marker) {
			t.Fatalf("management UI lacks version/import marker %s", marker)
		}
	}
}
