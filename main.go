package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/user"
	"path"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	openCodeBaseURL    = "https://opencode.ai"
	openCodeModelsURL  = "https://opencode.ai/zen/v1/models"
	mimoBootstrapURL   = "https://api.xiaomimimo.com/api/free-ai/bootstrap"
	mimoFreeChatURL    = "https://api.xiaomimimo.com/api/free-ai/openai/chat"
	mimoSystemMarker   = "You are MiMoCode, an interactive CLI tool that helps users with software engineering tasks."
	defaultHTTPTimeout = 60 * time.Second
)

type Config struct {
	AccessKey                string           `json:"access_key,omitempty"`
	AutoProbeEnabled         bool             `json:"auto_probe_enabled,omitempty"`
	AutoProbeIntervalMinutes int64            `json:"auto_probe_interval_minutes,omitempty"`
	AutoModel                AutoModelConfig  `json:"auto_model,omitempty"`
	DeletedProviderIDs       []string         `json:"deleted_provider_ids,omitempty"`
	Providers                []ProviderConfig `json:"providers"`
}

type AutoModelConfig struct {
	Enabled bool     `json:"enabled,omitempty"`
	Models  []string `json:"models,omitempty"`
}

type ProviderConfig struct {
	ID                    string            `json:"id"`
	Name                  string            `json:"name"`
	Type                  string            `json:"type"`
	Enabled               bool              `json:"enabled"`
	BaseURL               string            `json:"base_url,omitempty"`
	ImageEndpoint         string            `json:"image_endpoint,omitempty"`
	VideoEndpoint         string            `json:"video_endpoint,omitempty"`
	AudioEndpoint         string            `json:"audio_endpoint,omitempty"`
	ImageBaseURL          string            `json:"image_base_url,omitempty"`
	VideoBaseURL          string            `json:"video_base_url,omitempty"`
	AudioBaseURL          string            `json:"audio_base_url,omitempty"`
	APIKey                string            `json:"api_key,omitempty"`
	APIKeys               []string          `json:"api_keys,omitempty"`
	AccessToken           string            `json:"access_token,omitempty"`
	RefreshToken          string            `json:"refresh_token,omitempty"`
	Email                 string            `json:"email,omitempty"`
	DisplayName           string            `json:"display_name,omitempty"`
	ExpiresIn             int64             `json:"expires_in,omitempty"`
	ProviderSpecificData  map[string]string `json:"provider_specific_data,omitempty"`
	Models                []string          `json:"models,omitempty"`
	EnabledModels         []string          `json:"enabled_models,omitempty"`
	AvailableModels       []string          `json:"available_models,omitempty"`
	ModelLatencyMS        map[string]int64  `json:"model_latency_ms,omitempty"`
	ModelErrors           map[string]string `json:"model_errors,omitempty"`
	AvailabilityCheckedAt int64             `json:"availability_checked_at,omitempty"`
	FetchModels           bool              `json:"fetch_models,omitempty"`
}

type HealthStatus struct {
	OK              bool             `json:"ok"`
	Service         string           `json:"service"`
	Providers       int              `json:"providers"`
	Connected       []HealthProvider `json:"connected"`
	ConnectedCount  int              `json:"connected_count"`
	PublishedModels int              `json:"published_models"`
	AutoModel       HealthAutoModel  `json:"auto_model"`
}

type HealthProvider struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Type            string `json:"type"`
	Models          int    `json:"models"`
	AvailableModels int    `json:"available_models"`
	PublishedModels int    `json:"published_models"`
	AuthStatus      string `json:"auth_status"`
}

type HealthAutoModel struct {
	Enabled bool   `json:"enabled"`
	Models  int    `json:"models"`
	Active  string `json:"active"`
	OK      bool   `json:"ok"`
}

type Server struct {
	dataDir     string
	config      Config
	mu          sync.RWMutex
	probeMu     sync.Mutex
	client      *http.Client
	mimo        *MimoAuth
	adminSecret string
}

type MimoAuth struct {
	mu        sync.Mutex
	jwt       string
	expiresAt time.Time
	sessionID string
	client    *http.Client
}

type chatRequest struct {
	Model  string          `json:"model"`
	Stream bool            `json:"stream"`
	Raw    json.RawMessage `json:"-"`
}

type mediaRequest struct {
	Model string          `json:"model"`
	Raw   json.RawMessage `json:"-"`
}

type internalBypassKey struct{}

func main() {
	tuneRuntime()

	port := envDefault("PORT", "20129")
	dataDir := envDefault("DATA_DIR", "data")

	srv, err := NewServer(dataDir)
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.handleRoot)
	mux.HandleFunc("/admin", srv.handleAdmin)
	mux.HandleFunc("/api/config", srv.handleConfig)
	mux.HandleFunc("/api/provider/models", srv.handleProviderModels)
	mux.HandleFunc("/api/provider/probe", srv.handleProviderProbe)
	mux.HandleFunc("/api/provider/probe-model", srv.handleProviderProbeModel)
	mux.HandleFunc("/api/provider/selection", srv.handleProviderSelection)
	mux.HandleFunc("/api/oauth/qoder/device-code", srv.handleQoderDeviceCode)
	mux.HandleFunc("/api/oauth/qoder/poll", srv.handleQoderPoll)
	mux.HandleFunc("/api/oauth/gemini/authorize", srv.handleGeminiAuthorize)
	mux.HandleFunc("/api/oauth/gemini/callback", srv.handleGeminiCallback)
	mux.HandleFunc("/api/oauth/kilo/device-code", srv.handleKiloDeviceCode)
	mux.HandleFunc("/api/oauth/kilo/poll", srv.handleKiloPoll)
	mux.HandleFunc("/api/oauth/cline/authorize", srv.handleClineAuthorize)
	mux.HandleFunc("/api/oauth/cline/callback", srv.handleClineCallback)
	mux.HandleFunc("/health", srv.handleHealth)
	mux.HandleFunc("/v1/models", srv.handleModels)
	mux.HandleFunc("/v1/tools", srv.handleTools)
	mux.HandleFunc("/tools.json", srv.handleTools)
	mux.HandleFunc("/v1/chat/completions", srv.handleChatCompletions)
	mux.HandleFunc("/v1/images", srv.handleMedia("image"))
	mux.HandleFunc("/v1/images/models", srv.handleMediaModels("image"))
	mux.HandleFunc("/v1/videos", srv.handleMedia("video"))
	mux.HandleFunc("/v1/videos/models", srv.handleMediaModels("video"))
	mux.HandleFunc("/v1/audio", srv.handleMedia("audio"))
	mux.HandleFunc("/v1/audio/models", srv.handleMediaModels("audio"))

	go srv.autoProbeLoop()

	addr := ":" + port
	log.Printf("9router-lite listening on http://localhost%s", addr)
	log.Fatal(http.ListenAndServe(addr, logRequest(mux)))
}

func NewServer(dataDir string) (*Server, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}

	cfg, err := loadConfig(dataDir)
	if err != nil {
		return nil, err
	}

	client := &http.Client{
		Timeout: defaultHTTPTimeout,
		Transport: &http.Transport{
			DisableKeepAlives:   true,
			MaxIdleConns:        0,
			MaxIdleConnsPerHost: 0,
			IdleConnTimeout:     5 * time.Second,
			DialContext:         preferIPv4DialContext(),
			Proxy:               http.ProxyFromEnvironment,
		},
	}
	return &Server{
		dataDir:     dataDir,
		config:      cfg,
		client:      client,
		adminSecret: newSessionID(),
		mimo: &MimoAuth{
			sessionID: newSessionID(),
			client:    client,
		},
	}, nil
}

func loadConfig(dataDir string) (Config, error) {
	file := path.Join(dataDir, "config.json")
	b, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		cfg := defaultConfig()
		return cfg, saveConfig(dataDir, cfg)
	}
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, err
	}
	if len(cfg.Providers) == 0 {
		cfg = defaultConfig()
	}
	return mergeDefaultConfig(cfg), nil
}

func saveConfig(dataDir string, cfg Config) error {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path.Join(dataDir, "config.json"), append(b, '\n'), 0600)
}

func defaultConfig() Config {
	return Config{Providers: []ProviderConfig{
		{
			ID:      "oc",
			Name:    "OpenCode Free",
			Type:    "opencode-free",
			Enabled: true,
			Models: []string{
				"big-pickle",
				"deepseek-v4-flash-free",
				"mimo-v2.5-free",
				"minimax-m3-free",
				"nemotron-3-ultra-free",
				"north-mini-code-free",
				"qwen3.6-plus-free",
			},
			FetchModels: false,
		},
		{ID: "mmf", Name: "MiMo Code Free", Type: "mimo-free", Enabled: true, Models: []string{"mimo-auto"}},
		{
			ID:      "qoder",
			Name:    "Qoder Free",
			Type:    "qoder",
			Enabled: false,
			Models:  qoderStaticModels(),
		},
		{
			ID:      "gemini",
			Name:    "Gemini CLI",
			Type:    "gemini-cli",
			Enabled: false,
			Models:  []string{"gemini-3-flash-preview", "gemini-3-pro-preview"},
		},
		{ID: "codex", Name: "OpenAI Codex OAuth", Type: "placeholder", Enabled: false},
		{
			ID:      "kilo",
			Name:    "Kilo Code OAuth",
			Type:    "kilocode",
			Enabled: false,
			BaseURL: "https://api.kilo.ai/api/openrouter/chat/completions",
		},
		{
			ID:      "cline",
			Name:    "Cline OAuth",
			Type:    "cline",
			Enabled: false,
			BaseURL: "https://api.cline.bot/api/v1/chat/completions",
			Models: []string{
				"anthropic/claude-opus-4.7",
				"anthropic/claude-sonnet-4.6",
				"anthropic/claude-opus-4.6",
				"openai/gpt-5.3-codex",
				"openai/gpt-5.4",
				"google/gemini-3.1-pro-preview",
				"google/gemini-3.1-flash-lite-preview",
				"kwaipilot/kat-coder-pro",
			},
		},
		{
			ID:          "glm",
			Name:        "GLM",
			Type:        "openai",
			Enabled:     false,
			BaseURL:     "https://open.bigmodel.cn/api/paas/v4",
			Models:      []string{"glm-5.1", "glm-5", "glm-4.7", "glm-4.6v"},
			FetchModels: false,
		},
		{
			ID:          "groq",
			Name:        "Groq",
			Type:        "openai",
			Enabled:     false,
			BaseURL:     "https://api.groq.com/openai/v1",
			Models:      []string{"llama-3.3-70b-versatile", "meta-llama/llama-4-maverick-17b-128e-instruct", "qwen/qwen3-32b", "openai/gpt-oss-120b"},
			FetchModels: false,
		},
		{
			ID:          "deepseek",
			Name:        "DeepSeek",
			Type:        "openai",
			Enabled:     false,
			BaseURL:     "https://api.deepseek.com",
			Models:      []string{"deepseek-v4-pro", "deepseek-v4-flash", "deepseek-chat", "deepseek-reasoner"},
			FetchModels: false,
		},
		{
			ID:          "mimo",
			Name:        "Xiaomi MiMo",
			Type:        "openai",
			Enabled:     false,
			BaseURL:     "https://api.xiaomimimo.com/v1",
			Models:      []string{"mimo-v2.5-pro", "mimo-v2.5", "mimo-v2-omni", "mimo-v2-flash"},
			FetchModels: false,
		},
		{
			ID:          "custom",
			Name:        "Custom OpenAI Compatible",
			Type:        "openai",
			Enabled:     false,
			BaseURL:     "https://example.com/v1",
			Models:      []string{"model-id"},
			FetchModels: false,
		},
	}}
}

