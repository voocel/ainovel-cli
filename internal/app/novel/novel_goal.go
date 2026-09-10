package novel

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/creation"
	"github.com/voocel/ainovel-cli/internal/domain/model"
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
	covered   int            // extend：已覆盖章节数
	number    int            // chapter/rewrite：章节序号
	plan      model.PlanNode // chapter/rewrite：章节计划
	chapterID string         // rewrite/canon：目标正文章节
	facts     []string       // canon：待核验的事实 ID；空表示该章未入账
	chapters  []string       // review：范围内全部正文章节
	notes     []string       // rewrite：审阅意见；extend：上一窗口的审阅意见
	revision  model.Revision // review：绑定的 Revision；rewrite：所依据裁定的 Revision（槽位 ID）
	// verifyIntent 标记终审（§6.4 完成条件 3）：pass 必须携带逐项意图核验声明；
	// 阶段审阅不要求（必须出现的要素可能落在后续章节）。
	verifyIntent bool
	// directives 是命中本任务作用域的用户要求（§4.9），随任务输入进入指令平面。
	directives []model.Directive
	// basis 是任务基线（D48/D51）：review 为裁定将继承的证据基线，write/rewrite 为本章要求
	// 作用域，extend 为所依据的窗口裁定基线。
	basis model.EvidenceBasis
}

// Next 的顺序即小说的推进规则：蓝图超目标先停下 → 完成契约（D29）→ 先审后扩（§6.3）
// → 终审通过即完成（§6.4）→ 阻塞发现按预算重写 → 否则发起审阅。
func (Policy) Next(project projectdoc.Snapshot, run model.CreationRun, evidence Evidence) (creation.Step, error) {
	goal, err := model.DecodeNovelGoal(run.Goal)
	if err != nil {
		return creation.Step{}, err
	}
	if planCount := len(chapterPlansInOrder(project.Plan)); planCount > goal.TargetChapters {
		return creation.Step{Wait: fmt.Sprintf(
			"蓝图包含 %d 个有效章节节点，但目标是 %d 章；请先裁剪蓝图或调整目标",
			planCount, goal.TargetChapters,
		)}, nil
	}
	var item novelItem
	var reviewScope []string
	unmet := completionUnmet(project, goal)
	if gaps := CanonGaps(project); len(gaps) > 0 {
		// 事实缺口优先（§4.4 D41 / §4.5）：待核验事实与未入账章节先核验，
		// 旧事实不得继续指导后续创作与审阅。
		gap := gaps[0]
		item = novelItem{kind: workVerifyCanon, chapterID: gap.ChapterID, number: gap.Number, facts: gap.Pending, revision: gap.Revision}
	} else if unmet == "" {
		// 完成契约（§6.4）：内容齐全不等于完成，必须有基线仍成立的审阅通过证据；
		// 相干文档或要求再变化时旧证据随基线自动失效。
		reviewScope = goalManuscriptIDs(project, goal)
	} else {
		var ok bool
		if item, ok = nextNovelItem(project, goal); !ok {
			return creation.Step{}, fmt.Errorf("creation run %q has unmet goal but no work item: %w", run.ID, model.ErrInvalid)
		}
		// 先审后扩（§6.3）：扩窗前必须有覆盖当前窗口且仍然有效的阶段审阅证据，
		// 让每段新规划都建立在已审阅的正文之上。
		if item.kind == workExtendPlan {
			reviewScope = manuscriptIDsUpTo(project, item.covered)
		}
	}
	if reviewScope != nil {
		verdict := latestVerdict(evidence.Verdicts, func(verdict model.ReviewVerdict) bool {
			return verdictCovers(verdict, reviewScope)
		})
		switch {
		case verdict != nil && verdict.Status == model.ReviewPass && unmet == "":
			// 完成条件 3（§6.4）：Intent 的必须出现/禁止出现/结局方向与用户要求
			// 必须被显式核验为满足——笼统的 pass 不构成完成证据。
			if !verdict.IntentSatisfied() || !verdict.DirectivesSatisfied() {
				return creation.Step{Fail: "审阅通过但未逐项核验意图或用户要求，需要人工检查"}, nil
			}
			return creation.Step{Done: fmt.Sprintf("全书 %d 章完成并通过审阅", goal.TargetChapters)}, nil
		case verdict != nil && verdict.Status == model.ReviewPass:
			// 窗口审阅通过：意见随扩窗输入进入下一段规划（更新上下文 → 继续规划）；
			// 扩窗以该裁定为基线（D51），裁定失效则扩窗任务失效。
			item.notes, item.basis = verdictNotes(*verdict), verdict.Basis
		case verdict != nil:
			if evidence.RepairsUsed >= run.Strategy.AutoRepairBudget {
				return creation.Step{Wait: fmt.Sprintf(
					"自动修订预算 %d 次已用尽，需要你查看审阅意见后决断",
					run.Strategy.AutoRepairBudget,
				)}, nil
			}
			item = rewriteItem(project, *verdict)
			if item.chapterID == "" {
				return creation.Step{Fail: "审阅发现指向了不存在的章节，需要人工检查"}, nil
			}
		default:
			basis, err := ReviewBasis(project, reviewScope)
			if err != nil {
				return creation.Step{}, err
			}
			item = novelItem{
				kind: workReview, chapters: reviewScope, revision: project.Revision,
				basis: basis, verifyIntent: unmet == "",
			}
		}
	}
	item.directives = coveringDirectives(project, item)
	work, err := item.work(run, goal)
	if err != nil {
		return creation.Step{}, err
	}
	return creation.Step{Work: &work}, nil
}

