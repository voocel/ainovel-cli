package agents

import (
	"testing"

	"github.com/voocel/litellm"
	"github.com/voocel/litellm/provider/openai"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// 提示词缓存的接线断言。
//
// 历史上（#142）Responses 端点必须关掉显式断点：litellm v1.8.11 的 responses
// 适配器只允许 input_text 携带 cache_control，而框架的"滚动尖端"断点在工具
// 循环里会落在 tool_result 上，被本地校验直接拒绝。v1.9.6 起 responses.go 支持
// 在 tool result 上落 promptCacheBreakpoint，**该 gate 不再需要**，因此三种
// 端点（chat / responses / anthropic）的 Worker 都必须带齐：
//   - Config.Cache：框架的滚动尖端 + 上一条消息断点；
//   - system 首块的缓存断点：缓存地板；
//   - 角色独占的 prompt_cache_key：路由亲和。
func TestBuildWorkersCachesEveryProvider(t *testing.T) {
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
	workers, _, _ := BuildWorkers(cfg, st, tools.NewStyleStatsIndex(st), models, assets.Load("", assets.LoadOptions{}), nil, nil)

	keys := make(map[string]string)
	for _, name := range []string{"architect_short", "architect_long", "writer", "editor"} {
		c, ok := workers.cfgs[name]
		if !ok {
			t.Fatalf("worker %s not registered", name)
		}
		if c.Cache == nil || c.Cache.TTL != workerCacheTTL {
			t.Fatalf("%s 必须携带显式缓存断点（v1.9.6 起所有端点都支持），got %#v", name, c.Cache)
		}
		if len(c.System) == 0 {
			t.Fatalf("%s 缺少 system 提示词块", name)
		}
		block, ok := c.System[0].(litellm.TextBlock)
		if !ok || block.Cache == nil {
			t.Fatalf("%s 的 system 首块必须带缓存断点（缓存地板），got %T", name, c.System[0])
		}
		raw, ok := c.Model.Request.ProviderOptions[openai.ProviderOptionPromptCacheKey]
		if !ok || len(raw) == 0 {
			t.Fatalf("%s 缺少 prompt_cache_key 路由亲和", name)
		}
		keys[name] = string(raw)
	}
	// 一角色一键：同一本书内各 Worker 不能挤进同一个路由桶。
	for a, ka := range keys {
		for b, kb := range keys {
			if a != b && ka == kb {
				t.Fatalf("%s 与 %s 共享 prompt_cache_key %s", a, b, ka)
			}
		}
	}
}