func mergeDefaultConfig(cfg Config) Config {
	defaults := defaultConfig()
	byID := map[string]ProviderConfig{}
	for _, p := range defaults.Providers {
		byID[p.ID] = p
	}
	deleted := sliceSet(cfg.DeletedProviderIDs)

	seen := map[string]bool{}
	merged := make([]ProviderConfig, 0, len(defaults.Providers))
	for _, p := range cfg.Providers {
		d, ok := byID[p.ID]
		if !ok {
			if !isCustomOpenAIProvider(p) {
				continue
			}
			if p.Name == "" {
				p.Name = "Custom OpenAI Compatible"
			}
			p.Type = "openai"
			if p.ProviderSpecificData == nil {
				p.ProviderSpecificData = map[string]string{}
			}
			p.ProviderSpecificData["customProvider"] = "true"
			merged = append(merged, p)
			continue
		}
		delete(deleted, p.ID)
		seen[p.ID] = true
		if p.Name == "" {
			p.Name = d.Name
		}
		if p.Type == "" {
			p.Type = d.Type
		}
		if p.Type == "placeholder" && d.Type != "placeholder" {
			p.Type = d.Type
		}
		if p.BaseURL == "" {
			p.BaseURL = d.BaseURL
		}
		if len(p.Models) == 0 {
			p.Models = d.Models
		}
		if p.Type == "openai" && p.FetchModels && len(providerAPIKeys(p)) > 0 && len(p.Models) > 0 {
			if p.ProviderSpecificData == nil {
				p.ProviderSpecificData = map[string]string{}
			}
			p.ProviderSpecificData["apiModelsFetched"] = "true"
		}
		merged = append(merged, p)
	}
	for _, p := range defaults.Providers {
		if deleted[p.ID] {
			continue
		}
		if !seen[p.ID] {
			merged = append(merged, p)
		}
	}
	cfg.DeletedProviderIDs = keysFromSet(deleted)
	cfg.Providers = merged
	return cfg
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin", http.StatusFound)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	cfg := s.currentConfig()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var connected []HealthProvider
	totalPublished := 0
	for _, p := range cfg.Providers {
		published := 0
		if p.Enabled {
			published = len(s.visibleModelsForProvider(ctx, p))
			totalPublished += published
		}
		if p.Enabled && providerHasCredential(p) {
			connected = append(connected, HealthProvider{
				ID:              p.ID,
				Name:            p.Name,
				Type:            p.Type,
				Models:          len(p.Models),
				AvailableModels: len(p.AvailableModels),
				PublishedModels: published,
				AuthStatus:      firstNonEmpty(p.ProviderSpecificData["authStatus"], "ok"),
			})
		}
	}
	autoTarget, autoOK := s.resolveAutoModel(ctx)
	status := HealthStatus{
		OK:              true,
		Service:         "9router-lite",
		Providers:       len(cfg.Providers),
		Connected:       connected,
		ConnectedCount:  len(connected),
		PublishedModels: totalPublished,
		AutoModel: HealthAutoModel{
			Enabled: cfg.AutoModel.Enabled,
			Models:  len(cfg.AutoModel.Models),
			Active:  autoTarget,
			OK:      autoOK,
		},
	}
	if wantsHTML(r) && !wantsJSON(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(renderHealthHTML(status)))
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(renderAdminLoginHTML("登录请求无效")))
			return
		}
		if strings.TrimSpace(r.Form.Get("password")) != s.adminPassword() {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(renderAdminLoginHTML("密码错误")))
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     "nr_admin",
			Value:    s.adminCookieValue(),
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   86400 * 30,
		})
		http.Redirect(w, r, "/admin", http.StatusFound)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if strings.TrimSpace(r.URL.Query().Get("logout")) == "1" {
		http.SetCookie(w, &http.Cookie{Name: "nr_admin", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		http.Redirect(w, r, "/admin", http.StatusFound)
		return
	}
	if !s.hasAdminSession(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(renderAdminLoginHTML("")))
		return
	}
	s.refreshKiloModelsIfStale(r.Context())
	cfg := s.currentConfig()
	b, _ := json.MarshalIndent(cfg, "", "  ")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(adminHTMLLiteV2(string(b))))
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.currentConfig())
	case http.MethodPost:
		var cfg Config
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&cfg); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		cfg = mergeDefaultConfig(cfg)
		if err := validateConfig(cfg); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if err := saveConfig(s.dataDir, cfg); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		s.mu.Lock()
		s.config = cfg
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleProviderModels(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	p, ok := s.providerByID(strings.TrimSpace(body.ID))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "provider not found"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	ids, err := s.fetchProviderModels(ctx, p)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	oldEnabled := append([]string(nil), p.EnabledModels...)
	manual := providerManualPublishOverride(p)
	p.Models = uniqueStrings(ids)
	if manual {
		p.EnabledModels = orderedIntersection(p.Models, oldEnabled)
	} else {
		p.EnabledModels = append([]string(nil), p.Models...)
	}
	p.AvailableModels = nil
	p.ModelLatencyMS = nil
	p.ModelErrors = nil
	p.AvailabilityCheckedAt = 0
	p.FetchModels = true
	if p.ProviderSpecificData == nil {
		p.ProviderSpecificData = map[string]string{}
	}
	p.ProviderSpecificData["apiModelsFetched"] = "true"
	if !manual {
		clearManualPublishOverride(&p)
	}
	if err := s.updateProvider(p); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"provider": p,
		"count":    len(p.Models),
		"models":   p.Models,
	})
}

func (s *Server) handleProviderProbe(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	p, ok := s.providerByID(strings.TrimSpace(body.ID))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "provider not found"})
		return
	}
	if !p.Enabled {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "provider is not enabled"})
		return
	}
	if len(p.Models) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "provider has no loaded models"})
		return
	}
	available, failures, latencies := s.probeProviderModels(r.Context(), p, p.Models)
	p.AvailableModels = uniqueStrings(available)
	p.ModelLatencyMS = latencies
	p.ModelErrors = failures
	p.AvailabilityCheckedAt = time.Now().Unix()
	if err := s.updateProvider(p); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"id":               p.ID,
		"available_count":  len(p.AvailableModels),
		"available_models": p.AvailableModels,
		"latencies":        p.ModelLatencyMS,
		"failures":         failures,
	})
}

func (s *Server) handleProviderProbeModel(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID              string `json:"id"`
		Model           string `json:"model"`
		AutoPublish     bool   `json:"auto_publish"`
		DropUnavailable bool   `json:"drop_unavailable_on_failure"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<18)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	p, ok := s.providerByID(strings.TrimSpace(body.ID))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "provider not found"})
		return
	}
	if !p.Enabled {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "provider is not enabled"})
		return
	}
	model := strings.TrimSpace(body.Model)
	if model == "" || !sliceSet(p.Models)[model] {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "model is not loaded by provider"})
		return
	}

	start := time.Now()
	probeCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	err := s.probeSingleModel(probeCtx, p.ID, model)
	cancel()
	latency := time.Since(start).Milliseconds()
	p = updateProbeResult(p, model, err, latency, body.AutoPublish)
	if body.DropUnavailable && err != nil {
		if len(p.EnabledModels) == 0 {
			p.EnabledModels = append([]string(nil), p.Models...)
		}
		p.EnabledModels = removeString(p.EnabledModels, model)
		if p.ProviderSpecificData == nil {
			p.ProviderSpecificData = map[string]string{}
		}
		p.ProviderSpecificData["manualPublishOverride"] = "true"
	}
	if err := s.updateProvider(p); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	resp := map[string]any{
		"ok":         err == nil,
		"id":         p.ID,
		"model":      model,
		"latency_ms": latency,
		"provider":   p,
	}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleProviderSelection(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID            string   `json:"id"`
		EnabledModels []string `json:"enabled_models"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<18)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	p, ok := s.providerByID(strings.TrimSpace(body.ID))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "provider not found"})
		return
	}
	allowed := sliceSet(p.Models)
	var selected []string
	for _, id := range uniqueStrings(body.EnabledModels) {
		if allowed[id] {
			selected = append(selected, id)
		}
	}
	p.EnabledModels = selected
	if p.ProviderSpecificData == nil {
		p.ProviderSpecificData = map[string]string{}
	}
	p.ProviderSpecificData["manualPublishOverride"] = "true"
	if err := s.updateProvider(p); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": p.ID, "enabled_models": p.EnabledModels})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireAccessKey(w, r) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	var models []map[string]any
	grouped := map[string][]string{}
	for _, p := range s.enabledProviders() {
		ids := s.visibleModelsForProvider(ctx, p)
		if len(ids) > 0 {
			grouped[p.Name] = append([]string(nil), ids...)
		}
		for _, id := range ids {
			models = append(models, map[string]any{
				"id":       p.ID + "/" + id,
				"object":   "model",
				"created":  0,
				"owned_by": p.ID,
			})
		}
	}
	if target, ok := s.resolveAutoModel(ctx); ok {
		grouped["Auto"] = []string{target}
		models = append(models, map[string]any{
			"id":       "auto",
			"object":   "model",
			"created":  0,
			"owned_by": "auto",
		})
	}
	sort.Slice(models, func(i, j int) bool {
		return fmt.Sprint(models[i]["id"]) < fmt.Sprint(models[j]["id"])
	})
	if wantsHTML(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(renderModelsHTML(grouped)))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": models})
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireAccessKey(w, r) {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 20<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	var req chatRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	req.Raw = raw

	if strings.TrimSpace(req.Model) == "auto" {
		target, ok := s.resolveAutoModel(r.Context())
		if !ok {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "auto model has no available target"})
			return
		}
		req.Model = target
		req.Raw, err = replaceModel(raw, target)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
	}

	providerID, upstreamModel, ok := strings.Cut(req.Model, "/")
	if !ok || providerID == "" || upstreamModel == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "model must be provider/model, for example oc/big-pickle"})
		return
	}

	p, ok := s.providerByID(providerID)
	if !ok || !p.Enabled {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "provider is not enabled: " + providerID})
		return
	}
	if bypass, _ := r.Context().Value(internalBypassKey{}).(bool); !bypass && !sliceSet(s.visibleModelsForProvider(r.Context(), p))[upstreamModel] {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "model is not published or not available: " + req.Model})
		return
	}

	switch p.Type {
	case "opencode-free":
		s.proxyOpenCode(w, r, req, upstreamModel)
	case "mimo-free":
		s.proxyMimoFree(w, r, req, upstreamModel)
	case "qoder":
		s.proxyQoder(w, r, p, req, upstreamModel)
	case "gemini-cli":
		s.proxyGeminiCLI(w, r, p, req, upstreamModel)
	case "kilocode":
		s.proxyKiloCode(w, r, p, req, upstreamModel)
	case "cline":
		s.proxyCline(w, r, p, req, upstreamModel)
	case "openai":
		s.proxyOpenAI(w, r, p, req, upstreamModel)
	default:
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":    "provider is kept as placeholder in lite MVP",
			"provider": p.ID,
			"type":     p.Type,
		})
	}
}

