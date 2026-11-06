package arbiter

import (
	"context"
	"fmt"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/utils"
)

// InterventionFacts 干预分诊的事实包(Collect 时刻快照)。
// Engine 在边界执行 Dispatch 前用 Phase/QueueHead 做对账(咨询与执行之间隔着
// worker 运行,事实可能已推进;不符 → 丢弃并以新事实重询)。
type InterventionFacts struct {
	Phase                    string           `json:"phase,omitempty"`
	Flow                     string           `json:"flow,omitempty"`
	Title                    string           `json:"title,omitempty"`
	CompletedChapters        int              `json:"completed_chapters"`
	OutlinedChapters         int              `json:"outlined_chapters,omitempty"`
	DynamicPlanning          bool             `json:"dynamic_planning"`
	NextChapter              int              `json:"next_chapter,omitempty"`
	PendingRewrites          []int            `json:"pending_rewrites,omitempty"`
	ReopenCount              int              `json:"reopen_count,omitempty"` // 用户显式 /reopen 重开完结书的累计次数
	FoundationMissing        []string         `json:"foundation_missing,omitempty"`
	PlanningTier             string           `json:"planning_tier,omitempty"`
	AdvanceMode              string           `json:"advance_mode,omitempty"`
	HasAdvanceHold           bool             `json:"has_advance_hold"`
	AdvanceHoldAfter         string           `json:"advance_hold_after,omitempty"`
	AdvanceHoldTargetChapter int              `json:"advance_hold_target_chapter,omitempty"`
	AdvanceHoldReason        string           `json:"advance_hold_reason,omitempty"`
	Running                  bool             `json:"running"`                  // 干预到达时是否有 run 在进行
	CheckpointSeq            int64            `json:"checkpoint_seq,omitempty"` // Collect 时刻最新 checkpoint;Engine 对账用
	RecentDecisions          []RecentDecision `json:"recent_decisions,omitempty"`
}

// RecentDecision 是干预记忆:最近几次裁定的摘要,覆盖"上次改的怎么样了"类跨干预引用。
type RecentDecision struct {
	At     string `json:"at"`
	Input  string `json:"input"`
	Reason string `json:"reason,omitempty"`
}

// QueueHead 返回重写队列头(无则 0),Engine 对账用。
func (f InterventionFacts) QueueHead() int {
	if len(f.PendingRewrites) > 0 {
		return f.PendingRewrites[0]
	}
	return 0
}

// CollectInterventionFacts 从 store 读齐分诊事实。任何控制事实读取失败都显式
// 返回错误，禁止 Arbiter 在零值拼成的不完整快照上做语义决策。
func CollectInterventionFacts(st *storepkg.Store) (InterventionFacts, error) {
	var f InterventionFacts
	if st == nil {
		return f, fmt.Errorf("store 不能为空")
	}
	missing, err := st.FoundationMissing()
	if err != nil {
		return f, fmt.Errorf("读取基础设定状态: %w", err)
	}
	f.FoundationMissing = missing
	book, err := st.Book.Load()
	if err != nil {
		return f, fmt.Errorf("读取作品信息: %w", err)
	}
	if book != nil {
		f.Title = book.Title
	}
	p, err := st.Progress.Load()
	if err != nil {
		return f, fmt.Errorf("读取进度: %w", err)
	}
	if p != nil {
		f.Phase = string(p.Phase)
		f.Flow = string(p.Flow)
		f.CompletedChapters = len(p.CompletedChapters)
		f.DynamicPlanning = p.Layered
		if p.Layered {
			outline, outlineErr := st.Outline.LoadOutline()
			if outlineErr != nil {
				return f, fmt.Errorf("读取当前详细大纲: %w", outlineErr)
			}
			f.OutlinedChapters = len(outline)
		} else {
			f.OutlinedChapters = p.TotalChapters
		}
		f.NextChapter = p.NextChapter()
		f.PendingRewrites = append([]int(nil), p.PendingRewrites...)
		f.ReopenCount = p.ReopenCount
	}
	meta, err := st.RunMeta.Load()
	if err != nil {
		return f, fmt.Errorf("读取运行元信息: %w", err)
	}
	if meta != nil {
		f.PlanningTier = string(meta.PlanningTier)
		f.AdvanceMode = string(meta.AdvanceMode)
		if meta.AdvanceHold != nil {
			f.HasAdvanceHold = true
			f.AdvanceHoldAfter = string(meta.AdvanceHold.After)
			f.AdvanceHoldTargetChapter = meta.AdvanceHold.TargetChapter
			f.AdvanceHoldReason = meta.AdvanceHold.Reason
		}
	}
	if cp := st.Checkpoints.LatestGlobal(); cp != nil {
		f.CheckpointSeq = cp.Seq
	}
	recent, err := st.Decisions.Recent(5)
	if err != nil {
		return f, fmt.Errorf("读取近期裁定: %w", err)
	}
	for _, r := range recent {
		if r.Kind != "intervention" {
			continue
		}
		f.RecentDecisions = append(f.RecentDecisions, RecentDecision{
			At: r.At, Input: utils.TruncateRunes(r.Input, 80), Reason: r.Reason,
		})
	}
	return f, nil
}

