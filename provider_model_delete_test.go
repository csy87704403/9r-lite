package main

import "testing"

func TestDeleteProviderModelCleansReferences(t *testing.T) {
	s := &Server{
		dataDir: t.TempDir(),
		config: Config{
			AutoModel:   AutoModelConfig{Enabled: true, Models: []string{"test/keep", "test/remove"}},
			ModelGroups: []ModelGroup{{ID: "group", Models: []string{"test/remove"}}},
			Providers: []ProviderConfig{{
				ID:              "test",
				Models:          []string{"keep", "remove"},
				EnabledModels:   []string{"keep", "remove"},
				AvailableModels: []string{"keep", "remove"},
				LockedModels:    []string{"remove"},
				ModelKinds:      map[string]string{"remove": "image"},
				ModelLatencyMS:  map[string]int64{"remove": 123},
				ModelErrors:     map[string]string{"remove": "failed"},
			}},
		},
	}

	p, err := s.deleteProviderModel("test", "remove")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Models) != 1 || p.Models[0] != "keep" {
		t.Fatalf("models = %#v", p.Models)
	}
	if len(p.EnabledModels) != 1 || p.EnabledModels[0] != "keep" {
		t.Fatalf("enabled models = %#v", p.EnabledModels)
	}
	if len(p.AvailableModels) != 1 || p.AvailableModels[0] != "keep" {
		t.Fatalf("available models = %#v", p.AvailableModels)
	}
	if len(p.LockedModels) != 0 || p.ModelKinds["remove"] != "" || p.ModelLatencyMS["remove"] != 0 || p.ModelErrors["remove"] != "" {
		t.Fatalf("probe state was not cleaned: %#v", p)
	}
	if len(s.config.AutoModel.Models) != 1 || s.config.AutoModel.Models[0] != "test/keep" {
		t.Fatalf("auto models = %#v", s.config.AutoModel.Models)
	}
	if len(s.config.ModelGroups[0].Models) != 0 {
		t.Fatalf("group models = %#v", s.config.ModelGroups[0].Models)
	}
}
