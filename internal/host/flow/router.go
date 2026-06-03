// Package flow 实现垂类路由：Host 根据事实决定下一个调哪个子代理做什么。
//
// 设计原则：
//   - Route 是纯函数：输入 State，输出 *Instruction。无 IO、无 Store 调用，可单测。
//   - State 由 LoadState（非纯）从 Store 构造，一次性把路由需要的事实读齐。
//   - 返回 nil 是合法的：表示"裁定场景，让 Coordinator LLM 自主决策"。
//
// Router 覆盖的是"查表型"决策（每章下一步、弧末后处理、队列驱动），
// 不覆盖"语义理解型"决策（选规划师、处理用户 Steer、输出总结）。
package flow

import (
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// Instruction 指示 Host 下一步要求 Coordinator 调用的子代理与任务。
type Instruction struct {
	Agent   string // architect_long / architect_short / writer / editor
	Task    string // 给子代理的任务描述
	Reason  string // 给 Coordinator 看的理由（可选，方便调试与日志）
	Chapter int    // writer 任务涉及的章节号（续写/重写/打磨）；0 表示不涉及（editor/architect 任务）
}

// State 是 Route 的输入：所有事实必须在此显式声明，禁止 Route 内部读 Store。
type State struct {
	Progress *domain.Progress

	// 上一个已完成章节（Progress.CompletedChapters 末尾）；为 0 表示尚未开始写作。
	LastCompleted int

	// 上一章的弧边界信息；IsArcEnd=false 时其他字段无意义。
	// 当 LastCompleted=0 或非 Layered 模式时应为 nil。
	ArcBoundary *storepkg.ArcBoundary

	// 弧末后处理的三个事实：评审 / 弧摘要 / 卷摘要是否已完成。
	HasArcReview     bool
	HasArcSummary    bool
	HasVolumeSummary bool

	// 基础设定缺项（规划阶段的补齐信号）。
	FoundationMissing []string

	// 竞稿事实（ContestEnabled=false 时其余字段无意义）。
	ContestEnabled  bool            // 是否启用多人格竞稿
	Personas        []string        // persona slug 列表，顺序即写作顺序
	ContestChapter  int             // 当前竞稿目标章（= NextChapter），0 表示不适用
	CandidatesReady map[string]bool // 各 persona 候选稿是否到位
	HasVerdict      bool            // 本章是否已有裁定
	VerdictWinner   string          // 中选 persona slug
	IsPromoted      bool            // 中选稿是否已提升为正式 draft.md

	VerdictRevisionNotes string // 中选稿的修改意见（来自 verdict，供润色 writer 参考）
}

// Route 根据事实返回下一步指令；返回 nil 表示让 Coordinator LLM 自主裁定。
//
// 决策优先级（互斥，自上而下匹配第一个）：
//  1. Phase=Complete        → nil（LLM 输出总结）
//  2. Phase!=Writing        → nil（LLM 裁定规划师选型 / 规划补齐）
//  3. PendingRewrites 非空  → writer 按队列重写/打磨
//  4. Flow=Reviewing        → nil（editor 刚保存 review，verdict 分叉由工具层处理）
//  5. Flow=Steering         → nil（用户干预处理中）
//  6. 弧末评审缺失           → editor(arc review)
//  7. 弧末评审有但弧摘要缺失  → editor(arc summary)
//  8. 卷末弧摘要有但卷摘要缺失 → editor(volume summary)
//  9. 下一弧是骨架           → architect_long(expand_arc)
//
// 10. 卷末需决策下一卷       → architect_long(append_volume / complete_book)
// 11. 竞稿章（启用且 ContestChapter>0）→ routeContest 四步子状态机
// 12. 其它                  → writer(写 next_chapter)
func Route(s State) *Instruction {
	p := s.Progress
	if p == nil {
		return nil
	}

	// 1. 终态：让 LLM 输出总结
	if p.Phase == domain.PhaseComplete {
		return nil
	}

	// 2. 规划阶段由 Coordinator 裁定（选 architect_long/short + 补齐循环）
	if p.Phase != domain.PhaseWriting {
		return nil
	}

	// 3. 重写/打磨队列优先（事实已在工具层落盘，Router 只照单派发）
	if len(p.PendingRewrites) > 0 {
		ch := p.PendingRewrites[0]
		verb := "重写"
		if p.Flow == domain.FlowPolishing {
			verb = "打磨"
		}
		return &Instruction{
			Agent:   "writer",
			Task:    fmt.Sprintf("%s第 %d 章", verb, ch),
			Reason:  fmt.Sprintf("PendingRewrites 队列剩余 %d 章", len(p.PendingRewrites)),
			Chapter: ch,
		}
	}

	// 4. 审阅中：save_review 刚落盘，verdict 升级/降级由工具层处理，路由不介入
	if p.Flow == domain.FlowReviewing {
		return nil
	}

	// 5. 用户干预处理中：Coordinator 正在裁定，Host 不抢占
	if p.Flow == domain.FlowSteering {
		return nil
	}

	// 6-10. 分层模式的弧末后处理
	if p.Layered && s.ArcBoundary != nil && s.ArcBoundary.IsArcEnd {
		b := s.ArcBoundary
		switch {
		case !s.HasArcReview:
			return &Instruction{
				Agent:  "editor",
				Task:   fmt.Sprintf("对第 %d 卷第 %d 弧做弧级评审（scope=arc）", b.Volume, b.Arc),
				Reason: "弧末评审未完成",
			}
		case !s.HasArcSummary:
			return &Instruction{
				Agent:  "editor",
				Task:   fmt.Sprintf("生成第 %d 卷第 %d 弧摘要（save_arc_summary）", b.Volume, b.Arc),
				Reason: "弧摘要未完成",
			}
		case b.IsVolumeEnd && !s.HasVolumeSummary:
			return &Instruction{
				Agent:  "editor",
				Task:   fmt.Sprintf("生成第 %d 卷卷摘要（save_volume_summary）", b.Volume),
				Reason: "卷摘要未完成",
			}
		case b.NeedsExpansion && b.NextArc > 0:
			return &Instruction{
				Agent:  "architect_long",
				Task:   fmt.Sprintf("展开第 %d 卷第 %d 弧（save_foundation type=expand_arc）", b.NextVolume, b.NextArc),
				Reason: "下一弧骨架待展开",
			}
		case b.NeedsNewVolume:
			return &Instruction{
				Agent:  "architect_long",
				Task:   "评估后调用 save_foundation type=append_volume（继续写）或 type=complete_book（全书结束）",
				Reason: "卷末需决定追加新卷或结束全书",
			}
		}
	}

	// 11. 多人格竞稿编排：竞稿章的下一步由子状态机权威决定
	//（返回 nil 表示无指令可派——等 dispatcher 内联提升，或数据异常降级交 LLM 裁定）
	if s.ContestEnabled && s.ContestChapter > 0 {
		return routeContest(s)
	}

	// 12. 正常续写（单 Writer 或竞稿未启用）
	next := p.NextChapter()
	if next <= 0 {
		return nil
	}
	return &Instruction{
		Agent:   "writer",
		Task:    fmt.Sprintf("写第 %d 章", next),
		Reason:  "续写下一章",
		Chapter: next,
	}
}

// routeContest 计算竞稿章的下一步指令；返回 nil 表示"无 writer/judge 指令需要派"
// （要么等 dispatcher 内联提升，要么本章已完成）。
func routeContest(s State) *Instruction {
	ch := s.ContestChapter
	// 1. 候选稿未齐 → 逐个派 persona writer 写候选
	for _, persona := range s.Personas {
		if !s.CandidatesReady[persona] {
			return &Instruction{
				Agent:   "writer_" + persona,
				Task:    fmt.Sprintf("写第 %d 章候选稿", ch),
				Reason:  fmt.Sprintf("竞稿：persona %s 候选稿未完成", persona),
				Chapter: ch,
			}
		}
	}
	// 2. 候选齐、无裁定 → 派 judge
	if !s.HasVerdict {
		return &Instruction{
			Agent:   "judge",
			Task:    fmt.Sprintf("评审第 %d 章的 %d 份候选稿，选优并给修改意见（save_verdict）", ch, len(s.Personas)),
			Reason:  "竞稿：候选稿已齐，待选优",
			Chapter: ch,
		}
	}
	// 3. 有裁定、未提升 → 返回 nil，由 dispatcher 内联提升
	if !s.IsPromoted {
		return nil
	}
	// 数据不一致（IsPromoted 但无 winner），保守降级返回 nil 交上层裁定，不派 writer_ 坏指令
	if s.VerdictWinner == "" {
		return nil
	}
	// 4. 已提升 → 派中选 writer 润色（Task 文本与候选不同，规避 dedupe）
	return &Instruction{
		Agent:   "writer_" + s.VerdictWinner,
		Task:    fmt.Sprintf("按选优意见润色并提交第 %d 章。选优意见：%s", ch, s.VerdictRevisionNotes),
		Reason:  fmt.Sprintf("竞稿：%s 中选，润色后提交", s.VerdictWinner),
		Chapter: ch,
	}
}

// FormatMessage 把 Instruction 格式化为发给 Coordinator 的用户消息。
// 格式固定，便于 Coordinator prompt 识别与 LLM 直接响应。
func FormatMessage(i *Instruction) string {
	return fmt.Sprintf(
		"[Host 下达指令] 下一步：调用 subagent(%s, %q)\n理由：%s\n这是流程层的明确指令，请立即执行，不要先调 novel_context，不要先输出推理。",
		i.Agent, i.Task, i.Reason,
	)
}
