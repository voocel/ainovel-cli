package app

import (
	"path/filepath"
	"testing"
)

func TestConfigRoundTripAndEnvOverride(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cfg")
	for _, key := range []string{"AINOVEL_PROVIDER", "AINOVEL_MODEL", "AINOVEL_API_KEY", "AINOVEL_BASE_URL"} {
		t.Setenv(key, "")
	}

	if loaded, err := LoadConfig(dir); err != nil || loaded.Configured() {
		t.Fatalf("missing config = %#v, %v, want empty without error", loaded, err)
	}
	saved := Config{Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-test"}
	if err := SaveConfig(dir, saved); err != nil {
		t.Fatalf("save config: %v", err)
	}
	resolved, err := ResolveConfig(dir)
	if err != nil || resolved != saved {
		t.Fatalf("resolved = %#v, %v, want stored config", resolved, err)
	}

	t.Setenv("AINOVEL_MODEL", "deepseek-reasoner")
	resolved, err = ResolveConfig(dir)
	if err != nil || resolved.Model != "deepseek-reasoner" || resolved.Provider != "deepseek" {
		t.Fatalf("env override = %#v, %v", resolved, err)
	}
}

func TestResolveConfigRejectsHalfConfigured(t *testing.T) {
	dir := t.TempDir()
	for _, key := range []string{"AINOVEL_PROVIDER", "AINOVEL_MODEL", "AINOVEL_API_KEY", "AINOVEL_BASE_URL"} {
		t.Setenv(key, "")
	}
	t.Setenv("AINOVEL_PROVIDER", "openai")
	if _, err := ResolveConfig(dir); err == nil {
		t.Fatal("half-configured model settings were accepted")
	}
}
