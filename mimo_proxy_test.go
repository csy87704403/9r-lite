package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewServerUsesDedicatedMimoProxy(t *testing.T) {
	t.Setenv("MIMO_PROXY_URL", "http://127.0.0.1:7890")

	s, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if s.mimo.client == s.client {
		t.Fatal("MiMo should use a dedicated HTTP client when MIMO_PROXY_URL is set")
	}

	transport, ok := s.mimo.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected MiMo transport type %T", s.mimo.client.Transport)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://api.xiaomimimo.com", nil)
	proxyURL, err := transport.Proxy(req)
	if err != nil {
		t.Fatal(err)
	}
	if proxyURL == nil || proxyURL.String() != "http://127.0.0.1:7890" {
		t.Fatalf("unexpected MiMo proxy URL: %v", proxyURL)
	}
}

func TestNewServerRejectsInvalidMimoProxy(t *testing.T) {
	t.Setenv("MIMO_PROXY_URL", "not-a-proxy-url")

	if _, err := NewServer(t.TempDir()); err == nil || !strings.Contains(err.Error(), "MIMO_PROXY_URL") {
		t.Fatalf("expected MIMO_PROXY_URL validation error, got %v", err)
	}
}

func TestProxyRawWithClientUsesProvidedClient(t *testing.T) {
	var requestedURL string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedURL = r.URL.String()
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer proxy.Close()

	client, err := newHTTPClient(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	recorder := httptest.NewRecorder()
	status := s.proxyRawWithClient(recorder, req, client, "http://mimo.invalid/chat", []byte(`{}`), map[string]string{"Content-Type": "application/json"})

	if status != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", status, recorder.Body.String())
	}
	if requestedURL != "http://mimo.invalid/chat" {
		t.Fatalf("request did not use the provided proxy client: %q", requestedURL)
	}
}
