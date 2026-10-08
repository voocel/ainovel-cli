package novel

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/creation"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/domain/narrative"
)

// 小说目标的推导规则（应用编排层，D49）：什么算完成、下一步写什么、审阅何时发生、
// 修订预算怎么用，全部在这里；内核只驱动它给出的步骤，不认识任何小说概念。

type Policy struct{}

func (Policy) ValidateGoal(payload json.RawMessage) error {
	_, err := model.DecodeNovelGoal(model.CreationRunGoal{Kind: model.GoalNovel, Payload: payload})
	return err
}

type workKind int

const (
	workDevelopPlan workKind = iota
	// workExtendPlan 是滚动规划的扩窗（§6.3）：蓝图已有章节但未覆盖目标，
	// 走 revise_plan 增量展开，而不是重跑整体规划。
	workExtendPlan
	workWriteChapter
	workReview
	workRewrite
	// workVerifyCanon 是事实核验（§4.4 D41 / §4.5）：正文改动后待核验或未入账的章节，
	// 先核验事实再写、审后续内容。
	workVerifyCanon
)

type novelItem struct {
	kind      workKind
	covered   int                   // extend：已覆盖章节数
	number    int                   // chapter/rewrite：章节序号
	plan      model.PlanNode        // chapter/rewrite：章节计划
	chapterID string                // rewrite/canon：目标正文章节
	facts     []string              // canon：待核验的事实 ID；空表示该章未入账
	chapters  []string              // review：窗口内正文章节
	span      [2]int                // review：窗口的首末章号
	reviewed  []string              // review：上一轮已审且正文未变、只作上下文的章（D68）
	prior     []model.ReviewFinding // review：焦点章上一轮的阻塞意见
	notes     []string              // rewrite：阻塞意见；extend：上一窗口的审阅意见
	revision  model.Revision        // review：绑定的 Revision；rewrite：所依据裁定的 Revision（槽位 ID）
	// requirements 是窗口要逐项核验的要求（D62）；pending 是扩窗时仍待兑现的要求原文。
	requirements []model.Requirement
	pending      []string
	// concluded：extend 时已覆盖的末章是作为全书结局写成的，这次扩窗是续写（D63）。
	concluded bool
	// directives 是命中本任务作用域的用户要求（§4.9），随任务输入进入指令平面。
	directives []model.Directive
	// basis 是任务基线（D48/D51）：review 为裁定将继承的证据基线，write/rewrite 为本章要求
	// 作用域，extend 为所依据的窗口裁定基线。
	basis model.EvidenceBasis
}

