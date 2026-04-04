package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
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

func TestInferNovelNameFromPremise(t *testing.T) {
	premise := `# 长夜燃灯

## 题材和基调
东方玄幻。`
	if got := domain.ExtractNovelNameFromPremise(premise); got != "长夜燃灯" {
		t.Fatalf("expected heading novel name, got %q", got)
	}
}

func TestInferNovelNameFromPremiseRequiresFirstLineHeading(t *testing.T) {
	premise := `书名：蜕壳窟求生录

## 核心冲突
测试`
	if got := domain.ExtractNovelNameFromPremise(premise); got != "" {
		t.Fatalf("expected no novel name without first-line heading, got %q", got)
	}
}

func TestInferNovelNameFromPremiseDoesNotUseSecondLevelHeading(t *testing.T) {
	premise := `## 题材和基调
凡人修仙。`
	if got := domain.ExtractNovelNameFromPremise(premise); got != "" {
		t.Fatalf("expected no novel name from h2 heading, got %q", got)
	}
}

func TestActiveChapterFromTasksPrefersLatestActiveWriterTask(t *testing.T) {
	tasks := []TaskSnapshot{
		{Kind: string(domain.TaskChapterWrite), Status: string(domain.TaskRunning), Chapter: 5},
		{Kind: string(domain.TaskChapterWrite), Status: string(domain.TaskSucceeded), Chapter: 4},
	}
	if got := activeChapterFromTasks(tasks); got != 5 {
		t.Fatalf("expected active chapter 5, got %d", got)
	}
}