// completionUnmet 是完成契约的最小落点（D29）：在同一 Revision 上验证蓝图覆盖
// 目标章数且每章有正文。队列为空不等于完成。返回空串表示契约满足。
func completionUnmet(project projectdoc.Snapshot, goal model.NovelGoal) string {
	plans := chapterPlansInOrder(project.Plan)
	if len(plans) != goal.TargetChapters {
		return fmt.Sprintf("蓝图有 %d 章，目标 %d 章", len(plans), goal.TargetChapters)
	}
	written := manuscriptsByPlanNode(project.Manuscript)
	for index, plan := range plans[:goal.TargetChapters] {
		if _, ok := written[plan.ID]; !ok {
			return fmt.Sprintf("第 %d 章《%s》还没有正文", index+1, plan.Title)
		}
	}
	return ""
}

func nextNovelItem(project projectdoc.Snapshot, goal model.NovelGoal) (novelItem, bool) {
	plans := chapterPlansInOrder(project.Plan)
	if len(plans) == 0 {
		return novelItem{kind: workDevelopPlan}, true
	}
	// 先写满当前窗口再扩窗（§6.3）：窗口末尾的阶段审阅意见要能流进下一段规划。
	covered := min(len(plans), goal.TargetChapters)
	written := manuscriptsByPlanNode(project.Manuscript)
	for index, plan := range plans[:covered] {
		if _, ok := written[plan.ID]; !ok {
			return novelItem{kind: workWriteChapter, number: index + 1, plan: plan, basis: chapterBasis(project, index+1, plan.ID)}, true
		}
	}
	if len(plans) < goal.TargetChapters {
		return novelItem{kind: workExtendPlan, covered: covered}, true
	}
	return novelItem{}, false
}

func rewriteItem(project projectdoc.Snapshot, verdict model.ReviewVerdict) novelItem {
	byID := manuscriptsByID(project.Manuscript)
	planByID := make(map[string]model.PlanNode, len(project.Plan))
	for _, node := range project.Plan {
		planByID[node.ID] = node
	}
	for _, chapterID := range verdict.BlockingChapters() {
		chapter, ok := byID[chapterID]
		if !ok {
			continue
		}
		plan, ok := planByID[chapter.PlanNodeID]
		if !ok {
			continue
		}
		var notes []string
		for _, finding := range verdict.Findings {
			if finding.ChapterID == chapterID {
				notes = append(notes, finding.Note)
			}
		}
		return novelItem{
			kind: workRewrite, chapterID: chapterID, number: chapter.Number,
			plan: plan, notes: notes, revision: verdict.Revision, basis: chapterBasis(project, chapter.Number, plan.ID),
		}
	}
	return novelItem{}
}

