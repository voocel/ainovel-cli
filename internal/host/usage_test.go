package host

import (
	"testing"

	"github.com/Accelerator-mzq/ainovel-cli/internal/models"
	"github.com/voocel/agentcore"
)

// makeUsageMsg 构造一条 OnMessage 回调能接受的消息（带 Usage）。
// Role 必须显式置成 assistant：UsageTracker.Record 现在按角色筛，
// 只有 assistant 消息才会被累计（其它角色天然不带 usage）。
func makeUsageMsg(input, cacheRead, cacheWrite, output int) agentcore.AgentMessage {
	return agentcore.Message{
		Role: agentcore.RoleAssistant,
		Usage: &agentcore.Usage{
			Input: input, Output: output, CacheRead: cacheRead, CacheWrite: cacheWrite,
		},
	}
}

// Test_pushSample_RingBuffer 验证滑动窗的轮转语义：
// 前 N 次直接 append；之后按 sampleIdx 覆盖最旧条目。recentSums 始终反映"最近 N 次"。
func Test_pushSample_RingBuffer(t *testing.T) {
	var tot agentTotals

	for i := 1; i <= recentSampleCap; i++ {
		pushSample(&tot, i, i*100)
	}
	if got := len(tot.samples); got != recentSampleCap {
		t.Fatalf("after %d pushes, samples len=%d want %d", recentSampleCap, got, recentSampleCap)
	}

	pushSample(&tot, 999, 99900)
	if got := len(tot.samples); got != recentSampleCap {
		t.Fatalf("after overflow, samples len=%d want %d (no growth)", got, recentSampleCap)
	}
	cacheRead, input := recentSums(&tot)
	expectedCacheRead := 999
	expectedInput := 99900
	for i := 2; i <= recentSampleCap; i++ {
		expectedCacheRead += i
		expectedInput += i * 100
	}
	if cacheRead != expectedCacheRead || input != expectedInput {
		t.Fatalf("recentSums after overflow = (%d, %d), want (%d, %d)",
			cacheRead, input, expectedCacheRead, expectedInput)
	}
}

// Test_UsageTracker_RecordAccumulates 验证 Record 多 role 累计正确，
// 整体合并 = 所有 role 之和；per-role 各自独立。
func Test_UsageTracker_RecordAccumulates(t *testing.T) {
	tk := NewUsageTracker(nil, nil) // modelSet=nil → 走 provider Cost 兜底，不影响累计逻辑

	tk.Record("writer", makeUsageMsg(1000, 800, 0, 200))
	tk.Record("writer", makeUsageMsg(1500, 1200, 100, 300))
	tk.Record("editor", makeUsageMsg(500, 0, 0, 100))

	cost, in, out, cr, cw := tk.Totals()
	if in != 3000 || out != 600 || cr != 2000 || cw != 100 {
		t.Fatalf("totals = (in=%d out=%d cr=%d cw=%d), want (3000 600 2000 100)", in, out, cr, cw)
	}
	if cost != 0 {
		t.Errorf("cost should be 0 when modelSet=nil and no provider Cost, got %v", cost)
	}

	per := tk.PerAgent()
	if len(per) != 2 {
		t.Fatalf("per-agent len=%d want 2", len(per))
	}
	// PerAgent 按 CacheRead 降序：writer (2000) 应排在 editor (0) 前
	if per[0].Role != "writer" || per[1].Role != "editor" {
		t.Fatalf("per-agent order = %s,%s want writer,editor", per[0].Role, per[1].Role)
	}
	if per[0].Input != 2500 || per[0].CacheRead != 2000 {
		t.Errorf("writer totals = (in=%d cr=%d), want (2500 2000)", per[0].Input, per[0].CacheRead)
	}
}

// Test_UsageTracker_ArchitectAliasNormalized 验证 architect_short/mid/long
// 都归一到同一个 "architect" key（避免被 /model 切换的子角色拆成多行）。
func Test_UsageTracker_ArchitectAliasNormalized(t *testing.T) {
	tk := NewUsageTracker(nil, nil)
	tk.Record("architect_short", makeUsageMsg(100, 50, 0, 20))
	tk.Record("architect_mid", makeUsageMsg(200, 100, 0, 40))
	tk.Record("architect_long", makeUsageMsg(300, 150, 0, 60))

	per := tk.PerAgent()
	if len(per) != 1 {
		t.Fatalf("aliases must merge to single role, got %d entries: %+v", len(per), per)
	}
	if per[0].Role != "architect" {
		t.Fatalf("merged role name = %q, want architect", per[0].Role)
	}
	if per[0].Input != 600 || per[0].CacheRead != 300 {
		t.Errorf("merged totals = (in=%d cr=%d), want (600 300)", per[0].Input, per[0].CacheRead)
	}
}