// Next 的顺序即小说的推进规则：蓝图超出固定篇幅先停下 → 事实缺口先核验（D41）→ 审阅
// 闸门（D62：写满一个窗口先审、先审后扩，阻塞发现按预算重写，到期要求必须兑现）→ 完成
// 契约（D29/§6.4）→ 写章或扩窗（D63：全书章数未知或蓝图未到末章时扩窗）。
func (Policy) Next(project projectdoc.Snapshot, run model.CreationRun, evidence Evidence) (creation.Step, error) {
	goal, err := model.DecodeNovelGoal(run.Goal)
	if err != nil {
		return creation.Step{}, err
	}
	length := lengthOf(project, goal.TargetChapters)
	if planCount := len(model.ChapterPlansInOrder(project.Plan)); length.Fixed > 0 && planCount > length.Fixed {
		return creation.Step{Wait: fmt.Sprintf(
			"蓝图包含 %d 个有效章节节点，但篇幅固定为 %d 章；请先裁剪蓝图或调整篇幅",
			planCount, length.Fixed,
		)}, nil
	}
	var item novelItem
	if gaps := CanonGaps(project); len(gaps) > 0 {
		// 事实缺口优先（§4.4 D41 / §4.5）：待核验事实与未入账章节先核验，
		// 旧事实不得继续指导后续创作与审阅。
		gap := gaps[0]
		item = novelItem{kind: workVerifyCanon, chapterID: gap.ChapterID, number: gap.Number, facts: gap.Pending, revision: gap.Revision}
	} else {
		// 完成契约（§6.4）：内容齐全不等于完成，全书每章都要有基线仍成立的通过裁定、
		// 每项要求都已兑现；相干文档或要求再变化时旧证据随基线自动失效。
		unmet := completionUnmet(project, length)
		ledger := newReviewLedger(project, length.Final, evidence.Verdicts).withPrior(evidence.Prior)
		through := length.Final
		if unmet != "" {
			var ok bool
			if item, ok = nextNovelItem(project, length); !ok {
				return creation.Step{}, fmt.Errorf("creation run %q has unmet goal but no work item: %w", run.ID, model.ErrInvalid)
			}
			through = ledger.reviewedThrough(item)
		}
		// AI 定的收官不得让用户要求落空（D63）：固定篇幅是用户自己划的边界，AI 的收官
		// 承诺不是——作用域落在全书之外的要求交给用户裁决，而不是静默跳过后完成。
		if length.Fixed == 0 && len(ledger.dropped) > 0 {
			directive := ledger.dropped[0]
			return creation.Step{Wait: fmt.Sprintf(
				"AI 承诺全书 %d 章收官，要求「%s」（作用域：%s）因此落空；请退役或调整这条要求，或固定更长的篇幅",
				length.Final, directive.Text, storyOf(project).DescribeScope(directive),
			)}, nil
		}
		canRepair := func(chapterID string) bool { return evidence.Repairs[chapterID] < run.Strategy.AutoRepairBudget }
		step := ledger.gate(through, canRepair)
		switch {
		case step.blocked != nil:
			item = rewriteItem(project, *step.blocked, step.chapter)
			if item.chapterID == "" {
				return creation.Step{Fail: "审阅发现指向了不存在的章节，需要人工检查"}, nil
			}
			if !canRepair(item.chapterID) {
				return creation.Step{Wait: fmt.Sprintf(
					"第 %d 章《%s》的自动修订预算（每章 %d 次）已用尽，需要你查看审阅意见后决断",
					item.number, item.plan.Title, run.Strategy.AutoRepairBudget,
				)}, nil
			}
		case step.review != nil:
			basis, err := ReviewBasis(project, step.review)
			if err != nil {
				return creation.Step{}, err
			}
			item = novelItem{
				kind: workReview, chapters: step.review, reviewed: step.reviewed, prior: step.prior,
				requirements: ledger.window(step.review), revision: project.Revision, basis: basis,
				span: [2]int{ledger.numbers[step.review[0]], ledger.numbers[step.review[len(step.review)-1]]},
			}
		case unmet == "":
			return creation.Step{Done: fmt.Sprintf("全书 %d 章完成并通过审阅", length.Final)}, nil
		case item.kind == workExtendPlan:
			// 先审后扩（§6.3）：窗口意见与待兑现要求随扩窗输入进入下一段规划；扩窗以
			// 覆盖末章的裁定为基线（D51），裁定失效则扩窗任务失效。
			last := ledger.current[ledger.chapters[item.covered-1].ID]
			item.notes, item.basis = ledger.notes(item.covered), last.Verdict.Basis
			item.pending, item.concluded = ledger.pending(item.covered), ledger.concluded(item.covered)
		}
	}
	item.directives = coveringDirectives(project, item)
	work, err := item.work(run, goal.Premise, length)
	if err != nil {
		return creation.Step{}, err
	}
	return creation.Step{Work: &work}, nil
}

// reviewedThrough 是开始本条目前必须审过的章数（D62/D67）：写第 n 章前审完已封口的
// 窗口（蓝图一次铺满也按容量分窗审），扩窗前审完已覆盖章节。
func (l reviewLedger) reviewedThrough(item novelItem) int {
	switch item.kind {
	case workWriteChapter:
		return l.settledThrough(item.number - 1)
	case workExtendPlan:
		return item.covered
	default:
		return 0
	}
}

// completionUnmet 是完成契约的最小落点（D29/D63）：全书章数已知（用户固定或收官承诺），
// 在同一 Revision 上蓝图恰好覆盖它且每章有正文。队列为空不等于完成。返回空串表示满足。
func completionUnmet(project projectdoc.Snapshot, length Length) string {
	if length.Final == 0 {
		return "全书尚未收官"
	}
	plans := model.ChapterPlansInOrder(project.Plan)
	if len(plans) != length.Final {
		return fmt.Sprintf("蓝图有 %d 章，全书 %d 章", len(plans), length.Final)
	}
	written := manuscriptsByPlanNode(project.Manuscript)
	for index, plan := range plans {
		if _, ok := written[plan.ID]; !ok {
			return fmt.Sprintf("第 %d 章《%s》还没有正文", index+1, plan.Title)
		}
	}
	return ""
}

