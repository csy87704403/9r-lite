package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testGroupedServer() *Server {
	return &Server{config: Config{
		AccessKey: "master-key",
		ModelGroups: []ModelGroup{
			{ID: "free", Name: "Free", APIKey: "group-key", Enabled: true, Models: []string{"test/free"}},
		},
		Providers: []ProviderConfig{
			{
				ID:            "test",
				Name:          "Test",
				Type:          "openai",
				Enabled:       true,
				Models:        []string{"free", "paid"},
				EnabledModels: []string{"free", "paid"},
			},
		},
	}}
}

func TestModelGroupKeyFiltersModels(t *testing.T) {
	s := testGroupedServer()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer group-key")
	rr := httptest.NewRecorder()

	s.handleModels(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 || body.Data[0].ID != "test/free" {
		t.Fatalf("unexpected models: %#v", body.Data)
	}
}

func TestMasterKeyStillSeesAllModels(t *testing.T) {
	s := testGroupedServer()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer master-key")
	rr := httptest.NewRecorder()

	s.handleModels(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Data []any `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 2 {
		t.Fatalf("model count = %d, want 2", len(body.Data))
	}
}

func TestModelGroupKeyCannotCallOutsideGroup(t *testing.T) {
	s := testGroupedServer()
	raw := []byte(`{"model":"test/paid","messages":[{"role":"user","content":"hello"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer group-key")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	s.handleChatCompletions(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}
