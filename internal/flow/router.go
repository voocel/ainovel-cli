// Package flow 实现垂类路由：Host 根据事实决定下一个调哪个子代理做什么。
//
// 设计原则：
//   - Route 是纯函数：输入 State，输出 *Instruction。无 IO、无 Store 调用，可单测。
//   - State 由 LoadState（非纯）从 Store 构造，一次性把路由需要的事实读齐。
//   - 返回 nil 是合法的：表示当前没有可由确定性事实推出的 Worker 指令；
//     Engine 再按终态、启动补裁或等待用户干预处理。
//
// Router 覆盖的是"查表型"决策（每章下一步、弧末后处理、队列驱动），
// 不覆盖"语义理解型"决策（选规划师、处理用户 Steer、输出总结）。
package flow

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// plannerForTier 从已落盘的规划级别推导规划师身份:short 归短篇规划师,
// mid/long 归长篇规划师(与启动 Arbiter 的选型口径一致)。
func plannerForTier(tier domain.PlanningTier) string {
	if tier == domain.PlanningTierShort {
		return "architect_short"
	}
	return "architect_long"
}

// Instruction 指示 Engine 下一步直接运行的 Worker 与任务。
type Instruction struct {
	Agent   string // architect_long / architect_short / writer / editor
	Task    string // 给子代理的任务描述
	Reason  string // 路由理由（用于事件、日志与失败裁定）
	Chapter int    // writer 任务涉及的章节号（续写/重写/打磨）；0 表示不涉及（editor/architect 任务）
}

type AggregateKind string

const (
	AggregateArcReview     AggregateKind = "arc_review"
	AggregateArcSummary    AggregateKind = "arc_summary"
	AggregateVolumeSummary AggregateKind = "volume_summary"
	AggregateGlobalReview  AggregateKind = "global_review"
)

type AggregateRefresh struct {
	Kind         AggregateKind
	Volume       int
	Arc          int
	StartChapter int
	EndChapter   int
}

// State 是 Route 的输入：所有事实必须在此显式声明，禁止 Route 内部读 Store。
type State struct {
	Progress *domain.Progress

	// 已完成章节中的最大章节号；为 0 表示尚未开始写作。
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

	// 已落盘的规划级别（save_foundation 落 scale 时写入 RunMeta）。
	// 空 = 首次规划尚未产出任何设定，规划师身份不可判定。
	PlanningTier domain.PlanningTier

	// 非分层书：最近完成章是否已有 scope=global 的全局审阅
	//（仅在 ShouldReview 触发点有意义；分层书恒 false）。
	HasGlobalReview bool

	// 必须在续写前由 Architect 处理的外部修订影响。普通 Writer 反馈留到下一次
	// 自然结构操作统一吸收，不为每章额外派发规划师。
	ImmediateFeedbackCount int

	// 正常收尾或修订/返工后最早一个需要由 Editor 生成的聚合工件。
	AggregateRefresh *AggregateRefresh

	// 整批正文返工结束后的单次定向复核，优先于摘要和续写。
	ReviewRecheck *domain.ReviewEntry
}