// AdvanceHoldOp 一次性暂停动作：在工作边界、返工排空或目标章节完成后暂停，也可取消。
type AdvanceHoldOp struct {
	Cancel        bool                    `json:"cancel,omitempty"`
	After         domain.AdvanceHoldAfter `json:"after,omitempty"`
	TargetChapter int                     `json:"target_chapter,omitempty"`
	Reason        string                  `json:"reason,omitempty"`
}

// ReopenOp 完本返工:把全书重开进返工态并把目标章入队(仅 phase=complete 合法)。
type ReopenOp struct {
	Chapters []int  `json:"chapters"`
	Reason   string `json:"reason,omitempty"`
}

// InterventionDecision 干预裁定。动作组合自由,执行顺序由 Engine 固定:
// answer → rules → hold → reopen → dispatch;至多一个 dispatch(类型事实)。
type InterventionDecision struct {
	Answer   string         `json:"answer,omitempty"`
	Rules    string         `json:"rules,omitempty"`
	Hold     *AdvanceHoldOp `json:"hold,omitempty"`
	Reopen   *ReopenOp      `json:"reopen,omitempty"`
	Dispatch *DispatchOp    `json:"dispatch,omitempty"`
	Reason   string         `json:"reason"`
}

var interventionContract = llmcontract.Contract{
	Name:        "arbiter_intervention",
	Description: "用户干预裁定：回答、规则、暂停、重开与派单",
	Schema: schema.Object(
		schema.Property("answer", llmcontract.Nullable(schema.String("回显给用户的文字；无则为 null"))).Required(),
		schema.Property("rules", llmcontract.Nullable(schema.String("要落盘的长效写作规则原文；无则为 null"))).Required(),
		schema.Property("hold", llmcontract.Nullable(schema.Object(
			schema.Property("cancel", schema.Bool("是否取消既有一次性暂停")).Required(),
			schema.Property("after", llmcontract.Nullable(schema.Enum("暂停触发点；取消时为 null", string(domain.AdvanceHoldAtBoundary), string(domain.AdvanceHoldAfterRewritesDrained), string(domain.AdvanceHoldAtChapter)))).Required(),
			schema.Property("target_chapter", llmcontract.Nullable(schema.Int("after=chapter 时的目标章节；其他情况为 null"))).Required(),
			schema.Property("reason", llmcontract.Nullable(schema.String("用户诉求摘要；取消时可为 null"))).Required(),
		))).Required(),
		schema.Property("reopen", llmcontract.Nullable(schema.Object(
			schema.Property("chapters", schema.Array("需要重开的章节号", schema.Int("章节号"))).Required(),
			schema.Property("reason", llmcontract.Nullable(schema.String("重开理由"))).Required(),
		))).Required(),
		schema.Property("dispatch", dispatchSchema("派单目标；无需派单时为 null")).Required(),
		schema.Property("reason", schema.String("一句话裁定理由")).Required(),
	),
}

