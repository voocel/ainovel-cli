package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestConfigRoundTripAndEnvOverride(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cfg")
	clearModelEnv(t)

	if loaded, err := LoadConfig(dir); err != nil || loaded.Configured() {
		t.Fatalf("missing config = %#v, %v, want empty without error", loaded, err)
	}
	saved := Config{}.WithProvider("deepseek", "deepseek-chat", ProviderConfig{APIKey: "sk-test"})
	if err := SaveConfig(dir, saved); err != nil {
		t.Fatalf("save config: %v", err)
	}
	// 启动解析把当前连接的 type 补成显式值，其余与保存的一致。
	resolved, err := ResolveConfig(dir)
	pc, _ := saved.ActiveProvider()
	if err != nil || !reflect.DeepEqual(resolved, saved.WithProvider(saved.Provider, saved.Model, pc)) {
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
	clearModelEnv(t)
	t.Setenv("AINOVEL_PROVIDER", "openai")
	if _, err := ResolveConfig(dir); err == nil {
		t.Fatal("half-configured model settings were accepted")
	}
}

func clearModelEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"AINOVEL_PROVIDER", "AINOVEL_MODEL", "AINOVEL_API_KEY", "AINOVEL_BASE_URL", "AINOVEL_PROVIDER_TYPE", "AINOVEL_API", "AINOVEL_THINKING"} {
		t.Setenv(key, "")
	}
}

func TestNamedConnectionsAndEnvironmentIsolation(t *testing.T) {
	clearModelEnv(t)
	dir := t.TempDir()
	cfg := Config{}.WithProvider("first", "model-a", ProviderConfig{Type: "openai", APIKey: "first-secret", BaseURL: "https://first.test"})
	cfg = cfg.WithProvider("second", "model-b", ProviderConfig{Type: "openai", API: "responses", APIKey: "second-secret", BaseURL: "https://second.test"})
	if err := SaveConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AINOVEL_PROVIDER", "first")
	t.Setenv("AINOVEL_MODEL", "override-model")
	t.Setenv("AINOVEL_API_KEY", "override-secret")
	resolved, err := ResolveConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	pc, err := resolved.ActiveProvider()
	if err != nil || pc.Type != "openai" || pc.APIKey != "override-secret" || pc.BaseURL != "https://first.test" || resolved.Model != "override-model" {
		t.Fatalf("resolved: %#v %#v %v", resolved, pc, err)
	}
	if !reflect.DeepEqual(resolved.Providers["second"], cfg.Providers["second"]) {
		t.Fatal("override modified another connection")
	}
	t.Setenv("AINOVEL_PROVIDER", "missing")
	if _, err := ResolveConfig(dir); err == nil {
		t.Fatal("unknown connection accepted")
	}
}

