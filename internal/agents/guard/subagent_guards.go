package guard

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/store"
)

// subagentMaxConsecutiveBlocks 连续阻拦 N 次后升级为终止，避免弱模型死循环。
const subagentMaxConsecutiveBlocks = 3

// BlockHook 是 StopGuard 的审计回调：每次拦截/升级时同步调用。Host 用它把拦截
// 事实浮出到 TUI 事件流与离屏通知——否则拦截只进日志，用户在界面上只看到
// "卡顿+token 变快"，无从判断系统是在自愈还是在空转（issue #75）。
// 回调不参与 guard 决策。reason 取值：
//   - "blocked"    已注入催促消息，模型将继续推进
//   - "escalated"  连续空转超限，本轮 run 终止交回上层
//   - "hard_stop"  provider 拒答（safety/content_filter），立即终止
type BlockHook func(agent, reason string, consecutive int32)

// hardStopReasons 是无法用催促消息恢复的 provider 端拒答原因。注入
// "必须 commit" 对它们无效，反而每次产生一次完整 LLM 调用的 token 消耗，
// 并最终升级 escalate 后让 Engine 重跑整个 Worker 任务，叠加多倍浪费
// （实测 ch02 撞 safety 时一次写章产生 3 次重派 17 次 LLM 调用、命中率
// 从 50% 跌到 2.8%）。
//
// 注意 StopReasonError / StopReasonAborted 不需要列入：agentcore 在
// loop.go 收到这两种 stop reason 时直接终止 run，根本不会调用 StopGuard。
// 这里只列那些会真正走到 StopGuard 的 provider 拒答语义。
var hardStopReasons = map[agentcore.StopReason]struct{}{
	"safety":         {},
	"content_filter": {},
}

// newCheckpointDeltaGuard 构造一个 StopGuard：
// 在 baseline 之后若未出现指定 step 的 checkpoint，则拒绝 end_turn。
// baseline 由调用方在 factory 时刻捕获，保证 per-run 语义正确。
//
// blockMsg 接收 baseline 之后已观测到的 checkpoint step 集合，按实际进度组装
// 催促消息——静态消息在"必需工具本身持续报错"的场景下是误导（催模型去调一个
// 正在失败的工具，见 #75）。
//
// 计数语义是"有进展即重置"：两次拦截之间出现过
// 任何新 checkpoint（重新 draft / check 等）视为模型在推进，consecutive 归零；
// 只有毫无产物的连续空转才累计并升级终止。
func newCheckpointDeltaGuard(st *store.Store, agentName string, requiredSteps []string, blockMsg func(seen map[string]struct{}) string, onBlock BlockHook) agentcore.StopGuard {
	var baseline int64
	if cp := st.Checkpoints.LatestGlobal(); cp != nil {
		baseline = cp.Seq
	}
	need := make(map[string]struct{}, len(requiredSteps))
	for _, s := range requiredSteps {
		need[s] = struct{}{}
	}
	var consecutive atomic.Int32
	var lastBlockSeq atomic.Int64 // 上次拦截时观测到的最新 checkpoint Seq；-1 表示尚未拦截过
	lastBlockSeq.Store(-1)
	return func(_ context.Context, info agentcore.StopInfo) agentcore.StopDecision {
		// 不可恢复错误：直接升级，不浪费一次催促。
		if _, hard := hardStopReasons[info.Message.StopReason]; hard {
			slog.Error("subagent stop_guard 检测到不可恢复停机，立即升级",
				"module", "agent.guard", "agent", agentName,
				"turn", info.TurnIndex, "stop_reason", info.Message.StopReason)
			if onBlock != nil {
				onBlock(agentName, "hard_stop", consecutive.Load())
			}
			return agentcore.StopDecision{Allow: false, Escalate: true}
		}
		// 倒序扫描 baseline 之后的 checkpoint，收集已出现的 step（放行判定 + 进度消息共用）。
		// 新 checkpoint 在尾部，遇到 <= baseline 即可 break。
		all := st.Checkpoints.All()
		latestSeq := baseline
		seen := make(map[string]struct{})
		for i := len(all) - 1; i >= 0; i-- {
			cp := all[i]
			if cp.Seq <= baseline {
				break
			}
			if cp.Seq > latestSeq {
				latestSeq = cp.Seq
			}
			seen[cp.Step] = struct{}{}
		}
		for s := range need {
			if _, ok := seen[s]; ok {
				consecutive.Store(0)
				return agentcore.StopDecision{Allow: true}
			}
		}
		// 上次拦截以来有新工件落盘 = 模型在推进（如被催后重新 draft 再试探收尾），
		// 重置计数；升级只应惩罚毫无进展的空转，而不是把整个 run 的拦截攒在一起报废。
		if prev := lastBlockSeq.Load(); prev >= 0 && latestSeq > prev {
			consecutive.Store(0)
		}
		lastBlockSeq.Store(latestSeq)
		n := consecutive.Add(1)
		if n > subagentMaxConsecutiveBlocks {
			slog.Error("subagent stop_guard 连续阻拦超限，升级为终止",
				"module", "agent.guard", "agent", agentName, "turn", info.TurnIndex, "consecutive", n)
			if onBlock != nil {
				onBlock(agentName, "escalated", n)
			}
			return agentcore.StopDecision{Allow: false, Escalate: true}
		}
		slog.Warn("subagent stop_guard 拦截 end_turn",
			"module", "agent.guard", "agent", agentName, "turn", info.TurnIndex, "consecutive", n)
		if onBlock != nil {
			onBlock(agentName, "blocked", n)
		}
		return agentcore.StopDecision{Allow: false, InjectMessage: blockMsg(seen)}
	}
}

