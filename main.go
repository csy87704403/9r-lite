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
	"strconv"
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
	ModelGroups              []ModelGroup     `json:"model_groups,omitempty"`
	DeletedProviderIDs       []string         `json:"deleted_provider_ids,omitempty"`
	Providers                []ProviderConfig `json:"providers"`
}

type AutoModelConfig struct {
	Enabled      bool     `json:"enabled,omitempty"`
	Models       []string `json:"models,omitempty"`
	VisionModels []string `json:"vision_models,omitempty"`
}

type ModelGroup struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	APIKey  string   `json:"api_key"`
	Enabled bool     `json:"enabled"`
	Models  []string `json:"models,omitempty"`
}

type accessScope struct {
	Full  bool
	Group *ModelGroup
}

type ProviderConfig struct {
	ID                    string            `json:"id"`
	Name                  string            `json:"name"`
	Type                  string            `json:"type"`
	Enabled               bool              `json:"enabled"`
	BaseURL               string            `json:"base_url,omitempty"`
	ImageEndpoint         string            `json:"image_endpoint,omitempty"`
	ImageEditEndpoint     string            `json:"image_edit_endpoint,omitempty"`
	VideoEndpoint         string            `json:"video_endpoint,omitempty"`
	AudioEndpoint         string            `json:"audio_endpoint,omitempty"`
	TTSEndpoint           string            `json:"tts_endpoint,omitempty"`
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
	LockedModels          []string          `json:"locked_models,omitempty"`
	ModelKinds            map[string]string `json:"model_kinds,omitempty"`
	AvailableModels       []string          `json:"available_models,omitempty"`
	ModelLatencyMS        map[string]int64  `json:"model_latency_ms,omitempty"`
	ModelErrors           map[string]string `json:"model_errors,omitempty"`
	AvailabilityCheckedAt int64             `json:"availability_checked_at,omitempty"`
	FetchModels           bool              `json:"fetch_models,omitempty"`
}

type HealthStatus struct {
	OK              bool                  `json:"ok"`
	Service         string                `json:"service"`
	Providers       int                   `json:"providers"`
	Connected       []HealthProvider      `json:"connected"`
	ConnectedCount  int                   `json:"connected_count"`
	PublishedModels int                   `json:"published_models"`
	AutoModel       HealthAutoModel       `json:"auto_model"`
	MediaTemplates  []HealthMediaTemplate `json:"media_templates,omitempty"`
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

type HealthMediaTemplate struct {
	Type           string                    `json:"type"`
	ProviderID     string                    `json:"provider_id"`
	ProviderName   string                    `json:"provider_name"`
	Model          string                    `json:"model"`
	UpstreamModel  string                    `json:"upstream_model"`
	Method         string                    `json:"method"`
	Endpoint       string                    `json:"endpoint"`
	RequestBody    map[string]any            `json:"request_body,omitempty"`
	Form           map[string]string         `json:"form,omitempty"`
	Curl           string                    `json:"curl"`
	ExtraTemplates []HealthMediaCurlTemplate `json:"extra_templates,omitempty"`
	Note           string                    `json:"note,omitempty"`
}

type HealthMediaCurlTemplate struct {
	Name        string            `json:"name"`
	Method      string            `json:"method"`
	Endpoint    string            `json:"endpoint"`
	RequestBody map[string]any    `json:"request_body,omitempty"`
	Form        map[string]string `json:"form,omitempty"`
	Curl        string            `json:"curl"`
	Note        string            `json:"note,omitempty"`
}

type Server struct {
	dataDir     string
	config      Config
	mu          sync.RWMutex
	probeMu     sync.Mutex
	client      *http.Client
	mimo        *MimoAuth
	adminMu     sync.RWMutex
	adminHash   []byte
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
	mux.HandleFunc("/admin/help", srv.handleAdminHelp)
	mux.HandleFunc("/api/admin/password", srv.handleAdminPassword)
	mux.HandleFunc("/api/config", srv.handleConfig)
	mux.HandleFunc("/api/provider/models", srv.handleProviderModels)
	mux.HandleFunc("/api/provider/probe", srv.handleProviderProbe)
	mux.HandleFunc("/api/provider/probe-model", srv.handleProviderProbeModel)
	mux.HandleFunc("/api/provider/probe-key", srv.handleProviderProbeKey)
	mux.HandleFunc("/api/provider/selection", srv.handleProviderSelection)
	mux.HandleFunc("/api/provider/model/delete", srv.handleProviderModelDelete)
	mux.HandleFunc("/api/oauth/qoder/device-code", srv.handleQoderDeviceCode)
	mux.HandleFunc("/api/oauth/qoder/poll", srv.handleQoderPoll)
	mux.HandleFunc("/api/oauth/kilo/device-code", srv.handleKiloDeviceCode)
	mux.HandleFunc("/api/oauth/kilo/poll", srv.handleKiloPoll)
	mux.HandleFunc("/api/oauth/cline/authorize", srv.handleClineAuthorize)
	mux.HandleFunc("/api/oauth/cline/callback", srv.handleClineCallback)
	mux.HandleFunc("/health", srv.handleHealth)
	mux.HandleFunc("/v1/models", srv.handleModels)
	mux.HandleFunc("/v2/models", srv.handleClaudeCodeModels)
	mux.HandleFunc("/v1/tools", srv.handleTools)
	mux.HandleFunc("/tools.json", srv.handleTools)
	mux.HandleFunc("/v1/chat/completions", srv.handleChatCompletions)
	mux.HandleFunc("/anthropic/v1/models", srv.handleAnthropicModels)
	mux.HandleFunc("/anthropic/v1/messages", srv.handleAnthropicMessages)
	mux.HandleFunc("/anthropic/v1/messages/count_tokens", srv.handleAnthropicCountTokens)
	mux.HandleFunc("/v1/images", srv.handleMedia("image"))
	mux.HandleFunc("/v1/images/models", srv.handleMediaModels("image"))
	mux.HandleFunc("/v1/videos", srv.handleMedia("video"))
	mux.HandleFunc("/v1/videos/models", srv.handleMediaModels("video"))
	mux.HandleFunc("/v1/audio", srv.handleMedia("audio"))
	mux.HandleFunc("/v1/audio/models", srv.handleMediaModels("audio"))
	mux.HandleFunc("/v1/tts/models", srv.handleMediaModels("tts"))

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

	client, err := newHTTPClient("")
	if err != nil {
		return nil, err
	}
	mimoClient := client
	if proxyAddress := strings.TrimSpace(os.Getenv("MIMO_PROXY_URL")); proxyAddress != "" {
		mimoClient, err = newHTTPClient(proxyAddress)
		if err != nil {
			return nil, fmt.Errorf("invalid MIMO_PROXY_URL")
		}
	}
	srv := &Server{
		dataDir:     dataDir,
		config:      cfg,
		client:      client,
		adminSecret: newSessionID(),
		mimo: &MimoAuth{
			sessionID: newSessionID(),
			client:    mimoClient,
		},
	}
	adminHash, err := loadAdminPasswordHash(dataDir)
	if err != nil {
		return nil, err
	}
	srv.adminHash = adminHash
	return srv, nil
}

func newHTTPClient(proxyAddress string) (*http.Client, error) {
	proxyFunc := http.ProxyFromEnvironment
	if proxyAddress = strings.TrimSpace(proxyAddress); proxyAddress != "" {
		proxyURL, err := url.Parse(proxyAddress)
		if err != nil || proxyURL.Host == "" {
			return nil, errors.New("invalid proxy URL")
		}
		switch strings.ToLower(proxyURL.Scheme) {
		case "http", "https", "socks5", "socks5h":
		default:
			return nil, errors.New("unsupported proxy URL scheme")
		}
		proxyFunc = http.ProxyURL(proxyURL)
	}
	return &http.Client{
		Timeout: defaultHTTPTimeout,
		Transport: &http.Transport{
			DisableKeepAlives:   true,
			MaxIdleConns:        0,
			MaxIdleConnsPerHost: 0,
			IdleConnTimeout:     5 * time.Second,
			DialContext:         preferIPv4DialContext(),
			Proxy:               proxyFunc,
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
			if !isCustomProvider(p) {
				continue
			}
			if p.Name == "" {
				p.Name = "Custom Compatible"
			}
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
		if (p.Type == "openai" || p.Type == "anthropic") && p.FetchModels && len(providerAPIKeys(p)) > 0 && len(p.Models) > 0 {
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
	normalizeConfigModelRefs(&cfg)
	return cfg
}

func normalizeConfigModelRefs(cfg *Config) {
	if cfg == nil {
		return
	}
	normalize := func(ref string) string {
		providerID, model, ok := strings.Cut(strings.TrimSpace(ref), "/")
		if !ok || providerID == "" || model == "" {
			return strings.TrimSpace(ref)
		}
		for _, p := range cfg.Providers {
			if providerID == p.ID || providerID == providerPublicID(p) {
				return providerModelRef(p, model)
			}
		}
		return strings.TrimSpace(ref)
	}
	for i, ref := range cfg.AutoModel.Models {
		cfg.AutoModel.Models[i] = normalize(ref)
	}
	cfg.AutoModel.Models = uniqueStrings(cfg.AutoModel.Models)
	for i, ref := range cfg.AutoModel.VisionModels {
		cfg.AutoModel.VisionModels[i] = normalize(ref)
	}
	cfg.AutoModel.VisionModels = uniqueStrings(cfg.AutoModel.VisionModels)
	for i := range cfg.ModelGroups {
		for j, ref := range cfg.ModelGroups[i].Models {
			cfg.ModelGroups[i].Models[j] = normalize(ref)
		}
		cfg.ModelGroups[i].Models = uniqueStrings(cfg.ModelGroups[i].Models)
	}
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin", http.StatusFound)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	cfg := s.currentConfig()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	revealSecrets := s.requestHasAccessKey(r)
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
		MediaTemplates: s.healthMediaTemplates(revealSecrets),
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
		if !s.verifyAdminPassword(r.Form.Get("password")) {
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
	probeModels := chatModelIDs(p, p.Models)
	if len(probeModels) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "provider has no text chat models to probe"})
		return
	}
	available, failures, latencies := s.probeProviderModels(r.Context(), p, probeModels)
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
	if providerModelIsMedia(p, model) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "media models are not probed by chat probe"})
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

func (s *Server) handleProviderProbeKey(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID       string `json:"id"`
		Model    string `json:"model"`
		KeyIndex int    `json:"key_index"`
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
	if p.Type != "openai" && p.Type != "anthropic" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "key probe only supports API key providers"})
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
	if providerModelIsMedia(p, model) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "media models are not probed by chat probe"})
		return
	}
	keys := providerAPIKeys(p)
	if body.KeyIndex < 0 || body.KeyIndex >= len(keys) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "key index is out of range"})
		return
	}

	start := time.Now()
	probeCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	status, respBody, err := s.probeSingleCompatibleModelWithKey(probeCtx, p, model, keys[body.KeyIndex])
	cancel()
	latency := time.Since(start).Milliseconds()
	if err == nil {
		s.markProviderKeyActive(p.ID, body.KeyIndex, model)
		p, _ = s.providerByID(p.ID)
		p = updateProbeResult(p, model, nil, latency, false)
		_ = s.updateProvider(p)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": p.ID, "model": model, "key_index": body.KeyIndex, "latency_ms": latency, "provider": p})
		return
	}
	if isCredentialKeyError(status, respBody) {
		s.markProviderKeyFailed(p.ID, body.KeyIndex, "", true)
	} else if isQuotaKeyError(status, respBody) {
		s.markProviderKeyFailed(p.ID, body.KeyIndex, model, false)
	}
	p, _ = s.providerByID(p.ID)
	p = updateProbeResult(p, model, err, latency, false)
	_ = s.updateProvider(p)
	writeJSON(w, http.StatusOK, map[string]any{"ok": false, "id": p.ID, "model": model, "key_index": body.KeyIndex, "latency_ms": latency, "error": err.Error(), "provider": p})
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