func nextNovelItem(project projectdoc.Snapshot, length Length) (novelItem, bool) {
	plans := model.ChapterPlansInOrder(project.Plan)
	if len(plans) == 0 {
		return novelItem{kind: workDevelopPlan}, true
	}
	// 先写满当前窗口再扩窗（§6.3）：窗口审阅意见要能流进下一段规划。
	covered := len(plans)
	if length.Final > 0 {
		covered = min(covered, length.Final)
	}
	written := manuscriptsByPlanNode(project.Manuscript)
	for index, plan := range plans[:covered] {
		if _, ok := written[plan.ID]; !ok {
			return novelItem{kind: workWriteChapter, number: index + 1, plan: plan, basis: chapterBasis(project, index+1, plan.ID)}, true
		}
	}
	if length.Final == 0 || covered < length.Final {
		return novelItem{kind: workExtendPlan, covered: covered}, true
	}
	return novelItem{}, false
}

// rewriteItem 按裁定重写 chapterID（为空取第一个阻塞章）：只带该章的阻塞意见——参考
// 意见不值得一次返工，带上只会扩大改动面（D68）。
func rewriteItem(project projectdoc.Snapshot, verdict model.ReviewVerdict, chapterID string) novelItem {
	byID := manuscriptsByID(project.Manuscript)
	planByID := make(map[string]model.PlanNode, len(project.Plan))
	for _, node := range project.Plan {
		planByID[node.ID] = node
	}
	for _, id := range verdict.BlockingChapters() {
		if chapterID != "" && id != chapterID {
			continue
		}
		chapter, ok := byID[id]
		if !ok {
			continue
		}
		plan, ok := planByID[chapter.PlanNodeID]
		if !ok {
			continue
		}
		var notes []string
		for _, finding := range verdict.Findings {
			if finding.ChapterID == id && finding.Severity == model.FindingBlocking {
				notes = append(notes, finding.Note)
			}
		}
		return novelItem{
			kind: workRewrite, chapterID: id, number: chapter.Number,
			plan: plan, notes: notes, revision: verdict.Revision, basis: chapterBasis(project, chapter.Number, plan.ID),
		}
	}
	return novelItem{}
}

// chapterBasis 是章节任务的基线（D51）：只钉住本章命中的要求作用域。
func chapterBasis(project projectdoc.Snapshot, number int, planID string) model.EvidenceBasis {
	return model.EvidenceBasis{Scopes: []model.ScopeBasis{directiveScope(project, directiveTarget(project, number, planID))}}
}

// ReviewBasis 是窗口审阅的证据基线（D62、D69）：只钉裁定所依据的输入——窗口与衔接章
// 的正文、窗口各章的计划节点（章在故事中的位置，要求作用域据此匹配）与 Intent，加每章
// 命中的要求作用域。实体、卷弧与 Canon 只是审阅上下文：它们随故事推进原地更新，钉住
// 会让一次别名追加废掉提到该角色的全部窗口；与正文的一致性归影响分析与事实核验（D41）。
func ReviewBasis(project projectdoc.Snapshot, chapters []string) (model.EvidenceBasis, error) {
	byID := manuscriptsByID(project.Manuscript)
	refs := []model.DocumentRef{{Kind: model.DocumentIntent, ID: model.SingletonDocumentID}}
	basis := model.EvidenceBasis{Scopes: make([]model.ScopeBasis, 0, len(chapters))}
	for _, id := range chapters {
		chapter, ok := byID[id]
		if !ok {
			return model.EvidenceBasis{}, fmt.Errorf("review chapter %q is not in the manuscript: %w", id, model.ErrInvalid)
		}
		refs = append(refs, manuscriptRef(id), model.DocumentRef{Kind: model.DocumentPlan, ID: chapter.PlanNodeID})
		basis.Scopes = append(basis.Scopes, directiveScope(project, directiveTarget(project, chapter.Number, chapter.PlanNodeID)))
	}
	if seam := seamChapter(project, chapters); seam != "" {
		refs = append(refs, manuscriptRef(seam))
	}
	for _, ref := range refs {
		entry, ok := project.Index[ref.Key()]
		if !ok {
			return model.EvidenceBasis{}, fmt.Errorf("basis document %s is absent at revision %d: %w", ref.Key(), project.Revision, model.ErrInvalid)
		}
		basis.Documents = append(basis.Documents, model.DocumentBasis{Ref: ref, Revision: entry.Revision})
	}
	return basis.Normalize(), nil
}

