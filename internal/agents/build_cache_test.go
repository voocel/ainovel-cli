package agents

import (
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// Responses 端点不接受贴在 tool_result 上的显式断点（litellm v1.8.11 的
// responses 适配器只允许 input_text 携带断点），工具循环第一轮就会被本地校验
// 拒绝。BuildWorkers 必须按 provider 的 api 关掉 CacheLastMessage，同时保留
// prompt_cache_key 路由亲和；chat/anthropic 端点的断点不能一起丢。
func TestBuildWorkersExplicitCacheFollowsProviderAPI(t *testing.T) {
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

	for _, name := range []string{"architect_short", "architect_long"} {
		c, ok := runner.AgentConfig(name)
		if !ok {
			t.Fatalf("agent %s not registered", name)
		}
		if c.CacheLastMessage != "" {
			t.Fatalf("%s 走 responses 端点，显式断点会打在 tool_result 上被适配器拒绝，CacheLastMessage=%q", name, c.CacheLastMessage)
		}
		if c.PromptCacheKey == "" {
			t.Fatalf("%s 的 prompt_cache_key 路由亲和不能随断点一起关闭", name)
		}
	}

	writer, ok := runner.AgentConfig("writer")
	if !ok {
		t.Fatal("writer not registered")
	}
	if writer.CacheLastMessage != cacheControlEphemeral {
		t.Fatalf("chat 端点应保留显式断点，CacheLastMessage=%q", writer.CacheLastMessage)
	}
}
