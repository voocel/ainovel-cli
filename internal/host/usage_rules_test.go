package host

// 规则归一化的用量归属回归（issue: 17 次调用只记到 16 次）。
//
// 归一化是启动期真实发生的 LLM 调用：它在 Engine 之外、由 Host 直接触发，
// 曾经完全绕开 UsageTracker——usage.json 里整笔丢失，预算与 UI 对此失明。
// 修复点是 rulesModel() 用 bootstrap.TrackUsage 包一层，task 标 "rules"。
//
// 本测试特意走真实适配器（bootstrap.NewModelSet + httptest 的 OpenAI 兼容端点），
// 而不是假 provider：假 provider 全绿曾经漏过真实适配器层的格式拒绝，
// 「响应里的 usage 能穿过适配器落到台账」必须在协议层验证。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestRulesModelUsageRecorded(t *testing.T) {
	const (
		providerKey = "localtest"
		modelName   = "rules-test-model"
		wantInput   = 1234
		wantOutput  = 56
	)

	// userrules 服务内部固定用 rules.DefaultOptions()：它扫 ~/.ainovel/rules 与
	// ./.ainovel/rules。开发机上的真实规则文件会改变调用次数与请求内容，
	// 所以把 HOME 和 cwd 都指到临时目录，保证测试 hermetic。
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost {
			t.Errorf("规则归一化应发 POST，实际 %s", r.Method)
		}
		var body struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("解析归一化请求失败: %v", err)
		}
		// 结构化一次性调用：非流式、带模型名、system+user 两条消息。
		if body.Stream {
			t.Errorf("规则归一化应为非流式调用，实际 stream=true")
		}
		if body.Model != modelName {
			t.Errorf("请求模型 = %q，期望 %q", body.Model, modelName)
		}
		if len(body.Messages) != 2 {
			t.Errorf("归一化请求应带 system+user 两条消息，实际 %d", len(body.Messages))
		}

		// 归一化 DTO 的最小合法输出（全字段满足契约 schema）。
		content, err := json.Marshal(map[string]any{
			"structured": map[string]any{
				"genre":             "",
				"forbidden_chars":   []string{},
				"forbidden_phrases": []string{},
				"fatigue_words":     []any{},
			},
			"preferences": "冷峻克制，多用身体感知",
			"uncertain":   []string{},
		})
		if err != nil {
			t.Errorf("构造归一化响应失败: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-rules-usage",
			"object":  "chat.completion",
			"created": 1767225600,
			"model":   modelName,
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": string(content)},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens":     wantInput,
				"completion_tokens": wantOutput,
				"total_tokens":      wantInput + wantOutput,
			},
		})
	}))
	t.Cleanup(srv.Close)

	// 真实适配器：openai 兼容协议 + 指向 httptest 的端点。
	models, err := bootstrap.NewModelSet(bootstrap.Config{
		Provider:  providerKey,
		ModelName: modelName,
		Providers: map[string]bootstrap.ProviderConfig{
			providerKey: {
				Type:    "openai",
				APIKey:  "test-key",
				BaseURL: srv.URL + "/v1",
				Models:  []bootstrap.ModelConfig{{Name: modelName, ContextWindow: 128000}},
			},
		},
	})
	if err != nil {
		t.Fatalf("NewModelSet: %v", err)
	}

	dir := t.TempDir()
	st := storepkg.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("store init: %v", err)
	}
	usage := NewUsageTracker(models, st)

	// 只装配规则归一化实际读到的字段。走 PrepareUserRules（新建路径）：
	// 它把用户的原始创作要求交给归一化，因此一定有 LLM 调用；恢复路径的
	// ensureUserRules 在同一 store 无规则文件时只会读 system_defaults，
	// 不会发请求。两条路径共用同一个 rulesModel()，覆盖到的是同一段记账代码。
	h := &Host{store: st, models: models, usage: usage, events: make(chan Event, 4)}
	h.runCtx, h.runCancel = context.WithCancel(context.Background())
	t.Cleanup(h.runCancel)
	if err := h.PrepareUserRules("冷峻克制的悬疑故事，禁用「突然」"); err != nil {
		t.Fatalf("PrepareUserRules: %v", err)
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("规则归一化应只发一次请求（无重试/无反馈重问），实际 %d 次", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "meta", "user_rules.json")); err != nil {
		t.Fatalf("规则归一化未落盘快照（大概率请求层失败）: %v", err)
	}

	// ── 内存台账 ──
	_, input, output, cacheRead, cacheWrite := usage.Totals()
	if input != wantInput || output != wantOutput {
		t.Fatalf("台账 overall = input %d / output %d，期望 %d / %d", input, output, wantInput, wantOutput)
	}
	if cacheRead != 0 || cacheWrite != 0 {
		t.Fatalf("端点未报缓存，台账不应凭空造缓存计数：read=%d write=%d", cacheRead, cacheWrite)
	}

	var defaultRow *AgentUsage
	for _, row := range usage.PerAgent() {
		if row.Role == "default" {
			r := row
			defaultRow = &r
			break
		}
	}
	if defaultRow == nil {
		t.Fatalf("台账缺少 default 归属：%+v", usage.PerAgent())
	}
	if defaultRow.Input != wantInput || defaultRow.Output != wantOutput {
		t.Fatalf("default 归属 = input %d / output %d，期望 %d / %d",
			defaultRow.Input, defaultRow.Output, wantInput, wantOutput)
	}

	// task 不落盘（只做缓存血统身份），从 live 跟踪状态确认这笔走的是
	// rulesModel 的 "rules" 标签，而不是别的 default 归属调用。
	usage.mu.Lock()
	st0 := usage.cacheTrack["default"]
	task := ""
	if st0 != nil {
		task = st0.task
	}
	usage.mu.Unlock()
	if task != "rules" {
		t.Fatalf("default 归属的 task = %q，期望 %q（rulesModel 的标签）", task, "rules")
	}

	// ── 落盘 JSON ──
	if err := usage.SaveNow(); err != nil {
		t.Fatalf("usage 落盘: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "meta", "usage.json"))
	if err != nil {
		t.Fatalf("读取 meta/usage.json: %v", err)
	}
	var state domain.UsageState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("解析 meta/usage.json: %v\n%s", err, raw)
	}
	if state.Schema != domain.UsageSchemaVersion {
		t.Fatalf("schema = %d，期望 %d", state.Schema, domain.UsageSchemaVersion)
	}
	if state.Overall.Input != wantInput || state.Overall.Output != wantOutput {
		t.Fatalf("overall 未包含这笔规则归一化用量：input %d / output %d，期望 %d / %d\n%s",
			state.Overall.Input, state.Overall.Output, wantInput, wantOutput, raw)
	}
	perDefault, ok := state.PerAgent["default"]
	if !ok {
		t.Fatalf("per_agent 缺少 default：%s", raw)
	}
	if perDefault.Input != wantInput || perDefault.Output != wantOutput {
		t.Fatalf("per_agent.default = input %d / output %d，期望 %d / %d\n%s",
			perDefault.Input, perDefault.Output, wantInput, wantOutput, raw)
	}
}
