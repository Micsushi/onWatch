package agent

import (
	"log/slog"
	"testing"
	"time"
)

// Wake settings are edited from the dashboard, so they have to survive a
// restart. The config key existed but nothing ever wrote or read it.
func TestAntigravityWakeConfig_RoundTripsThroughTheStore(t *testing.T) {
	store := newMemoryWakeStore()

	runner := NewAntigravityWakeRunner(store, AntigravityWakeConfig{}, slog.Default())
	runner.UpdateConfig(AntigravityWakeConfig{
		Enabled:    true,
		Model:      "flash_lite",
		ProjectDir: `C:\probe`,
		Cooldown:   42 * time.Minute,
	})

	if raw := store.data[AntigravityWakeConfigSetting]; raw == "" {
		t.Fatal("UpdateConfig did not persist the configuration")
	}

	loaded := LoadAntigravityWakeConfig(store, AntigravityWakeConfig{})
	if !loaded.Enabled {
		t.Fatal("Enabled did not survive the round trip")
	}
	if loaded.Model != "flash_lite" {
		t.Fatalf("Model = %q, want flash_lite", loaded.Model)
	}
	if loaded.ProjectDir != `C:\probe` {
		t.Fatalf("ProjectDir = %q, want the stored directory", loaded.ProjectDir)
	}
	if loaded.Cooldown != 42*time.Minute {
		t.Fatalf("Cooldown = %v, want 42m", loaded.Cooldown)
	}
}

// With nothing stored the caller's defaults are used unchanged.
func TestLoadAntigravityWakeConfig_FallsBackWhenUnset(t *testing.T) {
	store := newMemoryWakeStore()
	fallback := AntigravityWakeConfig{Model: "flash", Cooldown: time.Hour}

	loaded := LoadAntigravityWakeConfig(store, fallback)
	if loaded.Model != "flash" || loaded.Cooldown != time.Hour {
		t.Fatalf("loaded = %+v, want the fallback", loaded)
	}
	if loaded.Enabled {
		t.Fatal("wake defaulted to enabled; it must be opt-in")
	}

	// Corrupt JSON must not lose the fallback either.
	store.data[AntigravityWakeConfigSetting] = "{not json"
	if loaded := LoadAntigravityWakeConfig(store, fallback); loaded.Model != "flash" {
		t.Fatalf("loaded = %+v, want the fallback for unreadable config", loaded)
	}
}

// A nil store must not panic - onWatch can run without persistence.
func TestLoadAntigravityWakeConfig_NilStore(t *testing.T) {
	fallback := AntigravityWakeConfig{Model: "pro"}
	if loaded := LoadAntigravityWakeConfig(nil, fallback); loaded.Model != "pro" {
		t.Fatalf("loaded = %+v, want the fallback", loaded)
	}
}