// 环境变量指向文件里没有的连接：连接由环境变量定义，不沿用文件里其他连接的密钥或地址。
func TestEnvironmentConnectionDoesNotReuseCredentials(t *testing.T) {
	clearModelEnv(t)
	dir := t.TempDir()
	if err := os.WriteFile(ConfigPath(dir), []byte(`{"provider":"openai","model":"old-model","providers":{"openai":{"api_key":"private","base_url":"https://private.test"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AINOVEL_PROVIDER", "my-local")
	t.Setenv("AINOVEL_PROVIDER_TYPE", "openai")
	t.Setenv("AINOVEL_API", "chat")
	t.Setenv("AINOVEL_MODEL", "local-model")
	cfg, err := ResolveConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	pc, err := cfg.ActiveProvider()
	if err != nil || pc.Type != "openai" || pc.API != "chat" || pc.APIKey != "" || pc.BaseURL != "" {
		t.Fatalf("credentials leaked: %#v %v", pc, err)
	}
}

func TestWithProviderPreservesConnectionsAndDoesNotMutateInput(t *testing.T) {
	base := Config{}.WithProvider("deepseek", "deepseek-chat", ProviderConfig{APIKey: "old-secret"})
	cfg := base.WithProvider("proxy", "new-model", ProviderConfig{Type: "openai", Models: []string{"new-model"}})
	if cfg.Provider != "proxy" || cfg.Providers["deepseek"].APIKey != "old-secret" || len(cfg.Providers["proxy"].Models) != 1 {
		t.Fatalf("other connections must survive a switch: %#v", cfg)
	}
	updated := cfg.WithProvider("proxy", "second-model", cfg.Providers["proxy"])
	if len(cfg.Providers["proxy"].Models) != 1 || len(updated.Providers["proxy"].Models) != 2 {
		t.Fatal("input models mutated or model not added")
	}
	updated.Providers["deepseek"] = ProviderConfig{}
	if cfg.Providers["deepseek"].APIKey != "old-secret" {
		t.Fatal("input map mutated")
	}
}

func TestSaveConfigNormalizesAndRestrictsPermissions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(ConfigPath(dir), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{}.WithProvider("openai", "model", ProviderConfig{APIKey: "secret"})
	if err := SaveConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(dir)
	if err != nil || loaded.Providers["openai"].APIKey != "secret" {
		t.Fatalf("saved: %#v %v", loaded, err)
	}
	info, err := os.Stat(ConfigPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v", info.Mode())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatal("temporary file leaked")
	}
}

func TestActiveProviderValidation(t *testing.T) {
	if _, err := (Config{Provider: "openai", Providers: map[string]ProviderConfig{}}).ActiveProvider(); err == nil {
		t.Fatal("missing connection accepted")
	}
	for _, pc := range []ProviderConfig{{Type: "openai", API: "invalid"}, {Type: "anthropic", API: "responses"}} {
		cfg := Config{}.WithProvider("custom", "model", pc)
		if cfg.Configured() || cfg.Validate() == nil {
			t.Fatalf("invalid config accepted: %#v", pc)
		}
	}
	cfg := Config{}.WithProvider("openai", "model", ProviderConfig{})
	pc, err := cfg.ActiveProvider()
	if err != nil || pc.Type != "openai" {
		t.Fatalf("default type: %#v %v", pc, err)
	}
}

func TestEnvironmentProtocolOverridesPrecedeValidation(t *testing.T) {
	clearModelEnv(t)
	dir := t.TempDir()
	if err := os.WriteFile(ConfigPath(dir), []byte(`{"provider":"proxy","model":"model","providers":{"proxy":{"type":"anthropic","api":"invalid"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AINOVEL_PROVIDER_TYPE", "openai")
	t.Setenv("AINOVEL_API", "responses")
	cfg, err := ResolveConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	pc, err := cfg.ActiveProvider()
	if err != nil || pc.Type != "openai" || pc.API != "responses" {
		t.Fatalf("override: %#v %v", pc, err)
	}
}

func TestCustomConnectionProtocolValidation(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic", "gemini", "typo", "deepseek"} {
		c := Config{}.WithProvider("my-proxy", "model", ProviderConfig{Type: protocol})
		err := c.Validate()
		valid := protocol == "openai" || protocol == "anthropic" || protocol == "gemini"
		if (err == nil) != valid {
			t.Fatalf("protocol %q: %v", protocol, err)
		}
	}
	// 内置服务商的连接不必写 type：连接名就是协议。
	if err := (Config{}).WithProvider("deepseek", "model", ProviderConfig{}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestThinkingAndRoleOverridesRoundTrip(t *testing.T) {
	clearModelEnv(t)
	dir := t.TempDir()
	cfg := Config{Thinking: "high"}.WithProvider("deepseek", "deepseek-chat", ProviderConfig{APIKey: "k"})
	cfg = cfg.WithProvider("proxy", "gpt-5", ProviderConfig{Type: "openai", APIKey: "p"})
	cfg = cfg.WithProvider("deepseek", "deepseek-chat", cfg.Providers["deepseek"])
	cfg = cfg.WithRole("writer", "proxy", "gpt-5-writer", "medium").WithRole("editor", "deepseek", "deepseek-chat", "")
	if err := SaveConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Thinking != "high" || resolved.Roles["writer"] != (RoleConfig{Provider: "proxy", Model: "gpt-5-writer", Thinking: "medium"}) ||
		resolved.Roles["editor"] != (RoleConfig{Provider: "deepseek", Model: "deepseek-chat"}) {
		t.Fatalf("resolved = %#v", resolved)
	}
	if got := resolved.Providers["proxy"].Models; len(got) != 2 || got[1] != "gpt-5-writer" {
		t.Fatalf("role model was not remembered on its connection: %v", got)
	}
	t.Setenv("AINOVEL_THINKING", "low")
	if resolved, err = ResolveConfig(dir); err != nil || resolved.Thinking != "low" {
		t.Fatalf("env thinking override = %#v, %v", resolved, err)
	}

	cleared := cfg.WithoutRole("writer").WithoutRole("editor")
	if cleared.Roles != nil || len(cfg.Roles) != 2 {
		t.Fatalf("WithoutRole = %#v (input roles %d)", cleared.Roles, len(cfg.Roles))
	}
	for _, bad := range []Config{
		cfg.WithRole("writer", "missing", "m", ""),
		cfg.WithRole("writer", "proxy", "", ""),
	} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("invalid role accepted: %#v", bad.Roles)
		}
	}
	if _, err := cfg.Connection("proxy"); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Connection("missing"); err == nil {
		t.Fatal("unknown connection accepted")
	}
}