func Test_UsageTracker_PerModelAccumulates(t *testing.T) {
	tk := NewUsageTracker(nil, nil)
	tk.accumulate("writer", "openrouter", "model-a", agentcore.Usage{Input: 1000, Output: 200, CacheRead: 700})
	tk.accumulate("editor", "openrouter", "model-b", agentcore.Usage{Input: 500, Output: 100})
	tk.accumulate("writer", "openrouter", "model-a", agentcore.Usage{Input: 300, Output: 80, CacheRead: 200})

	perModel := tk.PerModel()
	if len(perModel) != 2 {
		t.Fatalf("per-model len=%d want 2", len(perModel))
	}
	seen := map[string]AgentUsage{}
	for _, m := range perModel {
		seen[m.Model] = m
	}
	if seen["openrouter/model-a"].Input != 1300 || seen["openrouter/model-a"].CacheRead != 900 {
		t.Errorf("model-a totals = %+v", seen["openrouter/model-a"])
	}
	if seen["openrouter/model-b"].Output != 100 {
		t.Errorf("model-b totals = %+v", seen["openrouter/model-b"])
	}

	snap := tk.Snapshot()
	restored := NewUsageTracker(nil, nil)
	restored.applyState(snap)
	if got := restored.PerModel(); len(got) != 2 {
		t.Fatalf("restored per-model len=%d want 2: %+v", len(got), got)
	}
}

func Test_UsageTracker_RecordUsesActualUsageModel(t *testing.T) {
	tk := NewUsageTracker(nil, nil)
	tk.Record("writer", agentcore.Message{
		Role: agentcore.RoleAssistant,
		Usage: &agentcore.Usage{
			Provider: "openrouter",
			Model:    "google/gemini-2.5-pro",
			Input:    1000,
			Output:   200,
		},
	})

	perModel := tk.PerModel()
	if len(perModel) != 1 {
		t.Fatalf("per-model len=%d want 1: %+v", len(perModel), perModel)
	}
	if perModel[0].Model != "openrouter/google/gemini-2.5-pro" {
		t.Fatalf("model key = %q, want openrouter/google/gemini-2.5-pro", perModel[0].Model)
	}
	if perModel[0].Input != 1000 || perModel[0].Output != 200 {
		t.Fatalf("model totals = %+v", perModel[0])
	}
}

func Test_UsageTracker_ProviderOnlyDoesNotInventModelKey(t *testing.T) {
	tk := NewUsageTracker(nil, nil)
	tk.Record("writer", agentcore.Message{
		Role: agentcore.RoleAssistant,
		Usage: &agentcore.Usage{
			Provider: "openrouter",
			Input:    1000,
			Output:   200,
		},
	})

	if got := tk.PerModel(); len(got) != 0 {
		t.Fatalf("provider-only usage must not create model stats without a model, got %+v", got)
	}
}

// Test_UsageTracker_RecentWindowReflectsLatest 验证滑动窗反映"最近 N 次"，
// 不被早期低命中拖累 — 这正是 P1 要解决的"前期拖累 vs 稳态低命中"问题。
func Test_UsageTracker_RecentWindowReflectsLatest(t *testing.T) {
	tk := NewUsageTracker(nil, nil)

	// 前 5 次极低命中（首章场景）
	for i := 0; i < 5; i++ {
		tk.Record("writer", makeUsageMsg(1000, 0, 0, 200))
	}
	// 后 8 次（>5）高命中（稳态场景）
	for i := 0; i < 8; i++ {
		tk.Record("writer", makeUsageMsg(1000, 900, 0, 200))
	}

	per := tk.PerAgent()
	if len(per) != 1 {
		t.Fatalf("len=%d want 1", len(per))
	}
	w := per[0]

	// 累计：13 次中 8 次有命中 → 7200/13000 ≈ 55.4%
	cumulativeRate := float64(w.CacheRead) / float64(w.Input) * 100
	if cumulativeRate < 50 || cumulativeRate > 60 {
		t.Errorf("cumulative hit rate = %.1f%%, want ~55%%", cumulativeRate)
	}

	// 滑动窗：最近 10 次中 8 次高命中 + 2 次零命中 → 7200/10000 = 72%
	if w.RecentSamples != recentSampleCap {
		t.Errorf("recent samples = %d, want %d (window full)", w.RecentSamples, recentSampleCap)
	}
	recentRate := float64(w.RecentCacheRead) / float64(w.RecentInput) * 100
	if recentRate < 70 || recentRate > 75 {
		t.Errorf("recent hit rate = %.1f%%, want ~72%% (proves window dropped early misses)", recentRate)
	}
	// 关键：近 N 次明显高于累计，证明早期 0 已被丢出窗
	if recentRate <= cumulativeRate {
		t.Errorf("recent (%.1f%%) must exceed cumulative (%.1f%%) once window slides past early misses",
			recentRate, cumulativeRate)
	}
}

