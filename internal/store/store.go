// Package store provides a simple JSON file store for configuration and cached data.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const configDir = ".clawclient"
const configFile = "config.json"

// Config holds all persistent configuration.
type Config struct {
	GatewayURL string `json:"gatewayUrl"`
	AuthToken  string `json:"authToken"`
	DeviceID   string `json:"deviceId"`
}

// Store manages persistent configuration.
type Store struct {
	mu       sync.RWMutex
	config   Config
	filePath string
}

// New creates a new store, loading from disk if available.
func New() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home dir: %w", err)
	}

	dir := filepath.Join(home, configDir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}

	s := &Store{
		filePath: filepath.Join(dir, configFile),
	}

	// Load existing config
	s.load()
	return s, nil
}

// Get returns the current config.
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config
}

// SetGateway updates gateway URL and token, saving to disk.
func (s *Store) SetGateway(url, token string) error {
	s.mu.Lock()
	s.config.GatewayURL = url
	s.config.AuthToken = token
	s.mu.Unlock()
	return s.save()
}

// SetDeviceID updates the device ID.
func (s *Store) SetDeviceID(id string) error {
	s.mu.Lock()
	s.config.DeviceID = id
	s.mu.Unlock()
	return s.save()
}

// IsConfigured returns true if gateway URL and token are set.
func (s *Store) IsConfigured() bool {
	cfg := s.Get()
	return cfg.GatewayURL != "" && cfg.AuthToken != ""
}

func (s *Store) load() {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return // File doesn't exist yet, that's fine
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	json.Unmarshal(data, &s.config)
}

func (s *Store) save() error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.config, "", "  ")
	s.mu.RUnlock()

	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(s.filePath, data, 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	return nil
}
