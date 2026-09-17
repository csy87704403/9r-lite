package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type clineCatalogTransport func(*http.Request) (*http.Response, error)

func (f clineCatalogTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFetchClineModelsIncludesFreeModels(t *testing.T) {
	s := &Server{client: &http.Client{Transport: clineCatalogTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != clineRecommendedModelsURL || r.Method != http.MethodGet {
			t.Fatalf("unexpected catalog request: %s %s", r.Method, r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"free":[{"id":"cline-free/deepseek-v4.1-flash"},{"id":" stealth/union-alpha "},{"id":"cline-free/muse-spark-1.3-contributor"},{"id":"z-ai/glm-5.3-flash"},{"id":"cline-free/solar-pro4"},{"id":"poolside/laguna-s-2.1:free"},{"id":"stealth/union-alpha"},{"id":""}],"recommended":[{"id":"paid/model"}],"clinePass":[{"id":"subscription/model"}]}`))}, nil
	})}}
	p := ProviderConfig{Type: "cline", Models: []string{"cline-free/glm-5.2"}}
	models, err := s.fetchProviderModels(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	want := "cline-free/deepseek-v4.1-flash,stealth/union-alpha,cline-free/muse-spark-1.3-contributor,z-ai/glm-5.3-flash,cline-free/solar-pro4,poolside/laguna-s-2.1:free"
	if strings.Join(models, ",") != want {
		t.Fatalf("unexpected free models: %#v", models)
	}
	if strings.Join(s.modelsForProvider(t.Context(), p), ",") != "cline-free/glm-5.2" {
		t.Fatal("cached model lookup must not inject hardcoded models")
	}
}

func TestFetchClineModelsFailurePreservesList(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"empty", `{"free":[]}`, 200},
		{"missing", `{"recommended":[{"id":"paid/model"}]}`, 200},
		{"invalid", `not json`, 200},
		{"rate limited", `{}`, 429},
		{"server error", `{}`, 500},
		{"network failure", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{client: &http.Client{Transport: clineCatalogTransport(func(r *http.Request) (*http.Response, error) {
				if tc.status == 0 {
					return nil, io.ErrUnexpectedEOF
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}}
			p := ProviderConfig{Type: "cline", Models: []string{"existing/model"}}
			if _, err := s.fetchProviderModels(t.Context(), p); err == nil {
				t.Fatal("expected catalog error")
			}
			if strings.Join(p.Models, ",") != "existing/model" {
				t.Fatal("failed fetch changed existing models")
			}
		})
	}
}

func TestClineProbeOmitsMaxTokens(t *testing.T) {
	if got := probeMaxTokens(ProviderConfig{Type: "cline"}); got != 0 {
		t.Fatalf("probeMaxTokens(cline) = %d, want 0", got)
	}
}

func TestUpsertClineAccountPreservesExistingAccounts(t *testing.T) {
	p := ProviderConfig{Type: "cline", Email: "one@example.com", AccessToken: "token-one", RefreshToken: "refresh-one"}
	p, index := upsertClineAccount(p, ClineAccount{Email: "two@example.com", AccessToken: "token-two", RefreshToken: "refresh-two"})
	if index != 1 || len(p.ClineAccounts) != 2 || p.Email != "two@example.com" {
		t.Fatalf("second account was not appended correctly: index=%d provider=%#v", index, p)
	}
	p, index = upsertClineAccount(p, ClineAccount{Email: "ONE@example.com", AccessToken: "token-one-new", RefreshToken: "refresh-one-new"})
	if index != 0 || len(p.ClineAccounts) != 2 || p.AccessToken != "token-one-new" {
		t.Fatalf("existing account was not updated correctly: index=%d provider=%#v", index, p)
	}
}

func TestClineAccountsRotateOnQuotaAndRateLimit(t *testing.T) {
	var calls []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer workos:")
		calls = append(calls, token)
		switch token {
		case "token-one":
			writeJSON(w, http.StatusPaymentRequired, map[string]any{"error": "insufficient balance"})
		case "token-two":
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate limit exceeded"})
		default:
			writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "OK"}}}})
		}
	}))
	defer upstream.Close()

	p := ProviderConfig{
		ID:      "cline",
		Type:    "cline",
		Enabled: true,
		BaseURL: upstream.URL,
		ClineAccounts: []ClineAccount{
			{Email: "one@example.com", AccessToken: "token-one"},
			{Email: "two@example.com", AccessToken: "token-two"},
			{Email: "three@example.com", AccessToken: "token-three"},
		},
	}
	syncClineLegacyAccount(&p)
	s := &Server{config: Config{Providers: []ProviderConfig{p}}, client: upstream.Client(), dataDir: t.TempDir()}
	resp, updated, err := s.doClineRequest(t.Context(), p, false, []byte(`{"model":"cline-free/glm-5.2"}`), true)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || updated.ActiveClineAccount != 2 {
		t.Fatalf("status=%d active=%d", resp.StatusCode, updated.ActiveClineAccount)
	}
	if strings.Join(calls, ",") != "token-one,token-two,token-three" {
		t.Fatalf("calls = %#v", calls)
	}
	stored, _ := s.providerByID("cline")
	if stored.ActiveClineAccount != 2 || stored.AccessToken != "token-three" {
		t.Fatalf("active account was not persisted: %#v", stored)
	}

	calls = nil
	resp, _, err = s.doClineRequest(t.Context(), stored, false, []byte(`{"model":"cline-free/glm-5.2"}`), true)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if strings.Join(calls, ",") != "token-three" {
		t.Fatalf("next request did not start with active account: %#v", calls)
	}
}

func TestClineRequestAddsTaskContextAndDoesNotRefreshForbidden(t *testing.T) {
	var taskID string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer workos:test-token" {
			t.Errorf("Authorization = %q", got)
		}
		taskID = r.Header.Get("X-Task-ID")
		if r.Header.Get("X-CLIENT-TYPE") != "9router-lite" {
			t.Errorf("X-CLIENT-TYPE = %q", r.Header.Get("X-CLIENT-TYPE"))
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":"model is only available via Cline product surfaces"}`)
	}))
	defer upstream.Close()

	s := &Server{client: upstream.Client()}
	resp, _, err := s.doClineRequest(context.Background(), ProviderConfig{
		Type:         "cline",
		BaseURL:      upstream.URL,
		AccessToken:  "test-token",
		RefreshToken: "must-not-be-used",
	}, false, []byte(`{"model":"cline-free/glm-5.2"}`), true)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if strings.TrimSpace(taskID) == "" {
		t.Fatal("X-Task-ID was not sent")
	}
}