func (s *Server) handleMediaModels(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !s.requireAccessKey(w, r) {
			return
		}
		var models []map[string]any
		grouped := map[string][]string{}
		for _, p := range s.enabledProviders() {
			if mediaEndpoint(p, kind) == "" {
				continue
			}
			for _, id := range mediaModelsForKind(p.Models, kind) {
				grouped[p.Name] = append(grouped[p.Name], id)
				models = append(models, map[string]any{
					"id":       p.ID + "/" + id,
					"object":   "model",
					"created":  0,
					"owned_by": p.ID,
					"type":     kind,
				})
			}
		}
		sort.Slice(models, func(i, j int) bool {
			return fmt.Sprint(models[i]["id"]) < fmt.Sprint(models[j]["id"])
		})
		if wantsHTML(r) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(renderModelsHTML(grouped)))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": models})
	}
}

func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireAccessKey(w, r) {
		return
	}
	base := requestBaseURL(r)
	tools := []map[string]any{}
	for _, kind := range []string{"image", "video", "audio"} {
		tool := s.mediaToolDefinition(kind, base)
		if tool != nil {
			tools = append(tools, tool)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object":   "tool_list",
		"base_url": base + "/v1",
		"tools":    tools,
	})
}

func (s *Server) handleMedia(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !s.requireAccessKey(w, r) {
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<20))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		var req mediaRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		providerID, upstreamModel, ok := strings.Cut(strings.TrimSpace(req.Model), "/")
		if !ok || providerID == "" || upstreamModel == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "model must be provider/model"})
			return
		}
		p, ok := s.providerByID(providerID)
		if !ok || !p.Enabled {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "provider is not enabled: " + providerID})
			return
		}
		target := mediaEndpoint(p, kind)
		if target == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": kind + " endpoint is not configured for provider: " + providerID})
			return
		}
		keys := providerAPIKeys(p)
		if len(keys) == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "provider api_key is empty"})
			return
		}
		body, err := replaceModel(raw, upstreamModel)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		headers := map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + keys[0],
			"Accept":        "application/json",
			"User-Agent":    "9router-lite/0.1",
		}
		if len(keys) > 1 {
			s.proxyPostRotating(w, r, target, body, keys, headers)
			return
		}
		s.proxyRaw(w, r, target, body, headers)
	}
}

func (s *Server) proxyOpenCode(w http.ResponseWriter, r *http.Request, req chatRequest, upstreamModel string) {
	body, err := replaceModel(req.Raw, upstreamModel)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	headers := map[string]string{
		"Content-Type":        "application/json",
		"Authorization":       "Bearer public",
		"x-opencode-client":   "desktop",
		"Accept":              acceptHeader(req.Stream),
		"User-Agent":          "9router-lite/0.1",
		"Cache-Control":       "no-cache",
		"X-Accel-Buffering":   "no",
		"Connection":          "keep-alive",
		"Transfer-Encoding":   "chunked",
		"X-9Router-Lite-Mode": "opencode-free",
	}
	s.proxyRaw(w, r, openCodeBaseURL+"/zen/v1/chat/completions", body, headers)
}

func (s *Server) proxyMimoFree(w http.ResponseWriter, r *http.Request, req chatRequest, upstreamModel string) {
	body, err := replaceModel(req.Raw, upstreamModel)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	body, err = injectMimoSystemMarker(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	jwt, err := s.mimo.JWT(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}

	headers := map[string]string{
		"Content-Type":        "application/json",
		"Authorization":       "Bearer " + jwt,
		"X-Mimo-Source":       "mimocode-cli-free",
		"x-session-affinity":  s.mimo.sessionID,
		"Accept":              acceptHeader(req.Stream),
		"User-Agent":          "9router-lite/0.1",
		"Cache-Control":       "no-cache",
		"X-Accel-Buffering":   "no",
		"X-9Router-Lite-Mode": "mimo-free",
	}
	status := s.proxyRaw(w, r, mimoFreeChatURL, body, headers)
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		s.mimo.Reset()
	}
}

func (s *Server) proxyOpenAI(w http.ResponseWriter, r *http.Request, p ProviderConfig, req chatRequest, upstreamModel string) {
	if strings.TrimSpace(p.BaseURL) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "provider base_url is empty"})
		return
	}
	keys := providerAPIKeys(p)
	if len(keys) == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "provider api_key is empty"})
		return
	}
	body, err := replaceModel(req.Raw, upstreamModel)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	if isCustomOpenAIProvider(p) && len(keys) > 1 {
		s.proxyOpenAIRotating(w, r, p, req, upstreamModel, body, keys)
		return
	}

	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + keys[0],
		"Accept":        acceptHeader(req.Stream),
		"User-Agent":    "9router-lite/0.1",
	}
	s.proxyRaw(w, r, joinURL(p.BaseURL, "/chat/completions"), body, headers)
}

func (s *Server) proxyOpenAIRotating(w http.ResponseWriter, r *http.Request, p ProviderConfig, req chatRequest, upstreamModel string, body []byte, keys []string) {
	target := joinURL(p.BaseURL, "/chat/completions")
	headers := map[string]string{
		"Content-Type": "application/json",
		"Accept":       acceptHeader(req.Stream),
		"User-Agent":   "9router-lite/0.1",
	}
	s.proxyPostRotating(w, r, target, body, keys, headers)
}

func (s *Server) proxyPostRotating(w http.ResponseWriter, r *http.Request, target string, body []byte, keys []string, baseHeaders map[string]string) {
	var lastStatus int
	var lastHeader http.Header
	var lastBody []byte
	var lastErr error
	for i, key := range keys {
		headers := make(map[string]string, len(baseHeaders)+1)
		for k, v := range baseHeaders {
			headers[k] = v
		}
		headers["Authorization"] = "Bearer " + key
		status, header, respBody, err := s.postUpstreamBuffered(r.Context(), target, body, headers)
		lastStatus, lastHeader, lastBody, lastErr = status, header, respBody, err
		if err != nil {
			break
		}
		if status >= 200 && status <= 299 {
			writeBufferedUpstream(w, status, header, respBody)
			return
		}
		if i == len(keys)-1 || !isQuotaKeyError(status, respBody) {
			break
		}
	}
	if lastErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": lastErr.Error()})
		return
	}
	writeBufferedUpstream(w, lastStatus, lastHeader, lastBody)
}

func (s *Server) postUpstreamBuffered(ctx context.Context, target string, body []byte, headers map[string]string) (int, http.Header, []byte, error) {
	defer s.client.CloseIdleConnections()
	defer debug.FreeOSMemory()
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return http.StatusBadRequest, nil, nil, err
	}
	for k, v := range headers {
		upReq.Header.Set(k, v)
	}
	resp, err := s.client.Do(upReq)
	if err != nil {
		return http.StatusBadGateway, nil, nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return resp.StatusCode, resp.Header, respBody, err
	}
	return resp.StatusCode, resp.Header, respBody, nil
}

func writeBufferedUpstream(w http.ResponseWriter, status int, header http.Header, body []byte) {
	for k, values := range header {
		if shouldSkipHeader(k) {
			continue
		}
		for _, v := range values {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (s *Server) proxyRaw(w http.ResponseWriter, r *http.Request, target string, body []byte, headers map[string]string) int {
	defer s.client.CloseIdleConnections()
	defer debug.FreeOSMemory()

	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return http.StatusBadRequest
	}
	for k, v := range headers {
		upReq.Header.Set(k, v)
	}

	resp, err := s.client.Do(upReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return http.StatusBadGateway
	}
	defer resp.Body.Close()

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
	return resp.StatusCode
}

func (s *Server) modelsForProvider(ctx context.Context, p ProviderConfig) []string {
	defer s.client.CloseIdleConnections()

	switch p.Type {
	case "opencode-free":
		if p.FetchModels {
			ids, err := fetchOpenCodeModels(ctx, s.client)
			if err == nil && len(ids) > 0 {
				return ids
			}
		}
		if len(p.Models) > 0 {
			return p.Models
		}
		return []string{"big-pickle"}
	case "mimo-free":
		if len(p.Models) > 0 {
			return p.Models
		}
		return []string{"mimo-auto"}
	case "qoder":
		if p.AccessToken != "" && p.ProviderSpecificData["userId"] != "" {
			ids, err := fetchQoderModels(ctx, s.client, p, false)
			if err == nil && len(ids) > 0 {
				return ids
			}
		}
		if len(p.Models) > 0 {
			return p.Models
		}
		return qoderStaticModels()
	case "gemini-cli":
		ids, err := s.fetchGeminiCLIModels(ctx, p)
		if err == nil && len(ids) > 0 {
			return ids
		}
		return p.Models
	case "kilocode":
		ids, err := fetchKiloFreeModels(ctx, s.client, p)
		if err == nil && len(ids) > 0 {
			return ids
		}
		return p.Models
	case "cline":
		return p.Models
	case "openai":
		if p.FetchModels && p.BaseURL != "" && len(providerAPIKeys(p)) > 0 {
			ids, err := fetchOpenAIModels(ctx, s.client, p)
			if err == nil && len(ids) > 0 {
				return ids
			}
		}
		return p.Models
	default:
		return p.Models
	}
}

func (s *Server) fetchProviderModels(ctx context.Context, p ProviderConfig) ([]string, error) {
	switch p.Type {
	case "opencode-free":
		ids, err := fetchOpenCodeModels(ctx, s.client)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return nil, errors.New("opencode returned no models")
		}
		return ids, nil
	case "mimo-free":
		if len(p.Models) > 0 {
			return p.Models, nil
		}
		return []string{"mimo-auto"}, nil
	case "qoder":
		if p.AccessToken == "" || p.ProviderSpecificData["userId"] == "" {
			return nil, errors.New("qoder is not logged in")
		}
		return fetchQoderModels(ctx, s.client, p, true)
	case "kilocode":
		return fetchKiloFreeModels(ctx, s.client, p)
	case "openai":
		if strings.TrimSpace(p.BaseURL) == "" {
			return nil, errors.New("provider base_url is empty")
		}
		if len(providerAPIKeys(p)) == 0 {
			return nil, errors.New("provider api_key is empty")
		}
		return fetchOpenAIModels(ctx, s.client, p)
	default:
		if len(p.Models) == 0 {
			return nil, fmt.Errorf("%s does not support model fetch", p.ID)
		}
		return p.Models, nil
	}
}

func (s *Server) updateProvider(next ProviderConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for i, p := range s.config.Providers {
		if p.ID == next.ID {
			s.config.Providers[i] = next
			found = true
			break
		}
	}
	if !found {
		s.config.Providers = append(s.config.Providers, next)
	}
	return saveConfig(s.dataDir, s.config)
}

func (s *Server) markProviderAuthState(id, status, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.config.Providers {
		if p.ID != id {
			continue
		}
		if p.ProviderSpecificData == nil {
			p.ProviderSpecificData = map[string]string{}
		}
		if status == "" || status == "ok" {
			p.ProviderSpecificData["authStatus"] = "ok"
			delete(p.ProviderSpecificData, "lastAuthError")
		} else {
			p.ProviderSpecificData["authStatus"] = status
			p.ProviderSpecificData["lastAuthError"] = message
		}
		s.config.Providers[i] = p
		_ = saveConfig(s.dataDir, s.config)
		return
	}
}

func fetchOpenCodeModels(ctx context.Context, client *http.Client) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openCodeModelsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-opencode-client", "desktop")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("opencode models status %d", resp.StatusCode)
	}
	ids, err := parseModelIDs(resp.Body)
	if err != nil {
		return nil, err
	}
	out := ids[:0]
	for _, id := range ids {
		if strings.HasSuffix(id, "-free") || id == "big-pickle" {
			out = append(out, id)
		}
	}
	return uniqueStrings(out), nil
}

