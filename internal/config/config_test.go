package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(`server_url = "https://example.com"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := s.Get()
	if got.ServerURL != "https://example.com" {
		t.Errorf("server_url not loaded: %q", got.ServerURL)
	}
	if got.UpdateChannel != "stable" {
		t.Errorf("default update_channel not applied: %q", got.UpdateChannel)
	}
	if got.LogLevel != "info" {
		t.Errorf("default log_level not applied: %q", got.LogLevel)
	}
}

func TestSetDeviceKeyPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(`server_url = "https://x.test"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.HasDeviceKey() {
		t.Fatal("expected empty device key")
	}
	if err := s.SetDeviceKey("k-123"); err != nil {
		t.Fatalf("set: %v", err)
	}

	s2, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := s2.DeviceKey(); got != "k-123" {
		t.Errorf("expected key persisted, got %q", got)
	}
	if got := s2.Get().ServerURL; got != "https://x.test" {
		t.Errorf("server_url lost: %q", got)
	}
}
