package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"
)

const clineModelSyncDefaultMinutes int64 = 60

var errClineModelSyncBusy = errors.New("已有模型探测或更新任务正在执行，请稍后再试")

type ClineModelSync struct {
	Enabled           bool           `json:"enabled"`
	IntervalMinutes   int64          `json:"interval_minutes,omitempty"`
	OfficialModels    []string       `json:"official_models,omitempty"`
	ManualModels      []string       `json:"manual_models,omitempty"`
	FailureCounts     map[string]int `json:"failure_counts,omitempty"`
	LastRunAt         int64          `json:"last_run_at,omitempty"`
	LastError         string         `json:"last_error,omitempty"`
	LastSummary       string         `json:"last_summary,omitempty"`
	LastRemovedModels []string       `json:"last_removed_models,omitempty"`
}

type clineModelProbeResult struct {
	ok, permanent, pause bool
	err                  string
	latency              int64
}

func clineModelSyncConfigStale(current, incoming Config) bool {
	for _, stored := range current.Providers {
		if stored.Type != "cline" || stored.ClineModelSync == nil || stored.ClineModelSync.LastRunAt == 0 {
			continue
		}
		for _, submitted := range incoming.Providers {
			if submitted.ID == stored.ID && (submitted.ClineModelSync == nil || submitted.ClineModelSync.LastRunAt < stored.ClineModelSync.LastRunAt) {
				return true
			}
		}
	}
	return false
}

// Snapshots are deep copies: probes may refresh accounts while the catalog job runs.
func (s *Server) clineModelSyncSnapshot(id string) (ProviderConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.config.Providers {
		if p.ID == id && p.Type == "cline" {
			return cloneClineModelSyncProvider(p), true
		}
	}
	return ProviderConfig{}, false
}

func cloneClineModelSyncProvider(p ProviderConfig) ProviderConfig {
	var copy ProviderConfig
	_ = json.Unmarshal(mustJSON(p), &copy)
	if copy.ClineModelSync == nil {
		copy.ClineModelSync = &ClineModelSync{IntervalMinutes: clineModelSyncDefaultMinutes}
	}
	if copy.ClineModelSync.FailureCounts == nil {
		copy.ClineModelSync.FailureCounts = map[string]int{}
	}
	return copy
}