func fetchOpenAIModels(ctx context.Context, client *http.Client, p ProviderConfig) ([]string, error) {
	keys := providerAPIKeys(p)
	if len(keys) == 0 {
		return nil, errors.New("provider api_key is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, joinURL(p.BaseURL, "/models"), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+keys[0])
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%s models status %d", p.ID, resp.StatusCode)
	}
	return parseModelIDs(resp.Body)
}

func parseModelIDs(r io.Reader) ([]string, error) {
	var raw any
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, err
	}
	root := raw
	if m, ok := raw.(map[string]any); ok {
		if v, ok := m["data"]; ok {
			root = v
		} else if v, ok := m["models"]; ok {
			root = v
		}
	}
	arr, ok := root.([]any)
	if !ok {
		return nil, nil
	}
	var ids []string
	for _, item := range arr {
		switch v := item.(type) {
		case string:
			ids = append(ids, v)
		case map[string]any:
			if id, ok := v["id"].(string); ok && id != "" {
				ids = append(ids, id)
			}
		}
	}
	return uniqueStrings(ids), nil
}

func replaceModel(raw []byte, model string) ([]byte, error) {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	body["model"] = model
	return json.Marshal(body)
}

func injectMimoSystemMarker(raw []byte) ([]byte, error) {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	msgs, _ := body["messages"].([]any)
	for _, item := range msgs {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		content, _ := msg["content"].(string)
		if role == "system" && strings.Contains(content, mimoSystemMarker) {
			return raw, nil
		}
	}
	body["messages"] = append([]any{map[string]any{"role": "system", "content": mimoSystemMarker}}, msgs...)
	return json.Marshal(body)
}

