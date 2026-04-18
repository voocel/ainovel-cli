package host

import (
	"strings"
	"sync"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/models"
)

// UsageTracker 累计整个会话所有 agent 的 LLM 输入/输出 token 与美元成本。
//
// 工作机制：
//   - 每次 agent 的 OnMessage 回调触发时调用 Record(agentName, msg)
//   - agentName 映射到 role（architect_* 归一为 architect），查 ModelSet 当前该 role 绑定的模型
//   - 用 models.DefaultRegistry 查模型价格，按非缓存输入/输出/缓存读/缓存写四项累乘
//   - 注册表无此模型时，退回 msg.Usage.Cost.Total（provider 自带，可能为 0）
//   - 模型热切换（/model）后续消息自动按新模型算价，旧消息保留旧成本
//
// 线程安全。
type UsageTracker struct {
	mu              sync.Mutex
	totalInput      int
	totalOutput     int
	totalCacheRead  int
	totalCacheWrite int
	totalCost       float64
	modelSet        *bootstrap.ModelSet
}

func NewUsageTracker(set *bootstrap.ModelSet) *UsageTracker {
	return &UsageTracker{modelSet: set}
}

// Record 累计一条 agent 消息的 token 与成本。msg 非 Message / Usage 为空时忽略。
func (t *UsageTracker) Record(agentName string, msg agentcore.AgentMessage) {
	if t == nil {
		return
	}
	m, ok := msg.(agentcore.Message)
	if !ok || m.Usage == nil {
		return
	}

	cost := t.resolveCost(agentName, *m.Usage)

	t.mu.Lock()
	defer t.mu.Unlock()
	t.totalInput += m.Usage.Input
	t.totalOutput += m.Usage.Output
	t.totalCacheRead += m.Usage.CacheRead
	t.totalCacheWrite += m.Usage.CacheWrite
	t.totalCost += cost
}

// Totals 返回累计总量的快照。
func (t *UsageTracker) Totals() (cost float64, input, output, cacheRead, cacheWrite int) {
	if t == nil {
		return 0, 0, 0, 0, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.totalCost, t.totalInput, t.totalOutput, t.totalCacheRead, t.totalCacheWrite
}

func (t *UsageTracker) resolveCost(agentName string, u agentcore.Usage) float64 {
	modelName := ""
	if t.modelSet != nil {
		_, modelName, _ = t.modelSet.CurrentSelection(agentRoleName(agentName))
	}
	if entry, ok := models.DefaultRegistry().Resolve(modelName); ok {
		if c := computeCost(u, *entry); c > 0 {
			return c
		}
	}
	// registry 没命中（自定义代理/未知模型），回落到 provider 自带成本
	if u.Cost != nil {
		return u.Cost.Total
	}
	return 0
}

// agentRoleName 把 subagent 名字归一到 role 名。
// architect_short/mid/long 都归到 architect；其他原样返回。
func agentRoleName(agentName string) string {
	if strings.HasPrefix(agentName, "architect_") {
		return "architect"
	}
	return agentName
}

// computeCost 按 $/1M tokens 单价计算本次调用的美元开销。
//
// Anthropic / OpenAI 的 Input 通常已包含缓存读取部分，因此缓存读/写单独记账前，
// 先从 Input 中扣除 CacheRead 得到"非缓存输入"。
// 若 provider 并未合并（扣完为负），当作 Input 不含缓存，按原值计算。
func computeCost(u agentcore.Usage, e models.ModelEntry) float64 {
	nonCachedInput := u.Input - u.CacheRead
	if nonCachedInput < 0 {
		nonCachedInput = u.Input
	}
	c := 0.0
	c += float64(nonCachedInput) * e.InputCostPer1M / 1_000_000
	c += float64(u.Output) * e.OutputCostPer1M / 1_000_000
	c += float64(u.CacheRead) * e.CacheReadCostPer1M / 1_000_000
	c += float64(u.CacheWrite) * e.CacheWriteCostPer1M / 1_000_000
	return c
}