// seamChapter 是窗口之前最近的已写正文：衔接检查用，与 derive 装配的"上一章"同一口径。
func seamChapter(project projectdoc.Snapshot, chapters []string) string {
	written := manuscriptsByPlanNode(project.Manuscript)
	seam := ""
	for _, plan := range model.ChapterPlansInOrder(project.Plan) {
		chapter, ok := written[plan.ID]
		if !ok {
			continue
		}
		if slices.Contains(chapters, chapter.ID) {
			return seam
		}
		seam = chapter.ID
	}
	return ""
}

// verdictNotes 摘出裁定中全部发现的意见文本：pass 裁定的 note 级发现
// 正是下一段规划应当吸收的反馈。
func verdictNotes(verdict model.ReviewVerdict) []string {
	var notes []string
	for _, finding := range verdict.Findings {
		notes = append(notes, finding.Note)
	}
	return notes
}

// storyOf 是作品的故事索引：交给模型或用户的文字用章号、名称指称，不露文档 ID（D66）。
func storyOf(project projectdoc.Snapshot) *narrative.Story {
	return narrative.New(narrative.Content{Plan: project.Plan, Entities: project.Entities, Canon: project.Canon, Manuscript: project.Manuscript})
}

func manuscriptsByID(manuscript []model.ManuscriptChapter) map[string]model.ManuscriptChapter {
	byID := make(map[string]model.ManuscriptChapter, len(manuscript))
	for _, chapter := range manuscript {
		byID[chapter.ID] = chapter
	}
	return byID
}

func manuscriptsByPlanNode(manuscript []model.ManuscriptChapter) map[string]model.ManuscriptChapter {
	byPlan := make(map[string]model.ManuscriptChapter, len(manuscript))
	for _, chapter := range manuscript {
		byPlan[chapter.PlanNodeID] = chapter
	}
	return byPlan
}

// coveringDirectives 装配命中本任务的用户要求（§4.9）：写/重写按目标章命中，
// 规划面向未来取全部 active。它只做装配，不参与推导。
func coveringDirectives(project projectdoc.Snapshot, item novelItem) []model.Directive {
	switch item.kind {
	case workWriteChapter, workRewrite:
		return model.ActiveDirectivesFor(project.Directives, directiveTarget(project, item.number, item.plan.ID))
	case workReview, workVerifyCanon:
		// 审阅的要求以三态核验项投递（D62），事实核验不涉及要求。
		return nil
	default:
		return model.ActiveDirectives(project.Directives)
	}
}

