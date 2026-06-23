package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestUsableMimoProxyNodes(t *testing.T) {
	got := usableMimoProxyNodes([]string{"DIRECT", "node-a", "node-a", " ", "REJECT", "node-b"})
	want := []string{"node-a", "node-b"}
	if len(got) != len(want) {
		t.Fatalf("nodes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("nodes = %v, want %v", got, want)
		}
	}
}

func TestMimoProxyControllerFetchAndSelect(t *testing.T) {
	var mu sync.Mutex
	current := "node-a"
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/proxies/MonoCloud" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			mu.Lock()
			now := current
			mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{"all": []string{"DIRECT", "node-a", "node-b"}, "now": now})
		case http.MethodPut:
			var body struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			current = body.Name
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	defer controller.Close()

	s := &Server{
		mimoProxyControlURL:    controller.URL,
		mimoProxyGroup:         "MonoCloud",
		mimoProxyControlClient: controller.Client(),
	}
	state, err := s.fetchMimoProxyGroup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Now != "node-a" {
		t.Fatalf("current node = %q, want node-a", state.Now)
	}
	if err := s.selectMimoProxyNode(context.Background(), "node-b"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	selected := current
	mu.Unlock()
	if selected != "node-b" {
		t.Fatalf("selected node = %q, want node-b", selected)
	}
	if err := s.selectMimoProxyNode(context.Background(), "missing-node"); err == nil {
		t.Fatal("selecting a node outside the group should fail")
	}
}

func TestMimoProxyNodeRequiresRealChatSuccess(t *testing.T) {
	var mu sync.Mutex
	current := "bad-node"
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			mu.Lock()
			now := current
			mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{"all": []string{"bad-node", "good-node"}, "now": now})
		case http.MethodPut:
			var body struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			current = body.Name
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	defer controller.Close()

	s, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.mimoProxyControlURL = controller.URL
	s.mimoProxyGroup = "MonoCloud"
	s.mimoProxyControlClient = controller.Client()
	s.mimo.client = &http.Client{
		Timeout: 5 * time.Second,
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			mu.Lock()
			node := current
			mu.Unlock()
			status := http.StatusOK
			body := `{"jwt":"test-jwt"}`
			if r.URL.String() == mimoFreeChatURL {
				body = `{"choices":[{"message":{"role":"assistant","content":"OK"}}]}`
				if node == "bad-node" {
					status = 441
					body = `{"error":{"code":"441","message":"illegal region"}}`
				}
			}
			return &http.Response{
				StatusCode: status,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    r,
			}, nil
		}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.testMimoProxyNode(ctx, "bad-node"); err == nil {
		t.Fatal("a node with a failing MiMo chat request must not be accepted")
	}
	latency, err := s.testMimoProxyNode(ctx, "good-node")
	if err != nil {
		t.Fatal(err)
	}
	if latency < 0 {
		t.Fatalf("latency = %d, want non-negative", latency)
	}
	p, ok := s.providerByID("mmf")
	if !ok || !sliceSet(p.AvailableModels)["mimo-auto"] {
		t.Fatal("successful node should mark mimo-auto available")
	}
}
