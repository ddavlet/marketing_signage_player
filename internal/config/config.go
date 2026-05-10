package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/BurntSushi/toml"
)

const DefaultPath = "/etc/marketing-signage/config.toml"

// Snapshot is the on-disk schema. Only fields that should survive restarts
// belong here; per-device settings (sync interval, schedule) come from the
// heartbeat response and live in memory only.
type Snapshot struct {
	ServerURL     string `toml:"server_url"`
	DeviceKey     string `toml:"device_key"`
	UpdateChannel string `toml:"update_channel"`
	LogLevel      string `toml:"log_level"`
	ChromiumPath  string `toml:"chromium_path"`
	DataDir       string `toml:"data_dir"`
}

// Store wraps a Snapshot with atomic load/save and concurrent access.
type Store struct {
	path string
	mu   sync.RWMutex
	data Snapshot
}

func defaults() Snapshot {
	return Snapshot{
		UpdateChannel: "stable",
		LogLevel:      "info",
		DataDir:       "/var/lib/marketing-signage",
	}
}

func Load(path string) (*Store, error) {
	s := &Store{
		path: path,
		data: defaults(),
	}
	if _, err := toml.DecodeFile(path, &s.data); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return s, nil
}

func (s *Store) Path() string { return s.path }

func (s *Store) Get() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data
}

func (s *Store) HasDeviceKey() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.DeviceKey != ""
}

func (s *Store) DeviceKey() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.DeviceKey
}

func (s *Store) SetDeviceKey(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.DeviceKey = key
	return s.saveLocked()
}

func (s *Store) ClearDeviceKey() error {
	return s.SetDeviceKey("")
}

func (s *Store) saveLocked() error {
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".config.toml.*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			os.Remove(tmpPath)
		}
	}()
	if err := toml.NewEncoder(tmp).Encode(s.data); err != nil {
		tmp.Close()
		return fmt.Errorf("encode: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	committed = true
	return nil
}
