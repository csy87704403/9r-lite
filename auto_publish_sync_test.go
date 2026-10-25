package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func publishSyncConfig() Config {
	return Config{
		AutoModels: []AutoModelConfig{
			{ID: "auto", Enabled: true, Models: []string{"custom-sync/b", "custom-sync/hidden", "custom-sync/a", "custom-sync/deleted", "gone/model"}},
			{ID: "auto-code", Enabled: true, Models: []string{"Sync/a", "Sync/hidden", "Sync/b"}},
			{ID: "auto-disabled", Models: []string{"custom-sync/hidden"}},
		},
		Providers: []ProviderConfig{{
			ID: "custom-sync", Name: "Sync", Type: "openai", Enabled: true,
			Models: []string{"a", "b", "hidden"}, EnabledModels: []string{"a", "b"},
			ProviderSpecificData: map[string]string{"manualPublishOverride": "true"},
		}},
	}
}

func assertPublishSyncAutos(t *testing.T, cfg Config, want ...[]string) {
	t.Helper()
	if len(cfg.AutoModels) != len(want) {
		t.Fatalf("Auto list count = %d, want %d", len(cfg.AutoModels), len(want))
	}
	for i, models := range want {
		if !reflect.DeepEqual(uniqueStrings(cfg.AutoModels[i].Models), uniqueStrings(models)) {
			t.Fatalf("%s models = %v, want %v", cfg.AutoModels[i].ID, cfg.AutoModels[i].Models, models)
		}
	}
	if !reflect.DeepEqual(cfg.AutoModel.Models, cfg.AutoModels[0].Models) {
		t.Fatal("legacy default Auto diverged from auto_models")
	}
}

func TestPublishSyncLoadRemovesHistoricalUnpublishedAndDeletedRefs(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, publishSyncConfig()); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	assertPublishSyncAutos(t, cfg, []string{"Sync/b", "Sync/a"}, []string{"Sync/a", "Sync/b"}, nil)
	if !cfg.AutoModels[0].Enabled || cfg.AutoModels[2].Enabled {
		t.Fatal("cleanup changed Auto enabled state")
	}
	// A subsequent normal save/restart must not resurrect removed entries.
	if err := saveConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	again, err := loadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	assertPublishSyncAutos(t, again, []string{"Sync/b", "Sync/a"}, []string{"Sync/a", "Sync/b"}, nil)
}

func TestPublishSyncMigratesLegacyAutoAndSlashModelIDs(t *testing.T) {
	cfg := Config{
		AutoModel: AutoModelConfig{Enabled: true, Models: []string{"custom-sync/vendor/live", "Sync/vendor/hidden"}, VisionModels: []string{"custom-sync/vendor/vision"}},
		Providers: []ProviderConfig{{ID: "custom-sync", Name: "Sync", Type: "openai", Enabled: true,
			Models: []string{"vendor/live", "vendor/hidden", "vendor/vision"}, EnabledModels: []string{"vendor/live", "vendor/vision"}}},
	}
	cfg = mergeDefaultConfig(cfg)
	assertPublishSyncAutos(t, cfg, []string{"Sync/vendor/live", "Sync/vendor/vision"})
}

func TestPublishSyncDistinguishesPublishSelectionFromTransientFailures(t *testing.T) {
	for _, mode := range []string{"probe-failed", "quota", "stream-error", "disabled-provider", "default-selection", "explicit-none"} {
		t.Run(mode, func(t *testing.T) {
			cfg := publishSyncConfig()
			p := &cfg.Providers[0]
			want := []string{"Sync/b", "Sync/a"}
			switch mode {
			case "probe-failed":
				p.AvailabilityCheckedAt = 1
				p.AvailableModels = nil
				p.ModelErrors = map[string]string{"a": "rate limited", "b": "upstream failed"}
			case "quota":
				p.QuotaBlockedModels = []string{"a", "b"}
			case "stream-error":
				p.ModelErrors = map[string]string{"a": "unexpected EOF"}
			case "disabled-provider":
				p.Enabled = false
			case "default-selection":
				p.EnabledModels = nil
				p.ProviderSpecificData = nil
				want = []string{"Sync/b", "Sync/hidden", "Sync/a"}
			case "explicit-none":
				p.EnabledModels = nil
				want = nil
			}
			cfg = mergeDefaultConfig(cfg)
			if !reflect.DeepEqual(uniqueStrings(cfg.AutoModels[0].Models), uniqueStrings(want)) {
				t.Fatalf("models = %v, want %v", cfg.AutoModels[0].Models, want)
			}
		})
	}
}

func publishSyncRequest(s *Server, url string, body any) *http.Request {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	r.AddCookie(&http.Cookie{Name: "nr_admin", Value: s.adminCookieValue()})
	return r
}

