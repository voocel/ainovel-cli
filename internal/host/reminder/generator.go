// Package reminder 提供 Coordinator 的每轮 system-reminder 生成逻辑。
//
// 设计原则（对应 docs/refactor-v2.md 铁律二）：
//   - 纯函数：每个 Generator 只读 store 状态，不产生副作用
//   - 原子语义：单条 reminder 只回答"一个问题"，避免一条 reminder 承担多职责
//   - 成本敏感：reminder 只在确有指引价值时生成，空字符串直接跳过
//
// 工具链：agentcore.ReminderGenerator 会在每 turn 调用 Generate；
// 最终 reminder 作为一次性 system 消息注入到 LLM 请求里。
package reminder

import (
	"context"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// Generator 是每轮 reminder 生成函数的抽象。
// 返回空字符串表示本轮不注入该 reminder。
type Generator interface {
	Source() string
	Generate(ctx context.Context, state State) string
}

// State 是 Generator 在本轮可读的 store 快照。
// 快照在每 turn 开头一次性拉取，避免每个 Generator 都独立读 store。
type State struct {
	Progress *domain.Progress
	// LatestGlobalCheckpoint 是最近一次全局级 checkpoint，用于感知弧/卷结束、重写完成等事件。
	LatestGlobalCheckpoint *domain.Checkpoint
	// ArcHandoffPending 为 true 表示当前弧已全部写完但 arc_summary 尚未归档，
	// 此时 Coordinator 必须先按 coordinator.md 的弧结束流程交接，再派写下一章。
	ArcHandoffPending bool
	// FoundationMissing 是基础设定中尚缺的项（premise/outline/characters/world_rules/compass）。
	FoundationMissing []string
	TurnIndex         int
}

// Aggregate 把一组 Generator 组合成 agentcore.ReminderGenerator。
// 每 turn 先拉 store 快照，再按注册顺序逐个询问，相同 Source 后覆盖先。
func Aggregate(st *store.Store, gens ...Generator) agentcore.ReminderGenerator {
	return func(ctx context.Context, turn agentcore.TurnInfo) []agentcore.Reminder {
		state := loadState(st, turn)
		out := make([]agentcore.Reminder, 0, len(gens))
		seen := make(map[string]int, len(gens))
		for _, g := range gens {
			if g == nil {
				continue
			}
			content := g.Generate(ctx, state)
			if content == "" {
				continue
			}
			r := agentcore.Reminder{Source: g.Source(), Content: content}
			if idx, ok := seen[r.Source]; ok {
				out[idx] = r
				continue
			}
			seen[r.Source] = len(out)
			out = append(out, r)
		}
		return out
	}
}

// Default 返回默认启用的全部 Generator，按注入优先级排列。
// 排在前面的 Generator 产出在最终消息里靠前。
func Default() []Generator {
	return []Generator{
		bookCompleteGen{},
		queueGuardGen{},
		flowGen{},
	}
}

func loadState(st *store.Store, turn agentcore.TurnInfo) State {
	progress, _ := st.Progress.Load()
	latest := st.Checkpoints.LatestGlobal()
	return State{
		Progress:               progress,
		LatestGlobalCheckpoint: latest,
		ArcHandoffPending:      isArcHandoffPending(st, progress),
		FoundationMissing:      st.FoundationMissing(),
		TurnIndex:              turn.TurnIndex,
	}
}

// isArcHandoffPending 判断是否"下一章超出已展开大纲范围"。
// 分层模式下这等价于"上一弧写完但还没展开下一弧"，此时 flowGen 必须刹车，
// 让 LLM 回到 coordinator.md 的弧结束流程（editor 摘要 → architect 展开）。
func isArcHandoffPending(st *store.Store, p *domain.Progress) bool {
	if p == nil || !p.Layered || p.Phase != domain.PhaseWriting {
		return false
	}
	if len(p.PendingRewrites) > 0 || (p.Flow != "" && p.Flow != domain.FlowWriting) {
		return false
	}
	volumes, _ := st.Outline.LoadLayeredOutline()
	return p.NextChapter() > domain.TotalChapters(volumes)
}