// ValidateAgainst 按事实做机械校验(场景内合法性;类型已排除跨场景动作)。
func (d *InterventionDecision) ValidateAgainst(f InterventionFacts) error {
	if strings.TrimSpace(d.Reason) == "" {
		return fmt.Errorf("reason 不能为空")
	}
	if d.Answer == "" && d.Rules == "" && d.Hold == nil && d.Reopen == nil && d.Dispatch == nil {
		return fmt.Errorf("空决策：至少要有一个动作或 answer")
	}
	if err := d.Dispatch.validate(); err != nil {
		return err
	}
	if err := validateDispatchAgainst(d.Dispatch, f.Phase); err != nil {
		return err
	}
	complete := f.Phase == string(domain.PhaseComplete)
	if d.Reopen != nil {
		if !complete {
			return fmt.Errorf("reopen 仅限完本期（当前 phase=%s）", f.Phase)
		}
		if len(d.Reopen.Chapters) == 0 {
			return fmt.Errorf("reopen.chapters 不能为空")
		}
		for _, ch := range d.Reopen.Chapters {
			if ch < 1 || ch > f.CompletedChapters {
				return fmt.Errorf("reopen 章节 %d 越界（已完成 %d 章）", ch, f.CompletedChapters)
			}
		}
	}
	if complete && d.Dispatch != nil {
		return fmt.Errorf("完本期禁止直接派单；返工用 reopen（入队后由 Router 自动派发）")
	}
	if d.Hold != nil && !d.Hold.Cancel {
		if f.Phase != string(domain.PhaseWriting) {
			return fmt.Errorf("一次性暂停仅限写作期（当前 phase=%s）", f.Phase)
		}
		hold := domain.AdvanceHold{After: d.Hold.After, TargetChapter: d.Hold.TargetChapter, Reason: d.Hold.Reason}
		if err := hold.Validate(); err != nil {
			return fmt.Errorf("hold 无效: %w", err)
		}
		nextChapter := f.NextChapter
		if nextChapter == 0 {
			nextChapter = f.CompletedChapters + 1
		}
		if hold.After == domain.AdvanceHoldAtChapter && hold.TargetChapter < nextChapter {
			return fmt.Errorf("目标章节 %d 早于当前下一章 %d", hold.TargetChapter, nextChapter)
		}
	}
	return nil
}

// validateDispatchAgainst 把提示词中的阶段纪律落实为机械防线。Architect 可在规划期
// 与写作期维护结构；Writer/Editor 只能消费已经完整且进入 writing 的作品事实。
func validateDispatchAgainst(dispatch *DispatchOp, phase string) error {
	if dispatch == nil {
		return nil
	}
	if phase == "" {
		return fmt.Errorf("缺少 phase，禁止执行派单")
	}
	if phase == string(domain.PhaseComplete) {
		return fmt.Errorf("完本期禁止直接派单")
	}
	switch dispatch.Agent {
	case "writer", "editor":
		if phase != string(domain.PhaseWriting) {
			return fmt.Errorf("%s 仅能在 writing 阶段派发（当前 phase=%s）", dispatch.Agent, phase)
		}
	}
	return nil
}

// DecideIntervention 干预分诊。失败语义:返回 error → 调用方显式回显
// 真实失败原因,且不产生任何写入(宁可不动,不可误动)。
func DecideIntervention(ctx context.Context, model llmcontract.Model, systemPrompt string, facts InterventionFacts, text string) (InterventionDecision, error) {
	payload, err := marshalPayload(struct {
		Intervention string            `json:"intervention"`
		Facts        InterventionFacts `json:"facts"`
	}{Intervention: text, Facts: facts})
	if err != nil {
		return InterventionDecision{}, err
	}
	return decide(ctx, model, interventionContract, systemPrompt, payload, func(d *InterventionDecision) error {
		return d.ValidateAgainst(facts)
	})
}