// staticBlockMsg 把固定文案适配成 blockMsg 签名（架构/编辑器的产物是单工具落盘，
// 不存在多步进度，静态催促即够）。
func staticBlockMsg(msg string) func(map[string]struct{}) string {
	return func(map[string]struct{}) string { return msg }
}

// NewWriterStopGuard 要求 writer 本轮至少产生一次成功的 commit_chapter。
// 催促消息按已落盘的 step 进度组装：writer 是唯一有多步工具链的子代理，
// 静态的"必须调 commit_chapter"在前置步骤缺失或 commit 本身报错时是误导。
func NewWriterStopGuard(st *store.Store, onBlock BlockHook) agentcore.StopGuard {
	return newCheckpointDeltaGuard(st, "writer", []string{"commit"}, writerBlockMsg, onBlock)
}

// writerBlockMsg 按本轮已出现的 checkpoint step 判断 writer 卡在哪一步。
// step 名与各工具落盘值对应：plan / draft / edit / consistency_check / commit。
func writerBlockMsg(seen map[string]struct{}) string {
	_, hasDraft := seen["draft"]
	_, hasEdit := seen["edit"]
	_, hasCheck := seen["consistency_check"]
	switch {
	case !hasDraft && !hasEdit:
		return "禁止结束：本轮尚未落盘任何正文。请按 plan_chapter → draft_chapter → check_consistency → commit_chapter 的顺序完成本章；正文只输出在聊天里等于丢失，必须通过工具落盘并提交。"
	case !hasCheck:
		return "禁止结束：正文已落盘但未收尾。请先调 check_consistency 核对一致性，再调 commit_chapter 提交本章。draft_chapter / edit_chapter 只是保存草稿，不算完成。"
	default:
		return "禁止结束：本章只差 commit_chapter 提交。请立即调用 commit_chapter；若它返回错误，先按错误信息处理（核对章节号、按提示补齐前置动作）再重试提交，不要在未提交的状态下结束。"
	}
}

// NewArchitectStopGuard 要求 architect 本轮至少落盘一次规划产物。
func NewArchitectStopGuard(st *store.Store, onBlock BlockHook) agentcore.StopGuard {
	return newCheckpointDeltaGuard(st, "architect",
		[]string{
			"book", "premise", "outline", "layered_outline", "characters", "world_rules",
			"foundation_audit", "expand_next_arc", "append_volume", "update_compass", "complete_book", "revise_outline", "resolve_outline_feedback",
		},
		staticBlockMsg("你必须调用 save_book、save_foundation、expand_next_arc、revise_outline、resolve_outline_feedback 或 audit_foundation 将产出落盘后才能结束。只输出 Markdown/JSON 文字等于丢失。"),
		onBlock,
	)
}

// NewEditorStopGuard 要求 editor 本轮落盘与"任务"匹配的产物后才能结束。
//
// 任务感知：被派去生成摘要时，仅 save_review（复核）不算完成——必须产出对应摘要。
// 否则"被派生成弧摘要却先复核"的 editor 会满足旧的宽松判据提前结束，弧摘要永不落盘
// （配合 dispatcher 去重哑火曾导致卷中骨架弧死循环，详见 outline-exhaustion-livelock）。
// 终态工具退出同样会咨询 StopGuard（契约测试 TestContract_TerminalToolExitConsultsStopGuard），
// 所以 save_review 在 build.go 里硬停是安全的：摘要任务里 editor 先复核时本 guard 会
// 否决该次退出并催促，直到对应摘要落盘。
func NewEditorStopGuard(st *store.Store, task string, onBlock BlockHook) agentcore.StopGuard {
	switch {
	case strings.Contains(task, "save_review_check"):
		return newCheckpointDeltaGuard(st, "editor", []string{"review_check"},
			staticBlockMsg("本次任务是返工后的单次定向复核：必须调用 save_review_check 保存结论，不得用 save_review 发起新一轮返工。"), onBlock)
	case strings.Contains(task, "save_volume_summary") || strings.Contains(task, "卷摘要"):
		return newCheckpointDeltaGuard(st, "editor", []string{"volume_summary"},
			staticBlockMsg("本次任务是生成卷摘要：你必须调用 save_volume_summary 落盘后才能结束，save_review 复核不算完成。"), onBlock)
	case strings.Contains(task, "save_arc_summary") || strings.Contains(task, "弧摘要"):
		return newCheckpointDeltaGuard(st, "editor", []string{"arc_summary"},
			staticBlockMsg("本次任务是生成弧摘要：你必须调用 save_arc_summary 落盘后才能结束，save_review 复核不算完成。"), onBlock)
	default:
		// 评审或临时任务：任一审阅/摘要落盘即可（保持既有宽松行为）。
		return newCheckpointDeltaGuard(st, "editor",
			[]string{"review", "arc_summary", "volume_summary"},
			staticBlockMsg("你必须调用 save_review / save_arc_summary / save_volume_summary 之一落盘结果后才能结束。"), onBlock)
	}
}