func (m *MimoAuth) JWT(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.jwt != "" && time.Now().Before(m.expiresAt.Add(-5*time.Minute)) {
		return m.jwt, nil
	}

	payload, _ := json.Marshal(map[string]string{"client": machineFingerprint()})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mimoBootstrapURL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("MiMo bootstrap failed: status %d", resp.StatusCode)
	}
	var data struct {
		JWT string `json:"jwt"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	if data.JWT == "" {
		return "", errors.New("MiMo bootstrap returned no JWT")
	}
	m.jwt = data.JWT
	m.expiresAt = jwtExpiry(data.JWT)
	return m.jwt, nil
}

func (m *MimoAuth) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jwt = ""
	m.expiresAt = time.Time{}
}

func machineFingerprint() string {
	hostname, _ := os.Hostname()
	username := "unknown-user"
	if u, err := user.Current(); err == nil && u.Username != "" {
		username = u.Username
	}
	seed := strings.Join([]string{hostname, runtime.GOOS, runtime.GOARCH, username}, "|")
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

func jwtExpiry(jwt string) time.Time {
	parts := strings.Split(jwt, ".")
	if len(parts) < 2 {
		return time.Now().Add(50 * time.Minute)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Now().Add(50 * time.Minute)
	}
	var data struct {
		Exp float64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &data); err != nil || data.Exp <= 0 || math.IsNaN(data.Exp) {
		return time.Now().Add(50 * time.Minute)
	}
	return time.Unix(int64(data.Exp), 0)
}

func (s *Server) currentConfig() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config
}

func (s *Server) requireAccessKey(w http.ResponseWriter, r *http.Request) bool {
	if bypass, _ := r.Context().Value(internalBypassKey{}).(bool); bypass {
		return true
	}
	key := strings.TrimSpace(s.currentConfig().AccessKey)
	if key == "" {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "gateway access_key is not configured"})
		return false
	}
	token := strings.TrimSpace(r.Header.Get("x-api-key"))
	if token == "" {
		token = extractBearerToken(r.Header.Get("Authorization"))
	}
	if token == "" {
		token = strings.TrimSpace(r.URL.Query().Get("key"))
	}
	if token != key {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid access_key"})
		return false
	}
	return true
}

func (s *Server) adminPassword() string {
	if v := strings.TrimSpace(os.Getenv("ADMIN_PASSWORD")); v != "" {
		return v
	}
	if v := strings.TrimSpace(s.currentConfig().AccessKey); v != "" {
		return v
	}
	return "123456"
}

func (s *Server) adminCookieValue() string {
	sum := sha256.Sum256([]byte(s.adminPassword() + "|" + s.adminSecret))
	return hex.EncodeToString(sum[:])
}

func (s *Server) hasAdminSession(r *http.Request) bool {
	cookie, err := r.Cookie("nr_admin")
	if err != nil {
		return false
	}
	return cookie.Value == s.adminCookieValue()
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if s.hasAdminSession(r) {
		return true
	}
	if strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html") {
		http.Redirect(w, r, "/admin", http.StatusFound)
		return false
	}
	writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "admin login required"})
	return false
}

func (s *Server) enabledProviders() []ProviderConfig {
	cfg := s.currentConfig()
	var out []ProviderConfig
	for _, p := range cfg.Providers {
		if p.Enabled {
			out = append(out, p)
		}
	}
	return out
}

func (s *Server) providerByID(id string) (ProviderConfig, bool) {
	cfg := s.currentConfig()
	for _, p := range cfg.Providers {
		if p.ID == id {
			return p, true
		}
	}
	return ProviderConfig{}, false
}

func providerHasCredential(p ProviderConfig) bool {
	if len(providerAPIKeys(p)) > 0 || p.AccessToken != "" || p.Type == "opencode-free" || p.Type == "mimo-free" {
		return true
	}
	return isCustomOpenAIProvider(p) && strings.TrimSpace(p.BaseURL) != "" && p.BaseURL != "https://example.com/v1"
}

func providerAPIKeys(p ProviderConfig) []string {
	seen := map[string]bool{}
	var keys []string
	for _, key := range append([]string{p.APIKey}, p.APIKeys...) {
		key = strings.TrimSpace(key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	return keys
}

func (s *Server) visibleModelsForProvider(ctx context.Context, p ProviderConfig) []string {
	_ = ctx
	base := p.Models
	base = uniqueStrings(base)
	if providerManualPublishOverride(p) {
		return orderedIntersection(base, uniqueStrings(p.EnabledModels))
	}
	selected := p.EnabledModels
	if len(selected) == 0 {
		selected = base
	}
	selected = orderedIntersection(base, uniqueStrings(selected))
	if p.AvailabilityCheckedAt == 0 && len(p.AvailableModels) == 0 {
		return selected
	}
	return orderedIntersection(selected, uniqueStrings(p.AvailableModels))
}

func (s *Server) resolveAutoModel(ctx context.Context) (string, bool) {
	cfg := s.currentConfig()
	if !cfg.AutoModel.Enabled {
		return "", false
	}
	for _, candidate := range uniqueStrings(cfg.AutoModel.Models) {
		if candidate == "auto" {
			continue
		}
		providerID, model, ok := strings.Cut(candidate, "/")
		if !ok || providerID == "" || model == "" {
			continue
		}
		p, ok := s.providerByID(providerID)
		if !ok || !p.Enabled {
			continue
		}
		if sliceSet(s.visibleModelsForProvider(ctx, p))[model] {
			return providerID + "/" + model, true
		}
	}
	return "", false
}

func (s *Server) probeProviderModels(ctx context.Context, p ProviderConfig, models []string) ([]string, map[string]string, map[string]int64) {
	models = uniqueStrings(models)
	failures := map[string]string{}
	latencies := map[string]int64{}
	if len(models) == 0 {
		return nil, failures, latencies
	}
	var available []string
	for _, id := range models {
		start := time.Now()
		probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := s.probeSingleModel(probeCtx, p.ID, id)
		cancel()
		latencies[id] = time.Since(start).Milliseconds()
		if err == nil {
			available = append(available, id)
			continue
		}
		failures[id] = err.Error()
	}
	return available, failures, latencies
}

func (s *Server) probeSingleModel(ctx context.Context, providerID, model string) error {
	body := map[string]any{
		"model": providerID + "/" + model,
		"messages": []map[string]string{
			{"role": "user", "content": "Reply with OK."},
		},
		"stream":     false,
		"max_tokens": 4,
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(raw))
	req = req.WithContext(context.WithValue(ctx, internalBypassKey{}, true))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.handleChatCompletions(rr, req)
	if rr.Code >= 200 && rr.Code < 300 {
		return nil
	}
	return errors.New(formatProbeFailure(rr.Code, rr.Body.String()))
}

func updateProbeResult(p ProviderConfig, model string, probeErr error, latency int64, autoPublish bool) ProviderConfig {
	available := sliceSet(p.AvailableModels)
	if probeErr == nil {
		available[model] = true
	} else {
		delete(available, model)
	}
	p.AvailableModels = orderedIntersection(p.Models, keysFromSet(available))
	if p.ModelLatencyMS == nil {
		p.ModelLatencyMS = map[string]int64{}
	}
	p.ModelLatencyMS[model] = latency
	if p.ModelErrors == nil {
		p.ModelErrors = map[string]string{}
	}
	if probeErr == nil {
		delete(p.ModelErrors, model)
	} else {
		p.ModelErrors[model] = probeErr.Error()
	}
	p.AvailabilityCheckedAt = time.Now().Unix()
	if autoPublish {
		p.EnabledModels = append([]string(nil), p.AvailableModels...)
		clearManualPublishOverride(&p)
	}
	return p
}

func formatProbeFailure(status int, body string) string {
	body = strings.TrimSpace(body)
	lower := strings.ToLower(body)
	switch {
	case strings.Contains(lower, "insufficient_credits") || strings.Contains(lower, "insufficient credits"):
		return "额度不足"
	case strings.Contains(lower, "promotion has ended") || strings.Contains(lower, "quota"):
		return "免费额度已结束"
	case strings.Contains(lower, "context deadline exceeded") || strings.Contains(lower, "timeout"):
		return "请求超时"
	case strings.Contains(lower, "rate limit") || strings.Contains(lower, "too many"):
		return "请求过于频繁"
	case strings.Contains(lower, "forbidden") || strings.Contains(lower, "permission") || strings.Contains(lower, "not allowed"):
		return "权限不足"
	case strings.Contains(lower, "unauthorized") || strings.Contains(lower, "invalid api key"):
		return "权限不足"
	case strings.Contains(lower, "model") && strings.Contains(lower, "not"):
		return "模型暂不可用"
	case status == http.StatusBadGateway || status == http.StatusGatewayTimeout:
		return "上游响应异常"
	case status == http.StatusTooManyRequests:
		return "请求过于频繁"
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "权限不足"
	}
	if body != "" {
		return truncateString(body, 60)
	}
	return fmt.Sprintf("请求失败(%d)", status)
}

func isQuotaKeyError(status int, body []byte) bool {
	lower := strings.ToLower(string(body))
	if strings.Contains(lower, "insufficient_credits") ||
		strings.Contains(lower, "insufficient credits") ||
		strings.Contains(lower, "insufficient_quota") ||
		strings.Contains(lower, "insufficient quota") ||
		strings.Contains(lower, "quota exceeded") ||
		strings.Contains(lower, "quota_exceeded") ||
		strings.Contains(lower, "promotion has ended") ||
		strings.Contains(lower, "free promotion has ended") ||
		strings.Contains(lower, "credits") ||
		strings.Contains(lower, "billing") ||
		strings.Contains(lower, "balance") {
		return true
	}
	return status == http.StatusPaymentRequired && (strings.Contains(lower, "credit") || strings.Contains(lower, "quota"))
}

func (s *Server) probeAllProviders(ctx context.Context, autoPublish bool) {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	for _, p := range s.enabledProviders() {
		models := s.visibleModelsForProvider(ctx, p)
		if len(models) == 0 {
			continue
		}
		available, failures, latencies := s.probeProviderModels(ctx, p, models)
		p.AvailableModels = uniqueStrings(available)
		p.ModelLatencyMS = latencies
		p.ModelErrors = failures
		p.AvailabilityCheckedAt = time.Now().Unix()
		if autoPublish {
			p.EnabledModels = append([]string(nil), p.AvailableModels...)
			clearManualPublishOverride(&p)
		}
		_ = s.updateProvider(p)
	}
}

func (s *Server) autoProbeLoop() {
	var nextRun time.Time
	var lastInterval time.Duration
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		cfg := s.currentConfig()
		if !cfg.AutoProbeEnabled || cfg.AutoProbeIntervalMinutes <= 0 {
			nextRun = time.Time{}
			lastInterval = 0
			continue
		}
		interval := time.Duration(cfg.AutoProbeIntervalMinutes) * time.Minute
		if nextRun.IsZero() || interval != lastInterval {
			lastInterval = interval
			nextRun = time.Now().Add(randomizedProbeInterval(interval))
			continue
		}
		if time.Now().Before(nextRun) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), randomizedProbeInterval(interval))
		s.probeAllProviders(ctx, true)
		cancel()
		nextRun = time.Now().Add(randomizedProbeInterval(interval))
	}
}

func wantsHTML(r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("view")), "html") {
		return true
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	return strings.Contains(accept, "text/html")
}

func wantsJSON(r *http.Request) bool {
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "json" || strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("view")), "json") {
		return true
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	return strings.Contains(accept, "application/json") && !strings.Contains(accept, "text/html")
}

func validateConfig(cfg Config) error {
	seen := map[string]bool{}
	for _, p := range cfg.Providers {
		if p.ID == "" {
			return errors.New("provider id is required")
		}
		if strings.Contains(p.ID, "/") || strings.ContainsAny(p.ID, " \t\r\n") {
			return fmt.Errorf("invalid provider id: %s", p.ID)
		}
		if seen[p.ID] {
			return fmt.Errorf("duplicate provider id: %s", p.ID)
		}
		seen[p.ID] = true
	}
	return nil
}

func isCustomOpenAIProvider(p ProviderConfig) bool {
	return p.Type == "openai" && (p.ID == "custom" || strings.HasPrefix(p.ID, "custom-") || strings.HasPrefix(p.ID, "custom_") || strings.HasPrefix(p.ID, "custom"))
}

func extractBearerToken(header string) string {
	header = strings.TrimSpace(header)
	if len(header) < 8 || !strings.EqualFold(header[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(header[7:])
}

func sliceSet(items []string) map[string]bool {
	out := map[string]bool{}
	for _, item := range uniqueStrings(items) {
		out[item] = true
	}
	return out
}

func keysFromSet(items map[string]bool) []string {
	var out []string
	for item, ok := range items {
		if ok {
			out = append(out, item)
		}
	}
	sort.Strings(out)
	return out
}

func providerManualPublishOverride(p ProviderConfig) bool {
	return p.ProviderSpecificData != nil && p.ProviderSpecificData["manualPublishOverride"] == "true"
}

func clearManualPublishOverride(p *ProviderConfig) {
	if p.ProviderSpecificData == nil {
		return
	}
	delete(p.ProviderSpecificData, "manualPublishOverride")
	if len(p.ProviderSpecificData) == 0 {
		p.ProviderSpecificData = nil
	}
}

func randomizedProbeInterval(base time.Duration) time.Duration {
	if base <= 0 {
		return base
	}
	var b [1]byte
	if _, err := rand.Read(b[:]); err != nil {
		return base
	}
	percent := 70 + int(b[0])%61
	d := base * time.Duration(percent) / 100
	if d < time.Minute {
		return time.Minute
	}
	return d
}

func orderedIntersection(base, picked []string) []string {
	allowed := sliceSet(picked)
	var out []string
	for _, item := range uniqueStrings(base) {
		if allowed[item] {
			out = append(out, item)
		}
	}
	return out
}

func acceptHeader(stream bool) string {
	if stream {
		return "text/event-stream"
	}
	return "application/json"
}

func joinURL(baseURL, suffix string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return strings.TrimRight(baseURL, "/") + suffix
	}
	u.Path = strings.TrimRight(u.Path, "/") + suffix
	return u.String()
}

func mediaEndpoint(p ProviderConfig, kind string) string {
	switch kind {
	case "image":
		if strings.TrimSpace(p.ImageEndpoint) != "" {
			return strings.TrimSpace(p.ImageEndpoint)
		}
		return strings.TrimSpace(p.ImageBaseURL)
	case "video":
		if strings.TrimSpace(p.VideoEndpoint) != "" {
			return strings.TrimSpace(p.VideoEndpoint)
		}
		return strings.TrimSpace(p.VideoBaseURL)
	case "audio":
		if strings.TrimSpace(p.AudioEndpoint) != "" {
			return strings.TrimSpace(p.AudioEndpoint)
		}
		return strings.TrimSpace(p.AudioBaseURL)
	default:
		return ""
	}
}

func mediaModelsForKind(models []string, kind string) []string {
	var needles []string
	switch kind {
	case "image":
		needles = []string{"image", "imagen", "flux", "sdxl", "dall-e", "gpt-image", "agnes-image"}
	case "video":
		needles = []string{"video", "veo", "sora", "kling", "runway", "agnes-video"}
	case "audio":
		needles = []string{"audio", "tts", "speech", "voice", "music", "lyria", "whisper"}
	default:
		return nil
	}
	var out []string
	for _, model := range uniqueStrings(models) {
		lower := strings.ToLower(model)
		for _, needle := range needles {
			if strings.Contains(lower, needle) {
				out = append(out, model)
				break
			}
		}
	}
	return out
}

func (s *Server) mediaToolDefinition(kind, base string) map[string]any {
	var name, path, description string
	var schema map[string]any
	var example map[string]any
	switch kind {
	case "image":
		name = "generate_image"
		path = "/v1/images"
		description = "Generate an image from a text prompt."
		schema = map[string]any{
			"model":  "provider/model",
			"prompt": "text prompt",
			"size":   "optional image size, for example 1024x1024",
		}
		example = map[string]any{
			"model":  firstMediaModel(s.enabledProviders(), kind),
			"prompt": "a futuristic city at sunset",
			"size":   "1024x1024",
		}
	case "video":
		name = "generate_video"
		path = "/v1/videos"
		description = "Generate a video from a text prompt."
		schema = map[string]any{
			"model":  "provider/model",
			"prompt": "text prompt",
		}
		example = map[string]any{
			"model":  firstMediaModel(s.enabledProviders(), kind),
			"prompt": "a cinematic shot of waves crashing on black rocks",
		}
	case "audio":
		name = "generate_audio"
		path = "/v1/audio"
		description = "Generate or process audio with the configured upstream audio endpoint."
		schema = map[string]any{
			"model": "provider/model",
			"input": "text or audio input, depending on the upstream endpoint",
			"voice": "optional voice name for TTS endpoints",
		}
		example = map[string]any{
			"model": firstMediaModel(s.enabledProviders(), kind),
			"input": "Hello, this is an audio generation test.",
			"voice": "alloy",
		}
	default:
		return nil
	}

	var models []string
	for _, p := range s.enabledProviders() {
		if mediaEndpoint(p, kind) == "" {
			continue
		}
		for _, model := range mediaModelsForKind(p.Models, kind) {
			models = append(models, p.ID+"/"+model)
		}
	}
	models = uniqueStrings(models)
	if len(models) == 0 {
		return nil
	}
	return map[string]any{
		"name":        name,
		"type":        kind,
		"method":      http.MethodPost,
		"endpoint":    base + path,
		"description": description,
		"models":      models,
		"schema":      schema,
		"example":     example,
	}
}

func firstMediaModel(providers []ProviderConfig, kind string) string {
	for _, p := range providers {
		if mediaEndpoint(p, kind) == "" {
			continue
		}
		models := mediaModelsForKind(p.Models, kind)
		if len(models) > 0 {
			return p.ID + "/" + models[0]
		}
	}
	return "provider/model"
}

func requestBaseURL(r *http.Request) string {
	proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if proto == "" {
		if r.TLS != nil {
			proto = "https"
		} else {
			proto = "http"
		}
	}
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = r.Host
	}
	return proto + "://" + host
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func removeString(in []string, target string) []string {
	var out []string
	for _, item := range uniqueStrings(in) {
		if item != target {
			out = append(out, item)
		}
	}
	return out
}

func renderHealthHTML(status HealthStatus) string {
	autoActive := status.AutoModel.Active
	if autoActive == "" {
		autoActive = "未命中"
	}
	autoState := "停用"
	if status.AutoModel.Enabled && status.AutoModel.OK {
		autoState = "正常"
	} else if status.AutoModel.Enabled {
		autoState = "无可用目标"
	}
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>9Router Lite Health</title><style>body{font-family:system-ui,-apple-system,Segoe UI,sans-serif;margin:0;background:#fafafa;color:#111}main{max-width:980px;margin:32px auto;padding:0 20px}h1{font-size:26px;margin:0 0 8px}.muted{color:#666;font-size:13px}.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:12px;margin:18px 0}.card{background:#fff;border:1px solid #ddd;border-radius:8px;padding:14px}.num{font-size:28px;font-weight:750;margin-top:4px}.ok{color:#047857}.warn{color:#a16207}.bad{color:#b91c1c}table{width:100%;border-collapse:collapse;background:#fff;border:1px solid #ddd;border-radius:8px;overflow:hidden}th,td{text-align:left;border-bottom:1px solid #eee;padding:10px;font-size:13px}th{background:#f3f4f6;color:#444}code{background:#eee;padding:2px 5px;border-radius:4px}</style></head><body><main>`)
	b.WriteString(`<h1>9Router Lite 健康状态</h1><div class="muted">程序请求 JSON 地址：<code>/health?format=json</code></div>`)
	b.WriteString(`<div class="grid">`)
	b.WriteString(fmt.Sprintf(`<div class="card"><div class="muted">服务</div><div class="num ok">%s</div></div>`, htmlEscape(status.Service)))
	b.WriteString(fmt.Sprintf(`<div class="card"><div class="muted">已连接源</div><div class="num">%d</div></div>`, status.ConnectedCount))
	b.WriteString(fmt.Sprintf(`<div class="card"><div class="muted">已发布模型</div><div class="num">%d</div></div>`, status.PublishedModels))
	b.WriteString(fmt.Sprintf(`<div class="card"><div class="muted">Auto</div><div class="num">%s</div><div class="muted">%s</div></div>`, htmlEscape(autoState), htmlEscape(autoActive)))
	b.WriteString(`</div><table><thead><tr><th>Provider</th><th>类型</th><th>已加载</th><th>可用</th><th>已发布</th><th>认证</th></tr></thead><tbody>`)
	if len(status.Connected) == 0 {
		b.WriteString(`<tr><td colspan="6" class="muted">暂无已连接源</td></tr>`)
	}
	for _, p := range status.Connected {
		authClass := "ok"
		if p.AuthStatus != "ok" {
			authClass = "warn"
		}
		b.WriteString(`<tr>`)
		b.WriteString(`<td><strong>` + htmlEscape(p.Name) + `</strong><div class="muted">` + htmlEscape(p.ID) + `</div></td>`)
		b.WriteString(`<td>` + htmlEscape(p.Type) + `</td>`)
		b.WriteString(fmt.Sprintf(`<td>%d</td><td>%d</td><td>%d</td>`, p.Models, p.AvailableModels, p.PublishedModels))
		b.WriteString(`<td class="` + authClass + `">` + htmlEscape(p.AuthStatus) + `</td>`)
		b.WriteString(`</tr>`)
	}
	b.WriteString(`</tbody></table></main></body></html>`)
	return b.String()
}

