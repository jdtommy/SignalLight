package config

import (
	"testing"
)

func TestConfigSaveLoadClear(t *testing.T) {
	// GetConfigPath() resolves to a local ./config.json (if present) or
	// %APPDATA%\SignalLight\config.json — neither of which Save/Load let us override
	// directly. Redirect both by running in an isolated temp dir with APPDATA pointed
	// there too, so this test can't read or clobber a real, already-paired device's config.
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	t.Setenv("APPDATA", tmpDir)

	testCfg := &Config{
		TargetMAC:    "AA:BB:CC:DD:EE:FF",
		DeviceName:   "Test Desk Light",
		SharedSecret: "test-secret-123",
		Paired:       true,
	}

	// Test Save
	if err := Save(testCfg); err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	// Test Load
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if loaded.TargetMAC != "AA:BB:CC:DD:EE:FF" {
		t.Errorf("Expected MAC 'AA:BB:CC:DD:EE:FF', got '%s'", loaded.TargetMAC)
	}
	if loaded.DeviceName != "Test Desk Light" {
		t.Errorf("Expected Name 'Test Desk Light', got '%s'", loaded.DeviceName)
	}
	if loaded.SharedSecret != "test-secret-123" {
		t.Errorf("Expected secret 'test-secret-123', got '%s'", loaded.SharedSecret)
	}
	if !loaded.Paired {
		t.Errorf("Expected Paired=true, got false")
	}

	// Test Clear
	if err := Clear(); err != nil {
		t.Fatalf("Failed to clear config: %v", err)
	}

	cleared, err := Load()
	if err != nil {
		t.Fatalf("Failed to load cleared config: %v", err)
	}
	if cleared.Paired {
		t.Errorf("Expected Paired=false after Clear(), got true")
	}
}
