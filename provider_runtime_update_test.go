package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func runtimeProbeServer(t *testing.T) (*Server, <-chan struct{}, chan<- struct{}) {
	t.Helper()
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	p := ProviderConfig{ID: "cline", Type: "cline", Enabled: true, Models: []string{"a", "b"}, EnabledModels: []string{"a", "b"}, AvailableModels: []string{"a", "b"},
		ModelMultimodal: map[string]bool{"a": true, "b": true}, ClineAccounts: []ClineAccount{{Email: "user@example.invalid", AccessToken: "old", RefreshToken: "old-refresh"}}}
	s := &Server{config: Config{Providers: []ProviderConfig{p}}, dataDir: t.TempDir(), client: &http.Client{Transport: clineCatalogTransport(func(r *http.Request) (*http.Response, error) {
		once.Do(func() { close(started) })
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"OK"}}]}`))}, nil
	})}}
	return s, started, release
}

func runtimeProbeRequest(s *Server, raw string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/admin/provider/probe-model", strings.NewReader(raw))
	r.AddCookie(&http.Cookie{Name: "nr_admin", Value: s.adminCookieValue()})
	return r
}

func TestProviderProbePreservesConcurrentOAuthRefreshAndPublishSelection(t *testing.T) {
	s, started, release := runtimeProbeServer(t)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		s.handleProviderProbeModel(w, runtimeProbeRequest(s, `{"id":"cline","model":"a","auto_publish":true}`))
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("probe did not start")
	}
	_, err := s.mutateProvider("cline", func(latest *ProviderConfig) error {
		latest.ClineAccounts[0].AccessToken, latest.ClineAccounts[0].RefreshToken = "new-access", "new-refresh"
		syncClineLegacyAccount(latest)
		latest.EnabledModels = []string{"b"}
		latest.ProviderSpecificData = map[string]string{"manualPublishOverride": "true"}
		return nil
	})
	close(release)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := s.providerByID("cline")
	if w.Code != http.StatusOK || stored.AccessToken != "new-access" || stored.ClineAccounts[0].RefreshToken != "new-refresh" || strings.Join(stored.EnabledModels, ",") != "b" || !providerManualPublishOverride(stored) {
		t.Fatalf("probe rolled back new login or selection: status=%d selection=%v", w.Code, stored.EnabledModels)
	}
}

func TestProviderProbeCannotRestoreConcurrentlyDeletedModel(t *testing.T) {
	s, started, release := runtimeProbeServer(t)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		s.handleProviderProbeModel(w, runtimeProbeRequest(s, `{"id":"cline","model":"a","auto_publish":true}`))
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("probe did not start")
	}
	_, err := s.deleteProviderModel("cline", "a")
	close(release)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := s.providerByID("cline")
	_, latencyExists := stored.ModelLatencyMS["a"]
	if w.Code != http.StatusConflict || containsString(stored.Models, "a") || containsString(stored.EnabledModels, "a") || containsString(stored.AvailableModels, "a") || latencyExists {
		t.Fatalf("probe restored deleted model or metadata: status=%d models=%v", w.Code, stored.Models)
	}
}

func TestScheduledProbePreservesConcurrentCredentialsAndSelection(t *testing.T) {
	s, started, release := runtimeProbeServer(t)
	done := make(chan struct{})
	go func() { s.probeAllProviders(context.Background(), true); close(done) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduled probe did not start")
	}
	_, err := s.mutateProvider("cline", func(latest *ProviderConfig) error {
		latest.ClineAccounts[0].AccessToken, latest.ClineAccounts[0].RefreshToken = "new-access", "new-refresh"
		syncClineLegacyAccount(latest)
		latest.EnabledModels = []string{"b"}
		latest.ProviderSpecificData = map[string]string{"manualPublishOverride": "true"}
		return nil
	})
	close(release)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := s.providerByID("cline")
	if stored.AccessToken != "new-access" || stored.ClineAccounts[0].RefreshToken != "new-refresh" || strings.Join(stored.EnabledModels, ",") != "b" || !providerManualPublishOverride(stored) {
		t.Fatal("scheduled probe rolled back concurrent credentials or manual publish selection")
	}
}

func TestProviderModelFetchPreservesConcurrentListEditsAndMeasurements(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	p := ProviderConfig{ID: "p", Type: "openai", Enabled: true, BaseURL: "https://upstream.invalid/v1", APIKey: "test-key", Models: []string{"a", "b"}, EnabledModels: []string{"a", "b"}}
	s := &Server{config: Config{Providers: []ProviderConfig{p}}, dataDir: t.TempDir(), client: &http.Client{Transport: clineCatalogTransport(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-release
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"a"},{"id":"b"},{"id":"d"}]}`))}, nil
	})}}
	w, done := httptest.NewRecorder(), make(chan struct{})
	go func() { s.handleProviderModels(w, runtimeProbeRequest(s, `{"id":"p"}`)); close(done) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("model fetch did not start")
	}
	_, err := s.mutateProvider("p", func(latest *ProviderConfig) error {
		latest.Models = []string{"a", "c"}
		latest.EnabledModels, latest.AvailableModels = []string{"c"}, []string{"c"}
		latest.ModelLatencyMS = map[string]int64{"c": 42}
		latest.ProviderSpecificData = map[string]string{"manualPublishOverride": "true"}
		return nil
	})
	close(release)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := s.providerByID("p")
	if w.Code != http.StatusOK || containsString(stored.Models, "b") || !containsString(stored.Models, "c") || !containsString(stored.Models, "d") || strings.Join(stored.EnabledModels, ",") != "c" || strings.Join(stored.AvailableModels, ",") != "c" || stored.ModelLatencyMS["c"] != 42 {
		t.Fatalf("model fetch overwrote concurrent edits: status=%d models=%v selected=%v", w.Code, stored.Models, stored.EnabledModels)
	}
}

func TestProviderKeyProbeCannotApplyOldKeyResultToReplacement(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	p := ProviderConfig{ID: "p", Type: "openai", Enabled: true, BaseURL: "https://upstream.invalid/v1", APIKey: "old-key", Models: []string{"a"}, EnabledModels: []string{"a"}}
	s := &Server{config: Config{Providers: []ProviderConfig{p}}, dataDir: t.TempDir(), client: &http.Client{Transport: clineCatalogTransport(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-release
		return &http.Response{StatusCode: http.StatusPaymentRequired, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"Insufficient balance"}`))}, nil
	})}}
	w, done := httptest.NewRecorder(), make(chan struct{})
	go func() {
		s.handleProviderProbeKey(w, runtimeProbeRequest(s, `{"id":"p","model":"a","key_index":0}`))
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("key probe did not start")
	}
	_, err := s.mutateProvider("p", func(latest *ProviderConfig) error { latest.APIKey = "new-key"; return nil })
	close(release)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := s.providerByID("p")
	if w.Code != http.StatusConflict || stored.APIKey != "new-key" || modelQuotaBlocked(stored, "a") || len(failedKeyIndexesForModel(stored, "a")) != 0 || stored.ModelErrors["a"] != "" {
		t.Fatalf("old key result affected replacement key: status=%d", w.Code)
	}
}