func renderAdminLoginHTML(message string) string {
	msg := ""
	if strings.TrimSpace(message) != "" {
		msg = `<div class="err">` + htmlEscape(message) + `</div>`
	}
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>9Router Lite 登录</title><style>body{font-family:system-ui,-apple-system,Segoe UI,sans-serif;margin:0;background:#fafafa;color:#111;min-height:100vh;display:grid;place-items:center}.box{width:min(420px,calc(100vw - 32px));background:#fff;border:1px solid #ddd;border-radius:8px;padding:24px;box-sizing:border-box}h1{font-size:26px;margin:0 0 8px}.muted{color:#666;font-size:13px;margin-bottom:18px}.field{display:grid;gap:6px;margin:12px 0}.field label{font-size:13px;color:#444}.field input{width:100%;box-sizing:border-box;padding:11px 12px;border:1px solid #ddd;border-radius:6px;font:16px/1.3 system-ui,-apple-system,Segoe UI,sans-serif}button{width:100%;background:#111;color:#fff;border:0;border-radius:6px;padding:11px 14px;font:inherit;cursor:pointer;margin-top:8px}.err{color:#b91c1c;font-size:13px;margin:10px 0}.hint{color:#666;font-size:12px;margin-top:14px;line-height:1.5}code{background:#eee;padding:2px 5px;border-radius:4px}</style></head><body><form class="box" method="post" action="/admin"><h1>9Router Lite</h1><div class="muted">请输入管理密码访问后台</div>` + msg + `<div class="field"><label>管理密码</label><input name="password" type="password" autocomplete="current-password" autofocus></div><button type="submit">登录</button><div class="hint">优先使用环境变量 <code>ADMIN_PASSWORD</code>；未设置时使用网关访问密钥；首次未设置访问密钥时默认是 <code>123456</code>。</div></form></body></html>`
}

func renderModelsHTML(grouped map[string][]string) string {
	type row struct {
		Name   string
		Models []string
	}
	var rows []row
	for name, models := range grouped {
		if len(models) == 0 {
			continue
		}
		rows = append(rows, row{Name: name, Models: models})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>模型列表</title><style>body{font-family:system-ui,-apple-system,Segoe UI,sans-serif;margin:0;background:#fafafa;color:#111}main{max-width:980px;margin:32px auto;padding:0 20px}h1{font-size:26px;margin:0 0 8px}.item{background:#fff;border:1px solid #ddd;border-radius:8px;padding:14px;margin:12px 0}.name{font-size:17px;font-weight:700}.meta{font-size:12px;color:#666;margin:4px 0 10px}.models{font:13px/1.5 ui-monospace,SFMono-Regular,Consolas,monospace;white-space:pre-wrap}</style></head><body><main><h1>模型列表</h1><div class="meta">按已连接且已发布的 API 源分组展示，只显示当前可用的模型。</div>`)
	for _, row := range rows {
		b.WriteString(`<div class="item"><div class="name">`)
		b.WriteString(htmlEscape(row.Name))
		b.WriteString(`</div><div class="meta">`)
		b.WriteString(fmt.Sprintf("共 %d 个模型，每行一个。", len(row.Models)))
		b.WriteString(`</div><div class="models">`)
		for i, model := range row.Models {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(htmlEscape(model))
		}
		b.WriteString(`</div></div>`)
	}
	b.WriteString(`</main></body></html>`)
	return b.String()
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func newSessionID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "ses_" + fmt.Sprint(time.Now().UnixNano())
	}
	for i := range buf {
		buf[i] = chars[int(buf[i])%len(chars)]
	}
	return "ses_" + string(buf)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func copyFlush(w http.ResponseWriter, r io.Reader) (int64, error) {
	buf := make([]byte, 32*1024)
	var written int64
	flusher, _ := w.(http.Flusher)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			m, werr := w.Write(buf[:n])
			written += int64(m)
			if flusher != nil {
				flusher.Flush()
			}
			if werr != nil {
				return written, werr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return written, nil
			}
			return written, err
		}
	}
}

func shouldSkipHeader(k string) bool {
	switch strings.ToLower(k) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func envDefault(k, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return fallback
}

func tuneRuntime() {
	debug.SetGCPercent(50)
	limitMB := int64(24)
	if raw := strings.TrimSpace(os.Getenv("LITE_MEMORY_LIMIT_MB")); raw != "" {
		var parsed int64
		if _, err := fmt.Sscan(raw, &parsed); err == nil && parsed > 0 {
			limitMB = parsed
		}
	}
	debug.SetMemoryLimit(limitMB << 20)
}

func preferIPv4DialContext() func(ctx context.Context, network, address string) (net.Conn, error) {
	base := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 15 * time.Second,
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err == nil {
			ips, lookupErr := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if lookupErr == nil {
				for _, ip := range ips {
					if v4 := ip.To4(); v4 != nil {
						if conn, dialErr := base.DialContext(ctx, "tcp4", net.JoinHostPort(v4.String(), port)); dialErr == nil {
							return conn, nil
						}
					}
				}
			}
		}
		return base.DialContext(ctx, network, address)
	}
}

func logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

func adminHTML(configJSON string) string {
	escaped := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(configJSON)
	return `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>9Router Lite</title>
<style>
body{font-family:system-ui,-apple-system,Segoe UI,sans-serif;margin:0;background:#fafafa;color:#111}
main{max-width:980px;margin:32px auto;padding:0 20px}
h1{font-size:26px;margin:0 0 8px}
.bar{display:flex;gap:10px;align-items:center;margin:18px 0}
a,button{font:inherit}
button{background:#111;color:white;border:0;border-radius:6px;padding:9px 14px;cursor:pointer}
textarea{width:100%;min-height:620px;box-sizing:border-box;font:13px/1.45 ui-monospace,SFMono-Regular,Consolas,monospace;border:1px solid #ddd;border-radius:6px;padding:14px;background:white}
.muted{color:#666;font-size:13px}
.ok{color:#047857}.err{color:#b91c1c}
code{background:#eee;padding:2px 5px;border-radius:4px}
</style>
</head>
<body>
<main>
<h1>9Router Lite</h1>
<div class="muted">Base URL: <code>/v1</code>，配置保存在本机 data/config.json。</div>
<div class="bar">
<button onclick="save()">Save</button>
<a href="/v1/models" target="_blank">/v1/models</a>
<a href="/health" target="_blank">/health</a>
<span id="status" class="muted"></span>
</div>
<div class="bar">
<button onclick="startQoder()">Start Qoder Login</button>
<button onclick="pollQoder()">Poll Qoder Token</button>
<span id="qoderStatus" class="muted"></span>
</div>
<div class="bar">
<button onclick="startGemini()">Start Gemini Login</button>
<span id="geminiStatus" class="muted"></span>
</div>
<div class="bar">
<button onclick="startKilo()">Start Kilo Login</button>
<button onclick="pollKilo()">Poll Kilo Token</button>
<span id="kiloStatus" class="muted"></span>
</div>
<div class="bar">
<button onclick="startCline()">Start Cline Login</button>
<span id="clineStatus" class="muted"></span>
</div>
<textarea id="cfg" spellcheck="false">` + escaped + `</textarea>
<script>
async function save(){
  const s=document.getElementById('status');
  try{
    const body=JSON.parse(document.getElementById('cfg').value);
    const res=await fetch('/api/config',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(body)});
    const data=await res.json();
    if(!res.ok) throw new Error(data.error || res.statusText);
    s.className='ok'; s.textContent='saved';
  }catch(e){ s.className='err'; s.textContent=e.message; }
}
async function startQoder(){
  const s=document.getElementById('qoderStatus');
  try{
    const data=await (await fetch('/api/oauth/qoder/device-code')).json();
    localStorage.setItem('qoder_flow', JSON.stringify(data));
    window.open(data.verification_uri_complete, '_blank', 'noopener,noreferrer');
    s.className='ok'; s.textContent='Qoder login page opened. Finish login, then click Poll.';
  }catch(e){ s.className='err'; s.textContent=e.message; }
}
async function pollQoder(){
  const s=document.getElementById('qoderStatus');
  try{
    const flow=JSON.parse(localStorage.getItem('qoder_flow')||'{}');
    if(!flow.device_code || !flow.codeVerifier) throw new Error('Start Qoder Login first');
    const res=await fetch('/api/oauth/qoder/poll',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({
      deviceCode: flow.device_code,
      codeVerifier: flow.codeVerifier,
      extraData: {_qoderMachineId: flow._qoderMachineId, _qoderNonce: flow._qoderNonce, _qoderVerifier: flow.codeVerifier}
    })});
    const data=await res.json();
    if(data.pending){ s.className='muted'; s.textContent='Still waiting for authorization'; return; }
    if(!res.ok || !data.success) throw new Error(data.errorDescription || data.error || 'poll failed');
    localStorage.removeItem('qoder_flow');
    s.className='ok'; s.textContent='Qoder connected. Refreshing config...';
    location.reload();
  }catch(e){ s.className='err'; s.textContent=e.message; }
}
async function startKilo(){
  const s=document.getElementById('kiloStatus');
  try{
    const data=await (await fetch('/api/oauth/kilo/device-code')).json();
    localStorage.setItem('kilo_flow', JSON.stringify(data));
    window.open(data.verification_uri_complete, '_blank', 'noopener,noreferrer');
    s.className='ok'; s.textContent='Kilo login page opened. Finish login, then click Poll.';
  }catch(e){ s.className='err'; s.textContent=e.message; }
}
async function pollKilo(){
  const s=document.getElementById('kiloStatus');
  try{
    const flow=JSON.parse(localStorage.getItem('kilo_flow')||'{}');
    if(!flow.device_code) throw new Error('Start Kilo Login first');
    const res=await fetch('/api/oauth/kilo/poll',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({deviceCode: flow.device_code})});
    const data=await res.json();
    if(data.pending){ s.className='muted'; s.textContent='Still waiting for authorization'; return; }
    if(!res.ok || !data.success) throw new Error(data.errorDescription || data.error || 'poll failed');
    localStorage.removeItem('kilo_flow');
    s.className='ok'; s.textContent='Kilo connected. Refreshing config...';
    location.reload();
  }catch(e){ s.className='err'; s.textContent=e.message; }
}
async function startCline(){
  const s=document.getElementById('clineStatus');
  try{
    const data=await (await fetch('/api/oauth/cline/authorize')).json();
    window.open(data.authUrl, '_blank', 'noopener,noreferrer');
    s.className='ok'; s.textContent='Cline login page opened. This page will refresh after callback saves the token.';
  }catch(e){ s.className='err'; s.textContent=e.message; }
}
</script>
</main>
</body>
</html>`
}

func adminHTMLLiteOld(configJSON string) string {
	escaped := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(configJSON)
	return `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>9Router Lite</title>
<style>
body{font-family:system-ui,-apple-system,Segoe UI,sans-serif;margin:0;background:#fafafa;color:#111}
main{max-width:1080px;margin:32px auto;padding:0 20px}
h1{font-size:28px;margin:0 0 8px}
h2{font-size:18px;margin:26px 0 10px}
.bar{display:flex;gap:10px;align-items:center;margin:16px 0;flex-wrap:wrap}
a,button{font:inherit}
button{background:#111;color:white;border:0;border-radius:6px;padding:9px 14px;cursor:pointer}
button.secondary{background:#fff;color:#111;border:1px solid #ddd}
button.small{padding:7px 10px;font-size:13px}
textarea{width:100%;min-height:420px;box-sizing:border-box;font:13px/1.45 ui-monospace,SFMono-Regular,Consolas,monospace;border:1px solid #ddd;border-radius:6px;padding:14px;background:white}
.muted{color:#666;font-size:13px}
.ok{color:#047857}
.err{color:#b91c1c}
code{background:#eee;padding:2px 5px;border-radius:4px}
.panel-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(280px,1fr));gap:12px;margin:12px 0 18px}
.card{background:#fff;border:1px solid #ddd;border-radius:8px;padding:14px}
.card h3{margin:0 0 6px;font-size:17px}
.api-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(280px,1fr));gap:12px;margin:10px 0 22px}
.api-card{background:#fff;border:1px solid #ddd;border-radius:8px;padding:14px}
.api-head{display:flex;justify-content:space-between;gap:12px;align-items:center;margin-bottom:10px}
.api-meta{font-size:12px;color:#666}
.field{display:grid;gap:6px;margin:10px 0}
.field label{font-size:13px;color:#444}
.field input{width:100%;box-sizing:border-box;padding:9px 10px;border:1px solid #ddd;border-radius:6px;background:#fff;font:13px/1.3 ui-monospace,SFMono-Regular,Consolas,monospace}
.toggle{display:flex;gap:8px;align-items:center;font-size:13px;margin:8px 0}
.model-list{display:grid;gap:8px;margin-top:10px;max-height:280px;overflow:auto;padding-right:4px}
.model-item{display:flex;gap:8px;align-items:flex-start;font-size:13px}
.model-item.off{color:#999}
.model-name{font-family:ui-monospace,SFMono-Regular,Consolas,monospace;word-break:break-all}
.mono{font-family:ui-monospace,SFMono-Regular,Consolas,monospace;white-space:pre-wrap}
details{margin-top:22px}
</style>
</head>
<body>
<main>
<h1>9Router Lite</h1>
<div class="muted">接口基址：<code>/v1</code>。配置保存在 <code>data/config.json</code>。</div>
<div class="bar">
<button onclick="saveGateway()">保存网关设置</button>
<button class="secondary" onclick="openModelsPage()">打开模型页</button>
<a href="/health" target="_blank">/health</a>
<span id="status" class="muted"></span>
</div>
<div class="field">
<label>访问密钥</label>
<input id="accessKey" type="password" placeholder="第三方 Agent 访问 /v1 时需要带这个 key">
</div>
<h2>已连接源</h2>
<div id="providerStatus" class="panel-grid"></div>
<h2>OAuth 登录</h2>
<div class="bar">
<button onclick="startQoder()">开始 Qoder 登录</button>
<button onclick="pollQoder()">轮询 Qoder 令牌</button>
<span id="qoderStatus" class="muted"></span>
</div>
<div class="bar">
<button onclick="startGemini()">开始 Gemini 登录</button>
<span id="geminiStatus" class="muted"></span>
</div>
<div class="bar">
<button onclick="startKilo()">开始 Kilo 登录</button>
<button onclick="pollKilo()">轮询 Kilo 令牌</button>
<span id="kiloStatus" class="muted"></span>
</div>
<div class="bar">
<button onclick="startCline()">开始 Cline 登录</button>
<span id="clineStatus" class="muted"></span>
</div>
<h2>API 密钥提供商</h2>
<div class="muted">支持 <code>GLM</code>、<code>Groq</code>、<code>DeepSeek</code>、<code>Xiaomi MiMo</code> 和 <code>自定义 OpenAI Compatible</code>。</div>
<div id="apiProviders" class="api-grid"></div>
<h2>模型发布</h2>
<div class="muted">只会把你勾选且探测可用的模型加入 <code>/v1/models</code>。</div>
<div id="publishProviders" class="panel-grid"></div>
<details>
<summary>原始配置</summary>
<div class="bar"><button class="secondary" onclick="save()">保存原始配置</button></div>
<textarea id="cfg" spellcheck="false">` + escaped + `</textarea>
</details>
<script>
const apiProviderIDs=['glm','groq','deepseek','mimo','custom'];
const statusProviderIDs=['oc','mmf','qoder','gemini','kilo','cline','glm','groq','deepseek','mimo','custom'];
function parseConfig(){ try { return JSON.parse(document.getElementById('cfg').value); } catch { return null; } }
function setConfig(cfg){ document.getElementById('cfg').value=JSON.stringify(cfg,null,2); document.getElementById('accessKey').value=cfg.access_key || ''; }
function providerConnected(p){ return !!(p && p.enabled && (p.api_key || p.access_token || p.type === 'opencode-free' || p.type === 'mimo-free')); }
function esc(v){ return String(v || '').replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('>','&gt;').replaceAll('"','&quot;'); }
function unique(arr){ return [...new Set((arr || []).filter(Boolean))]; }
function selectedModels(p){ return unique((p && p.enabled_models && p.enabled_models.length) ? p.enabled_models : (p && p.models) || []); }
function availableModels(p){ return unique((p && p.available_models) || []); }
function visibleModels(p){ const selected=selectedModels(p); const available=availableModels(p); if(!available.length) return selected; const set=new Set(available); return selected.filter(x=>set.has(x)); }
function loadedModelsText(p){ return unique((p && p.models) || []).join('<br>'); }
function setText(id, cls, text){ const el=document.getElementById(id); if(!el) return; el.className=cls; el.textContent=text; }
function renderProviderStatus(){
  const root=document.getElementById('providerStatus'); const cfg=parseConfig();
  if(!cfg || !Array.isArray(cfg.providers)){ root.innerHTML=''; return; }
  const items=statusProviderIDs.map(id=>{
    const p=cfg.providers.find(x=>x.id===id);
    if(!p || !providerConnected(p)) return '';
    const loaded=unique(p.models || []); const available=availableModels(p); const published=visibleModels(p);
    return '<div class="card"><h3>'+esc(p.name)+'</h3><div class="muted">已连接</div><div class="muted">已加载 '+loaded.length+' 个模型，可用 '+(available.length || loaded.length)+' 个，已发布 '+published.length+' 个</div>'+(loaded.length?'<div class="muted" style="margin-top:10px">已加载模型</div><div class="mono">'+loadedModelsText(p)+'</div>':'')+'</div>';
  }).filter(Boolean);
  root.innerHTML=items.join('') || '<div class="muted">当前没有已连接的 provider。</div>';
}
function renderAPIProviders(){
  const root=document.getElementById('apiProviders'); const cfg=parseConfig();
  if(!cfg || !Array.isArray(cfg.providers)){ root.innerHTML=''; return; }
  root.innerHTML=apiProviderIDs.map(id=>{
    const p=cfg.providers.find(x=>x.id===id); if(!p) return '';
    return '<div class="api-card"><div class="api-head"><strong>'+esc(p.name)+'</strong><span class="api-meta">'+esc(p.id)+'</span></div>'+(id==='custom'?'<div class="field"><label>名称</label><input id="name_'+id+'" value="'+esc(p.name || '')+'" placeholder="自定义源"></div>':'')+'<label class="toggle"><input type="checkbox" id="enabled_'+id+'" '+(p.enabled?'checked':'')+'> 启用</label><div class="field"><label>Base URL</label><input id="base_'+id+'" value="'+esc(p.base_url || '')+'" placeholder="https://example.com/v1"></div><div class="field"><label>API Key</label><input id="key_'+id+'" type="password" value="'+esc(p.api_key || '')+'" placeholder="sk-..."></div><div class="bar"><button onclick="saveAPIProvider(\''+id+'\')">保存</button><button class="secondary" onclick="fetchAPIProviderModels(\''+id+'\')">拉取模型</button><span id="apiStatus_'+id+'" class="muted"></span></div>'+(Array.isArray(p.models) && p.models.length ? '<div class="muted">已加载 '+p.models.length+' 个模型，可在下方“模型发布”里选择是否加入 <code>/v1/models</code>。</div>' : '')+'</div>';
  }).join('');
}
function renderPublishProviders(){
  const root=document.getElementById('publishProviders'); const cfg=parseConfig();
  if(!cfg || !Array.isArray(cfg.providers)){ root.innerHTML=''; return; }
  const items=cfg.providers.filter(p=>providerConnected(p) && Array.isArray(p.models) && p.models.length).map(p=>{
    const selected=new Set(selectedModels(p)); const available=new Set(availableModels(p)); const hasAvailability=available.size>0; const models=unique(p.models);
    const rows=models.map(model=>{ const usable=!hasAvailability || available.has(model); const checked=selected.has(model); return '<label class="model-item '+(usable?'':'off')+'"><input type="checkbox" data-provider="'+esc(p.id)+'" data-model="'+esc(model)+'" '+(checked?'checked':'')+' '+(usable?'':'disabled')+'><span class="model-name">'+esc(model)+(usable?'':'（不可用）')+'</span></label>'; }).join('');
    return '<div class="card"><h3>'+esc(p.name)+'</h3><div class="muted">已加载 '+models.length+' 个模型，当前发布 '+visibleModels(p).length+' 个</div><div class="bar"><button class="small" onclick="probeProvider(\''+p.id+'\')">探测可用性</button><button class="small secondary" onclick="saveModelSelection(\''+p.id+'\')">保存发布列表</button><span id="publishStatus_'+p.id+'" class="muted"></span></div><div class="model-list">'+rows+'</div></div>';
  });
  root.innerHTML=items.join('') || '<div class="muted">还没有可发布的模型。</div>';
}
async function reloadConfig(){ const res=await fetch('/api/config'); const cfg=await res.json(); setConfig(cfg); renderProviderStatus(); renderAPIProviders(); renderPublishProviders(); }
function buildAPIProvider(id){
  const cfg=parseConfig(); if(!cfg || !Array.isArray(cfg.providers)) throw new Error('config is invalid');
  const prev=cfg.providers.find(x=>x.id===id); if(!prev) throw new Error('provider not found: '+id);
  return { ...prev, name:id==='custom'?(document.getElementById('name_'+id).value.trim() || prev.name || 'Custom OpenAI Compatible'):prev.name, enabled:!!document.getElementById('enabled_'+id).checked, base_url:document.getElementById('base_'+id).value.trim(), api_key:document.getElementById('key_'+id).value.trim(), fetch_models:!!((prev && prev.models && prev.models.length) || prev.fetch_models) };
}
async function saveConfigObject(cfg){ const res=await fetch('/api/config',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(cfg)}); const data=await res.json(); if(!res.ok) throw new Error(data.error || res.statusText); return data; }
function applyAccessKey(cfg){ cfg.access_key=document.getElementById('accessKey').value.trim(); return cfg; }
async function saveGateway(){ try{ const cfg=parseConfig(); if(!cfg) throw new Error('config is invalid'); applyAccessKey(cfg); await saveConfigObject(cfg); await reloadConfig(); setText('status','ok','已保存'); }catch(e){ setText('status','err',e.message); } }
function openModelsPage(){ const key=document.getElementById('accessKey').value.trim(); if(!key){ setText('status','err','请先设置访问密钥'); return; } window.open('/v1/models?view=html&key='+encodeURIComponent(key),'_blank','noopener,noreferrer'); }
async function saveAPIProvider(id){ try{ const cfg=parseConfig(); if(!cfg || !Array.isArray(cfg.providers)) throw new Error('config is invalid'); applyAccessKey(cfg); const next=buildAPIProvider(id); cfg.providers=cfg.providers.map(p=>p.id===id?next:p); await saveConfigObject(cfg); await reloadConfig(); setText('apiStatus_'+id,'ok','已保存'); }catch(e){ setText('apiStatus_'+id,'err',e.message); } }
async function fetchAPIProviderModels(id){ try{ setText('apiStatus_'+id,'muted','正在保存...'); const cfg=parseConfig(); if(!cfg || !Array.isArray(cfg.providers)) throw new Error('config is invalid'); applyAccessKey(cfg); const next=buildAPIProvider(id); cfg.providers=cfg.providers.map(p=>p.id===id?next:p); await saveConfigObject(cfg); setText('apiStatus_'+id,'muted','正在拉取模型...'); const res=await fetch('/api/provider/models',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({id})}); const data=await res.json(); if(!res.ok) throw new Error(data.error || res.statusText); await reloadConfig(); setText('apiStatus_'+id,'ok','已拉取 '+(data.count || 0)+' 个模型'); }catch(e){ setText('apiStatus_'+id,'err',e.message); } }
async function probeProvider(id){ try{ setText('publishStatus_'+id,'muted','正在探测...'); const res=await fetch('/api/provider/probe',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({id})}); const data=await res.json(); if(!res.ok) throw new Error(data.error || res.statusText); await reloadConfig(); setText('publishStatus_'+id,'ok','可用 '+(data.available_count || 0)+' 个模型'); }catch(e){ setText('publishStatus_'+id,'err',e.message); } }
async function saveModelSelection(id){ try{ const nodes=[...document.querySelectorAll('input[data-provider="'+id+'"]:checked')]; const enabled_models=nodes.map(node=>node.getAttribute('data-model')); const res=await fetch('/api/provider/selection',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({id, enabled_models})}); const data=await res.json(); if(!res.ok) throw new Error(data.error || res.statusText); await reloadConfig(); setText('publishStatus_'+id,'ok','已保存发布列表'); }catch(e){ setText('publishStatus_'+id,'err',e.message); } }
async function save(){ try{ const body=JSON.parse(document.getElementById('cfg').value); applyAccessKey(body); const res=await fetch('/api/config',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(body)}); const data=await res.json(); if(!res.ok) throw new Error(data.error || res.statusText); setText('status','ok','已保存'); renderProviderStatus(); renderAPIProviders(); renderPublishProviders(); }catch(e){ setText('status','err',e.message); } }
async function startQoder(){ try{ const data=await (await fetch('/api/oauth/qoder/device-code')).json(); localStorage.setItem('qoder_flow', JSON.stringify(data)); window.open(data.verification_uri_complete, '_blank', 'noopener,noreferrer'); setText('qoderStatus','ok','已打开 Qoder 登录页，完成登录后点击轮询。'); }catch(e){ setText('qoderStatus','err',e.message); } }
async function pollQoder(){ try{ const flow=JSON.parse(localStorage.getItem('qoder_flow')||'{}'); if(!flow.device_code || !flow.codeVerifier) throw new Error('请先开始 Qoder 登录'); const res=await fetch('/api/oauth/qoder/poll',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({ deviceCode: flow.device_code, codeVerifier: flow.codeVerifier, extraData: {_qoderMachineId: flow._qoderMachineId, _qoderNonce: flow._qoderNonce, _qoderVerifier: flow.codeVerifier} })}); const data=await res.json(); if(data.pending){ setText('qoderStatus','muted','还在等待授权完成...'); return; } if(!res.ok || !data.success) throw new Error(data.errorDescription || data.error || 'poll failed'); localStorage.removeItem('qoder_flow'); await reloadConfig(); setText('qoderStatus','ok','Qoder 已连接。'); }catch(e){ setText('qoderStatus','err',e.message); } }
async function startGemini(){ try{ const data=await (await fetch('/api/oauth/gemini/authorize')).json(); if(!data.authUrl) throw new Error(data.error || 'missing auth url'); window.open(data.authUrl, '_blank', 'noopener,noreferrer'); setText('geminiStatus','ok','已打开 Gemini 登录页，回调完成后会自动保存令牌。'); }catch(e){ setText('geminiStatus','err',e.message); } }
async function startKilo(){ try{ const data=await (await fetch('/api/oauth/kilo/device-code')).json(); localStorage.setItem('kilo_flow', JSON.stringify(data)); window.open(data.verification_uri_complete, '_blank', 'noopener,noreferrer'); setText('kiloStatus','ok','已打开 Kilo 登录页，完成登录后点击轮询。'); }catch(e){ setText('kiloStatus','err',e.message); } }
async function pollKilo(){ try{ const flow=JSON.parse(localStorage.getItem('kilo_flow')||'{}'); if(!flow.device_code) throw new Error('请先开始 Kilo 登录'); const res=await fetch('/api/oauth/kilo/poll',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({deviceCode: flow.device_code})}); const data=await res.json(); if(data.pending){ setText('kiloStatus','muted','还在等待授权完成...'); return; } if(!res.ok || !data.success) throw new Error(data.errorDescription || data.error || 'poll failed'); localStorage.removeItem('kilo_flow'); await reloadConfig(); setText('kiloStatus','ok','Kilo 已连接。'); }catch(e){ setText('kiloStatus','err',e.message); } }
async function startCline(){ try{ const data=await (await fetch('/api/oauth/cline/authorize')).json(); window.open(data.authUrl, '_blank', 'noopener,noreferrer'); setText('clineStatus','ok','已打开 Cline 登录页，回调完成后会自动保存令牌。'); }catch(e){ setText('clineStatus','err',e.message); } }
const initCfg=parseConfig(); if(initCfg){ document.getElementById('accessKey').value=initCfg.access_key || ''; }
renderProviderStatus(); renderAPIProviders(); renderPublishProviders();
</script>
</main>
</body>
</html>`
}
