package arbiter

import (
	"context"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// InterventionFacts 干预分诊的事实包(Collect 时刻快照)。
// Engine 在边界执行 Dispatch 前用 Phase/QueueHead 做对账(咨询与执行之间隔着
// worker 运行,事实可能已推进;不符 → 丢弃并以新事实重询)。
type InterventionFacts struct {
	Phase             string           `json:"phase,omitempty"`
	Flow              string           `json:"flow,omitempty"`
	NovelName         string           `json:"novel_name,omitempty"`
	CompletedChapters int              `json:"completed_chapters"`
	TotalChapters     int              `json:"total_chapters,omitempty"`
	NextChapter       int              `json:"next_chapter,omitempty"`
	PendingRewrites   []int            `json:"pending_rewrites,omitempty"`
	ReopenCount       int              `json:"reopen_count,omitempty"` // 用户显式 /reopen 重开完结书的累计次数
	FoundationMissing []string         `json:"foundation_missing,omitempty"`
	PlanningTier      string           `json:"planning_tier,omitempty"`
	AdvanceMode       string           `json:"advance_mode,omitempty"`
	HasAdvanceHold    bool             `json:"has_advance_hold"`
	AdvanceHoldAfter  string           `json:"advance_hold_after,omitempty"`
	AdvanceHoldReason string           `json:"advance_hold_reason,omitempty"`
	Running           bool             `json:"running"`                  // 干预到达时是否有 run 在进行
	CheckpointSeq     int64            `json:"checkpoint_seq,omitempty"` // Collect 时刻最新 checkpoint;Engine 对账用
	RecentDecisions   []RecentDecision `json:"recent_decisions,omitempty"`
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
	p, err := st.Progress.Load()
	if err != nil {
		return f, fmt.Errorf("读取进度: %w", err)
	}
	if p != nil {
		f.Phase = string(p.Phase)
		f.Flow = string(p.Flow)
		f.NovelName = p.NovelName
		f.CompletedChapters = len(p.CompletedChapters)
		f.TotalChapters = p.TotalChapters
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
			At: r.At, Input: truncateRunes(r.Input, 80), Reason: r.Reason,
		})
	}
	return f, nil
}

// AdvanceHoldOp 一次性暂停动作：在 Worker 边界或返工排空后暂停，也可取消。
type AdvanceHoldOp struct {
	Cancel bool                    `json:"cancel,omitempty"`
	After  domain.AdvanceHoldAfter `json:"after,omitempty"`
	Reason string                  `json:"reason,omitempty"`
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
		if !d.Hold.After.Valid() {
			return fmt.Errorf("hold.after 必须是 boundary 或 rewrites_drained")
		}
		if strings.TrimSpace(d.Hold.Reason) == "" {
			return fmt.Errorf("设置一次性暂停必须带 reason（用户诉求摘要）")
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
func DecideIntervention(ctx context.Context, model agentcore.ChatModel, systemPrompt string, facts InterventionFacts, text string) (InterventionDecision, error) {
	payload := marshalPayload(struct {
		Intervention string            `json:"intervention"`
		Facts        InterventionFacts `json:"facts"`
	}{Intervention: text, Facts: facts})
	return decide(ctx, model, systemPrompt, payload, func(d *InterventionDecision) error {
		return d.ValidateAgainst(facts)
	})
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