// chapterBasis 是章节任务的基线（D51）：只钉住本章命中的要求作用域。
func chapterBasis(project projectdoc.Snapshot, number int, planID string) model.EvidenceBasis {
	return model.EvidenceBasis{Scopes: []model.ScopeBasis{directiveScope(project, directiveTarget(project, number, planID))}}
}

// ReviewBasis 是审阅证据的基线：范围内正文及其依赖闭包，加每章命中的要求作用域。
func ReviewBasis(project projectdoc.Snapshot, chapters []string) (model.EvidenceBasis, error) {
	byID := manuscriptsByID(project.Manuscript)
	targets := make([]model.DocumentRef, 0, len(chapters))
	scopes := make([]model.ScopeBasis, 0, len(chapters))
	for _, id := range chapters {
		chapter, ok := byID[id]
		if !ok {
			return model.EvidenceBasis{}, fmt.Errorf("review chapter %q is not in the manuscript: %w", id, model.ErrInvalid)
		}
		targets = append(targets, model.DocumentRef{Kind: model.DocumentManuscript, ID: id})
		scopes = append(scopes, directiveScope(project, directiveTarget(project, chapter.Number, chapter.PlanNodeID)))
	}
	canonTarget := model.ReviewCanonScope(project.Manuscript, chapters)
	targets = append(targets, model.CanonScopeRefs(project.Canon, project.Manuscript, canonTarget)...)
	scopes = append(scopes, canonScope(project, canonTarget))
	return basisFor(project, targets, scopes)
}

func verdictCovers(verdict model.ReviewVerdict, chapters []string) bool {
	if len(verdict.ChapterIDs) != len(chapters) {
		return false
	}
	reviewed := make(map[string]struct{}, len(verdict.ChapterIDs))
	for _, id := range verdict.ChapterIDs {
		reviewed[id] = struct{}{}
	}
	for _, id := range chapters {
		if _, ok := reviewed[id]; !ok {
			return false
		}
	}
	return true
}

func reviewOperationID(runID string, revision model.Revision) string {
	return runQuickID(runID, "review", "r"+strconv.FormatInt(int64(revision), 10))
}

func goalManuscriptIDs(project projectdoc.Snapshot, goal model.NovelGoal) []string {
	return manuscriptIDsUpTo(project, goal.TargetChapters)
}

