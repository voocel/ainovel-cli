// Package reminder 管理 Coordinator / SubAgent 的 StopGuard 与可选的每轮 reminder。
//
// 重构说明（2026-04-20）：
//   - 原有 flow / queue_guard / book_complete 三条每轮 reminder 已被 Host Flow Router 取代
//     （`internal/host/flow/`）。
//   - 这里保留 Aggregate/Default 的装配点，方便未来新增无法用路由表达的"事实提醒"。
//   - StopGuard（Coordinator + 三个子代理）仍由本包对外暴露，见 stop_guard.go / subagent_guards.go。
package reminder

import (
	"context"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// Generator 是每轮 reminder 生成函数的抽象。返回空串表示本轮不注入。
type Generator interface {
	Source() string
	Generate(ctx context.Context, state State) string
}

// State 是 Generator 在本轮可读的 store 快照。
type State struct {
	Progress          *domain.Progress
	FoundationMissing []string
	TurnIndex         int
}

// Aggregate 把一组 Generator 组合成 agentcore.ReminderGenerator。
// 每轮一次性拉快照，按注册顺序逐个询问；相同 Source 后写覆盖先写。
func Aggregate(st *store.Store, gens ...Generator) agentcore.ReminderGenerator {
	return func(ctx context.Context, turn agentcore.TurnInfo) []agentcore.Reminder {
		if len(gens) == 0 {
			return nil
		}
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

// Default 返回默认启用的 Generator。当前没有任何每轮 reminder——
// 全部流程决策由 Host Flow Router 负责。保留此函数是装配点的稳定约定。
func Default() []Generator { return nil }

func loadState(st *store.Store, turn agentcore.TurnInfo) State {
	progress, _ := st.Progress.Load()
	return State{
		Progress:          progress,
		FoundationMissing: st.FoundationMissing(),
		TurnIndex:         turn.TurnIndex,
	}
}
