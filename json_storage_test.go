package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestJSONReplacementKeepsBackupAndRejectsInvalidData(t *testing.T) {
	name := filepath.Join(t.TempDir(), "sample.json")
	first, second := []byte(`{"value":1}`), []byte(`{"value":2}`)
	if err := saveJSONFile(name, first); err != nil {
		t.Fatal(err)
	}
	if err := saveJSONFile(name, second); err != nil {
		t.Fatal(err)
	}
	current, _ := os.ReadFile(name)
	backup, _ := os.ReadFile(name + ".bak")
	if !bytes.Equal(current, second) || !bytes.Equal(backup, first) {
		t.Fatal("replacement/backup incorrect")
	}
	if err := saveJSONFile(name, []byte(`{"broken":`)); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	current, _ = os.ReadFile(name)
	if !bytes.Equal(current, second) {
		t.Fatal("invalid save changed live file")
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(name), "*.tmp"))
	if len(leftovers) != 0 {
		t.Fatal("temporary files leaked")
	}
}

func TestBackupFailureCannotTruncateLiveJSON(t *testing.T) {
	name := filepath.Join(t.TempDir(), "sample.json")
	first := []byte(`{"value":1}`)
	if err := saveJSONFile(name, first); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(name+".bak", 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveJSONFile(name, []byte(`{"value":2}`)); err == nil {
		t.Fatal("backup failure ignored")
	}
	current, _ := os.ReadFile(name)
	if !bytes.Equal(current, first) {
		t.Fatal("failed save damaged original")
	}
}

func TestConfigRecoversBackupWithoutOverwritingDamagedOriginal(t *testing.T) {
	dir := t.TempDir()
	first := Config{AccessKey: "first-key", Providers: []ProviderConfig{{ID: "oc", Type: "opencode-free", Enabled: true, Models: []string{"model"}}}}
	if err := saveConfig(dir, first); err != nil {
		t.Fatal(err)
	}
	second := cloneConfig(first)
	second.AccessKey = "second-key"
	if err := saveConfig(dir, second); err != nil {
		t.Fatal(err)
	}
	broken := []byte(`{"providers":`)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), broken, 0600); err != nil {
		t.Fatal(err)
	}
	recovered, err := loadConfig(dir)
	if err != nil || recovered.AccessKey != "first-key" {
		t.Fatalf("config recovery failed: %v", err)
	}
	original, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if !bytes.Equal(original, broken) {
		t.Fatal("broken original was silently overwritten")
	}
	copies, _ := filepath.Glob(filepath.Join(dir, "config.json.corrupt-*"))
	if len(copies) == 0 {
		t.Fatal("recovery did not preserve damaged file")
	}
}

func TestUnrecoverableConfigFailsClosed(t *testing.T) {
	for _, missingPrimary := range []bool{false, true} {
		dir := t.TempDir()
		name := filepath.Join(dir, "config.json")
		if !missingPrimary {
			if err := os.WriteFile(name, []byte(`{"broken":`), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(name+".bak", []byte(`{"broken":`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfig(dir); err == nil {
			t.Fatal("damaged credentials silently replaced with defaults")
		}
	}
}

func TestUsageRecoveryAndDamagedUsageDoesNotPreventStartup(t *testing.T) {
	s := securityTestServer(t)
	s.usage = newUsageStore()
	s.usage.UpdatedAt = 111
	if err := s.saveUsage(); err != nil {
		t.Fatal(err)
	}
	s.usage.UpdatedAt = 222
	if err := s.saveUsage(); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(s.dataDir, usageFileName)
	if err := os.WriteFile(name, []byte(`{"version":`), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := loadUsage(s.dataDir)
	if err != nil || store.UpdatedAt != 111 {
		t.Fatalf("usage backup not recovered: %v", err)
	}
	if err := os.WriteFile(name+".bak", []byte(`{"version":`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(s.dataDir, s.currentConfig()); err != nil {
		t.Fatal(err)
	}
	booted, err := NewServer(s.dataDir)
	if err != nil || booted == nil {
		t.Fatalf("damaged statistics blocked gateway startup: %v", err)
	}
	if len(booted.usage.Groups) != 0 {
		t.Fatal("unrecoverable statistics not reset")
	}
	broken, _ := os.ReadFile(name)
	if json.Valid(broken) {
		t.Fatal("damaged file was overwritten during read")
	}
	copies, _ := filepath.Glob(name + ".corrupt-*")
	if len(copies) < 2 {
		t.Fatal("damaged history not retained")
	}
}

func TestAdminPasswordBackupRecoveryDoesNotRestoreDefaultPassword(t *testing.T) {
	s := securityTestServer(t)
	if err := s.changeAdminPassword("gateway", "first-new-password"); err != nil {
		t.Fatal(err)
	}
	if err := s.changeAdminPassword("first-new-password", "second-new-password"); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(s.dataDir, adminAuthFileName)
	if err := os.WriteFile(name, []byte(`{"password_hash":`), 0600); err != nil {
		t.Fatal(err)
	}
	hash, err := loadAdminPasswordHash(s.dataDir)
	if err != nil || len(hash) == 0 {
		t.Fatalf("password backup recovery failed: %v", err)
	}
	s.adminHash = hash
	if !s.verifyAdminPassword("first-new-password") || s.verifyAdminPassword("gateway") {
		t.Fatal("password recovery downgraded authentication")
	}
}