// manuscriptIDsUpTo 取前 count 个章节计划对应的正文 ID，按章节顺序排列。
func manuscriptIDsUpTo(project projectdoc.Snapshot, count int) []string {
	plans := chapterPlansInOrder(project.Plan)
	if len(plans) > count {
		plans = plans[:count]
	}
	written := manuscriptsByPlanNode(project.Manuscript)
	ids := make([]string, 0, len(plans))
	for _, plan := range plans {
		if chapter, ok := written[plan.ID]; ok {
			ids = append(ids, chapter.ID)
		}
	}
	return ids
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

func chapterPlansInOrder(plan []model.PlanNode) []model.PlanNode {
	chapters := make([]model.PlanNode, 0, len(plan))
	for _, node := range plan {
		if node.Kind == model.PlanChapter {
			chapters = append(chapters, node)
		}
	}
	slices.SortFunc(chapters, func(left, right model.PlanNode) int {
		if left.Order != right.Order {
			return left.Order - right.Order
		}
		return strings.Compare(left.ID, right.ID)
	})
	return chapters
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
// 审阅取范围内各章的并集，规划面向未来取全部 active。它只做装配，不参与推导。
func coveringDirectives(project projectdoc.Snapshot, item novelItem) []model.Directive {
	switch item.kind {
	case workWriteChapter, workRewrite:
		return model.ActiveDirectivesFor(project.Directives, directiveTarget(project, item.number, item.plan.ID))
	case workReview:
		chapters := manuscriptsByID(project.Manuscript)
		union := make(map[string]model.Directive)
		for _, id := range item.chapters {
			if chapter, ok := chapters[id]; ok {
				for _, directive := range model.ActiveDirectivesFor(project.Directives, directiveTarget(project, chapter.Number, chapter.PlanNodeID)) {
					union[directive.ID] = directive
				}
			}
		}
		merged := make([]model.Directive, 0, len(union))
		for _, directive := range union {
			merged = append(merged, directive)
		}
		model.SortDirectives(merged)
		return merged
	case workVerifyCanon:
		return nil
	default:
		return model.ActiveDirectives(project.Directives)
	}
}

// work 把推导出的条目装配成要驱动的 Operation：槽位 ID、种类、类型化输入与各落点文案。
func (item novelItem) work(run model.CreationRun, goal model.NovelGoal) (creation.WorkItem, error) {
	work := creation.WorkItem{Reasons: item.reasons()}
	switch item.kind {
	case workDevelopPlan:
		work.ID, work.Kind = runQuickID(run.ID, "plan"), model.OperationDevelopPlan
		work.Input = model.DevelopPlanInput{
			Intent: goal.Premise, TargetChapters: goal.TargetChapters,
			RequestedChapters: min(run.Strategy.PlanWindowChapters, goal.TargetChapters),
			Goal:              "设计可直接用于连续创作的卷、故事弧与章节节点；先展开请求数量的 chapter 节点（滚动规划的首个窗口），保持稳定 ID 和合法父子关系",
		}
	case workExtendPlan:
		work.ID, work.Kind = runQuickID(run.ID, "plan", "extend", strconv.Itoa(item.covered)), model.OperationRevisePlan
		work.Input = model.RevisePlanInput{
			Intent: goal.Premise, TargetChapters: goal.TargetChapters,
			ExistingChapters:  item.covered,
			RequestedChapters: min(item.covered+run.Strategy.PlanWindowChapters, goal.TargetChapters),
			ReviewNotes:       item.notes,
			Basis:             item.basis,
			Goal:              "增量扩展章节计划到请求数量：保持已有节点与 ID 稳定，只补充后续 chapter 节点并挂在合法父节点下；结合上一窗口的审阅意见调整后续走向",
		}
	case workWriteChapter:
		work.ID, work.Kind = runQuickID(run.ID, "chapter", item.plan.ID), model.OperationWriteChapter
		work.Input = model.WriteChapterInput{
			ChapterPlanID: item.plan.ID, ChapterNumber: item.number, Directives: item.directives, Basis: item.basis,
			Goal: "完成本章工作稿并提交带稳定章节 ID、稳定 block_id 与大纲依赖的正式候选；严格满足 directives 列出的每条创作要求（用户原话），字数约束按 constraints 执行",
		}
	case workReview:
		goal := "阶段审阅范围内全部正文：检查跨章连续性与 Intent 方向一致性，产出结构化裁定与发现；意见将进入下一段规划"
		if item.verifyIntent {
			goal = "终审范围内全部正文：逐项核验 Intent 的必须出现、禁止出现与结局方向并在裁定中声明，检查跨章连续性；意图未满足即为阻塞发现"
		}
		if len(item.directives) > 0 {
			goal += "；逐项核验 directives 中的每条用户要求是否满足并在裁定 directives 中声明，未满足即为阻塞发现"
		}
		work.ID, work.Kind = reviewOperationID(run.ID, item.revision), model.OperationReviewRange
		work.Input = model.ReviewRangeInput{
			ChapterIDs: item.chapters, VerifyIntent: item.verifyIntent, Directives: item.directives,
			Basis: item.basis, Goal: goal,
		}
	case workVerifyCanon:
		reason := fmt.Sprintf("第 %d 章正文在事实入账后被修改，需要核验来源事实", item.number)
		if len(item.facts) == 0 {
			reason = fmt.Sprintf("第 %d 章没有入账事实，需要补账", item.number)
		}
		work.ID = runQuickID(run.ID, "canon", item.chapterID, "r"+strconv.FormatInt(int64(item.revision), 10))
		work.Kind = model.OperationReviseCanon
		work.Input = model.ReviseCanonInput{
			ChapterID: item.chapterID, FactIDs: item.facts, Reason: reason,
			Goal: "对照本章正文逐条核验 fact_ids 列出的事实：原样确认（带 old_value 重申报）、更新或删除，并补记正文里新出现的关键事实；只提交 canon 补丁，来源章固定为本章",
		}
	case workRewrite:
		work.ID = runQuickID(run.ID, "rewrite", item.chapterID, "r"+strconv.FormatInt(int64(item.revision), 10))
		work.Kind = model.OperationRewriteChapter
		work.Input = model.RewriteChapterInput{
			ChapterID: item.chapterID, ChapterPlanID: item.plan.ID, ChapterNumber: item.number,
			Findings: item.notes, Directives: item.directives, Basis: item.basis,
			Goal: "根据审阅意见重写本章：保持章节 ID 与 block 结构稳定，针对意见修改，不引入新的越界改动；重申报本章全部既有事实（确认、更新或删除）；严格满足 directives 列出的每条创作要求（用户原话），字数约束按 constraints 执行",
		}
	default:
		return creation.WorkItem{}, fmt.Errorf("unknown work item kind %d: %w", item.kind, model.ErrInvalid)
	}
	return work, nil
}

func (item novelItem) reasons() creation.WorkReasons {
	switch item.kind {
	case workDevelopPlan:
		return creation.WorkReasons{
			Waiting: "故事蓝图已拟好，等你确认后继续",
			Failure: "故事蓝图这次没能完成",
			Stuck:   "蓝图已定稿但没有产出任何章节节点，需要人工检查规划",
		}
	case workExtendPlan:
		return creation.WorkReasons{
			Waiting: "后续章节的蓝图已拟好，等你确认后继续",
			Failure: "后续章节的蓝图这次没能扩展完成",
			Stuck:   "蓝图扩展任务已结束但章节数没有增加，需要人工检查规划",
		}
	case workReview:
		return creation.WorkReasons{
			Waiting: "审阅在等你确认后继续",
			Failure: "审阅这次没能完成",
			Stuck:   "审阅任务已结束但没有留下有效裁定，需要人工检查",
		}
	case workVerifyCanon:
		return creation.WorkReasons{
			Waiting: fmt.Sprintf("第 %d 章的事实核验已完成，等你确认后继续", item.number),
			Failure: fmt.Sprintf("第 %d 章的事实核验这次没能完成", item.number),
			Stuck:   fmt.Sprintf("第 %d 章的事实核验已结束但缺口仍在，需要人工检查", item.number),
		}
	case workRewrite:
		return creation.WorkReasons{
			Waiting: fmt.Sprintf("第 %d 章《%s》已按审阅意见重写，等你过目后继续", item.number, item.plan.Title),
			Failure: fmt.Sprintf("第 %d 章《%s》按审阅意见重写失败", item.number, item.plan.Title),
			Stuck:   fmt.Sprintf("第 %d 章《%s》的重写已结束但审阅意见没有解决，需要人工检查", item.number, item.plan.Title),
		}
	default:
		return creation.WorkReasons{
			Waiting: fmt.Sprintf("第 %d 章《%s》初稿完成，等你审阅后继续", item.number, item.plan.Title),
			Failure: fmt.Sprintf("第 %d 章《%s》这次没能写完", item.number, item.plan.Title),
			Stuck:   fmt.Sprintf("第 %d 章《%s》的任务已结束但正文缺失，需要人工检查", item.number, item.plan.Title),
		}
	}
}