func TestClineAccountJSONDoesNotLoseTokens(t *testing.T) {
	p := ProviderConfig{Type: "cline", ClineAccounts: []ClineAccount{{Email: "one@example.com", AccessToken: "access", RefreshToken: "refresh"}}}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ProviderConfig
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.ClineAccounts) != 1 || decoded.ClineAccounts[0].RefreshToken != "refresh" {
		t.Fatalf("accounts were not preserved: %#v", decoded.ClineAccounts)
	}
}

func TestMergeDefaultConfigMigratesLegacyClineAccount(t *testing.T) {
	cfg := mergeDefaultConfig(Config{Providers: []ProviderConfig{{
		ID:           "cline",
		Type:         "cline",
		AccessToken:  "legacy-access",
		RefreshToken: "legacy-refresh",
		Email:        "legacy@example.com",
		ModelMultimodal: map[string]bool{
			"known-vision":    true,
			"old-wrong-false": false,
		},
	}}})
	var cline ProviderConfig
	for _, provider := range cfg.Providers {
		if provider.ID == "cline" {
			cline = provider
			break
		}
	}
	if len(cline.ClineAccounts) != 1 || cline.ClineAccounts[0].Email != "legacy@example.com" {
		t.Fatalf("legacy account was not migrated: %#v", cline.ClineAccounts)
	}
	if !cline.ModelMultimodal["known-vision"] {
		t.Fatal("known vision capability was lost")
	}
	if _, exists := cline.ModelMultimodal["old-wrong-false"]; exists {
		t.Fatal("old false capability cache was not invalidated")
	}
}
