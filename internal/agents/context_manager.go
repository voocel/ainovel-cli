package agents

import (
	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/agents/ctxpack"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/store"
)

// contextManagerConfig 聚合 Worker 压缩器的全部配置参数。
type contextManagerConfig struct {
	Store               *store.Store
	Restore             *ctxpack.WriterRestorePack
	KeepRecentTokens    int
	SummaryTokenBudget  int
	MinToolResultTokens int
	Prompts             ctxpack.SummaryPrompts
}

// newContextManager 构造实现 agentcore.Compactor 的压缩器。
//
// 压缩时机由框架掌握：估算超过 Config.CompactAt 时先压缩再发请求，
// provider 报 context overflow 时压缩后重试。压缩结果是整段替换的 history，
// 框架直接写回，不再需要旧 ContextEngine 的 project/recover 钩子。
func newContextManager(cfg contextManagerConfig) agentcore.Compactor {
	return ctxpack.NewWriterCompactor(ctxpack.WriterCompactConfig{
		Store:               cfg.Store,
		Restore:             cfg.Restore,
		KeepRecentTokens:    cfg.KeepRecentTokens,
		SummaryTokenBudget:  cfg.SummaryTokenBudget,
		MinToolResultTokens: cfg.MinToolResultTokens,
		Prompts:             cfg.Prompts,
	})
}

// compactAt 返回该窗口下触发压缩的估算 token 阈值（窗口减去压缩后必须留出的余量）。
// 返回值 <= 0 表示窗口未知，交由框架只在溢出时压缩。
func compactAt(window int) int {
	if window <= 0 {
		return 0
	}
	at := window - bootstrap.CompactReserveTokens(window)
	if at <= 0 {
		return 0
	}
	return at
}

// roleKeepRecentTokens 是压缩后原样保留的最近历史 token 预算。
// Architect / Editor 是"单任务、多次读取"：落盘数据可随时重读，
// 摘要足够接管进度，故与 writer 用同一预算。
const roleKeepRecentTokens = 20000

// roleContextProfile 描述 Architect / Editor 这类 Worker 的压缩档案：
// 角色专属的摘要提示词，其余参数与 writer 一致。
type roleContextProfile struct {
	Prompts ctxpack.SummaryPrompts
}

// newRoleContextManager 按该档案构建压缩器（窗口只影响 CompactAt，见 build.go）。
func newRoleContextManager(p roleContextProfile, st *store.Store) agentcore.Compactor {
	return newContextManager(contextManagerConfig{
		Store:               st,
		KeepRecentTokens:    roleKeepRecentTokens,
		MinToolResultTokens: 200,
		Prompts:             p.Prompts,
	})
}
