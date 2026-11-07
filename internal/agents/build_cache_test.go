package agents

import (
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// Worker 保留缓存意图，使 Responses 启动后切到 Anthropic 也能恢复显式缓存。
// 实际 OpenAI 请求不含不兼容断点的契约由 bootstrap 的真实适配器测试守护。
func TestBuildWorkersKeepCacheIntentAcrossProviderSelection(t *testing.T) {
	cfg := bootstrap.Config{
		Provider:  "chat",
		ModelName: "chat-model",
		Providers: map[string]bootstrap.ProviderConfig{
			"chat": {
				Type:    "deepseek",
				APIKey:  "test-key",
				BaseURL: "http://127.0.0.1:1/v1",
				Models:  []bootstrap.ModelConfig{{Name: "chat-model"}},
			},
			"resp": {
				Type:    "openai",
				API:     "responses",
				APIKey:  "test-key",
				BaseURL: "http://127.0.0.1:1/v1",
				Models:  []bootstrap.ModelConfig{{Name: "resp-model"}},
			},
		},
		Roles: map[string]bootstrap.RoleConfig{
			"architect": {Provider: "resp", Model: "resp-model"},
			"writer":    {Provider: "chat", Model: "chat-model"},
		},
	}
	if err := cfg.ValidateBase(); err != nil {
		t.Fatalf("validate config: %v", err)
	}

	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		t.Fatalf("model set: %v", err)
	}
	runner, _, _ := BuildWorkers(cfg, st, tools.NewStyleStatsIndex(st), models, assets.Load("", assets.LoadOptions{}), nil, nil)

	for _, name := range []string{"architect_short", "architect_long", "writer", "editor"} {
		c, ok := runner.AgentConfig(name)
		if !ok {
			t.Fatalf("agent %s not registered", name)
		}
		if c.CacheLastMessage != cacheControlEphemeral {
			t.Fatalf("%s 必须保留缓存意图供实际客户端处理，CacheLastMessage=%q", name, c.CacheLastMessage)
		}
		if c.PromptCacheKey == "" {
			t.Fatalf("%s 的 prompt_cache_key 路由亲和必须保留", name)
		}
	}
}
