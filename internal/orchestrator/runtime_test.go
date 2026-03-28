package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

func TestPersistModelChangeToPathUsesBaselineWhenMissing(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	baseline := bootstrap.Config{
		Provider:  "openrouter",
		ModelName: "old-model",
		Providers: map[string]bootstrap.ProviderConfig{
			"openrouter": {APIKey: "test-key", BaseURL: "https://openrouter.ai/api/v1"},
		},
	}

	if err := persistModelChangeToPath(cfgPath, "default", "openrouter", "new-model", baseline); err != nil {
		t.Fatalf("persistModelChangeToPath: %v", err)
	}

	cfg, err := bootstrap.LoadConfigFile(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if cfg.Provider != "openrouter" || cfg.ModelName != "new-model" {
		t.Fatalf("unexpected default model: %s/%s", cfg.Provider, cfg.ModelName)
	}
	if cfg.Providers["openrouter"].APIKey != "test-key" {
		t.Fatalf("expected provider config copied from baseline")
	}
}

func TestPersistModelChangeToPathReturnsErrorOnBrokenConfig(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfgPath, []byte("{invalid"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	err := persistModelChangeToPath(cfgPath, "default", "openrouter", "new-model", bootstrap.Config{
		Providers: map[string]bootstrap.ProviderConfig{
			"openrouter": {APIKey: "test-key"},
		},
	})
	if err == nil {
		t.Fatalf("expected parse error, got nil")
	}

	data, readErr := os.ReadFile(cfgPath)
	if readErr != nil {
		t.Fatalf("ReadFile: %v", readErr)
	}
	if string(data) != "{invalid" {
		t.Fatalf("broken config should not be overwritten")
	}
}