func (s *Server) handleClineModelSyncSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID              string `json:"id"`
		Enabled         bool   `json:"enabled"`
		IntervalMinutes int64  `json:"interval_minutes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if body.IntervalMinutes < 1 || body.IntervalMinutes > 10080 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "更新间隔必须是 1 到 10080 分钟"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.config.Providers {
		if p.ID != strings.TrimSpace(body.ID) || p.Type != "cline" {
			continue
		}
		p = cloneClineModelSyncProvider(p)
		p.ClineModelSync.Enabled = body.Enabled
		p.ClineModelSync.IntervalMinutes = body.IntervalMinutes
		s.config.Providers[i] = p
		if err := saveConfig(s.dataDir, s.config); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sync": p.ClineModelSync})
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"error": "Cline provider not found"})
}

func clineModelSyncDue(p ProviderConfig, now time.Time) bool {
	state := p.ClineModelSync
	if !p.Enabled || state == nil || !state.Enabled || len(clineProviderAccounts(p)) == 0 {
		return false
	}
	interval := state.IntervalMinutes
	if interval <= 0 || interval > 10080 {
		interval = clineModelSyncDefaultMinutes
	}
	return state.LastRunAt == 0 || now.Unix()-state.LastRunAt >= interval*60
}

func (s *Server) clineModelSyncLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		for _, p := range s.enabledProviders() {
			latest, ok := s.clineModelSyncSnapshot(p.ID)
			if ok && clineModelSyncDue(latest, time.Now()) {
				_, _ = s.syncClineModels(context.Background(), p.ID)
			}
		}
		<-ticker.C
	}
}

func clineCatalogCandidates(p ProviderConfig, official []string) ([]string, []string, []string) {
	state := p.ClineModelSync
	previousOfficial, currentOfficial := sliceSet(state.OfficialModels), sliceSet(official)
	manual := sliceSet(orderedIntersection(p.Models, state.ManualModels))
	for _, model := range p.Models {
		// Existing configs have no provenance; preserve unknown old entries and probe them.
		if !previousOfficial[model] && !currentOfficial[model] {
			manual[model] = true
		}
	}
	var candidates, retired []string
	locked := sliceSet(p.LockedModels)
	for _, model := range uniqueStrings(p.Models) {
		if previousOfficial[model] && !currentOfficial[model] && !manual[model] && !locked[model] {
			retired = append(retired, model)
		} else {
			candidates = append(candidates, model)
		}
	}
	candidates = uniqueStrings(append(candidates, official...))
	return candidates, orderedIntersection(candidates, keysFromSet(manual)), retired
}

func (s *Server) probeClineCatalogModel(ctx context.Context, p ProviderConfig, model string) clineModelProbeResult {
	started := time.Now()
	raw := mustJSON(openAIProbeRequest(p.ID+"/"+model, probeMaxTokens(p)))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(raw))
	req = req.WithContext(context.WithValue(ctx, internalBypassKey{}, true))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.handleChatCompletions(rr, req)
	result := clineModelProbeResult{latency: time.Since(started).Milliseconds()}
	if ctx.Err() != nil {
		result.err = "请求超时"
		return result
	}
	body := rr.Body.Bytes()
	result.ok = rr.Code >= 200 && rr.Code < 300 && !probeResponseHasExplicitError(body)
	if result.ok {
		return result
	}
	result.err = formatProbeFailure(rr.Code, string(body))
	result.pause = isCredentialKeyError(rr.Code, body) || isQuotaKeyError(rr.Code, body) || strings.Contains(string(body), "登录已失效")
	lower := strings.ToLower(string(body))
	for _, marker := range []string{"model_not_found", "model not found", "model does not exist", "model has been deleted", "unknown model"} {
		if strings.Contains(lower, marker) {
			result.permanent = true
		}
	}
	return result
}

func (s *Server) recordClineModelSyncFailure(id string, started time.Time, reason string, official, manual []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.config.Providers {
		if p.ID == id && p.Type == "cline" {
			p = cloneClineModelSyncProvider(p)
			if len(official) > 0 {
				selected := providerPublishedModelIDs(p)
				p.Models = uniqueStrings(append(p.Models, official...))
				p.EnabledModels = selected
				if p.ProviderSpecificData == nil {
					p.ProviderSpecificData = map[string]string{}
				}
				p.ProviderSpecificData["manualPublishOverride"] = "true"
				p.ProviderSpecificData["apiModelsFetched"] = "true"
				// Pausing cleanup must not discard the successfully fetched catalog.
				// Keep old provenance as well, so retirements can be cleaned next time.
				p.ClineModelSync.OfficialModels = uniqueStrings(append(p.ClineModelSync.OfficialModels, official...))
				p.ClineModelSync.ManualModels = uniqueStrings(append(p.ClineModelSync.ManualModels, manual...))
			}
			p.ClineModelSync.LastRunAt = started.Unix()
			p.ClineModelSync.LastError = reason
			p.ClineModelSync.LastSummary = "本轮未清理，已保留原模型列表"
			p.ClineModelSync.LastRemovedModels = nil
			s.config.Providers[i] = p
			return saveConfig(s.dataDir, s.config)
		}
	}
	return errors.New("Cline provider was removed during update")
}

func (s *Server) syncClineModels(ctx context.Context, id string) (*ClineModelSync, error) {
	if !s.probeMu.TryLock() {
		return nil, errClineModelSyncBusy
	}
	defer s.probeMu.Unlock()
	started := time.Now()
	p, ok := s.clineModelSyncSnapshot(id)
	if !ok || !p.Enabled {
		return nil, errors.New("Cline 渠道不存在或已停用")
	}
	if len(clineProviderAccounts(p)) == 0 {
		s.markProviderAuthState(id, "needs_login", errClineLoginRequired.Error())
		_ = s.recordClineModelSyncFailure(id, started, errClineLoginRequired.Error(), nil, nil)
		return nil, errClineLoginRequired
	}
	official, err := fetchClineFreeModels(ctx, s.client)
	if err != nil {
		if saveErr := s.recordClineModelSyncFailure(id, started, err.Error(), nil, nil); saveErr != nil {
			return nil, saveErr
		}
		return nil, err
	}
	candidates, manual, retired := clineCatalogCandidates(p, official)
	results := map[string]clineModelProbeResult{}
	locked := sliceSet(p.LockedModels)
	for _, model := range chatModelIDs(p, candidates) {
		if locked[model] {
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		result := s.probeClineCatalogModel(probeCtx, p, model)
		cancel()
		if result.pause || ctx.Err() != nil {
			reason := result.err
			if ctx.Err() != nil {
				reason = "更新被取消"
			}
			if err := s.recordClineModelSyncFailure(id, started, "暂停清理："+reason, official, manual); err != nil {
				return nil, err
			}
			latest, _ := s.clineModelSyncSnapshot(id)
			return latest.ClineModelSync, nil
		}
		results[model] = result
	}
	state, err := s.applyClineModelSyncResults(id, p, official, candidates, manual, retired, results, started)
	if err != nil {
		return nil, err
	}
	// Run the existing once-only image check after committing new models. Its time
	// is not included in the text latency, and it never controls model deletion.
	for _, model := range candidates {
		if result := results[model]; result.ok {
			s.clearAutoModelFailure(id, model)
			latest, exists := s.clineModelSyncSnapshot(id)
			if !exists || !latest.Enabled || ctx.Err() != nil {
				break
			}
			imageCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			s.probeModelMultimodalOnce(imageCtx, latest, model)
			cancel()
		}
	}
	return state, nil
}

func (s *Server) applyClineModelSyncResults(id string, original ProviderConfig, official, candidates, manual, retired []string, results map[string]clineModelProbeResult, started time.Time) (*ClineModelSync, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.config.Providers {
		if p.ID != id || p.Type != "cline" {
			continue
		}
		if !p.Enabled {
			return nil, errors.New("Cline 渠道已停用，未应用更新")
		}
		p = cloneClineModelSyncProvider(p)
		state := p.ClineModelSync
		selected := providerPublishedModelIDs(p)
		originalSet, latestSet := sliceSet(original.Models), sliceSet(p.Models)
		var models []string
		for _, model := range candidates {
			if !originalSet[model] || latestSet[model] {
				models = append(models, model)
			}
		}
		// Preserve manual additions or newly locked models made while probing.
		models = uniqueStrings(append(models, p.Models...))
		state.ManualModels = uniqueStrings(append(manual, state.ManualModels...))
		removed := sliceSet(retired)
		for _, model := range state.ManualModels {
			delete(removed, model)
		}
		for _, model := range p.LockedModels {
			delete(removed, model)
		}
		available := sliceSet(p.AvailableModels)
		if p.ModelLatencyMS == nil {
			p.ModelLatencyMS = map[string]int64{}
		}
		if p.ModelErrors == nil {
			p.ModelErrors = map[string]string{}
		}
		if p.ModelFailureCounts == nil {
			p.ModelFailureCounts = map[string]int{}
		}
		var usable int
		modelSet, lockedSet := sliceSet(models), sliceSet(p.LockedModels)
		for model, result := range results {
			if !modelSet[model] || lockedSet[model] {
				continue
			}
			p.ModelLatencyMS[model] = result.latency
			if result.ok {
				usable++
				available[model] = true
				delete(state.FailureCounts, model)
				delete(p.ModelFailureCounts, model)
				delete(p.ModelErrors, model)
				p.QuotaBlockedModels = removeString(p.QuotaBlockedModels, model)
			} else {
				state.FailureCounts[model]++
				p.ModelErrors[model] = result.err
				if result.permanent || state.FailureCounts[model] >= autoProbeFailureThreshold {
					removed[model] = true
				}
			}
		}
		state.LastRemovedModels = orderedIntersection(models, keysFromSet(removed))
		p.Models = nil
		for _, model := range models {
			if !removed[model] {
				p.Models = append(p.Models, model)
				continue
			}
			delete(available, model)
			delete(p.ModelKinds, model)
			delete(p.ModelMultimodal, model)
			delete(p.ModelLatencyMS, model)
			delete(p.ModelErrors, model)
			delete(p.ModelFailureCounts, model)
			p.QuotaBlockedModels = removeString(p.QuotaBlockedModels, model)
			delete(p.ProviderSpecificData, modelFailedKeyIndexesDataKey(model))
		}
		p.EnabledModels = orderedIntersection(p.Models, selected)
		p.AvailableModels = orderedIntersection(p.Models, keysFromSet(available))
		p.AvailabilityCheckedAt = time.Now().Unix()
		p.FetchModels = true
		if p.ProviderSpecificData == nil {
			p.ProviderSpecificData = map[string]string{}
		}
		p.ProviderSpecificData["apiModelsFetched"] = "true"
		p.ProviderSpecificData["manualPublishOverride"] = "true"
		// Keep provenance for locked retired entries so unlocking them later can
		// remove them as official retirements, rather than treating them as manual.
		state.OfficialModels = uniqueStrings(append(append([]string(nil), official...), orderedIntersection(p.LockedModels, state.OfficialModels)...))
		state.ManualModels = orderedIntersection(p.Models, state.ManualModels)
		state.LastRunAt = started.Unix()
		state.LastError = ""
		state.LastSummary = fmt.Sprintf("官方列表 %d 个，探测可用 %d 个，删除 %d 个", len(official), usable, len(state.LastRemovedModels))
		s.config.Providers[i] = p
		pruneAutoModelsForProviderLocked(&s.config, p)
		for _, model := range state.LastRemovedModels {
			for groupIndex := range s.config.ModelGroups {
				group := &s.config.ModelGroups[groupIndex]
				group.Models = removeString(removeString(group.Models, providerModelRef(p, model)), p.ID+"/"+model)
			}
		}
		if err := saveConfig(s.dataDir, s.config); err != nil {
			return nil, err
		}
		return cloneClineModelSyncProvider(p).ClineModelSync, nil
	}
	return nil, errors.New("Cline 渠道在更新期间被删除")
}
