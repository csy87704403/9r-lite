package main

import (
	"errors"
	"maps"
	"reflect"
	"slices"
	"strconv"
)

func cloneProviderConfig(p ProviderConfig) ProviderConfig {
	p.APIKeys = slices.Clone(p.APIKeys)
	p.ClineAccounts = slices.Clone(p.ClineAccounts)
	p.ProviderSpecificData = maps.Clone(p.ProviderSpecificData)
	p.Models = slices.Clone(p.Models)
	p.EnabledModels = slices.Clone(p.EnabledModels)
	p.LockedModels = slices.Clone(p.LockedModels)
	p.ModelKinds = maps.Clone(p.ModelKinds)
	p.ModelMultimodal = maps.Clone(p.ModelMultimodal)
	p.AvailableModels = slices.Clone(p.AvailableModels)
	p.QuotaBlockedModels = slices.Clone(p.QuotaBlockedModels)
	p.ModelLatencyMS = maps.Clone(p.ModelLatencyMS)
	p.ModelErrors = maps.Clone(p.ModelErrors)
	p.ModelFailureCounts = maps.Clone(p.ModelFailureCounts)
	if p.ClineModelSync != nil {
		state := *p.ClineModelSync
		state.OfficialModels = slices.Clone(state.OfficialModels)
		state.ManualModels = slices.Clone(state.ManualModels)
		state.FailureCounts = maps.Clone(state.FailureCounts)
		state.LastRemovedModels = slices.Clone(state.LastRemovedModels)
		p.ClineModelSync = &state
	}
	return p
}

func cloneAutoModelConfig(auto AutoModelConfig) AutoModelConfig {
	auto.Models = slices.Clone(auto.Models)
	auto.VisionModels = slices.Clone(auto.VisionModels)
	return auto
}

func cloneConfig(cfg Config) Config {
	cfg.AutoModel = cloneAutoModelConfig(cfg.AutoModel)
	cfg.AutoModels = slices.Clone(cfg.AutoModels)
	for i := range cfg.AutoModels {
		cfg.AutoModels[i] = cloneAutoModelConfig(cfg.AutoModels[i])
	}
	cfg.ModelGroups = slices.Clone(cfg.ModelGroups)
	for i := range cfg.ModelGroups {
		cfg.ModelGroups[i].Models = slices.Clone(cfg.ModelGroups[i].Models)
	}
	cfg.DeletedProviderIDs = slices.Clone(cfg.DeletedProviderIDs)
	cfg.Providers = slices.Clone(cfg.Providers)
	for i := range cfg.Providers {
		cfg.Providers[i] = cloneProviderConfig(cfg.Providers[i])
	}
	return cfg
}

// Runtime updates edit only their own fields on the latest stored provider.
// Never persist an entire request snapshot: another request may have refreshed it.
func (s *Server) mutateProvider(id string, mutate func(*ProviderConfig) error) (ProviderConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, stored := range s.config.Providers {
		if stored.ID != id {
			continue
		}
		next := cloneProviderConfig(stored)
		if err := mutate(&next); err != nil {
			return cloneProviderConfig(stored), err
		}
		if reflect.DeepEqual(next, stored) {
			return cloneProviderConfig(stored), nil
		}
		s.config.Providers[i] = cloneProviderConfig(next)
		pruneAutoModelsForProviderLocked(&s.config, next)
		return cloneProviderConfig(next), saveConfig(s.dataDir, s.config)
	}
	return ProviderConfig{}, errors.New("provider not found")
}

func sameProviderProbeTarget(before, current ProviderConfig) bool {
	if before.Type != current.Type || before.BaseURL != current.BaseURL || !slices.Equal(providerAPIKeys(before), providerAPIKeys(current)) {
		return false
	}
	if before.Type != "cline" {
		return before.AccessToken == current.AccessToken
	}
	oldAccounts, accounts := clineProviderAccounts(before), clineProviderAccounts(current)
	if len(oldAccounts) != len(accounts) {
		return false
	}
	for i, account := range oldAccounts {
		if findClineAccount(accounts, account, i) < 0 {
			return false
		}
	}
	return true
}

func sameProviderPublishSelection(before, current ProviderConfig) bool {
	return slices.Equal(before.EnabledModels, current.EnabledModels) && providerManualPublishOverride(before) == providerManualPublishOverride(current)
}

func applyProviderKeyStatus(p *ProviderConfig, index int, model string, failed, global bool) {
	if p.ProviderSpecificData == nil {
		p.ProviderSpecificData = map[string]string{}
	}
	updateSet := func(key string, add bool) {
		set := intSetFromCSV(p.ProviderSpecificData[key])
		if add {
			set[index] = true
		} else {
			delete(set, index)
		}
		if value := csvFromIntSet(set); value != "" {
			p.ProviderSpecificData[key] = value
		} else {
			delete(p.ProviderSpecificData, key)
		}
	}
	if failed {
		key := "failed_key_indexes"
		if !global && model != "" {
			key = modelFailedKeyIndexesDataKey(model)
		}
		updateSet(key, true)
		if global && parseProviderInt(p.ProviderSpecificData["active_key_index"], -1) == index {
			delete(p.ProviderSpecificData, "active_key_index")
		}
		return
	}
	p.ProviderSpecificData["active_key_index"] = strconv.Itoa(index)
	updateSet("failed_key_indexes", false)
	if model != "" {
		updateSet(modelFailedKeyIndexesDataKey(model), false)
	}
}