func (s *Server) handleProviderModelDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID    string `json:"id"`
		Model string `json:"model"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	p, err := s.deleteProviderModel(body.ID, body.Model)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "provider": p})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireAccessKey(w, r) {
		return
	}
	scope, _ := s.accessScopeForRequest(r)

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	var models []map[string]any
	grouped := map[string][]string{}
	for _, p := range s.enabledProviders() {
		if isClaudeCodeCompatibleProvider(p) {
			continue
		}
		ids := s.chatModelsForProvider(ctx, p)
		if len(ids) > 0 {
			var visible []string
			for _, id := range ids {
				if scopeAllowsProviderModel(scope, p, id) {
					visible = append(visible, id)
				}
			}
			if len(visible) > 0 {
				grouped[p.Name] = visible
			}
		}
		for _, id := range ids {
			if !scopeAllowsProviderModel(scope, p, id) {
				continue
			}
			models = append(models, map[string]any{
				"id":       providerModelRef(p, id),
				"object":   "model",
				"created":  0,
				"owned_by": providerPublicID(p),
			})
		}
	}
	if target, ok := s.resolveAutoModelForOpenAI(ctx); ok {
		if !isMediaModel(target) && scopeAllowsModel(scope, "auto") {
			grouped["Auto"] = []string{target}
			models = append(models, map[string]any{
				"id":       "auto",
				"object":   "model",
				"created":  0,
				"owned_by": "auto",
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

func (s *Server) handleClaudeCodeModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireAccessKey(w, r) {
		return
	}
	scope, _ := s.accessScopeForRequest(r)
	if !scope.Full {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "model group keys cannot access Claude Code compatible models"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	var models []map[string]any
	grouped := map[string][]string{}
	for _, p := range s.enabledProviders() {
		if !isClaudeCodeCompatibleProvider(p) {
			continue
		}
		ids := s.chatModelsForProvider(ctx, p)
		if len(ids) > 0 {
			grouped[p.Name] = ids
		}
		for _, id := range ids {
			models = append(models, map[string]any{
				"id":       providerModelRef(p, id),
				"object":   "model",
				"created":  0,
				"owned_by": providerPublicID(p),
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

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireAccessKey(w, r) {
		return
	}
	scope, _ := s.accessScopeForRequest(r)
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
	requestedModel := strings.TrimSpace(req.Model)

	if requestedModel == "auto" {
		if !scopeAllowsModel(scope, requestedModel) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "model is not allowed by this access key: " + requestedModel})
			return
		}
		hasImage := openAIChatHasImage(raw)
		target, ok := s.resolveAutoModelForOpenAI(r.Context())
		if hasImage {
			target, ok = s.resolveAutoVisionModelForOpenAI(r.Context())
		}
		if !ok {
			message := "auto model has no available target"
			if hasImage {
				message = "auto model has no available multimodal target"
			}
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": message})
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

	p, ok := s.providerByRouteID(providerID)
	if !ok || !p.Enabled {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "provider is not enabled: " + providerID})
		return
	}
	if isClaudeCodeCompatibleProvider(p) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "this provider only accepts native Claude Code requests; use the Anthropic Base URL"})
		return
	}
	if requestedModel != "auto" && !scopeAllowsProviderModel(scope, p, upstreamModel) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "model is not allowed by this access key: " + requestedModel})
		return
	}
	if bypass, _ := r.Context().Value(internalBypassKey{}).(bool); !bypass && !sliceSet(s.chatModelsForProvider(r.Context(), p))[upstreamModel] {
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
	case "kilocode":
		s.proxyKiloCode(w, r, p, req, upstreamModel)
	case "cline":
		s.proxyCline(w, r, p, req, upstreamModel)
	case "openai":
		if isResponsesCompatibleProvider(p) {
			s.proxyResponsesAsOpenAI(w, r, p, req, upstreamModel)
		} else {
			s.proxyOpenAI(w, r, p, req, upstreamModel)
		}
	case "anthropic":
		s.proxyAnthropicAsOpenAI(w, r, p, req, upstreamModel)
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
		scope, _ := s.accessScopeForRequest(r)
		var models []map[string]any
		grouped := map[string][]string{}
		for _, p := range s.enabledProviders() {
			if mediaEndpoint(p, kind) == "" {
				continue
			}
			for _, id := range mediaModelsForProviderKind(p, s.publishedModelsForProvider(p), kind) {
				if !scopeAllowsProviderModel(scope, p, id) {
					continue
				}
				grouped[p.Name] = append(grouped[p.Name], id)
				models = append(models, map[string]any{
					"id":       providerModelRef(p, id),
					"object":   "model",
					"created":  0,
					"owned_by": providerPublicID(p),
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
	scope, _ := s.accessScopeForRequest(r)
	if !scope.Full {
		writeJSON(w, http.StatusOK, map[string]any{"object": "tool_list", "base_url": requestBaseURL(r) + "/v1", "tools": []map[string]any{}})
		return
	}
	base := requestBaseURL(r)
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	tools := []map[string]any{}
	for _, kind := range []string{"image", "video", "audio", "tts"} {
		tool := s.mediaToolDefinition(kind, base, key)
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
		scope, _ := s.accessScopeForRequest(r)
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
		p, ok := s.providerByRouteID(providerID)
		if !ok || !p.Enabled {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "provider is not enabled: " + providerID})
			return
		}
		if !scopeAllowsProviderModel(scope, p, upstreamModel) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "model is not allowed by this access key: " + providerID + "/" + upstreamModel})
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
			s.proxyPostRotating(w, r, target, body, p, keys, headers, upstreamModel)
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
	status := s.proxyRawWithClient(w, r, s.mimo.client, mimoFreeChatURL, body, headers)
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

	if len(keys) > 1 {
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
	s.proxyPostRotating(w, r, target, body, p, keys, headers, upstreamModel)
}

func (s *Server) proxyPostRotating(w http.ResponseWriter, r *http.Request, target string, body []byte, p ProviderConfig, keys []string, baseHeaders map[string]string, upstreamModel string) {
	var lastStatus int
	var lastHeader http.Header
	var lastBody []byte
	var lastErr error
	order := rotatingKeyOrder(p, len(keys), upstreamModel)
	for orderIndex, keyIndex := range order {
		key := keys[keyIndex]
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
			s.markProviderKeyActive(p.ID, keyIndex, upstreamModel)
			writeBufferedUpstream(w, status, header, respBody)
			return
		}
		if isCredentialKeyError(status, respBody) {
			s.markProviderKeyFailed(p.ID, keyIndex, "", true)
		} else if isQuotaKeyError(status, respBody) {
			s.markProviderKeyFailed(p.ID, keyIndex, upstreamModel, false)
		} else {
			break
		}
		if orderIndex == len(order)-1 {
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
	return s.proxyRawWithClient(w, r, s.client, target, body, headers)
}

func (s *Server) proxyRawWithClient(w http.ResponseWriter, r *http.Request, client *http.Client, target string, body []byte, headers map[string]string) int {
	defer client.CloseIdleConnections()
	defer debug.FreeOSMemory()

	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return http.StatusBadRequest
	}
	for k, v := range headers {
		upReq.Header.Set(k, v)
	}

	resp, err := client.Do(upReq)
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
	case "kilocode":
		ids, err := fetchKiloFreeModels(ctx, s.client, p)
		if err == nil && len(ids) > 0 {
			return ids
		}
		return p.Models
	case "cline":
		return p.Models
	case "openai", "anthropic":
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
	case "openai", "anthropic":
		if strings.TrimSpace(p.BaseURL) == "" {
			return nil, errors.New("provider base_url is empty")
		}
		if len(providerAPIKeys(p)) == 0 {
			return nil, errors.New("provider api_key is empty")
		}
		return fetchCompatibleModels(ctx, s.client, p)
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

func (s *Server) deleteProviderModel(providerID, model string) (ProviderConfig, error) {
	providerID = strings.TrimSpace(providerID)
	model = strings.TrimSpace(model)
	if providerID == "" || model == "" {
		return ProviderConfig{}, errors.New("provider id and model are required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.config.Providers {
		if p.ID != providerID {
			continue
		}
		if !sliceSet(p.Models)[model] {
			return ProviderConfig{}, errors.New("model is not loaded by provider")
		}

		remaining := removeString(p.Models, model)
		if !providerManualPublishOverride(p) {
			p.EnabledModels = append([]string(nil), remaining...)
		} else {
			p.EnabledModels = removeString(p.EnabledModels, model)
		}
		p.Models = remaining
		p.AvailableModels = removeString(p.AvailableModels, model)
		p.LockedModels = removeString(p.LockedModels, model)
		delete(p.ModelKinds, model)
		delete(p.ModelLatencyMS, model)
		delete(p.ModelErrors, model)
		if p.ProviderSpecificData == nil {
			p.ProviderSpecificData = map[string]string{}
		}
		p.ProviderSpecificData["manualPublishOverride"] = "true"
		delete(p.ProviderSpecificData, modelFailedKeyIndexesDataKey(model))
		s.config.Providers[i] = p

		fullModel := providerModelRef(p, model)
		legacyModel := providerID + "/" + model
		s.config.AutoModel.Models = removeString(removeString(s.config.AutoModel.Models, fullModel), legacyModel)
		s.config.AutoModel.VisionModels = removeString(removeString(s.config.AutoModel.VisionModels, fullModel), legacyModel)
		for groupIndex := range s.config.ModelGroups {
			s.config.ModelGroups[groupIndex].Models = removeString(removeString(s.config.ModelGroups[groupIndex].Models, fullModel), legacyModel)
		}
		if err := saveConfig(s.dataDir, s.config); err != nil {
			return ProviderConfig{}, err
		}
		return p, nil
	}
	return ProviderConfig{}, errors.New("provider not found")
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
	return fetchCompatibleModels(ctx, client, p)
}

func fetchCompatibleModels(ctx context.Context, client *http.Client, p ProviderConfig) ([]string, error) {
	keys := providerAPIKeys(p)
	if len(keys) == 0 {
		return nil, errors.New("provider api_key is empty")
	}
	target := joinURL(p.BaseURL, "/models")
	if p.Type == "anthropic" {
		target = anthropicRequestTarget(p, "/models")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+keys[0])
	if p.Type == "anthropic" {
		applyAnthropicUpstreamHeaders(req.Header, nil, p, keys[0], false)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%s models status %d", p.ID, resp.StatusCode)
	}
	if isResponsesCompatibleProvider(p) {
		return parseResponsesModelIDs(resp.Body)
	}
	if p.Type == "anthropic" {
		return parseMessagesModelIDs(resp.Body)
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
	cfg := s.currentConfig()
	if strings.TrimSpace(cfg.AccessKey) == "" && len(cfg.ModelGroups) == 0 {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "gateway access_key is not configured"})
		return false
	}
	if _, ok := s.accessScopeForRequest(r); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid access_key"})
		return false
	}
	return true
}

func (s *Server) requestHasAccessKey(r *http.Request) bool {
	scope, ok := s.accessScopeForRequest(r)
	return ok && scope.Full
}

func requestAccessToken(r *http.Request) string {
	token := strings.TrimSpace(r.Header.Get("x-api-key"))
	if token == "" {
		token = extractBearerToken(r.Header.Get("Authorization"))
	}
	if token == "" {
		token = strings.TrimSpace(r.URL.Query().Get("key"))
	}
	return token
}

func (s *Server) accessScopeForRequest(r *http.Request) (accessScope, bool) {
	if bypass, _ := r.Context().Value(internalBypassKey{}).(bool); bypass {
		return accessScope{Full: true}, true
	}
	token := requestAccessToken(r)
	if token == "" {
		return accessScope{}, false
	}
	cfg := s.currentConfig()
	if key := strings.TrimSpace(cfg.AccessKey); key != "" && token == key {
		return accessScope{Full: true}, true
	}
	for i := range cfg.ModelGroups {
		group := cfg.ModelGroups[i]
		if group.Enabled && strings.TrimSpace(group.APIKey) != "" && token == strings.TrimSpace(group.APIKey) {
			return accessScope{Group: &group}, true
		}
	}
	return accessScope{}, false
}

func scopeAllowsModel(scope accessScope, model string) bool {
	if scope.Full {
		return true
	}
	if scope.Group == nil {
		return false
	}
	return sliceSet(scope.Group.Models)[strings.TrimSpace(model)]
}

func (s *Server) adminCookieValue() string {
	s.adminMu.RLock()
	secret := s.adminSecret
	s.adminMu.RUnlock()
	sum := sha256.Sum256([]byte("9router-lite-admin|" + secret))
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

func (s *Server) providerByRouteID(id string) (ProviderConfig, bool) {
	if p, ok := s.providerByID(id); ok {
		return p, true
	}
	cfg := s.currentConfig()
	for _, p := range cfg.Providers {
		if providerPublicID(p) == id {
			return p, true
		}
	}
	return ProviderConfig{}, false
}

func providerPublicID(p ProviderConfig) string {
	if isCustomProvider(p) {
		if name := strings.TrimSpace(p.Name); name != "" && !strings.Contains(name, "/") {
			return name
		}
	}
	return p.ID
}

func providerModelRef(p ProviderConfig, model string) string {
	return providerPublicID(p) + "/" + strings.TrimSpace(model)
}

func scopeAllowsProviderModel(scope accessScope, p ProviderConfig, model string) bool {
	if scopeAllowsModel(scope, providerModelRef(p, model)) {
		return true
	}
	return providerPublicID(p) != p.ID && scopeAllowsModel(scope, p.ID+"/"+strings.TrimSpace(model))
}

func providerHasCredential(p ProviderConfig) bool {
	if len(providerAPIKeys(p)) > 0 || p.AccessToken != "" || p.Type == "opencode-free" || p.Type == "mimo-free" {
		return true
	}
	return isCustomProvider(p) && strings.TrimSpace(p.BaseURL) != "" && p.BaseURL != "https://example.com/v1"
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

func rotatingKeyOrder(p ProviderConfig, count int, model string) []int {
	if count <= 0 {
		return nil
	}
	failed := failedKeyIndexesForModel(p, model)
	active := parseProviderInt(providerDataValueGo(p, "active_key_index"), -1)
	var order []int
	used := map[int]bool{}
	add := func(i int) {
		if i >= 0 && i < count && !used[i] && !failed[i] {
			used[i] = true
			order = append(order, i)
		}
	}
	add(active)
	for i := 0; i < count; i++ {
		add(i)
	}
	if len(order) == 0 {
		for i := 0; i < count; i++ {
			order = append(order, i)
		}
	}
	return order
}

func failedKeyIndexesForModel(p ProviderConfig, model string) map[int]bool {
	out := intSetFromCSV(providerDataValueGo(p, "failed_key_indexes"))
	if key := modelFailedKeyIndexesDataKey(model); key != "" {
		for index := range intSetFromCSV(providerDataValueGo(p, key)) {
			out[index] = true
		}
	}
	return out
}

func modelFailedKeyIndexesDataKey(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(model))
	return "failed_key_indexes_model_" + hex.EncodeToString(sum[:])[:16]
}

func providerDataValueGo(p ProviderConfig, key string) string {
	if p.ProviderSpecificData == nil {
		return ""
	}
	return strings.TrimSpace(p.ProviderSpecificData[key])
}

func parseProviderInt(value string, fallback int) int {
	i, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return i
}

func intSetFromCSV(value string) map[int]bool {
	out := map[int]bool{}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		i, err := strconv.Atoi(part)
		if err == nil && i >= 0 {
			out[i] = true
		}
	}
	return out
}

func csvFromIntSet(items map[int]bool) string {
	var values []int
	for item, ok := range items {
		if ok {
			values = append(values, item)
		}
	}
	sort.Ints(values)
	var parts []string
	for _, item := range values {
		parts = append(parts, strconv.Itoa(item))
	}
	return strings.Join(parts, ",")
}

func (s *Server) markProviderKeyActive(id string, index int, model string) {
	s.updateProviderKeyStatus(id, index, model, false, false)
}

func (s *Server) markProviderKeyFailed(id string, index int, model string, global bool) {
	s.updateProviderKeyStatus(id, index, model, true, global)
}

func (s *Server) updateProviderKeyStatus(id string, index int, model string, failed bool, global bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.config.Providers {
		if p.ID != id {
			continue
		}
		if p.ProviderSpecificData == nil {
			p.ProviderSpecificData = map[string]string{}
		}
		if failed {
			key := "failed_key_indexes"
			if !global {
				key = modelFailedKeyIndexesDataKey(model)
			}
			if key == "" {
				key = "failed_key_indexes"
			}
			failedSet := intSetFromCSV(p.ProviderSpecificData[key])
			failedSet[index] = true
			if global && parseProviderInt(p.ProviderSpecificData["active_key_index"], -1) == index {
				delete(p.ProviderSpecificData, "active_key_index")
			}
			if csv := csvFromIntSet(failedSet); csv != "" {
				p.ProviderSpecificData[key] = csv
			} else {
				delete(p.ProviderSpecificData, key)
			}
		} else {
			p.ProviderSpecificData["active_key_index"] = strconv.Itoa(index)
			failedSet := intSetFromCSV(p.ProviderSpecificData["failed_key_indexes"])
			delete(failedSet, index)
			if csv := csvFromIntSet(failedSet); csv != "" {
				p.ProviderSpecificData["failed_key_indexes"] = csv
			} else {
				delete(p.ProviderSpecificData, "failed_key_indexes")
			}
			if key := modelFailedKeyIndexesDataKey(model); key != "" {
				failedSet = intSetFromCSV(p.ProviderSpecificData[key])
				delete(failedSet, index)
				if csv := csvFromIntSet(failedSet); csv != "" {
					p.ProviderSpecificData[key] = csv
				} else {
					delete(p.ProviderSpecificData, key)
				}
			}
		}
		s.config.Providers[i] = p
		_ = saveConfig(s.dataDir, s.config)
		return
	}
}

func (s *Server) visibleModelsForProvider(ctx context.Context, p ProviderConfig) []string {
	_ = ctx
	selected := s.publishedModelsForProvider(p)
	if providerManualPublishOverride(p) {
		return selected
	}
	if p.AvailabilityCheckedAt == 0 && len(p.AvailableModels) == 0 {
		return selected
	}
	return orderedIntersection(selected, uniqueStrings(p.AvailableModels))
}

func (s *Server) publishedModelsForProvider(p ProviderConfig) []string {
	base := p.Models
	base = uniqueStrings(base)
	if providerManualPublishOverride(p) {
		return orderedIntersection(base, uniqueStrings(p.EnabledModels))
	}
	selected := p.EnabledModels
	if len(selected) == 0 {
		selected = base
	}
	return orderedIntersection(base, uniqueStrings(selected))
}

func (s *Server) chatModelsForProvider(ctx context.Context, p ProviderConfig) []string {
	return chatModelIDs(p, s.visibleModelsForProvider(ctx, p))
}

func chatModelIDs(p ProviderConfig, models []string) []string {
	var out []string
	for _, model := range uniqueStrings(models) {
		if !providerModelIsMedia(p, model) {
			out = append(out, model)
		}
	}
	return out
}

func unlockedModelIDs(models, locked []string) []string {
	lockedSet := sliceSet(locked)
	var out []string
	for _, model := range uniqueStrings(models) {
		if !lockedSet[model] {
			out = append(out, model)
		}
	}
	return out
}

func (s *Server) resolveAutoModel(ctx context.Context) (string, bool) {
	return s.resolveAutoModelMatching(ctx, nil)
}

func (s *Server) resolveAutoModelForOpenAI(ctx context.Context) (string, bool) {
	return s.resolveAutoModelMatching(ctx, func(p ProviderConfig) bool {
		return !isClaudeCodeCompatibleProvider(p)
	})
}

func (s *Server) resolveAutoVisionModel(ctx context.Context) (string, bool) {
	return s.resolveAutoVisionModelMatching(ctx, nil)
}

func (s *Server) resolveAutoVisionModelForOpenAI(ctx context.Context) (string, bool) {
	return s.resolveAutoVisionModelMatching(ctx, func(p ProviderConfig) bool {
		return !isClaudeCodeCompatibleProvider(p)
	})
}

func (s *Server) resolveAutoModelMatching(ctx context.Context, accept func(ProviderConfig) bool) (string, bool) {
	cfg := s.currentConfig()
	if !cfg.AutoModel.Enabled {
		return "", false
	}
	return s.resolveAutoCandidatesMatching(ctx, cfg.AutoModel.Models, accept)
}

func (s *Server) resolveAutoVisionModelMatching(ctx context.Context, accept func(ProviderConfig) bool) (string, bool) {
	cfg := s.currentConfig()
	if !cfg.AutoModel.Enabled {
		return "", false
	}
	return s.resolveAutoCandidatesMatching(ctx, cfg.AutoModel.VisionModels, accept)
}

func (s *Server) resolveAutoCandidatesMatching(ctx context.Context, candidates []string, accept func(ProviderConfig) bool) (string, bool) {
	for _, candidate := range uniqueStrings(candidates) {
		if candidate == "auto" {
			continue
		}
		providerID, model, ok := strings.Cut(candidate, "/")
		if !ok || providerID == "" || model == "" {
			continue
		}
		p, ok := s.providerByRouteID(providerID)
		if !ok || !p.Enabled {
			continue
		}
		if providerModelIsMedia(p, model) {
			continue
		}
		if accept != nil && !accept(p) {
			continue
		}
		if sliceSet(s.visibleModelsForProvider(ctx, p))[model] {
			return providerModelRef(p, model), true
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
	if isClaudeCodeCompatibleProvider(p) {
		start := time.Now()
		ids, err := fetchCompatibleModels(ctx, s.client, p)
		latency := time.Since(start).Milliseconds()
		listed := sliceSet(ids)
		var available []string
		for _, id := range models {
			latencies[id] = latency
			if err == nil && listed[id] {
				available = append(available, id)
			} else if err != nil {
				failures[id] = formatProbeFailure(http.StatusBadGateway, err.Error())
			} else {
				failures[id] = "上游模型列表中不存在"
			}
		}
		return available, failures, latencies
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
	if p, ok := s.providerByID(providerID); ok && isClaudeCodeCompatibleProvider(p) {
		ids, err := fetchCompatibleModels(ctx, s.client, p)
		if err != nil {
			return err
		}
		if !sliceSet(ids)[model] {
			return errors.New("上游模型列表中不存在")
		}
		return nil
	}
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

func (s *Server) probeSingleOpenAIModelWithKey(ctx context.Context, p ProviderConfig, model, key string) (int, []byte, error) {
	return s.probeSingleCompatibleModelWithKey(ctx, p, model, key)
}

func (s *Server) probeSingleCompatibleModelWithKey(ctx context.Context, p ProviderConfig, model, key string) (int, []byte, error) {
	if isClaudeCodeCompatibleProvider(p) {
		target := anthropicRequestTarget(p, "/models")
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return http.StatusBadRequest, nil, err
		}
		applyAnthropicUpstreamHeaders(req.Header, nil, p, key, false)
		resp, err := s.client.Do(req)
		if err != nil {
			return http.StatusBadGateway, nil, err
		}
		defer resp.Body.Close()
		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if readErr != nil {
			return resp.StatusCode, respBody, readErr
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return resp.StatusCode, respBody, errors.New(formatProbeFailure(resp.StatusCode, string(respBody)))
		}
		ids, err := parseModelIDs(bytes.NewReader(respBody))
		if err != nil {
			return resp.StatusCode, respBody, err
		}
		if !sliceSet(ids)[model] {
			return http.StatusNotFound, respBody, errors.New("上游模型列表中不存在")
		}
		return resp.StatusCode, respBody, nil
	}
	body := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": "Reply with OK."},
		},
		"stream":     false,
		"max_tokens": 4,
	}
	target := joinURL(p.BaseURL, "/chat/completions")
	if isResponsesCompatibleProvider(p) {
		body = map[string]any{"model": model, "input": "Reply with OK.", "stream": false, "max_output_tokens": 16}
		target = joinURL(p.BaseURL, "/responses")
	} else if p.Type == "anthropic" {
		body = map[string]any{
			"model": model, "messages": []map[string]string{{"role": "user", "content": "Reply with OK."}}, "stream": false, "max_tokens": 4,
		}
		if isClaudeCodeCompatibleProvider(p) {
			body["system"] = []any{map[string]any{"type": "text", "text": claudeCodeSystemPrompt}}
		}
		target = anthropicRequestTarget(p, "/messages")
	}
	raw, _ := json.Marshal(body)
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + key,
		"Accept":        "application/json",
		"User-Agent":    "9router-lite/0.1",
	}
	if p.Type == "anthropic" {
		anthropicHeaders := make(http.Header)
		applyAnthropicUpstreamHeaders(anthropicHeaders, nil, p, key, false)
		for name, values := range anthropicHeaders {
			if len(values) > 0 {
				headers[name] = values[0]
			}
		}
	}
	status, _, respBody, err := s.postUpstreamBuffered(ctx, target, raw, headers)
	if err != nil {
		return status, respBody, err
	}
	if status >= 200 && status <= 299 {
		return status, respBody, nil
	}
	return status, respBody, errors.New(formatProbeFailure(status, string(respBody)))
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

func isCredentialKeyError(status int, body []byte) bool {
	lower := strings.ToLower(string(body))
	if strings.Contains(lower, "invalid api key") ||
		strings.Contains(lower, "api key is invalid") ||
		strings.Contains(lower, "invalid authorization") ||
		strings.Contains(lower, "invalid bearer") ||
		strings.Contains(lower, "invalid token") ||
		strings.Contains(lower, "authentication failed") ||
		strings.Contains(lower, "no auth credentials") ||
		strings.Contains(lower, "incorrect api key") {
		return true
	}
	if status == http.StatusUnauthorized && !isQuotaKeyError(status, body) {
		return strings.Contains(lower, "api key") ||
			strings.Contains(lower, "auth") ||
			strings.Contains(lower, "token") ||
			strings.Contains(lower, "credential") ||
			strings.Contains(lower, "unauthorized")
	}
	return false
}

func (s *Server) probeAllProviders(ctx context.Context, autoPublish bool) {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	for _, p := range s.enabledProviders() {
		models := s.visibleModelsForProvider(ctx, p)
		models = unlockedModelIDs(models, p.LockedModels)
		models = chatModelIDs(p, models)
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
	if wantsJSON(r) {
		return false
	}
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
	routeIDs := map[string]bool{}
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
		routeIDs[p.ID] = true
		for model, kind := range p.ModelKinds {
			switch strings.ToLower(strings.TrimSpace(kind)) {
			case "", "auto", "text", "image", "video", "audio", "tts":
			default:
				return fmt.Errorf("invalid model kind for %s/%s: %s", p.ID, model, kind)
			}
		}
	}
	for _, p := range cfg.Providers {
		if !isCustomProvider(p) {
			continue
		}
		name := strings.TrimSpace(p.Name)
		if strings.Contains(name, "/") {
			return fmt.Errorf("custom provider name cannot contain '/': %s", name)
		}
		if !p.Enabled {
			continue
		}
		if name == "" {
			return fmt.Errorf("custom provider name is required: %s", p.ID)
		}
		publicID := providerPublicID(p)
		if publicID != p.ID && routeIDs[publicID] {
			return fmt.Errorf("custom provider name conflicts with another provider route: %s", publicID)
		}
		routeIDs[publicID] = true
	}
	groupIDs := map[string]bool{}
	groupKeys := map[string]bool{}
	mainKey := strings.TrimSpace(cfg.AccessKey)
	for _, group := range cfg.ModelGroups {
		id := strings.TrimSpace(group.ID)
		if id == "" {
			return errors.New("model group id is required")
		}
		if strings.ContainsAny(id, " /\\\t\r\n") {
			return fmt.Errorf("invalid model group id: %s", id)
		}
		if groupIDs[id] {
			return fmt.Errorf("duplicate model group id: %s", id)
		}
		groupIDs[id] = true
		if !group.Enabled {
			continue
		}
		if strings.TrimSpace(group.Name) == "" {
			return fmt.Errorf("model group name is required: %s", id)
		}
		key := strings.TrimSpace(group.APIKey)
		if key == "" {
			return fmt.Errorf("model group api_key is required: %s", group.Name)
		}
		if key == mainKey {
			return fmt.Errorf("model group api_key must differ from gateway access_key: %s", group.Name)
		}
		if groupKeys[key] {
			return fmt.Errorf("duplicate model group api_key: %s", group.Name)
		}
		groupKeys[key] = true
	}
	return nil
}

func isCustomOpenAIProvider(p ProviderConfig) bool {
	return p.Type == "openai" && isCustomProviderID(p.ID)
}

func isCustomProvider(p ProviderConfig) bool {
	return (p.Type == "openai" || p.Type == "anthropic") && isCustomProviderID(p.ID)
}

func isCustomProviderID(id string) bool {
	return id == "custom" || strings.HasPrefix(id, "custom-") || strings.HasPrefix(id, "custom_") || strings.HasPrefix(id, "custom")
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
	case "tts":
		return strings.TrimSpace(p.TTSEndpoint)
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
		needles = []string{"audio", "speech", "whisper", "transcrib"}
	case "tts":
		needles = []string{"tts", "voice", "neural", "zh-", "en-"}
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

func providerModelKind(p ProviderConfig, model string) string {
	kind := strings.ToLower(strings.TrimSpace(p.ModelKinds[model]))
	switch kind {
	case "text", "image", "video", "audio", "tts":
		return kind
	default:
		return "auto"
	}
}

func providerModelMatchesKind(p ProviderConfig, model, kind string) bool {
	explicit := providerModelKind(p, model)
	if explicit == "text" {
		return false
	}
	if explicit != "auto" {
		return explicit == kind
	}
	return len(mediaModelsForKind([]string{model}, kind)) > 0
}

func mediaModelsForProviderKind(p ProviderConfig, models []string, kind string) []string {
	var out []string
	for _, model := range uniqueStrings(models) {
		if providerModelMatchesKind(p, model, kind) {
			out = append(out, model)
		}
	}
	return out
}

func providerModelIsMedia(p ProviderConfig, model string) bool {
	for _, kind := range []string{"image", "video", "audio", "tts"} {
		if providerModelMatchesKind(p, model, kind) {
			return true
		}
	}
	return false
}

func isMediaModel(model string) bool {
	if _, raw, ok := strings.Cut(model, "/"); ok {
		model = raw
	}
	return len(mediaModelsForKind([]string{model}, "image")) > 0 ||
		len(mediaModelsForKind([]string{model}, "video")) > 0 ||
		len(mediaModelsForKind([]string{model}, "audio")) > 0 ||
		len(mediaModelsForKind([]string{model}, "tts")) > 0
}

func (s *Server) healthMediaTemplates(revealSecrets bool) []HealthMediaTemplate {
	var out []HealthMediaTemplate
	for _, p := range s.enabledProviders() {
		models := s.publishedModelsForProvider(p)
		for _, kind := range []string{"image", "video", "audio", "tts"} {
			endpoint := mediaEndpoint(p, kind)
			if endpoint == "" {
				continue
			}
			for _, model := range mediaModelsForProviderKind(p, models, kind) {
				out = append(out, healthMediaTemplateForProvider(p, kind, endpoint, model, revealSecrets))
			}
		}
	}
	return out
}

func healthMediaTemplateForProvider(p ProviderConfig, kind, endpoint, model string, revealSecrets bool) HealthMediaTemplate {
	key := "<upstream-api-key>"
	if revealSecrets {
		if keys := providerAPIKeys(p); len(keys) > 0 {
			key = keys[0]
		}
	}
	t := HealthMediaTemplate{
		Type:          kind,
		ProviderID:    p.ID,
		ProviderName:  p.Name,
		Model:         providerModelRef(p, model),
		UpstreamModel: model,
		Method:        http.MethodPost,
		Endpoint:      endpoint,
	}
	switch kind {
	case "image":
		body := imageTemplateBody(endpoint, model)
		t.RequestBody = body
		t.Curl = jsonCurl(endpoint, key, body)
		if isAgnesImageEndpoint(endpoint, model) {
			t.Note = "文生图 URL 位于 data[0].url。"
			t.ExtraTemplates = append(t.ExtraTemplates, agnesImageExtraTemplates(endpoint, model, key)...)
		}
		if transparentModel := transparentBackgroundModel(model); transparentModel != "" && !isAgnesImageEndpoint(endpoint, model) {
			transparent := map[string]any{
				"model":         transparentModel,
				"prompt":        "替换为用户最终透明背景生图提示词",
				"n":             1,
				"background":    "transparent",
				"output_format": "png",
			}
			t.ExtraTemplates = append(t.ExtraTemplates, HealthMediaCurlTemplate{
				Name:        "透明背景 PNG 生图",
				Method:      http.MethodPost,
				Endpoint:    endpoint,
				RequestBody: transparent,
				Curl:        jsonCurl(endpoint, key, transparent),
			})
		}
		if editEndpoint := strings.TrimSpace(p.ImageEditEndpoint); editEndpoint != "" {
			editForm := map[string]string{
				"model":  model,
				"prompt": "替换为用户最终图片编辑提示词",
				"n":      "1",
				"image":  "@替换为本地图片路径，例如 input.png",
			}
			t.ExtraTemplates = append(t.ExtraTemplates, HealthMediaCurlTemplate{
				Name:     "图片编辑：上传本地图片文件",
				Method:   http.MethodPost,
				Endpoint: editEndpoint,
				Form:     editForm,
				Curl:     formCurl(editEndpoint, key, editForm),
			})
			editBody := map[string]any{
				"model":  model,
				"prompt": "替换为用户最终图片编辑提示词",
				"images": []map[string]string{{"image_url": "https://example.com/input.png"}},
			}
			t.ExtraTemplates = append(t.ExtraTemplates, HealthMediaCurlTemplate{
				Name:        "图片编辑：使用图片 URL",
				Method:      http.MethodPost,
				Endpoint:    editEndpoint,
				RequestBody: editBody,
				Curl:        jsonCurl(editEndpoint, key, editBody),
			})
		}
	case "video":
		body := map[string]any{
			"model":      model,
			"prompt":     "替换为用户最终视频提示词",
			"height":     768,
			"width":      1152,
			"num_frames": 121,
			"frame_rate": 24,
		}
		t.RequestBody = body
		t.Curl = jsonCurl(endpoint, key, body)
		if isAgnesVideoEndpoint(endpoint, model) {
			t.ExtraTemplates = append(t.ExtraTemplates, agnesVideoExtraTemplates(endpoint, model, key)...)
		}
		queryEndpoint := videoQueryEndpoint(endpoint)
		t.ExtraTemplates = append(t.ExtraTemplates, HealthMediaCurlTemplate{
			Name:     "查询视频结果",
			Method:   http.MethodGet,
			Endpoint: queryEndpoint,
			Curl:     bearerGetCurl(queryEndpoint, key),
			Note:     "创建任务响应里的 video_id 用于查询；建议每 5 秒轮询一次，直到 status 为 completed。",
		})
	case "audio":
		form := map[string]string{
			"file":            "@替换为本地音频文件路径，例如 audio.mp3",
			"model":           model,
			"response_format": "json",
			"language":        "替换为音频语言代码，例如 zh 或 en",
		}
		t.Form = form
		t.Curl = formCurl(endpoint, key, form)
		t.Note = "Groq Audio Transcriptions 这类接口使用 multipart/form-data。"
	case "tts":
		t.RequestBody = map[string]any{
			"text":  "你好世界",
			"voice": model,
		}
		t.Curl = "curl -G " + shellQuoteDouble(endpoint) + " \\\n" +
			"  --data-urlencode " + shellQuoteDouble("text=你好世界") + " \\\n" +
			"  --data-urlencode " + shellQuoteDouble("voice="+model) + " \\\n" +
			"  --output output.mp3"
		t.Note = "Edge TTS server 默认是 GET /tts?text=...&voice=...，响应为 mp3。"
	}
	if custom := providerCurlTemplate(p, kind); custom != "" {
		t.Curl = renderCurlTemplate(custom, mediaTemplateVars(p, kind, endpoint, model, key))
		if t.Note == "" {
			t.Note = "使用 provider 自定义 curl 模板。"
		}
	}
	return t
}

func providerCurlTemplate(p ProviderConfig, kind string) string {
	if p.ProviderSpecificData == nil {
		return ""
	}
	switch kind {
	case "image":
		return strings.TrimSpace(p.ProviderSpecificData["curlTemplateImage"])
	case "video":
		return strings.TrimSpace(p.ProviderSpecificData["curlTemplateVideo"])
	case "audio":
		return strings.TrimSpace(p.ProviderSpecificData["curlTemplateAudio"])
	case "tts":
		return strings.TrimSpace(p.ProviderSpecificData["curlTemplateTTS"])
	default:
		return ""
	}
}

func mediaTemplateVars(p ProviderConfig, kind, endpoint, model, key string) map[string]string {
	return map[string]string{
		"endpoint":             endpoint,
		"image_edit_endpoint":  strings.TrimSpace(p.ImageEditEndpoint),
		"key":                  key,
		"model":                model,
		"transparent_model":    firstNonEmpty(transparentBackgroundModel(model), model),
		"prompt":               "替换为用户最终提示词",
		"image_prompt":         "替换为用户最终生图提示词",
		"transparent_prompt":   "替换为用户最终透明背景生图提示词",
		"edit_prompt":          "替换为用户最终图片编辑提示词",
		"video_prompt":         "替换为用户最终视频提示词",
		"audio_file":           "替换为本地音频文件路径，例如 audio.mp3",
		"image_file":           "替换为本地图片路径，例如 input.png",
		"tts_text":             "你好世界",
		"video_query_endpoint": videoQueryEndpoint(endpoint),
	}
}

func renderCurlTemplate(template string, vars map[string]string) string {
	out := template
	for key, value := range vars {
		out = strings.ReplaceAll(out, "{{"+key+"}}", value)
	}
	return out
}

func imageTemplateBody(endpoint, model string) map[string]any {
	if isAgnesImageEndpoint(endpoint, model) {
		return map[string]any{
			"model":  model,
			"prompt": "替换为用户最终生图提示词",
			"size":   "替换为用户需要的图片尺寸，例如 1024x768",
			"extra_body": map[string]any{
				"response_format": "url",
			},
		}
	}
	return map[string]any{
		"model":  model,
		"prompt": "替换为用户最终生图提示词",
		"n":      1,
	}
}

func agnesImageExtraTemplates(endpoint, model, key string) []HealthMediaCurlTemplate {
	imageToImageURL := map[string]any{
		"model":  model,
		"prompt": "替换为用户最终图片编辑提示词",
		"size":   "替换为用户需要的图片尺寸，例如 1024x768",
		"extra_body": map[string]any{
			"image":           []string{"https://example.com/input-image.png"},
			"response_format": "url",
		},
	}
	imageToImageBase64 := map[string]any{
		"model":  model,
		"prompt": "替换为用户最终图片编辑提示词",
		"size":   "替换为用户需要的图片尺寸，例如 1024x768",
		"extra_body": map[string]any{
			"image":           []string{"data:image/png;base64,BASE64_HERE"},
			"response_format": "b64_json",
		},
	}
	multiImage := map[string]any{
		"model":  model,
		"prompt": "替换为用户最终多图合成提示词",
		"size":   "替换为用户需要的图片尺寸，例如 1024x768",
		"extra_body": map[string]any{
			"image": []string{
				"https://example.com/input-image-1.png",
				"https://example.com/input-image-2.png",
			},
			"response_format": "url",
		},
	}
	return []HealthMediaCurlTemplate{
		{
			Name:        "图生图 / 图片编辑：URL 输入，URL 输出",
			Method:      http.MethodPost,
			Endpoint:    endpoint,
			RequestBody: imageToImageURL,
			Curl:        jsonCurl(endpoint, key, imageToImageURL),
			Note:        "生成图片 URL 位于 data[0].url。",
		},
		{
			Name:        "图生图 / 图片编辑：Data URI Base64 输入，Base64 输出",
			Method:      http.MethodPost,
			Endpoint:    endpoint,
			RequestBody: imageToImageBase64,
			Curl:        jsonCurl(endpoint, key, imageToImageBase64),
			Note:        "生成图片 Base64 位于 data[0].b64_json。",
		},
		{
			Name:        "多图合成：多个 URL 输入，URL 输出",
			Method:      http.MethodPost,
			Endpoint:    endpoint,
			RequestBody: multiImage,
			Curl:        jsonCurl(endpoint, key, multiImage),
			Note:        "生成图片 URL 位于 data[0].url。",
		},
	}
}

func isAgnesImageEndpoint(endpoint, model string) bool {
	return strings.Contains(strings.ToLower(endpoint), "agnes-ai.com") || strings.Contains(strings.ToLower(model), "agnes-image")
}

func isAgnesVideoEndpoint(endpoint, model string) bool {
	return strings.Contains(strings.ToLower(endpoint), "agnes-ai.com") || strings.Contains(strings.ToLower(model), "agnes-video")
}

func agnesVideoExtraTemplates(endpoint, model, key string) []HealthMediaCurlTemplate {
	singleImage := map[string]any{
		"model":      model,
		"prompt":     "替换为用户最终图生视频提示词",
		"image":      "https://example.com/input-image.png",
		"num_frames": 121,
		"frame_rate": 24,
	}
	multiImage := map[string]any{
		"model":  model,
		"prompt": "替换为用户最终多图视频提示词",
		"extra_body": map[string]any{
			"image": []string{
				"https://example.com/input-image-1.png",
				"https://example.com/input-image-2.png",
			},
		},
		"num_frames": 121,
		"frame_rate": 24,
	}
	keyframes := map[string]any{
		"model":  model,
		"prompt": "替换为用户最终关键帧动画提示词",
		"extra_body": map[string]any{
			"image": []string{
				"https://example.com/keyframe-1.png",
				"https://example.com/keyframe-2.png",
			},
			"mode": "keyframes",
		},
		"num_frames": 121,
		"frame_rate": 24,
	}
	return []HealthMediaCurlTemplate{
		{
			Name:        "单图生视频",
			Method:      http.MethodPost,
			Endpoint:    endpoint,
			RequestBody: singleImage,
			Curl:        jsonCurl(endpoint, key, singleImage),
		},
		{
			Name:        "多图视频生成",
			Method:      http.MethodPost,
			Endpoint:    endpoint,
			RequestBody: multiImage,
			Curl:        jsonCurl(endpoint, key, multiImage),
		},
		{
			Name:        "关键帧动画",
			Method:      http.MethodPost,
			Endpoint:    endpoint,
			RequestBody: keyframes,
			Curl:        jsonCurl(endpoint, key, keyframes),
		},
	}
}

func transparentBackgroundModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	switch model {
	case "gpt-image-1", "gpt-image-2":
		return "gpt-image-1"
	case "codex-gpt-image-1", "codex-gpt-image-2":
		return "codex-gpt-image-1"
	default:
		return ""
	}
}

func jsonCurl(endpoint, key string, body map[string]any) string {
	return "curl -X POST " + shellQuoteDouble(endpoint) + " \\\n" +
		"  -H " + shellQuoteDouble("Content-Type: application/json") + " \\\n" +
		"  -H " + shellQuoteDouble("Authorization: Bearer "+key) + " \\\n" +
		"  -d " + shellQuoteSingle(mustJSONString(body))
}

func formCurl(endpoint, key string, fields map[string]string) string {
	var b strings.Builder
	b.WriteString("curl -X POST ")
	b.WriteString(shellQuoteDouble(endpoint))
	b.WriteString(" \\\n  -H ")
	b.WriteString(shellQuoteDouble("Authorization: Bearer " + key))
	keys := keysFromStringMap(fields)
	for _, k := range keys {
		b.WriteString(" \\\n  -F ")
		b.WriteString(shellQuoteDouble(k + "=" + fields[k]))
	}
	return b.String()
}

func bearerGetCurl(endpoint, key string) string {
	return "curl -X GET " + shellQuoteDouble(endpoint) + " \\\n" +
		"  -H " + shellQuoteDouble("Authorization: Bearer "+key)
}

func videoQueryEndpoint(endpoint string) string {
	fallback := "https://apihub.agnes-ai.com/agnesapi?video_id=替换为创建任务返回的 video_id"
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fallback
	}
	if strings.Contains(strings.ToLower(u.Host), "agnes-ai.com") {
		return u.Scheme + "://" + u.Host + "/agnesapi?video_id=替换为创建任务返回的 video_id"
	}
	return fallback
}

func mustJSONString(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(b)
}

func shellQuoteSingle(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func shellQuoteDouble(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func keysFromStringMap(in map[string]string) []string {
	var keys []string
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (s *Server) mediaToolDefinition(kind, base, key string) map[string]any {
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
	case "tts":
		name = "text_to_speech"
		path = "/v1/tts"
		description = "Text-to-speech via the configured upstream TTS endpoint, for example edge-tts-server."
		schema = map[string]any{
			"voice": "voice name, for example zh-CN-XiaoxiaoNeural",
			"text":  "text to synthesize",
		}
		example = map[string]any{
			"voice": firstMediaModel(s.enabledProviders(), kind),
			"text":  "你好世界",
		}
	default:
		return nil
	}

	var models []string
	for _, p := range s.enabledProviders() {
		if mediaEndpoint(p, kind) == "" {
			continue
		}
		for _, model := range mediaModelsForProviderKind(p, s.publishedModelsForProvider(p), kind) {
			models = append(models, providerModelRef(p, model))
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
		"endpoint":    appendQueryKey(base+path, key),
		"description": description,
		"models":      models,
		"schema":      schema,
		"example":     example,
	}
}

func appendQueryKey(rawURL, key string) string {
	if strings.TrimSpace(key) == "" {
		return rawURL
	}
	sep := "?"
	if strings.Contains(rawURL, "?") {
		sep = "&"
	}
	return rawURL + sep + "key=" + url.QueryEscape(key)
}

func firstMediaModel(providers []ProviderConfig, kind string) string {
	for _, p := range providers {
		if mediaEndpoint(p, kind) == "" {
			continue
		}
		models := mediaModelsForProviderKind(p, uniqueStrings(p.EnabledModels), kind)
		if len(models) == 0 && !providerManualPublishOverride(p) {
			models = mediaModelsForProviderKind(p, p.Models, kind)
		}
		if len(models) > 0 {
			return providerModelRef(p, models[0])
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
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>9Router Lite 登录</title><style>body{font-family:system-ui,-apple-system,Segoe UI,sans-serif;margin:0;background:#fafafa;color:#111;min-height:100vh;display:grid;place-items:center}.box{width:min(420px,calc(100vw - 32px));background:#fff;border:1px solid #ddd;border-radius:8px;padding:24px;box-sizing:border-box}h1{font-size:26px;margin:0 0 8px}.muted{color:#666;font-size:13px;margin-bottom:18px}.field{display:grid;gap:6px;margin:12px 0}.field label{font-size:13px;color:#444}.field input{width:100%;box-sizing:border-box;padding:11px 12px;border:1px solid #ddd;border-radius:6px;font:16px/1.3 system-ui,-apple-system,Segoe UI,sans-serif}button{width:100%;background:#111;color:#fff;border:0;border-radius:6px;padding:11px 14px;font:inherit;cursor:pointer;margin-top:8px}.err{color:#b91c1c;font-size:13px;margin:10px 0}.hint{color:#666;font-size:12px;margin-top:14px;line-height:1.5}code{background:#eee;padding:2px 5px;border-radius:4px}</style></head><body><form class="box" method="post" action="/admin"><h1>9Router Lite</h1><div class="muted">请输入管理密码访问后台</div>` + msg + `<div class="field"><label>管理密码</label><input name="password" type="password" autocomplete="current-password" autofocus></div><button type="submit">登录</button><div class="hint">修改过密码时使用后台密码。首次启动依次使用 <code>ADMIN_PASSWORD</code>、网关访问密钥或默认密码 <code>123456</code>。</div></form></body></html>`
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
const statusProviderIDs=['oc','mmf','qoder','kilo','cline','glm','groq','deepseek','mimo','custom'];
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
