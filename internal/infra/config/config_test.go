package config

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestConfigRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cfg")
	if loaded, err := LoadConfig(dir); err != nil || loaded.Configured() {
		t.Fatalf("missing config = %#v, %v, want empty without error", loaded, err)
	}
	saved := Config{}.WithProvider("deepseek", "deepseek-chat", ProviderConfig{APIKey: "sk-test"})
	if err := SaveConfig(dir, saved); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if loaded, err := LoadConfig(dir); err != nil || !reflect.DeepEqual(loaded, saved) {
		t.Fatalf("loaded = %#v, %v, want stored config", loaded, err)
	}
}

// 多个同协议的连接各存各的，互不覆盖。
func TestNamedConnections(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{}.WithProvider("first", "model-a", ProviderConfig{Type: "openai", APIKey: "first-secret", BaseURL: "https://first.test"})
	cfg = cfg.WithProvider("second", "model-b", ProviderConfig{Type: "openai", API: "responses", APIKey: "second-secret", BaseURL: "https://second.test"})
	if err := SaveConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(dir)
	if err != nil || loaded.Provider != "second" || !reflect.DeepEqual(loaded.Providers["first"], cfg.Providers["first"]) {
		t.Fatalf("loaded = %#v %v", loaded, err)
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

func TestSaveConfigRestrictsPermissions(t *testing.T) {
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

func TestCustomConnectionProtocolValidation(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic", "gemini", "typo", "deepseek", "compat"} {
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
	dir := t.TempDir()
	cfg := Config{Thinking: "high"}.WithProvider("deepseek", "deepseek-chat", ProviderConfig{APIKey: "k"})
	cfg = cfg.WithProvider("proxy", "gpt-5", ProviderConfig{Type: "openai", APIKey: "p"})
	cfg = cfg.WithProvider("deepseek", "deepseek-chat", cfg.Providers["deepseek"])
	cfg = cfg.WithRole("writer", "proxy", "gpt-5-writer", "medium").WithRole("editor", "deepseek", "deepseek-chat", "")
	if err := SaveConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	resolved, err := LoadConfig(dir)
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

// 删除连接或模型前查引用：默认绑定或角色还在用的拒绝，删除不改输入。
func TestRemovalChecksReferences(t *testing.T) {
	cfg := Config{}.WithProvider("proxy", "gpt-5", ProviderConfig{Type: "openai", Models: []string{"gpt-5-mini"}})
	cfg = cfg.WithProvider("deepseek", "deepseek-chat", ProviderConfig{}).WithRole("writer", "proxy", "gpt-5-mini", "")
	if _, err := cfg.WithoutModel("deepseek", "deepseek-chat"); err == nil {
		t.Fatal("the default model was removed")
	}
	if _, err := cfg.WithoutConnection("proxy"); err == nil || !strings.Contains(err.Error(), "writer") {
		t.Fatalf("a connection the writer uses was removed: %v", err)
	}
	removed, err := cfg.WithoutModel("proxy", "gpt-5")
	if err != nil || !slices.Equal(removed.Providers["proxy"].Models, []string{"gpt-5-mini"}) || len(cfg.Providers["proxy"].Models) != 2 {
		t.Fatalf("WithoutModel = %+v %v (input %v)", removed.Providers["proxy"], err, cfg.Providers["proxy"].Models)
	}
	if removed, err = removed.WithoutRole("writer").WithoutConnection("proxy"); err != nil || len(removed.Providers) != 1 {
		t.Fatalf("WithoutConnection = %+v %v", removed.Providers, err)
	}
}
