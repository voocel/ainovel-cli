package arbiter

import (
	"context"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
	"github.com/voocel/ainovel-cli/internal/skills"
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
	AvailableSkills   []SkillInfo      `json:"available_skills,omitempty"`
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

// SkillInfo 是可用专项技能在事实包里的投影:只给判断是否命中所需的最少信息
// (正文不进事实包——那是执行期才需要的几百字,进裁定 payload 纯属浪费)。
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Agent       string `json:"agent"`
	Scope       string `json:"scope"`
}

// skillInfos 把 catalog 投影成事实包形态(字典序,与 Names 一致)。
func skillInfos(catalog skills.Catalog) []SkillInfo {
	list := catalog.List()
	if len(list) == 0 {
		return nil
	}
	out := make([]SkillInfo, 0, len(list))
	for _, sk := range list {
		out = append(out, SkillInfo{
			Name: sk.Name, Description: sk.Description,
			Agent: sk.Agent, Scope: string(sk.Scope),
		})
	}
	return out
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
//
// catalog 是当前生效的专项技能目录(零值 = 无可用技能,skill 位不进 schema)。
// 它不是 store 事实,由调用方在启动时加载一次并持有——技能是静态资产,
// 每次干预重扫目录只会在用户改文件时引入不确定性。
func CollectInterventionFacts(st *storepkg.Store, catalog skills.Catalog) (InterventionFacts, error) {
	var f InterventionFacts
	if st == nil {
		return f, fmt.Errorf("store 不能为空")
	}
	f.AvailableSkills = skillInfos(catalog)
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

// SkillOp 给本次派单挂一份专项技能(去 AI 味、收紧节奏等)。
//
// 它只提供**处理方法**,不放宽授权:修改范围仍由用户原文决定。Chapters 为空时
// 由 Engine 按 skill 声明的 scope 确定性推导目标,绝不由裁定层猜测扩大。
//
// 一次性语义:技能正文拼进本次 task,跑完即止,不落任何持久状态——想长效生效
// 走 Rules 通道(两条路径职责不重叠,见 internal/skills 包注释)。
type SkillOp struct {
	Name     string `json:"name"`
	Chapters []int  `json:"chapters,omitempty"`
}

// InterventionDecision 干预裁定。动作组合自由,执行顺序由 Engine 固定:
// answer → rules → hold → reopen → skill+dispatch;至多一个 dispatch(类型事实)。
type InterventionDecision struct {
	Answer   string         `json:"answer,omitempty"`
	Rules    string         `json:"rules,omitempty"`
	Hold     *AdvanceHoldOp `json:"hold,omitempty"`
	Reopen   *ReopenOp      `json:"reopen,omitempty"`
	Dispatch *DispatchOp    `json:"dispatch,omitempty"`
	Skill    *SkillOp       `json:"skill,omitempty"`
	Reason   string         `json:"reason"`
}

// interventionContract 按可用技能动态构造。
//
// 必须动态:技能名单来自用户目录,不是编译期常量。fingerprint 稳定性由
// Catalog.Names() 的字典序保证——同一技能集合恒产出同一 schema。
//
// 名单为空时**完全不生成 skill 属性**:strict 模式下空 enum 会被 provider 拒绝,
// 且"没有技能可选"本就该在类型上不可表达,而不是给个永远填不了的位。
func interventionContract(available []SkillInfo) llmcontract.Contract {
	props := []schema.Prop{
		schema.Property("answer", llmcontract.Nullable(schema.String("回显给用户的文字；无则为 null"))).Required(),
		schema.Property("rules", llmcontract.Nullable(schema.String("要落盘的长效写作规则原文；无则为 null"))).Required(),
		schema.Property("hold", llmcontract.Nullable(schema.Object(
			schema.Property("cancel", schema.Bool("是否取消既有一次性暂停")).Required(),
			schema.Property("after", llmcontract.Nullable(schema.Enum("暂停触发点；取消时为 null", string(domain.AdvanceHoldAtBoundary), string(domain.AdvanceHoldAfterRewritesDrained)))).Required(),
			schema.Property("reason", llmcontract.Nullable(schema.String("用户诉求摘要；取消时可为 null"))).Required(),
		))).Required(),
		schema.Property("reopen", llmcontract.Nullable(schema.Object(
			schema.Property("chapters", schema.Array("需要重开的章节号", schema.Int("章节号"))).Required(),
			schema.Property("reason", llmcontract.Nullable(schema.String("重开理由"))).Required(),
		))).Required(),
		schema.Property("dispatch", dispatchSchema("派单目标；无需派单时为 null")).Required(),
	}
	if names := skillNames(available); len(names) > 0 {
		props = append(props, schema.Property("skill", llmcontract.Nullable(schema.Object(
			schema.Property("name", schema.Enum("专项技能名；必须取自 facts.available_skills", names...)).Required(),
			schema.Property("chapters", llmcontract.Nullable(schema.Array("技能作用的章节号；留空(null)由系统按技能范围推导", schema.Int("章节号")))).Required(),
		))).Required())
	}
	props = append(props, schema.Property("reason", schema.String("一句话裁定理由")).Required())

	return llmcontract.Contract{
		Name:        "arbiter_intervention",
		Description: "用户干预裁定：回答、规则、暂停、重开、专项技能与派单",
		Schema:      schema.Object(props...),
	}
}

// skillNames 抽出技能名(已按 Catalog.List 的字典序)。
func skillNames(available []SkillInfo) []string {
	out := make([]string, 0, len(available))
	for _, s := range available {
		out = append(out, s.Name)
	}
	return out
}

// findSkill 在事实包里查技能;未找到即裁定引用了不存在的技能。
func findSkill(available []SkillInfo, name string) (SkillInfo, bool) {
	for _, s := range available {
		if s.Name == name {
			return s, true
		}
	}
	return SkillInfo{}, false
}

// ValidateAgainst 按事实做机械校验(场景内合法性;类型已排除跨场景动作)。
func (d *InterventionDecision) ValidateAgainst(f InterventionFacts) error {
	if strings.TrimSpace(d.Reason) == "" {
		return fmt.Errorf("reason 不能为空")
	}
	if d.Answer == "" && d.Rules == "" && d.Hold == nil && d.Reopen == nil && d.Dispatch == nil && d.Skill == nil {
		return fmt.Errorf("空决策：至少要有一个动作或 answer")
	}
	if err := d.Dispatch.validate(); err != nil {
		return err
	}
	if err := validateDispatchAgainst(d.Dispatch, f.Phase); err != nil {
		return err
	}
	if err := validateSkillAgainst(d.Skill, d.Dispatch, f); err != nil {
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
	if complete && d.Skill != nil && d.Reopen == nil {
		return fmt.Errorf("完本期挂技能必须同时 reopen 目标章节（重开后由 Router 自动派发）")
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

// validateSkillAgainst 机械校验专项技能:技能存在、执行者不冲突、范围与 scope 自洽。
//
// 与其他动作同一纪律——Arbiter 输出不可信,类型只能排除跨场景动作,场景内合法性
// 必须在这里拒死。技能校验尤其重要:它是唯一"名字来自用户文件"的动作,
// 模型幻想一个不存在的技能名在执行期会直接组装不出任务。
func validateSkillAgainst(skill *SkillOp, dispatch *DispatchOp, f InterventionFacts) error {
	if skill == nil {
		return nil
	}
	info, ok := findSkill(f.AvailableSkills, strings.TrimSpace(skill.Name))
	if !ok {
		if len(f.AvailableSkills) == 0 {
			return fmt.Errorf("当前没有可用专项技能，不得填 skill")
		}
		return fmt.Errorf("skill.name 非法: %q（可选 %s）", skill.Name,
			strings.Join(skillNames(f.AvailableSkills), " / "))
	}
	// 执行者冲突必须拒:技能正文是写给特定角色的执行标准(去 AI 味写给 editor),
	// 挂到 architect 上会让 Worker 收到一份它没有工具去执行的指令。
	if dispatch != nil && dispatch.Agent != info.Agent {
		return fmt.Errorf("技能 %s 需由 %s 执行，与 dispatch.agent=%s 冲突",
			info.Name, info.Agent, dispatch.Agent)
	}
	switch info.Scope {
	case "chapters":
		for _, ch := range skill.Chapters {
			if ch < 1 || ch > f.CompletedChapters {
				return fmt.Errorf("技能 %s 的章节 %d 越界（已完成 %d 章）", info.Name, ch, f.CompletedChapters)
			}
		}
	default:
		// forward / foundation 不作用于具体已写章节,带章节号即语义矛盾。
		if len(skill.Chapters) > 0 {
			return fmt.Errorf("技能 %s 的范围是 %s，不接受 chapters", info.Name, info.Scope)
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
	payload, err := marshalPayload(struct {
		Intervention string            `json:"intervention"`
		Facts        InterventionFacts `json:"facts"`
	}{Intervention: text, Facts: facts})
	if err != nil {
		return InterventionDecision{}, err
	}
	// 契约随事实包里的技能名单构造:enum 与校验共用同一份名单,不可能漂移。
	return decide(ctx, model, interventionContract(facts.AvailableSkills), systemPrompt, payload, func(d *InterventionDecision) error {
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
