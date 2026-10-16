package main

import (
	"reflect"
	"testing"
)

func TestCurrentConfigReturnsDetachedSnapshot(t *testing.T) {
	cfg := Config{
		AutoModel:          AutoModelConfig{Models: []string{"p/model"}, VisionModels: []string{"p/model"}},
		AutoModels:         []AutoModelConfig{{Models: []string{"p/model"}, VisionModels: []string{"p/model"}}},
		ModelGroups:        []ModelGroup{{ID: "group", Models: []string{"auto"}}},
		DeletedProviderIDs: []string{"deleted"},
		Providers: []ProviderConfig{{ID: "p", APIKeys: []string{"key"}, ClineAccounts: []ClineAccount{{AccessToken: "token"}},
			ProviderSpecificData: map[string]string{"setting": "old"}, Models: []string{"model"}, EnabledModels: []string{"model"}, LockedModels: []string{"model"},
			AvailableModels: []string{"model"}, QuotaBlockedModels: []string{"blocked"}, ModelKinds: map[string]string{"model": "chat"}, ModelMultimodal: map[string]bool{"model": true},
			ModelLatencyMS: map[string]int64{"model": 1}, ModelErrors: map[string]string{"model": "error"}, ModelFailureCounts: map[string]int{"model": 1},
			ClineModelSync: &ClineModelSync{OfficialModels: []string{"official"}, ManualModels: []string{"manual"}, FailureCounts: map[string]int{"model": 1}, LastRemovedModels: []string{"removed"}},
		}},
	}
	s := &Server{config: cloneConfig(cfg)}
	copy := s.currentConfig()
	copy.AutoModel.Models[0], copy.AutoModel.VisionModels[0] = "changed", "changed"
	copy.AutoModels[0].Models[0], copy.AutoModels[0].VisionModels[0] = "changed", "changed"
	copy.ModelGroups[0].Models[0], copy.DeletedProviderIDs[0] = "changed", "changed"
	p := &copy.Providers[0]
	p.APIKeys[0], p.ClineAccounts[0].AccessToken = "changed", "changed"
	p.ProviderSpecificData["setting"] = "changed"
	p.Models[0], p.EnabledModels[0], p.LockedModels[0], p.AvailableModels[0], p.QuotaBlockedModels[0] = "changed", "changed", "changed", "changed", "changed"
	p.ModelKinds["model"], p.ModelMultimodal["model"] = "changed", false
	p.ModelLatencyMS["model"], p.ModelErrors["model"], p.ModelFailureCounts["model"] = 2, "changed", 2
	p.ClineModelSync.OfficialModels[0], p.ClineModelSync.ManualModels[0], p.ClineModelSync.LastRemovedModels[0] = "changed", "changed", "changed"
	p.ClineModelSync.FailureCounts["model"] = 2
	if !reflect.DeepEqual(s.currentConfig(), cfg) {
		t.Fatal("editing a snapshot mutated live configuration")
	}
}

func TestMutateProviderPreservesLatestUnrelatedState(t *testing.T) {
	p := ProviderConfig{ID: "p", Models: []string{"a", "b"}, AvailableModels: []string{"a", "b"}, AccessToken: "old"}
	s := &Server{config: Config{Providers: []ProviderConfig{p}}, dataDir: t.TempDir()}
	if _, err := s.mutateProvider("p", func(latest *ProviderConfig) error {
		latest.AccessToken = "new"
		latest.AvailableModels = []string{"b"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := s.mutateProvider("p", func(latest *ProviderConfig) error { latest.Email = "user@example.invalid"; return nil })
	if err != nil || updated.AccessToken != "new" || !reflect.DeepEqual(updated.AvailableModels, []string{"b"}) {
		t.Fatalf("field update rolled back newer state: %v", err)
	}
	updated.AvailableModels[0] = "changed"
	stored, _ := s.providerByID("p")
	if stored.AvailableModels[0] != "b" {
		t.Fatal("runtime updater returned a live slice")
	}
}

func TestUpdateProviderDetachesCallerFields(t *testing.T) {
	s := &Server{dataDir: t.TempDir()}
	p := ProviderConfig{ID: "p", Models: []string{"a"}, ProviderSpecificData: map[string]string{"setting": "old"}}
	if err := s.updateProvider(p); err != nil {
		t.Fatal(err)
	}
	p.Models[0], p.ProviderSpecificData["setting"] = "changed", "changed"
	stored, _ := s.providerByID("p")
	if stored.Models[0] != "a" || stored.ProviderSpecificData["setting"] != "old" {
		t.Fatal("caller retained mutable aliases after provider save")
	}
}