// Route 根据事实返回下一步确定性指令；返回 nil 由 Engine 按调用上下文处理。
//
// 决策优先级（互斥，自上而下匹配第一个）：
//  1. Phase=Complete        → nil（Host 确定性输出总结）
//  2. 规划期设定缺项且规划师可判定 → 同一规划师补齐；否则 nil（Engine 启动补裁）
//  3. PendingRewrites 非空  → writer 按队列重写/打磨
//  4. Flow=Reviewing        → nil（dormant：当前无写入者，评审期 Flow 实为 writing）
//  5. Flow=Steering         → nil（用户干预处理中）
//  6. 外部修订导致聚合工件失效 → editor 重建
//  7. 外部修订影响后续规划     → architect 处理
//  8. 分层书到达弧末          → 评审、摘要、扩弧或续卷
//  9. 非分层全局审阅到期       → editor(global review)
//
// 10. 非分层大纲已耗尽        → architect(决定完结或续接大纲)
// 11. 其它                   → writer(写 next_chapter)
func Route(s State) *Instruction {
	p := s.Progress
	if p == nil {
		return nil
	}

	// 1. 终态：Host 根据 store 事实生成确定性总结
	if p.Phase == domain.PhaseComplete {
		return nil
	}

	// 2. 规划期补齐：查表型决策——缺什么在 store，规划师身份从已落盘的 scale 推导
	//    （short → architect_short，其余 → architect_long）。tier 为空说明首次规划
	//    尚未落盘任何设定（选型是语义判断），由 Engine 的 planStartFallback 补裁。
	if p.Phase != domain.PhaseWriting {
		if len(s.FoundationMissing) > 0 && s.PlanningTier != "" {
			task := fmt.Sprintf("补齐基础设定与作品信息缺项：%s；book 使用 save_book，其余基础设定使用 save_foundation 落盘", strings.Join(s.FoundationMissing, "、"))
			if len(s.FoundationMissing) == 1 && s.FoundationMissing[0] == "foundation_audit" {
				task = "基础设定已齐全：重新调用 novel_context 读取全部已落盘工件与 foundation_status.fingerprint，审查跨文件语义一致性后调用 audit_foundation；有问题先修正并重新审查"
			}
			return &Instruction{
				Agent:  plannerForTier(s.PlanningTier),
				Task:   task,
				Reason: "基础设定缺项未齐，照缺项续派同一规划师",
			}
		}
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

	// 4. 审阅中 → 交回 LLM。当前为 dormant 分支：save_review 只把 Flow 置为
	//    writing/rewriting/polishing，无任何生产路径置 reviewing（评审期 Flow 实为 writing，
	//    "评审先于续写"由 agentcore steering 优先级保证，不靠此分支）。保留以与 Steering
	//    对称，并在未来 editor 评审期显式置 reviewing 时使路由让位于 LLM。
	if p.Flow == domain.FlowReviewing {
		return nil
	}

	// 5. 用户干预处理中：Arbiter 正在裁定，Engine 不抢占
	if p.Flow == domain.FlowSteering {
		return nil
	}
	if review := s.ReviewRecheck; review != nil {
		return &Instruction{
			Agent: "editor",
			Task: fmt.Sprintf(
				"复核 scope=%s、chapter=%d 的原评审返工：调用 novel_context(chapter=%d)，仅回读 working_memory.review_recheck 指定的修改章节并核对原问题，用 save_review_check 保存一次复核结论；保留未解决项，不新增返工或重做全量评审",
				review.Scope, review.Chapter, review.Chapter,
			),
			Reason: "正文返工已排空，定向复核尚未完成",
		}
	}
	if refresh := s.AggregateRefresh; refresh != nil {
		switch refresh.Kind {
		case AggregateArcReview:
			return &Instruction{
				Agent: "editor",
				Task: fmt.Sprintf(
					"审阅第 %d 卷第 %d 弧（第 %d-%d 章）：调用 novel_context(chapter=%d)，save_review 使用 scope=arc、chapter=%d",
					refresh.Volume, refresh.Arc, refresh.StartChapter, refresh.EndChapter, refresh.EndChapter, refresh.EndChapter,
				),
				Reason: "弧级审阅缺失",
			}
		case AggregateArcSummary:
			return &Instruction{
				Agent:  "editor",
				Task:   fmt.Sprintf("生成第 %d 卷第 %d 弧摘要、角色快照与写作规则（save_arc_summary）", refresh.Volume, refresh.Arc),
				Reason: "弧级摘要缺失",
			}
		case AggregateVolumeSummary:
			return &Instruction{
				Agent:  "editor",
				Task:   fmt.Sprintf("生成第 %d 卷卷摘要（save_volume_summary）", refresh.Volume),
				Reason: "卷摘要缺失",
			}
		case AggregateGlobalReview:
			return &Instruction{
				Agent:  "editor",
				Task:   fmt.Sprintf("审阅前 %d 章：调用 novel_context(chapter=%d)，save_review 使用 scope=global、chapter=%d", refresh.EndChapter, refresh.EndChapter, refresh.EndChapter),
				Reason: "全局审阅缺失",
			}
		}
	}

	if s.ImmediateFeedbackCount > 0 {
		return &Instruction{
			Agent:  plannerForTier(s.PlanningTier),
			Task:   "仅处理 novel_context 中的外部修订 writer_feedback：核对已发生剧情与后续计划，需要调整时调用 revise_outline 或相应结构工具，无需调整时调用 resolve_outline_feedback；不得处理 foundation_status 或其它规划，落盘后用一句话结束",
			Reason: fmt.Sprintf("有 %d 条外部修订影响尚未传播到后续规划", s.ImmediateFeedbackCount),
		}
	}

	// 8. 分层模式的弧末后处理
	if p.Layered && s.ArcBoundary != nil && s.ArcBoundary.IsArcEnd {
		b := s.ArcBoundary
		switch {
		case !s.HasArcReview:
			return &Instruction{
				Agent: "editor",
				Task: fmt.Sprintf(
					"对第 %d 卷第 %d 弧（第 %d-%d 章）做弧级评审：调用 novel_context(chapter=%d)，save_review 使用 scope=arc、chapter=%d；issues[].chapters 只能落在该区间",
					b.Volume, b.Arc, b.StartChapter, b.EndChapter, b.EndChapter, b.EndChapter,
				),
				Reason: "弧末评审未完成",
			}
		case !s.HasArcSummary:
			return &Instruction{
				Agent:  "editor",
				Task:   fmt.Sprintf("生成第 %d 卷第 %d 弧摘要、角色快照与写作规则（save_arc_summary）", b.Volume, b.Arc),
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
				Task:   fmt.Sprintf("展开第 %d 卷第 %d 弧（expand_next_arc）", b.NextVolume, b.NextArc),
				Reason: "下一弧骨架待展开",
			}
		case b.NeedsNewVolume:
			return &Instruction{
				Agent:  "architect_long",
				Task:   "创建下一卷：按完结判定清单评估后调用 save_foundation——故事继续 → type=append_volume；故事接近终点 → type=append_volume 且卷 JSON 顶层带 \"final\": true（收官卷，整卷收线，写完自动完结）；全部完结条件当下已满足 → type=complete_book。三选一均须附 reason 参数写明判定理由",
				Reason: "卷末需决定追加新卷、收官卷或结束全书",
			}
		}
	}

	// 11. 非分层全局审阅：每 ReviewInterval 章一次(事实:该章的 global review 未落盘)。
	//     原为 commit_chapter 返回值里的 review_required 信号,现按事实推导——
	//     返回值只是事实的镜像,Route 从 store 直接看同一事实。
	if !p.Layered && s.LastCompleted > 0 {
		if due, reason := domain.ShouldReview(len(p.CompletedChapters)); due && !s.HasGlobalReview {
			return &Instruction{
				Agent:  "editor",
				Task:   fmt.Sprintf("对前 %d 章做全局审阅（save_review scope=global, chapter=%d）", s.LastCompleted, s.LastCompleted),
				Reason: reason,
			}
		}
	}

	// 12. 非分层大纲耗尽时不能继续派发越界章节。让 Architect 基于当前故事事实
	// 决定完结，或用 revise_outline 从 next 章续接计划。
	next := p.NextChapter()
	if next <= 0 {
		return nil
	}
	if !p.Layered && p.TotalChapters > 0 && next > p.TotalChapters {
		return &Instruction{
			Agent: plannerForTier(s.PlanningTier),
			Task: fmt.Sprintf(
				"非分层大纲已写完（已完成 %d 章，共 %d 章）：若故事已收束，调用 save_foundation(type=complete_book)；若仍需继续，用 revise_outline 从第 %d 章续接后续计划",
				len(p.CompletedChapters), p.TotalChapters, next,
			),
			Reason: "非分层大纲已耗尽，需决定完结或续接",
		}
	}

	// 13. 正常续写
	return &Instruction{
		Agent:   "writer",
		Task:    fmt.Sprintf("写第 %d 章", next),
		Reason:  "续写下一章",
		Chapter: next,
	}
}