// work 把推导出的条目装配成要驱动的 Operation：槽位 ID、种类、类型化输入与各落点文案。
// 规划任务的 ID 带上篇幅输入：篇幅或罗盘变化后是新任务，不复用旧输入（D63）。
func (item novelItem) work(run model.CreationRun, premise string, length Length) (creation.WorkItem, error) {
	work := creation.WorkItem{Reasons: item.reasons()}
	switch item.kind {
	case workDevelopPlan:
		work.ID, work.Kind = runQuickID(run.ID, "plan", length.inputID()), model.OperationDevelopPlan
		work.Input = model.DevelopPlanInput{
			Intent: premise, FixedChapters: length.Fixed,
			Goal: "设计可直接用于连续创作的卷、故事弧与章节大纲：卷与故事弧的骨架看得清多远就搭多远，这次只为第一个故事弧展开章节，这一弧多少章由你按故事走向决定，每章挂在所属故事弧下；" + length.planningGoal(),
		}
	case workExtendPlan:
		goal := "展开下一个故事弧的章节：已有章节保持不变，只在末尾追加并挂在所属故事弧下；下一弧已有骨架就按已发生的故事修订后展开，没有就新建（需要时新建卷），这一弧多少章由你按故事走向决定；结合审阅意见调整后续走向，为 pending_requirements 中尚未兑现的要求安排落点；" + length.planningGoal()
		parts := []string{"plan", "extend", strconv.Itoa(item.covered), length.inputID()}
		if item.concluded {
			goal += fmt.Sprintf("；第 %d 章已作为全书结局写成，这是续写：在这个结局之后开启新的篇章、接住已完成的故事，不重复收尾，并在 compass.ending 写下续写部分的终局方向", item.covered)
			parts = append(parts, "c")
		}
		work.ID = runQuickID(run.ID, parts...)
		work.Kind = model.OperationRevisePlan
		work.Input = model.RevisePlanInput{
			Intent: premise, FixedChapters: length.Fixed,
			ExistingChapters:    item.covered,
			ReviewNotes:         item.notes,
			PendingRequirements: item.pending,
			Basis:               item.basis,
			Goal:                goal,
		}
	case workWriteChapter:
		work.ID, work.Kind = runQuickID(run.ID, "chapter", item.plan.ID), model.OperationWriteChapter
		work.Input = model.WriteChapterInput{
			ChapterPlanID: item.plan.ID, ChapterNumber: item.number, Directives: item.directives, Basis: item.basis,
			Goal: "完成本章工作稿，连同本章的事实变化提交候选；严格满足 directives 列出的每条创作要求（用户原话），字数约束按 constraints 执行",
		}
	case workReview:
		goal := "审阅本窗口正文：检查与上一章的衔接、窗口内的连续性与 Intent 方向，产出结构化裁定与发现；上一章正文只用于衔接检查，不在审阅范围"
		if len(item.reviewed) > 0 {
			goal += "；reviewed 列出的章上一轮已审且正文未变，只作上下文：不再对它们记 blocking（违反 requirements 除外），其中的问题记为 note；改动章与它们之间的矛盾记在改动章上"
		}
		if len(item.prior) > 0 {
			goal += "；逐条核对 prior_findings 列出的上一轮阻塞问题是否已解决，并检查改动有没有引入新问题"
		}
		if len(item.requirements) > 0 {
			goal += "；逐项核验 requirements：已兑现为 satisfied，被违反为 violated 并用阻塞发现链接，仅凭本窗口正文还无法判断为 pending；settle 为 true 的项必须给出 satisfied 或 violated"
		}
		input := model.ReviewRangeInput{
			ChapterIDs: item.chapters, Requirements: item.requirements, Reviewed: item.reviewed, PriorFindings: item.prior,
			Basis: item.basis, Goal: goal,
		}
		work.ID, work.Kind, work.Input = reviewOperationID(run.ID, item.revision, input), model.OperationReviewRange, input
	case workVerifyCanon:
		reason := fmt.Sprintf("第 %d 章正文在事实入账后被修改，需要核验来源事实", item.number)
		if len(item.facts) == 0 {
			reason = fmt.Sprintf("第 %d 章没有入账事实，需要补账", item.number)
		}
		work.ID = runQuickID(run.ID, "canon", item.chapterID, "r"+strconv.FormatInt(int64(item.revision), 10))
		work.Kind = model.OperationReviseCanon
		work.Input = model.ReviseCanonInput{
			ChapterID: item.chapterID, FactIDs: item.facts, Reason: reason,
			Goal: "对照本章正文逐条核验 pending_facts 列出的事实：原样确认、更新或删除，并补记正文里新出现的关键事实；只提交事实（facts、confirm_facts、remove_facts），来源章就是本章",
		}
	case workRewrite:
		work.ID = runQuickID(run.ID, "rewrite", item.chapterID, "r"+strconv.FormatInt(int64(item.revision), 10))
		work.Kind = model.OperationRewriteChapter
		work.Input = model.RewriteChapterInput{
			ChapterID: item.chapterID, ChapterPlanID: item.plan.ID, ChapterNumber: item.number,
			Findings: item.notes, Directives: item.directives, Basis: item.basis,
			Goal: "根据审阅意见重写本章：针对意见修改，不引入新的越界改动，与前后章节的衔接保持一致；重申报本章全部既有事实（确认、更新或删除）；严格满足 directives 列出的每条创作要求（用户原话），字数约束按 constraints 执行",
		}
	default:
		return creation.WorkItem{}, fmt.Errorf("unknown work item kind %d: %w", item.kind, model.ErrInvalid)
	}
	return work, nil
}