// Test_computeSaved 验证 saved 算法：CacheRead × (Input价 - CacheRead价)；
// 价差 ≤ 0 或 InputCost ≤ 0 时返回 0（CacheWrite 溢价不抵扣）。
func Test_computeSaved(t *testing.T) {
	cases := []struct {
		name  string
		usage agentcore.Usage
		entry models.ModelEntry
		want  float64
	}{
		{
			name:  "anthropic 5m 命中节省 90%",
			usage: agentcore.Usage{Input: 100_000, CacheRead: 80_000},
			entry: models.ModelEntry{InputCostPer1M: 3.0, CacheReadCostPer1M: 0.3},
			want:  80_000 * (3.0 - 0.3) / 1_000_000, // 0.216
		},
		{
			name:  "无命中 saved=0",
			usage: agentcore.Usage{Input: 100_000, CacheRead: 0},
			entry: models.ModelEntry{InputCostPer1M: 3.0, CacheReadCostPer1M: 0.3},
			want:  0,
		},
		{
			name:  "模型未标价 saved=0",
			usage: agentcore.Usage{Input: 100_000, CacheRead: 50_000},
			entry: models.ModelEntry{InputCostPer1M: 0, CacheReadCostPer1M: 0},
			want:  0,
		},
		{
			name:  "异常价差 saved=0",
			usage: agentcore.Usage{Input: 100_000, CacheRead: 50_000},
			entry: models.ModelEntry{InputCostPer1M: 1.0, CacheReadCostPer1M: 2.0}, // 缓存反而更贵
			want:  0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeSaved(tc.usage, tc.entry)
			if got != tc.want {
				t.Errorf("computeSaved=%v want %v", got, tc.want)
			}
		})
	}
}

// Test_UsageTracker_CacheCapableSticky 验证 CacheCapable 一旦置 true 就不回退。
// 历史上跑过支持 cache 的模型 → 累计命中数据有效；中途切到不支持的模型不应让标记回退。
//
// 通过构造 perAgent 直接赋值模拟（resolveCost 路径需要 ModelSet+Registry，集成层已覆盖）。
func Test_UsageTracker_CacheCapableSticky(t *testing.T) {
	tk := NewUsageTracker(nil, nil)

	// 模拟"曾经跑过支持 cache 的模型 + 命中过"
	tk.perAgent["writer"] = &agentTotals{
		Input: 1000, CacheRead: 500, Output: 200, CacheCapable: true,
	}
	// 后续追加一次"不支持 cache 的模型调用"
	tk.Record("writer", makeUsageMsg(500, 0, 0, 100))

	per := tk.PerAgent()
	if len(per) != 1 || per[0].Role != "writer" {
		t.Fatalf("expected single writer entry, got %+v", per)
	}
	if !per[0].CacheCapable {
		t.Errorf("CacheCapable must remain true after switching to non-capable model")
	}
	if per[0].CacheRead != 500 || per[0].Input != 1500 {
		t.Errorf("totals after merge = (in=%d cr=%d), want (1500 500)",
			per[0].Input, per[0].CacheRead)
	}
}

// Test_UsageTracker_PerAgentSkipsZero 验证未消费 token 的 role 不出现在 PerAgent 中。
func Test_UsageTracker_PerAgentSkipsZero(t *testing.T) {
	tk := NewUsageTracker(nil, nil)
	// 构造一个 role 但不消费 token（极端情况）
	tk.perAgent["ghost"] = &agentTotals{}
	tk.Record("writer", makeUsageMsg(100, 50, 0, 20))

	per := tk.PerAgent()
	if len(per) != 1 || per[0].Role != "writer" {
		t.Fatalf("ghost role with zero tokens must be skipped, got %+v", per)
	}
}

