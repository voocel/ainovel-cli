package reminder

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/store"
)

// subagentMaxConsecutiveBlocks 连续阻拦 N 次后升级为终止，避免弱Mô hình死循环。
const subagentMaxConsecutiveBlocks = 3

// hardStopReasons 是Không thể用催促消息Phục hồi的 provider 端拒答原因。注入
// "必须 commit" 对它们Không có效，反而每次产生一次完整 LLM 调用的 token 消耗，
// 并最终升级 escalate 后让 coordinator 重派整个 SubAgent，叠加多倍浪费
// （实测 ch02 撞 safety 时一次写章产生 3 次重派 17 次 LLM 调用、命中率
// 从 50% 跌到 2.8%）。
//
// 注意 StopReasonError / StopReasonAborted 不Cần列入：agentcore 在
// loop.go 收到这两种 stop reason 时直接终止 run，根本不会调用 StopGuard。
// 这里只列那些会真正走到 StopGuard 的 provider 拒答语义。
var hardStopReasons = map[agentcore.StopReason]struct{}{
	"safety":         {},
	"content_filter": {},
}

// newCheckpointDeltaGuard 构造一个 StopGuard：
// 在 baseline 之后若未出现指定 step 的 checkpoint，则拒绝 end_turn。
// baseline 由调用方在 factory 时刻捕获，保证 per-run 语义正确。
func newCheckpointDeltaGuard(st *store.Store, agentName string, requiredSteps []string, blockMsg string) agentcore.StopGuard {
	var baseline int64
	if cp := st.Checkpoints.LatestGlobal(); cp != nil {
		baseline = cp.Seq
	}
	need := make(map[string]struct{}, len(requiredSteps))
	for _, s := range requiredSteps {
		need[s] = struct{}{}
	}
	var consecutive atomic.Int32
	return func(_ context.Context, info agentcore.StopInfo) agentcore.StopDecision {
		// 不可Phục hồiLỗi：直接升级，不浪费一次催促。
		if _, hard := hardStopReasons[info.Message.StopReason]; hard {
			slog.Error("subagent stop_guard Phát hiện不可Phục hồi停机，立即升级",
				"module", "host.reminder", "agent", agentName,
				"turn", info.TurnIndex, "stop_reason", info.Message.StopReason)
			return agentcore.StopDecision{Allow: false, Escalate: true}
		}
		// 倒序扫描：Mới checkpoint 在尾部，遇到 <= baseline 即可 break。
		all := st.Checkpoints.All()
		for i := len(all) - 1; i >= 0; i-- {
			cp := all[i]
			if cp.Seq <= baseline {
				break
			}
			if _, ok := need[cp.Step]; ok {
				consecutive.Store(0)
				return agentcore.StopDecision{Allow: true}
			}
		}
		n := consecutive.Add(1)
		if n > subagentMaxConsecutiveBlocks {
			slog.Error("subagent stop_guard 连续阻拦超限，升级为终止",
				"module", "host.reminder", "agent", agentName, "turn", info.TurnIndex, "consecutive", n)
			return agentcore.StopDecision{Allow: false, Escalate: true}
		}
		slog.Warn("subagent stop_guard 拦截 end_turn",
			"module", "host.reminder", "agent", agentName, "turn", info.TurnIndex, "consecutive", n)
		return agentcore.StopDecision{Allow: false, InjectMessage: blockMsg}
	}
}

// NewWriterStopGuard 要求 writer 本轮至少产生一次Thành công的 commit_chapter。
func NewWriterStopGuard(st *store.Store) agentcore.StopGuard {
	return newCheckpointDeltaGuard(st, "writer",
		[]string{"commit"},
		"你必须调用 commit_chapter Nộp本章后才能Kết thúc。draft_chapter 只是LưuBản nháp，不算Hoàn thành。",
	)
}

// NewArchitectStopGuard 要求 architect 本轮至少落盘一次 save_foundation。
func NewArchitectStopGuard(st *store.Store) agentcore.StopGuard {
	return newCheckpointDeltaGuard(st, "architect",
		[]string{
			"premise", "outline", "layered_outline", "characters", "world_rules",
			"expand_arc", "append_volume", "update_compass", "complete_book",
		},
		"你必须调用 save_foundation 将产出落盘后才能Kết thúc。只输出 Markdown/JSON 文字等于丢失。",
	)
}

// NewEditorStopGuard 要求 editor 本轮落盘与"任务"匹配的产物后才能Kết thúc。
//
// 任务感知：被派去生成Tóm tắt时，仅 save_review（复核）不算Hoàn thành——必须产出对应Tóm tắt。
// 否则"被派生成弧Tóm tắt却先复核"的 editor 会满足Cũ的宽松判据提前Kết thúc，弧Tóm tắt永不落盘
// （配合 dispatcher 去重哑火曾导致卷中骨架弧死循环，详见 outline-exhaustion-livelock）。
// StopAfterTool Thoát会绕过 StopGuard（loop.go），故 build.go 同步把 save_review 移出硬停，
// 让复核后能Tiếp tục走到Tóm tắt工具，再由本 guard 把关收尾。
func NewEditorStopGuard(st *store.Store, task string) agentcore.StopGuard {
	switch {
	case strings.Contains(task, "save_volume_summary") || strings.Contains(task, "卷Tóm tắt"):
		return newCheckpointDeltaGuard(st, "editor", []string{"volume_summary"},
			"本次任务是生成卷Tóm tắt：你必须调用 save_volume_summary 落盘后才能Kết thúc，save_review 复核不算Hoàn thành。")
	case strings.Contains(task, "save_arc_summary") || strings.Contains(task, "弧Tóm tắt"):
		return newCheckpointDeltaGuard(st, "editor", []string{"arc_summary"},
			"本次任务是生成弧Tóm tắt：你必须调用 save_arc_summary 落盘后才能Kết thúc，save_review 复核不算Hoàn thành。")
	default:
		// 评审或临时任务：任一审阅/Tóm tắt落盘即可（保持既有宽松行为）。
		return newCheckpointDeltaGuard(st, "editor",
			[]string{"review", "arc_summary", "volume_summary"},
			"你必须调用 save_review / save_arc_summary / save_volume_summary 之一落盘Kết quả后才能Kết thúc。")
	}
}
