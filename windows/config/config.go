package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Config struct {
	TargetMAC    string `json:"target_mac"`
	DeviceName   string `json:"device_name"`
	SharedSecret string `json:"shared_secret"`
	Paired       bool   `json:"paired"`
	WebPort      int    `json:"web_port,omitempty"`
}

var (
	mu         sync.RWMutex
	cachedConf *Config
)

// GetConfigPath returns the path to the active config file.
// If a local config.json exists next to the executable/working dir, it is used.
// Otherwise, it defaults to %APPDATA%\SignalLight\config.json.
func GetConfigPath() string {
	if _, err := os.Stat("config.json"); err == nil {
		return "config.json"
	}

	appData := os.Getenv("APPDATA")
	if appData == "" {
		appData = "."
	}
	return filepath.Join(appData, "SignalLight", "config.json")
}

// Load reads and parses the configuration file.
// If the file does not exist, an empty unpaired configuration is returned.
func Load() (*Config, error) {
	mu.Lock()
	defer mu.Unlock()

	path := GetConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cachedConf = &Config{Paired: false}
			return cachedConf, nil
		}
		// Unreadable config file (e.g. permissions): fail safe with an empty unpaired
		// config rather than nil, which would crash any caller that doesn't check err.
		return &Config{Paired: false}, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		// Corrupt config file: fail safe with an empty unpaired config rather than
		// returning nil, which would crash any caller that doesn't check err.
		return &Config{Paired: false}, err
	}

	cfg.TargetMAC = strings.ToUpper(strings.TrimSpace(cfg.TargetMAC))
	cfg.DeviceName = strings.TrimSpace(cfg.DeviceName)
	cachedConf = &cfg
	return &cfg, nil
}

// Save writes the configuration to disk.
func Save(cfg *Config) error {
	mu.Lock()
	defer mu.Unlock()

	path := GetConfigPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return err
	}

	cachedConf = cfg
	return nil
}

// Clear resets the paired configuration.
func Clear() error {
	cached := GetCached()
	return Save(&Config{
		TargetMAC:    "",
		DeviceName:   "",
		SharedSecret: "",
		Paired:       false,
		WebPort:      cached.WebPort,
	})
}

// GetCached returns the in-memory cached configuration.
func GetCached() *Config {
	mu.RLock()
	defer mu.RUnlock()
	if cachedConf == nil {
		return &Config{Paired: false}
	}
	return cachedConf
}