func (item novelItem) reasons() creation.WorkReasons {
	chapter := fmt.Sprintf("第 %d 章《%s》", item.number, item.plan.Title)
	switch item.kind {
	case workDevelopPlan:
		return creation.WorkReasons{
			Start:   "规划故事蓝图：搭起卷与故事弧，展开第一个故事弧的章节",
			Done:    "故事蓝图已生效",
			Waiting: "故事蓝图已拟好，等你确认后继续",
			Failure: "故事蓝图这次没能完成",
			Stuck:   "蓝图已定稿但没有产出任何章节节点，需要人工检查规划",
		}
	case workExtendPlan:
		start := fmt.Sprintf("扩展蓝图：前 %d 章已审完，展开下一个故事弧", item.covered)
		if item.concluded {
			start = fmt.Sprintf("续写：第 %d 章已作为结局写成，在它之后展开新的篇章", item.covered)
		}
		return creation.WorkReasons{
			Start:   start,
			Done:    "后续章节的蓝图已生效",
			Waiting: "后续章节的蓝图已拟好（可能含篇幅上限调整），等你确认后继续",
			Failure: "后续章节的蓝图这次没能扩展完成",
			Stuck:   "蓝图扩展任务已结束但章节数没有增加，需要人工检查规划",
		}
	case workReview:
		window := fmt.Sprintf("第 %d–%d 章", item.span[0], item.span[1])
		if item.span[0] == item.span[1] {
			window = fmt.Sprintf("第 %d 章", item.span[0])
		}
		start := "审阅" + window + "：先审后写，问题不带到后面"
		if len(item.reviewed) > 0 || len(item.prior) > 0 {
			start = "复审" + window + "：核对上一轮的意见是否已解决"
		}
		if len(item.requirements) > 0 {
			start += fmt.Sprintf("，并核验 %d 项要求", len(item.requirements))
		}
		return creation.WorkReasons{
			Start:   start,
			Done:    window + "审阅完成",
			Waiting: "审阅在等你确认后继续",
			Failure: "审阅这次没能完成",
			Stuck:   "审阅已结束但窗口仍未通过或要求仍未兑现，需要人工检查",
		}
	case workVerifyCanon:
		start := fmt.Sprintf("核验第 %d 章的 %d 条事实：正文在事实入账后改过", item.number, len(item.facts))
		if len(item.facts) == 0 {
			start = fmt.Sprintf("补记第 %d 章的事实：这一章还没有入账", item.number)
		}
		return creation.WorkReasons{
			Start:   start,
			Done:    fmt.Sprintf("第 %d 章的事实已核验入账", item.number),
			Waiting: fmt.Sprintf("第 %d 章的事实核验已完成，等你确认后继续", item.number),
			Failure: fmt.Sprintf("第 %d 章的事实核验这次没能完成", item.number),
			Stuck:   fmt.Sprintf("第 %d 章的事实核验已结束但缺口仍在，需要人工检查", item.number),
		}
	case workRewrite:
		return creation.WorkReasons{
			Start:   fmt.Sprintf("按审阅意见重写%s：%d 条阻塞意见", chapter, len(item.notes)),
			Done:    chapter + "的重写稿已入稿",
			Waiting: chapter + "已按审阅意见重写，等你过目后继续",
			Failure: chapter + "按审阅意见重写失败",
			Stuck:   chapter + "的重写已结束但审阅意见没有解决，需要人工检查",
		}
	default:
		return creation.WorkReasons{
			Start:   "写" + chapter,
			Done:    chapter + "已入稿",
			Waiting: chapter + "初稿完成，等你审阅后继续",
			Failure: chapter + "这次没能写完",
			Stuck:   chapter + "的任务已结束但正文缺失，需要人工检查",
		}
	}
}
