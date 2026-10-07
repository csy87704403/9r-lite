package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func clineSyncTestServer(t *testing.T, p ProviderConfig, official []string, respond func(*http.Request, string) (int, string)) *Server {
	t.Helper()
	return &Server{config: Config{Providers: []ProviderConfig{p}}, dataDir: t.TempDir(), client: &http.Client{Transport: clineCatalogTransport(func(r *http.Request) (*http.Response, error) {
		status, body := http.StatusOK, ""
		if r.URL.String() == clineRecommendedModelsURL {
			entries := []map[string]string{}
			for _, model := range official {
				entries = append(entries, map[string]string{"id": model})
			}
			body = string(mustJSON(map[string]any{"free": entries}))
		} else {
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			messages, _ := request["messages"].([]any)
			textProbe := false
			if len(messages) > 0 {
				message, _ := messages[0].(map[string]any)
				_, textProbe = message["content"].(string)
			}
			if _, present := request["max_tokens"]; present && textProbe {
				t.Fatal("Cline probe must not send max_tokens")
			}
			status, body = respond(r, stringValue(request["model"]))
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
}

func clineSyncSuccess() (int, string) {
	return 200, `{"data":{"choices":[{"message":{"content":"OK"}}],"usage":{"total_tokens":7}},"success":true}`
}

func TestClineModelSyncRefreshProbeAndCleanup(t *testing.T) {
	p := ProviderConfig{
		ID: "cline", Type: "cline", Enabled: true, AccessToken: "test",
		Models:          []string{"keep", "retired", "manual", "missing", "unstable", "locked"},
		EnabledModels:   []string{"keep", "retired", "manual", "missing", "unstable", "locked"},
		AvailableModels: []string{"keep", "retired", "manual", "missing", "unstable", "locked"},
		LockedModels:    []string{"locked"}, ModelMultimodal: map[string]bool{"keep": true, "manual": true, "retired": true},
		ModelLatencyMS: map[string]int64{"retired": 10, "locked": 99}, ModelErrors: map[string]string{"retired": "old error"},
		ModelKinds: map[string]string{"retired": "chat"}, QuotaBlockedModels: []string{"retired"},
		ClineModelSync: &ClineModelSync{OfficialModels: []string{"keep", "retired", "missing", "unstable", "locked"}, ManualModels: []string{"manual"}},
	}
	var probed []string
	s := clineSyncTestServer(t, p, []string{"keep", "new", "missing", "unstable", "locked"}, func(r *http.Request, model string) (int, string) {
		probed = append(probed, model)
		switch model {
		case "missing":
			return 404, `{"error":"model_not_found"}`
		case "unstable":
			return 503, `{"error":"temporarily unavailable"}`
		default:
			return clineSyncSuccess()
		}
	})
	s.config.AutoModels = []AutoModelConfig{{ID: "auto", Enabled: true, Models: []string{"cline/keep", "cline/retired", "cline/missing", "other/model"}}, {ID: "auto-2", Enabled: true, Models: []string{"cline/retired", "cline/manual"}}}
	s.config.ModelGroups = []ModelGroup{{ID: "g", Models: []string{"cline/retired", "cline/missing", "cline/manual", "other/model"}}}
	state, err := s.syncClineModels(t.Context(), "cline")
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := s.clineModelSyncSnapshot("cline")
	if got := strings.Join(stored.Models, ","); got != "keep,manual,unstable,locked,new" {
		t.Fatalf("models=%s", got)
	}
	if len(state.LastRemovedModels) != 2 || !sliceSet(state.LastRemovedModels)["retired"] || !sliceSet(state.LastRemovedModels)["missing"] {
		t.Fatalf("removed=%v", state.LastRemovedModels)
	}
	if strings.Join(state.ManualModels, ",") != "manual" || state.FailureCounts["unstable"] != 1 {
		t.Fatalf("state=%+v", state)
	}
	if stored.ModelLatencyMS["locked"] != 99 || !stored.ModelMultimodal["keep"] || stored.ModelErrors["unstable"] == "" {
		t.Fatal("existing probe state lost")
	}
	if sliceSet(stored.EnabledModels)["new"] {
		t.Fatal("new model was published without selection")
	}
	if strings.Contains(strings.Join(probed, ","), "locked") || strings.Contains(strings.Join(probed, ","), "retired") {
		t.Fatalf("should not probe locked or retired models: %v", probed)
	}
	if strings.Join(s.config.AutoModels[0].Models, ",") != "cline/keep,other/model" || strings.Join(s.config.AutoModels[1].Models, ",") != "cline/manual" || strings.Join(s.config.ModelGroups[0].Models, ",") != "cline/manual,other/model" {
		t.Fatal("model references were not cleaned")
	}
	if _, exists := stored.ModelMultimodal["retired"]; exists || stored.ModelErrors["retired"] != "" || stored.ModelLatencyMS["retired"] != 0 || len(stored.QuotaBlockedModels) != 0 {
		t.Fatal("deleted model metadata retained")
	}
	if s.usage.Groups != nil {
		t.Fatal("catalog probes must not enter client usage statistics")
	}
}

func TestClineModelSyncTemporaryFailuresAndRecovery(t *testing.T) {
	p := ProviderConfig{ID: "cline", Type: "cline", Enabled: true, AccessToken: "test", Models: []string{"model"}, AvailableModels: []string{"model"}, ModelMultimodal: map[string]bool{"model": true}}
	status := 429
	s := clineSyncTestServer(t, p, []string{"model"}, func(r *http.Request, model string) (int, string) {
		if status == 200 {
			return clineSyncSuccess()
		}
		return status, `{"error":"too many requests"}`
	})
	for round, code := range []int{429, 200, 429, 503, 429, 200} {
		status = code
		state, err := s.syncClineModels(t.Context(), "cline")
		if err != nil {
			t.Fatal(err)
		}
		stored, _ := s.clineModelSyncSnapshot("cline")
		if round == 4 {
			if len(stored.Models) != 0 || len(state.LastRemovedModels) != 1 {
				t.Fatal("third consecutive failure did not delete model")
			}
		} else if len(stored.Models) != 1 {
			t.Fatalf("unexpected deletion in round %d", round+1)
		}
		if code == 200 && state.FailureCounts["model"] != 0 {
			t.Fatal("success did not reset failure count")
		}
		// Reloading config must preserve the round counter across restarts.
		cfg, err := loadConfig(s.dataDir)
		if err != nil {
			t.Fatal(err)
		}
		s.config = cfg
	}
}

func TestClineModelSyncAccountErrorsNeverDeleteModels(t *testing.T) {
	for _, status := range []int{401, 402} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			p := ProviderConfig{ID: "cline", Type: "cline", Enabled: true, Models: []string{"missing", "blocked", "retired"}, ClineAccounts: []ClineAccount{{AccessToken: "one"}, {AccessToken: "two"}}, ModelLatencyMS: map[string]int64{"missing": 42}, ClineModelSync: &ClineModelSync{OfficialModels: []string{"missing", "blocked", "retired"}, FailureCounts: map[string]int{"missing": 2}}}
			var blockedCalls int
			s := clineSyncTestServer(t, p, []string{"missing", "blocked", "new"}, func(r *http.Request, model string) (int, string) {
				if model == "missing" {
					return 404, `{"error":"model_not_found"}`
				}
				blockedCalls++
				if status == 402 {
					return 402, `{"error":"insufficient balance"}`
				}
				return 401, `{"error":"unauthorized"}`
			})
			state, err := s.syncClineModels(t.Context(), "cline")
			if err != nil {
				t.Fatal(err)
			}
			stored, _ := s.clineModelSyncSnapshot("cline")
			if strings.Join(stored.Models, ",") != "missing,blocked,retired,new" || state.FailureCounts["missing"] != 2 || stored.ModelLatencyMS["missing"] != 42 || len(state.LastRemovedModels) != 0 || state.LastError == "" || blockedCalls != 2 {
				t.Fatalf("paused round changed models or failed to rotate: state=%+v calls=%d", state, blockedCalls)
			}
		})
	}
}

func TestClineModelSyncEmptyCatalogAndBusyPreserveModels(t *testing.T) {
	p := ProviderConfig{ID: "cline", Type: "cline", Enabled: true, AccessToken: "test", Models: []string{"old"}}
	s := clineSyncTestServer(t, p, nil, func(*http.Request, string) (int, string) { t.Fatal("empty catalog must not probe"); return 0, "" })
	if _, err := s.syncClineModels(t.Context(), "cline"); err == nil {
		t.Fatal("expected catalog error")
	}
	stored, _ := s.clineModelSyncSnapshot("cline")
	if strings.Join(stored.Models, ",") != "old" || stored.ClineModelSync.LastError == "" {
		t.Fatal("empty catalog cleared models")
	}
	s.probeMu.Lock()
	_, err := s.syncClineModels(context.Background(), "cline")
	s.probeMu.Unlock()
	if !errors.Is(err, errClineModelSyncBusy) {
		t.Fatalf("busy error=%v", err)
	}
}

func TestClineModelSyncKeepsConcurrentManualChanges(t *testing.T) {
	p := ProviderConfig{ID: "cline", Type: "cline", Enabled: true, AccessToken: "test", Models: []string{"keep", "user-deleted"}, ModelMultimodal: map[string]bool{"keep": true}, ClineModelSync: &ClineModelSync{OfficialModels: []string{"keep", "user-deleted"}}}
	s := clineSyncTestServer(t, p, []string{"keep", "user-deleted"}, func(*http.Request, string) (int, string) { return clineSyncSuccess() })
	original, _ := s.clineModelSyncSnapshot("cline")
	latest, _ := s.clineModelSyncSnapshot("cline")
	latest.Models = []string{"keep", "user-added"}
	latest.ClineModelSync.ManualModels = []string{"user-added"}
	if err := s.updateProvider(latest); err != nil {
		t.Fatal(err)
	}
	_, err := s.applyClineModelSyncResults("cline", original, []string{"keep", "user-deleted"}, []string{"keep", "user-deleted"}, nil, nil, map[string]clineModelProbeResult{"keep": {ok: true, latency: 10}, "user-deleted": {ok: true}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := s.clineModelSyncSnapshot("cline")
	if strings.Join(stored.Models, ",") != "keep,user-added" || strings.Join(stored.ClineModelSync.ManualModels, ",") != "user-added" {
		t.Fatal("concurrent model edits were lost")
	}
}

func TestClineModelSyncScheduleAndSettings(t *testing.T) {
	now := time.Now()
	p := ProviderConfig{ID: "cline", Type: "cline", Enabled: true, AccessToken: "test", ClineModelSync: &ClineModelSync{Enabled: true, IntervalMinutes: 60, LastRunAt: now.Add(-time.Hour).Unix()}}
	if !clineModelSyncDue(p, now) {
		t.Fatal("hourly refresh not due")
	}
	p.ClineModelSync.LastRunAt = now.Add(-time.Minute).Unix()
	if clineModelSyncDue(p, now) {
		t.Fatal("refresh ran too early")
	}
	p.ClineModelSync.LastRunAt = 0
	if !clineModelSyncDue(p, now) {
		t.Fatal("newly enabled refresh should run immediately")
	}
	p.Enabled = false
	if clineModelSyncDue(p, now) {
		t.Fatal("disabled provider must not refresh")
	}
	p.Enabled = true
	s := clineSyncTestServer(t, p, []string{"model"}, func(*http.Request, string) (int, string) { return clineSyncSuccess() })
	for _, tc := range []struct {
		body          string
		authenticated bool
		want          int
	}{
		{`{"id":"cline","enabled":true,"interval_minutes":10}`, false, 401},
		{`{"id":"cline","enabled":true,"interval_minutes":0}`, true, 400},
		{`{"id":"cline","enabled":true,"interval_minutes":10081}`, true, 400},
		{`{"id":"other","enabled":true,"interval_minutes":60}`, true, 404},
		{`{"id":"cline","enabled":true,"interval_minutes":15}`, true, 200},
	} {
		r := httptest.NewRequest(http.MethodPost, "/api/provider/cline-model-sync", strings.NewReader(tc.body))
		if tc.authenticated {
			r.AddCookie(&http.Cookie{Name: "nr_admin", Value: s.adminCookieValue()})
		}
		w := httptest.NewRecorder()
		s.handleClineModelSyncSettings(w, r)
		if w.Code != tc.want {
			t.Fatalf("settings status=%d want=%d", w.Code, tc.want)
		}
	}
	stored, _ := s.clineModelSyncSnapshot("cline")
	if !stored.ClineModelSync.Enabled || stored.ClineModelSync.IntervalMinutes != 15 {
		t.Fatal("settings not saved")
	}
}

func TestClineModelSyncLockedRetiredModelKeepsProvenance(t *testing.T) {
	p := ProviderConfig{ID: "cline", Type: "cline", Enabled: true, AccessToken: "test", Models: []string{"keep", "locked"}, LockedModels: []string{"locked"}, ModelMultimodal: map[string]bool{"keep": true}, ClineModelSync: &ClineModelSync{OfficialModels: []string{"keep", "locked"}}}
	s := clineSyncTestServer(t, p, []string{"keep"}, func(*http.Request, string) (int, string) { return clineSyncSuccess() })
	if _, err := s.syncClineModels(t.Context(), "cline"); err != nil {
		t.Fatal(err)
	}
	stored, _ := s.clineModelSyncSnapshot("cline")
	if !sliceSet(stored.Models)["locked"] || !sliceSet(stored.ClineModelSync.OfficialModels)["locked"] {
		t.Fatal("locked model or provenance was lost")
	}
	stored.LockedModels = nil
	if err := s.updateProvider(stored); err != nil {
		t.Fatal(err)
	}
	if _, err := s.syncClineModels(t.Context(), "cline"); err != nil {
		t.Fatal(err)
	}
	stored, _ = s.clineModelSyncSnapshot("cline")
	if sliceSet(stored.Models)["locked"] {
		t.Fatal("unlocked retired official model was misclassified as manual")
	}
}

func TestClineModelSyncManualFetchAlsoProbes(t *testing.T) {
	p := ProviderConfig{ID: "cline", Type: "cline", Enabled: true, AccessToken: "test", Models: []string{"missing"}, ClineModelSync: &ClineModelSync{ManualModels: []string{"missing"}}}
	var calls int
	s := clineSyncTestServer(t, p, []string{"new"}, func(r *http.Request, model string) (int, string) {
		calls++
		if model == "missing" {
			return 404, `{"error":"model_not_found"}`
		}
		return clineSyncSuccess()
	})
	r := httptest.NewRequest(http.MethodPost, "/api/provider/models", strings.NewReader(`{"id":"cline"}`))
	r.AddCookie(&http.Cookie{Name: "nr_admin", Value: s.adminCookieValue()})
	w := httptest.NewRecorder()
	s.handleProviderModels(w, r)
	if w.Code != 200 || calls < 2 {
		t.Fatalf("manual fetch status=%d calls=%d", w.Code, calls)
	}
	stored, _ := s.clineModelSyncSnapshot("cline")
	if strings.Join(stored.Models, ",") != "new" || len(stored.ClineModelSync.ManualModels) != 0 {
		t.Fatal("manual fetch bypassed probe or cleanup")
	}
}

func TestClineModelSyncRejectsStaleConfigSave(t *testing.T) {
	p := ProviderConfig{ID: "cline", Type: "cline", Enabled: true, AccessToken: "test", Models: []string{"keep"}, ClineModelSync: &ClineModelSync{LastRunAt: 200, FailureCounts: map[string]int{"removed": 3}}}
	s := clineSyncTestServer(t, p, []string{"keep"}, func(*http.Request, string) (int, string) { return clineSyncSuccess() })
	for _, timestamp := range []int64{100, 200} {
		incoming := cloneClineModelSyncProvider(p)
		incoming.ClineModelSync.LastRunAt = timestamp
		r := httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(string(mustJSON(Config{Providers: []ProviderConfig{incoming}}))))
		r.AddCookie(&http.Cookie{Name: "nr_admin", Value: s.adminCookieValue()})
		w := httptest.NewRecorder()
		s.handleConfig(w, r)
		want := 200
		if timestamp == 100 {
			want = 409
		}
		if w.Code != want {
			t.Fatalf("config save status=%d want=%d", w.Code, want)
		}
		stored, _ := s.clineModelSyncSnapshot("cline")
		if stored.ClineModelSync.LastRunAt != 200 || stored.ClineModelSync.FailureCounts["removed"] != 3 {
			t.Fatal("stale config rolled back catalog state")
		}
	}
}
