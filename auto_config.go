package main

import "strings"

func configuredAutoModels(cfg Config) []AutoModelConfig {
	if len(cfg.AutoModels) > 0 {
		return cfg.AutoModels
	}
	legacy := cfg.AutoModel
	legacy.ID = "auto"
	return []AutoModelConfig{legacy}
}

func autoModelConfigByID(cfg Config, id string) (AutoModelConfig, bool) {
	id = strings.TrimSpace(id)
	for _, auto := range configuredAutoModels(cfg) {
		if strings.TrimSpace(auto.ID) == id {
			return auto, true
		}
	}
	return AutoModelConfig{}, false
}

func (s *Server) enabledAutoModelConfig(id string) (AutoModelConfig, bool) {
	auto, ok := autoModelConfigByID(s.currentConfig(), id)
	return auto, ok && auto.Enabled
}

func enabledAutoModelConfigs(cfg Config) []AutoModelConfig {
	var out []AutoModelConfig
	for _, auto := range configuredAutoModels(cfg) {
		if auto.Enabled {
			out = append(out, auto)
		}
	}
	return out
}

func normalizeAutoModelConfigs(cfg *Config) {
	if cfg == nil {
		return
	}
	autos := append([]AutoModelConfig(nil), cfg.AutoModels...)
	if len(autos) == 0 {
		legacy := cfg.AutoModel
		legacy.ID = "auto"
		autos = []AutoModelConfig{legacy}
	} else {
		hasDefault := false
		for i := range autos {
			autos[i].ID = strings.TrimSpace(autos[i].ID)
			if autos[i].ID == "auto" {
				hasDefault = true
			}
		}
		if !hasDefault {
			legacy := cfg.AutoModel
			legacy.ID = "auto"
			autos = append([]AutoModelConfig{legacy}, autos...)
		}
	}
	for i := range autos {
		autos[i].ID = strings.TrimSpace(autos[i].ID)
		autos[i].Models = uniqueStrings(append(autos[i].Models, autos[i].VisionModels...))
		autos[i].VisionModels = nil
	}
	cfg.AutoModels = autos
	syncLegacyAutoModel(cfg)
}

func syncLegacyAutoModel(cfg *Config) {
	if cfg == nil {
		return
	}
	legacy := AutoModelConfig{}
	for _, auto := range cfg.AutoModels {
		if auto.ID == "auto" {
			legacy = auto
			break
		}
	}
	legacy.ID = ""
	legacy.VisionModels = nil
	cfg.AutoModel = legacy
}

func validAutoModelID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}
