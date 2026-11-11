package bootstrap

import (
	"errors"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/notify"
)

func TestConfigResolveReasoningEffort(t *testing.T) {
	cfg := Config{
		ReasoningEffort: "low", // 顶层默认
		Roles: map[string]RoleConfig{
			"writer":    {Provider: "p", Model: "m", ReasoningEffort: "high"}, // 角色覆盖
			"architect": {Provider: "p", Model: "m"},                          // 无 reasoning_effort，应回落默认
		},
	}

	cases := []struct {
		role string
		want string
	}{
		{"writer", "high"},   // 角色覆盖优先
		{"architect", "low"}, // 角色未配 → 回落顶层默认
		{"editor", "low"},    // 角色不存在 → 顶层默认
		{"", "low"},          // 空 → 顶层默认
		{"default", "low"},   // default → 顶层默认
		{"arbiter", "low"},   // 非配置角色（裁定恒随顶层默认）
	}
	for _, c := range cases {
		if got := cfg.ResolveReasoningEffort(c.role); got != c.want {
			t.Errorf("ResolveReasoningEffort(%q) = %q, want %q", c.role, got, c.want)
		}
	}

	// 顶层默认也为空时，未覆盖角色返回 ""（不覆盖）。
	empty := Config{Roles: map[string]RoleConfig{"writer": {ReasoningEffort: "xhigh"}}}
	if got := empty.ResolveReasoningEffort("editor"); got != "" {
		t.Errorf("空默认下 editor 应返回 \"\"，得 %q", got)
	}
	if got := empty.ResolveReasoningEffort("writer"); got != "xhigh" {
		t.Errorf("空默认下 writer 覆盖应生效，得 %q", got)
	}
}

func TestValidateBaseRejectsNonConfigurableRoles(t *testing.T) {
	for _, role := range []string{"coordinator", "arbiter"} {
		t.Run(role, func(t *testing.T) {
			cfg := Config{
				Provider:  "openrouter",
				ModelName: "test-model",
				Providers: map[string]ProviderConfig{
					"openrouter": {APIKey: "sk-test-123456"},
				},
				Roles: map[string]RoleConfig{
					role: {Provider: "openrouter", Model: "test-model"},
				},
			}

			err := cfg.ValidateBase()
			if err == nil {
				t.Fatalf("roles.%s 应被拒绝", role)
			}
			if !errors.Is(err, errs.ErrConfig) {
				t.Fatalf("应包装 errs.ErrConfig，得到: %v", err)
			}
		})
	}
}

func TestValidateBaseNotifyEventsMatchRuntimeContract(t *testing.T) {
	validConfig := func(events []string) Config {
		return Config{
			Provider:  "openrouter",
			ModelName: "test-model",
			Providers: map[string]ProviderConfig{
				"openrouter": {APIKey: "sk-test-123456"},
			},
			Notify: NotifyConfig{Events: events},
		}
	}

	cfg := validConfig(notify.Kinds())
	if err := cfg.ValidateBase(); err != nil {
		t.Fatalf("当前通知事件契约应全部通过配置校验: %v", err)
	}

	cfg = validConfig([]string{"repeat"})
	if err := cfg.ValidateBase(); !errors.Is(err, errs.ErrConfig) {
		t.Fatalf("旧 repeat 事件应被拒绝，得到: %v", err)
	}
}

func TestProviderStreamIdleTimeoutValue(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", defaultStreamIdleTimeout, false},
		{"900s", 15 * time.Minute, false},
		{"15m", 15 * time.Minute, false},
		{"abc", 0, true},
		{"-5s", 0, true},
		{"0", 0, true}, // 不提供"关闭看门狗"——真死流需要有限界
	}
	for _, c := range cases {
		got, err := ProviderConfig{StreamIdleTimeout: c.in}.StreamIdleTimeoutValue()
		if c.wantErr {
			if err == nil {
				t.Errorf("%q 应报错", c.in)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q = (%v, %v), want %v", c.in, got, err, c.want)
		}
	}
}

func TestValidateBaseRejectsBadStreamIdleTimeout(t *testing.T) {
	cfg := Config{
		Provider:  "openrouter",
		ModelName: "test-model",
		Providers: map[string]ProviderConfig{
			"openrouter": {APIKey: "sk-test-123456", StreamIdleTimeout: "fast"},
		},
	}
	if err := cfg.ValidateBase(); !errors.Is(err, errs.ErrConfig) {
		t.Fatalf("非法 stream_idle_timeout 应拒绝并包装 ErrConfig，得到: %v", err)
	}
}

func TestProviderPresetsIncludeAtlasCloudOpenAICompatibleDefaults(t *testing.T) {
	var atlas ProviderPreset
	for _, preset := range ProviderPresets() {
		if preset.Name == "atlascloud" {
			atlas = preset
			break
		}
	}
	if atlas.Name == "" {
		t.Fatal("ProviderPresets should include atlascloud")
	}
	if atlas.Label != "Atlas Cloud" || atlas.Type != "openai" {
		t.Fatalf("atlascloud preset label/type = %q/%q", atlas.Label, atlas.Type)
	}
	if atlas.BaseURL != "https://api.atlascloud.ai/v1" {
		t.Fatalf("atlascloud base url = %q", atlas.BaseURL)
	}
	if atlas.DefaultModel != "qwen/qwen3.5-flash" {
		t.Fatalf("atlascloud default model = %q", atlas.DefaultModel)
	}
	wantModels := map[string]int{
		"qwen/qwen3.5-flash":           1000000,
		"deepseek-ai/deepseek-v4-pro": 1048576,
	}
	if len(atlas.Models) != len(wantModels) {
		t.Fatalf("atlascloud models = %#v", atlas.Models)
	}
	for _, model := range atlas.Models {
		if want, ok := wantModels[model.Name]; !ok || model.ContextWindow != want {
			t.Fatalf("unexpected atlascloud model entry: %#v", model)
		}
	}
}

func TestPresetModelsWithSelectionPreservesCatalogAndAddsCustomSelection(t *testing.T) {
	preset := []ModelConfig{{Name: "qwen/qwen3.5-flash", ContextWindow: 1000000}}
	got := presetModelsWithSelection(preset, "custom-model")
	if len(got) != 2 {
		t.Fatalf("models = %#v", got)
	}
	if got[0].Name != "qwen/qwen3.5-flash" || got[0].ContextWindow != 1000000 {
		t.Fatalf("preset model was not preserved: %#v", got[0])
	}
	if got[1].Name != "custom-model" || got[1].ContextWindow != 0 {
		t.Fatalf("custom selected model not appended: %#v", got[1])
	}
}
