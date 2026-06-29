package domain

import "time"

// UsageSchemaVersion 是 meta/usage.json 的兼容Phiên bản号。
// 未来若 AgentUsageTotals 字段语义变化，递增此值；UsageStore.Load 见到不同Phiên bản应忽略并触发 replay 重建。
const UsageSchemaVersion = 2

// UsageState 是累计 token / cost 用量的可持久化Chụp。
// 内存中由 UsageTracker 维护，定期 debounce 落盘到 meta/usage.json。
//
// 注意：UsageTracker 内部的滑动窗 samples（"近 N 次命中率"）**不持久化**——
// 它只服务 UI 短期诊断，进程重启从RỗngBắt đầu重Mới积累几轮即可Phục hồi语义。
// MissingAssistantUsage 保留持久化，跨重启累积更有诊断价值。
type UsageState struct {
	Schema       int                         `json:"schema"`
	UpdatedAt    time.Time                   `json:"updated_at"`
	Overall      AgentUsageTotals            `json:"overall"`
	PerAgent     map[string]AgentUsageTotals `json:"per_agent"`
	PerModel     map[string]AgentUsageTotals `json:"per_model,omitempty"`
	MissingUsage int                         `json:"missing_assistant_usage"`
}

// AgentUsageTotals 是单个角色（或 overall）累计计数的可持久化形态。
type AgentUsageTotals struct {
	Input        int     `json:"input"`
	Output       int     `json:"output"`
	CacheRead    int     `json:"cache_read"`
	CacheWrite   int     `json:"cache_write"`
	Cost         float64 `json:"cost_usd"`
	Saved        float64 `json:"saved_usd"`
	CacheCapable bool    `json:"cache_capable"`
}