// Test_UsageTracker_MissingAssistantUsageCounted 验证 missingAssistantUsage
// 计数的判定边界：
//   - 累加路径只看 Usage != nil（不绑死 Role）
//   - 诊断路径要求 Role=Assistant 且 Content 非空 — 这才像"一次真 LLM 响应却
//     没拿到 usage"，对应上游 streaming 没发 OpenAI include_usage 那条 final
//     chunk 的典型表现。其它情形（user/tool 消息、空 content 的 assistant）
//     都不算 missing。
func Test_UsageTracker_MissingAssistantUsageCounted(t *testing.T) {
	tk := NewUsageTracker(nil, nil)

	withContent := func(text string) agentcore.Message {
		return agentcore.Message{
			Role:    agentcore.RoleAssistant,
			Content: []agentcore.ContentBlock{agentcore.TextBlock(text)},
		}
	}

	// assistant + 有 Content + nil Usage → 看起来是真响应但缺 usage，计入诊断
	tk.Record("writer", withContent("hi"))
	tk.Record("writer", withContent("again"))
	// assistant 但 Content 为空 → 异常恢复路径或占位消息，不算 missing
	tk.Record("writer", agentcore.Message{Role: agentcore.RoleAssistant})
	// user/tool 消息天然不携带 usage，无论 Content 是否为空都不算 missing
	tk.Record("writer", agentcore.Message{Role: agentcore.RoleUser, Content: []agentcore.ContentBlock{agentcore.TextBlock("u")}})
	tk.Record("writer", agentcore.Message{Role: agentcore.RoleTool, Content: []agentcore.ContentBlock{agentcore.TextBlock("t")}})
	// 正常带 usage → 走累加路径，不计入诊断
	tk.Record("writer", makeUsageMsg(100, 50, 0, 20))

	if got := tk.MissingAssistantUsage(); got != 2 {
		t.Errorf("MissingAssistantUsage=%d, want 2", got)
	}
	_, in, _, _, _ := tk.Totals()
	if in != 100 {
		t.Errorf("正常路径累计被破坏，input=%d want 100", in)
	}
}

// Test_UsageTracker_CacheCapableFromFacts 验证 CacheCapable 在注册表查不到该模型时
// 仍能根据"事实"标记为 true：自建 / 国内代理后端的模型经常不在 BerriAI/litellm
// 的 pricing 索引里，resolveCost 返回 capable=false；但只要 backend 真的返回了
// CacheRead 或 CacheWrite > 0，就证明该模型客观支持 prompt cache，per-role 行
// 不该显示"未启用"。
func Test_UsageTracker_CacheCapableFromFacts(t *testing.T) {
	tk := NewUsageTracker(nil, nil) // modelSet=nil → resolveCost 永远 capable=false

	// 一次有 CacheWrite（模拟首次写入 cache，注册表没标 capable，但事实证明支持）
	tk.Record("writer", makeUsageMsg(1000, 0, 200, 100))
	per := tk.PerAgent()
	if len(per) != 1 || !per[0].CacheCapable {
		t.Fatalf("CacheWrite > 0 应立即标记 CacheCapable=true，got %+v", per)
	}
	if !tk.OverallCacheCapable() {
		t.Errorf("overall CacheCapable 也应同步置 true")
	}

	// 反向：完全无 cache 活动的 role，CacheCapable 必须保持 false
	tk.Record("editor", makeUsageMsg(500, 0, 0, 100))
	per = tk.PerAgent()
	for _, a := range per {
		if a.Role == "editor" && a.CacheCapable {
			t.Errorf("editor 全程无 CacheRead/Write，CacheCapable 不应被错误标记为 true")
		}
	}
}

// Test_UsageTracker_AccumulatesAnyRoleWithUsage 验证累加路径解耦于 Role：
// 即使将来某个 adapter 把 usage 装配到非 assistant 角色的 message 上，
// 仍能正确累计。守住"装配规则与累加规则解耦"的契约。
func Test_UsageTracker_AccumulatesAnyRoleWithUsage(t *testing.T) {
	tk := NewUsageTracker(nil, nil)
	// 构造一条理论上不太常见的、带 Usage 的非 assistant 消息
	hypothetical := agentcore.Message{
		Role:  agentcore.RoleSystem,
		Usage: &agentcore.Usage{Input: 200, Output: 50, CacheRead: 100},
	}
	tk.Record("writer", hypothetical)

	_, in, out, cr, _ := tk.Totals()
	if in != 200 || out != 50 || cr != 100 {
		t.Errorf("未按 Usage 字段累加，got (in=%d out=%d cr=%d) want (200 50 100)", in, out, cr)
	}
	if tk.MissingAssistantUsage() != 0 {
		t.Errorf("有 Usage 不应计入 missing")
	}
}

// TestAgentRoleName_ContestWriters 验证竞稿写手 writer_<slug> 归并到 writer role，
// 与 build.go 的 agentToRole 对齐；architect_/editor 回归。
func TestAgentRoleName_ContestWriters(t *testing.T) {
	if got := agentRoleName("writer_persona1"); got != "writer" {
		t.Fatalf("writer_persona1 → %q, want writer", got)
	}
	if got := agentRoleName("writer_乌贼"); got != "writer" {
		t.Fatalf("writer_乌贼 → %q, want writer", got)
	}
	if got := agentRoleName("architect_short"); got != "architect" {
		t.Fatalf("architect_short → %q, want architect(回归)", got)
	}
	if got := agentRoleName("editor"); got != "editor" {
		t.Fatalf("editor → %q, want editor(回归)", got)
	}
}
