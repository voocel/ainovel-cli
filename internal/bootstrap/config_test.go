package bootstrap

import "testing"

func TestConfigResolveThinking(t *testing.T) {
	cfg := Config{
		Thinking: "low", // 顶层Mặc định
		Roles: map[string]RoleConfig{
			"writer":    {Provider: "p", Model: "m", Thinking: "high"}, // 角色覆盖
			"architect": {Provider: "p", Model: "m"},                   // Không có thinking，应回落Mặc định
		},
	}

	cases := []struct {
		role string
		want string
	}{
		{"writer", "high"},     // 角色覆盖优先
		{"architect", "low"},   // 角色未配 → 回落顶层Mặc định
		{"editor", "low"},      // 角色不存在 → 顶层Mặc định
		{"", "low"},            // Rỗng → 顶层Mặc định
		{"default", "low"},     // default → 顶层Mặc định
		{"coordinator", "low"}, // 未配 → 顶层Mặc định
	}
	for _, c := range cases {
		if got := cfg.ResolveThinking(c.role); got != c.want {
			t.Errorf("ResolveThinking(%q) = %q, want %q", c.role, got, c.want)
		}
	}

	// 顶层Mặc định也为Rỗng时，未覆盖角色Quay lại ""（不覆盖）。
	empty := Config{Roles: map[string]RoleConfig{"writer": {Thinking: "xhigh"}}}
	if got := empty.ResolveThinking("editor"); got != "" {
		t.Errorf("RỗngMặc định下 editor 应Quay lại \"\"，得 %q", got)
	}
	if got := empty.ResolveThinking("writer"); got != "xhigh" {
		t.Errorf("RỗngMặc định下 writer 覆盖应生效，得 %q", got)
	}
}