func TestPublishSyncSelectionRemovesFromEveryAutoAndDoesNotReadd(t *testing.T) {
	s := &Server{dataDir: t.TempDir(), adminSecret: "test", config: mergeDefaultConfig(publishSyncConfig())}
	selectModels := func(models []string) {
		t.Helper()
		r := publishSyncRequest(s, "/api/provider/selection", map[string]any{"id": "custom-sync", "enabled_models": models})
		w := httptest.NewRecorder()
		s.handleProviderSelection(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("selection: %d %s", w.Code, w.Body.String())
		}
	}
	selectModels([]string{"b"})
	assertPublishSyncAutos(t, s.currentConfig(), []string{"Sync/b"}, []string{"Sync/b"}, nil)
	selectModels([]string{"a", "b", "hidden"})
	assertPublishSyncAutos(t, s.currentConfig(), []string{"Sync/b"}, []string{"Sync/b"}, nil)
	cfg, err := loadConfig(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	assertPublishSyncAutos(t, cfg, []string{"Sync/b"}, []string{"Sync/b"}, nil)
}

func TestPublishSyncCatalogRemovalAndTransientRuntimeUpdate(t *testing.T) {
	s := &Server{dataDir: t.TempDir(), config: mergeDefaultConfig(publishSyncConfig())}
	_, err := s.mutateProvider("custom-sync", func(p *ProviderConfig) error {
		p.AvailabilityCheckedAt = 1
		p.AvailableModels = nil
		p.ModelErrors = map[string]string{"a": "429 too many requests", "b": "unexpected EOF"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPublishSyncAutos(t, s.currentConfig(), []string{"Sync/b", "Sync/a"}, []string{"Sync/a", "Sync/b"}, nil)
	_, err = s.mutateProvider("custom-sync", func(p *ProviderConfig) error {
		p.Models = []string{"a", "hidden"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPublishSyncAutos(t, s.currentConfig(), []string{"Sync/a"}, []string{"Sync/a"}, nil)
}

func TestPublishSyncConfigSaveCannotReintroduceStaleAutoRefs(t *testing.T) {
	for _, importing := range []bool{false, true} {
		t.Run(map[bool]string{false: "save", true: "import"}[importing], func(t *testing.T) {
			s := &Server{dataDir: t.TempDir(), adminSecret: "test", config: mergeDefaultConfig(publishSyncConfig())}
			r := publishSyncRequest(s, "/api/config", publishSyncConfig())
			r.Header.Set("If-Match", configVersion(s.currentConfig()))
			if importing {
				r.Header.Set("X-9Router-Config-Import", "true")
			}
			w := httptest.NewRecorder()
			s.handleConfig(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("config save: %d %s", w.Code, w.Body.String())
			}
			assertPublishSyncAutos(t, s.currentConfig(), []string{"Sync/b", "Sync/a"}, []string{"Sync/a", "Sync/b"}, nil)
		})
	}
}

func TestPublishSyncSelectionClearsEveryAutoWithoutDeletingLists(t *testing.T) {
	s := &Server{dataDir: t.TempDir(), adminSecret: "test", config: mergeDefaultConfig(publishSyncConfig())}
	w := httptest.NewRecorder()
	s.handleProviderSelection(w, publishSyncRequest(s, "/api/provider/selection", map[string]any{"id": "custom-sync", "enabled_models": []string{}}))
	if w.Code != http.StatusOK {
		t.Fatalf("clear selection: %d %s", w.Code, w.Body.String())
	}
	assertPublishSyncAutos(t, s.currentConfig(), nil, nil, nil)
	cfg, err := loadConfig(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	assertPublishSyncAutos(t, cfg, nil, nil, nil)
}

func TestPublishSyncDeletingProviderPreservesOtherProviderAndOrder(t *testing.T) {
	cfg := mergeDefaultConfig(publishSyncConfig())
	cfg.Providers = append(cfg.Providers, ProviderConfig{ID: "custom-other", Name: "Other", Type: "openai", Enabled: true, Models: []string{"x", "y"}})
	for i := range cfg.AutoModels {
		cfg.AutoModels[i].Models = append([]string{"Other/y"}, append(cfg.AutoModels[i].Models, "Other/x")...)
	}
	s := &Server{dataDir: t.TempDir(), adminSecret: "test", config: cloneConfig(cfg)}
	for i, p := range cfg.Providers {
		if p.ID == "custom-sync" {
			cfg.Providers = append(cfg.Providers[:i], cfg.Providers[i+1:]...)
			break
		}
	}
	r := publishSyncRequest(s, "/api/config", cfg)
	r.Header.Set("If-Match", configVersion(s.currentConfig()))
	w := httptest.NewRecorder()
	s.handleConfig(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("delete provider: %d %s", w.Code, w.Body.String())
	}
	assertPublishSyncAutos(t, s.currentConfig(), []string{"Other/y", "Other/x"}, []string{"Other/y", "Other/x"}, []string{"Other/y", "Other/x"})
}
