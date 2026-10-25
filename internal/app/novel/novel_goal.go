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
	"github.com/voocel/ainovel-cli/internal/domain/narrative"
)

// 小说目标的推导规则（应用编排层，D49）：什么算完成、下一步写什么、审阅何时发生、
// 修订预算怎么用，全部在这里；内核只驱动它给出的步骤，不认识任何小说概念。

type Policy struct{}

func (Policy) ValidateGoal(payload json.RawMessage) error {
	_, err := model.DecodeNovelGoal(model.CreationRunGoal{Kind: model.GoalNovel, Payload: payload})
	return err
}

// chapterSlot 是一章任务的定位：章号、章节计划，与已写的正文（写新章时为空）。
type chapterSlot struct {
	number    int
	plan      model.PlanNode
	chapterID string
}

func (c chapterSlot) label() string { return fmt.Sprintf("第 %d 章《%s》", c.number, c.plan.Title) }

// directives 是命中本章的用户要求（§4.9），随任务输入进入指令平面。
func (c chapterSlot) directives(project projectdoc.Snapshot) []model.Directive {
	return model.ActiveDirectivesFor(project.Directives, directiveTarget(model.IndexPlan(project.Plan), c.number, c.plan.ID))
}

// basis 是章节任务的基线（D51）：只钉住本章命中的要求作用域。
func (c chapterSlot) basis(project projectdoc.Snapshot) model.EvidenceBasis {
	return model.EvidenceBasis{Scopes: []model.ScopeBasis{directiveScope(project, directiveTarget(model.IndexPlan(project.Plan), c.number, c.plan.ID))}}
}

// upcoming 是还要规划或写的下一件事：write 非空是写这一章；否则是规划蓝图，covered 是已覆盖
// 的章数，0 即首次规划。
type upcoming struct {
	write   *chapterSlot
	covered int
}

// Next 的顺序即小说的推进规则：主编的修改等用户确认时先停下（D77）→ 蓝图超出固定篇幅
// 先停下 → 事实缺口先核验（D41）→ 用户点名的返工（D78/D86）→ 审阅闸门（D62：写满一个窗口
// 先审、先审后扩，阻塞发现按预算重写，用尽后标记接着写、违反用户要求的等用户，到期要求
// 必须兑现）→ 完成契约（D29/§6.4）→ 写章或扩窗（D63：全书章数未知或蓝图未到末章时扩窗）。
func (p Policy) Next(project projectdoc.Snapshot, run model.CreationRun, evidence Evidence) (creation.Step, error) {
	step, _, err := p.next(project, run, evidence)
	return step, err
}

// RepairHold 是推进卡住的那一章（D63、D94）：它的阻塞意见违反用户的要求，自动修订预算已
// 用尽，正等用户接受现状或跟主编说怎么改；没卡在这里为空。与 Next 同一次推导，不另立规则。
func (p Policy) RepairHold(project projectdoc.Snapshot, run model.CreationRun, evidence Evidence) (string, error) {
	_, held, err := p.next(project, run, evidence)
	return held, err
}

// Mark 是标记在案的一章（D94）：Repairs 是它用掉的自动修订次数。
type Mark struct {
	ChapterID string `json:"chapter_id"`
	Number    int    `json:"number"`
	Repairs   int    `json:"repairs"`
}

// Marks 是标记在案的章（D94）：只按审阅标准判的阻塞意见还在，自动修订已用尽，推进接着往下
// 走。与 Next 同一份裁定、预算与规则。
func (Policy) Marks(project projectdoc.Snapshot, run model.CreationRun, evidence Evidence) ([]Mark, error) {
	goal, err := model.DecodeNovelGoal(run.Goal)
	if err != nil {
		return nil, err
	}
	ledger := newReviewLedger(project, lengthOf(project, goal.TargetChapters).Final, evidence.Verdicts)
	var marks []Mark
	for _, number := range ledger.marked(repairable(run, evidence)) {
		id := ledger.chapters[number-1].ID
		marks = append(marks, Mark{ChapterID: id, Number: number, Repairs: evidence.Repairs[id]})
	}
	return marks, nil
}

// repairable 报告某章还有自动修订预算（D63）。
func repairable(run model.CreationRun, evidence Evidence) func(chapterID string) bool {
	return func(chapterID string) bool { return evidence.Repairs[chapterID] < run.Strategy.AutoRepairBudget }
}

func (Policy) next(project projectdoc.Snapshot, run model.CreationRun, evidence Evidence) (creation.Step, string, error) {
	goal, err := model.DecodeNovelGoal(run.Goal)
	if err != nil {
		return creation.Step{}, "", err
	}
	if evidence.Change {
		return creation.Step{Wait: "主编提出的修改等你确认，确认或撤回后接着写"}, "", nil
	}
	length := lengthOf(project, goal.TargetChapters)
	if planCount := len(model.ChapterPlansInOrder(project.Plan)); length.Fixed > 0 && planCount > length.Fixed {
		return creation.Step{Wait: fmt.Sprintf(
			"蓝图包含 %d 个有效章节节点，但篇幅固定为 %d 章；请先裁剪蓝图或调整篇幅",
			planCount, length.Fixed,
		)}, "", nil
	}
	var work creation.WorkItem
	if gaps := CanonGaps(project); len(gaps) > 0 {
		// 事实缺口优先（§4.4 D41 / §4.5）：待核验事实与未入账章节先核验，
		// 旧事实不得继续指导后续创作与审阅。
		work = canonWork(run, gaps[0])
	} else if chapter, rework, ok := firstRework(project); ok {
		// 用户说已写的不满足：先按要求重写，审阅再核验它，不把旧正文带进后面的审阅与写作。
		work = reworkWork(project, run, chapter, rework)
	} else {
		// 完成契约（§6.4）：内容齐全（没有要写或要规划的）不等于完成，全书每章都要有基线
		// 仍成立的裁定、阻塞意见已消除或接受或标记在案（D94）、每项要求都已兑现；相干文档
		// 或要求再变化时旧证据随基线自动失效。
		ledger := newReviewLedger(project, length.Final, evidence.Verdicts).withPrior(evidence.Prior)
		through := length.Final
		next, unwritten := nextUpcoming(project, length)
		if unwritten {
			through = ledger.reviewedThrough(next)
		}
		// AI 定的收官不得让用户要求落空（D63）：固定篇幅是用户自己划的边界，AI 的收官
		// 承诺不是——作用域落在全书之外的要求交给用户裁决，而不是静默跳过后完成。
		if length.Fixed == 0 && len(ledger.dropped) > 0 {
			directive := ledger.dropped[0]
			return creation.Step{Wait: fmt.Sprintf(
				"AI 承诺全书 %d 章收官，要求「%s」（作用域：%s）因此落空；请退役或调整这条要求，或固定更长的篇幅",
				length.Final, directive.Text, storyOf(project).DescribeScope(directive),
			)}, "", nil
		}
		canRepair := repairable(run, evidence)
		step := ledger.gate(through, canRepair)
		switch {
		case step.blocked != nil:
			target := step.chapter
			if target == "" {
				target = repairTarget(*step.blocked, canRepair)
			}
			chapter, findings, ok := blockedChapter(project, *step.blocked, target)
			if !ok {
				return creation.Step{Fail: "审阅发现指向了不存在的章节，需要人工检查"}, "", nil
			}
			if !canRepair(chapter.chapterID) {
				return creation.Step{Wait: fmt.Sprintf(
					"第 %d 章《%s》自动修订了 %d 次仍没做到你的要求，等你决定：接受现状，或跟主编说怎么改",
					chapter.number, chapter.plan.Title, evidence.Repairs[chapter.chapterID],
				)}, chapter.chapterID, nil
			}
			work = rewriteWork(project, run, chapter, findings, step.blocked.Revision)
		case step.review != nil:
			if work, err = reviewWork(project, run, ledger, step); err != nil {
				return creation.Step{}, "", err
			}
		case !unwritten:
			if marked := ledger.marked(canRepair); len(marked) > 0 {
				return creation.Step{Done: fmt.Sprintf("全书 %d 章完成；%s自动修订后仍有审阅意见，已标记", length.Final, chapterList(marked))}, "", nil
			}
			return creation.Step{Done: fmt.Sprintf("全书 %d 章完成并通过审阅", length.Final)}, "", nil
		case next.write != nil:
			work = writeWork(project, run, *next.write)
		case next.covered == 0:
			// 首次规划就要为用户已经提的要求安排落点：开书时给的大纲、蓝图等确认时提的要求。
			work = planWork(run, length, ledger.pending(0))
		default:
			work = extendWork(run, length, ledger, next.covered)
		}
	}
	return creation.Step{Work: &work}, "", nil
}

// chapterList 把章号写成「第 3、7 章」。
func chapterList(numbers []int) string {
	parts := make([]string, len(numbers))
	for i, number := range numbers {
		parts[i] = strconv.Itoa(number)
	}
	return "第 " + strings.Join(parts, "、") + " 章"
}

// reviewedThrough 是开始下一件事前必须审过的章数（D62/D67）：写第 n 章前审完已封口的
// 窗口（蓝图一次铺满也按容量分窗审），扩窗前审完已覆盖章节。
func (l reviewLedger) reviewedThrough(next upcoming) int {
	if next.write != nil {
		return l.settledThrough(next.write.number - 1)
	}
	return next.covered
}

// nextUpcoming 是还要规划或写的下一件事；全书章数已知（用户固定或收官承诺）、蓝图恰好
// 覆盖它且每章都有正文时没有（ok=false）。队列为空不等于完成，完成还要审阅（D29/D63）。
func nextUpcoming(project projectdoc.Snapshot, length Length) (upcoming, bool) {
	plans := model.ChapterPlansInOrder(project.Plan)
	if len(plans) == 0 {
		return upcoming{}, true
	}
	// 先写满当前窗口再扩窗（§6.3）：窗口审阅意见要能流进下一段规划。
	covered := len(plans)
	if length.Final > 0 {
		covered = min(covered, length.Final)
	}
	written := manuscriptsByPlanNode(project.Manuscript)
	for index, plan := range plans[:covered] {
		if _, ok := written[plan.ID]; !ok {
			return upcoming{write: &chapterSlot{number: index + 1, plan: plan}}, true
		}
	}
	if length.Final == 0 || covered < length.Final {
		return upcoming{covered: covered}, true
	}
	return upcoming{}, false
}

// blockedChapter 是裁定要重写的 chapterID（为空取第一个阻塞章）与它的阻塞意见——参考意见
// 不值得一次返工，带上只会扩大改动面（D68）。章节已不在作品里时 ok=false。
func blockedChapter(project projectdoc.Snapshot, verdict model.ReviewVerdict, chapterID string) (chapterSlot, []string, bool) {
	byID, plans := manuscriptsByID(project.Manuscript), model.IndexPlan(project.Plan)
	for _, id := range verdict.BlockingChapters() {
		if chapterID != "" && id != chapterID {
			continue
		}
		chapter, ok := byID[id]
		if !ok {
			continue
		}
		plan, ok := plans[chapter.PlanNodeID]
		if !ok {
			continue
		}
		var findings []string
		for _, finding := range verdict.Findings {
			if finding.ChapterID == id && finding.Severity == model.FindingBlocking {
				findings = append(findings, finding.Note)
			}
		}
		return chapterSlot{number: chapter.Number, plan: plan, chapterID: id}, findings, true
	}
	return chapterSlot{}, nil, false
}

// Rework 是一章待返工的依据（D78）：作用域命中它、入账晚于它正文最后一次变化的返工要求，
// 以及其中最晚的入账 Revision。
type Rework struct {
	Directives []model.Directive
	Revision   model.Revision
}

// ReworksDue 按正文章节 ID 列出待返工的章：重写之后正文更新，缺口自然关闭；审阅随后按
// 同一条要求核验。推导器与工作台共用这一条规则。
func ReworksDue(project projectdoc.Snapshot) map[string]Rework {
	var reworks []model.Directive
	for _, directive := range model.ActiveDirectives(project.Directives) {
		if directive.Rework {
			reworks = append(reworks, directive)
		}
	}
	plans := model.IndexPlan(project.Plan)
	due := make(map[string]Rework)
	for _, chapter := range project.Manuscript {
		written := project.Index[manuscriptRef(chapter.ID).Key()].Revision
		target := directiveTarget(plans, chapter.Number, chapter.PlanNodeID)
		for _, directive := range reworks {
			noted := project.Index[(model.DocumentRef{Kind: model.DocumentDirective, ID: directive.ID}).Key()].Revision
			if directive.Covers(target) && noted > written {
				rework := due[chapter.ID]
				rework.Directives, rework.Revision = append(rework.Directives, directive), max(rework.Revision, noted)
				due[chapter.ID] = rework
			}
		}
	}
	return due
}

// firstRework 是最靠前的待返工章；没有时 ok=false。
func firstRework(project projectdoc.Snapshot) (chapterSlot, Rework, bool) {
	due := ReworksDue(project)
	plans := model.IndexPlan(project.Plan)
	chapters := slices.Clone(project.Manuscript)
	slices.SortFunc(chapters, func(a, b model.ManuscriptChapter) int { return a.Number - b.Number })
	for _, chapter := range chapters {
		if rework, ok := due[chapter.ID]; ok {
			return chapterSlot{number: chapter.Number, plan: plans[chapter.PlanNodeID], chapterID: chapter.ID}, rework, true
		}
	}
	return chapterSlot{}, Rework{}, false
}

// ReviewBasis 是窗口审阅的证据基线（D62、D69）：只钉裁定所依据的输入——窗口与衔接章
// 的正文、窗口各章的计划节点（章在故事中的位置，要求作用域据此匹配）与 Intent，加每章
// 命中的要求作用域。实体、卷弧与 Canon 只是审阅上下文：它们随故事推进原地更新，钉住
// 会让一次别名追加废掉提到该角色的全部窗口；与正文的一致性归影响分析与事实核验（D41）。
func ReviewBasis(project projectdoc.Snapshot, chapters []string) (model.EvidenceBasis, error) {
	byID, plans := manuscriptsByID(project.Manuscript), model.IndexPlan(project.Plan)
	refs := []model.DocumentRef{{Kind: model.DocumentIntent, ID: model.SingletonDocumentID}}
	basis := model.EvidenceBasis{Scopes: make([]model.ScopeBasis, 0, len(chapters))}
	for _, id := range chapters {
		chapter, ok := byID[id]
		if !ok {
			return model.EvidenceBasis{}, fmt.Errorf("review chapter %q is not in the manuscript: %w", id, model.ErrInvalid)
		}
		refs = append(refs, manuscriptRef(id), model.DocumentRef{Kind: model.DocumentPlan, ID: chapter.PlanNodeID})
		basis.Scopes = append(basis.Scopes, directiveScope(project, directiveTarget(plans, chapter.Number, chapter.PlanNodeID)))
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
	return narrative.New(narrative.Content{Compass: project.Compass, Plan: project.Plan, Entities: project.Entities, Canon: project.Canon, Manuscript: project.Manuscript})
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

// 以下把推导出的缺口装配成要驱动的 Operation：槽位 ID、种类、类型化输入与各落点文案。
// 规划任务的 ID 带上篇幅输入：篇幅或罗盘变化后是新任务，不复用旧输入（D63）。

func planWork(run model.CreationRun, length Length, pending []string) creation.WorkItem {
	return creation.WorkItem{
		ID: runQuickID(run.ID, "plan", length.inputID()), Kind: model.OperationDevelopPlan,
		Input: model.DevelopPlanInput{
			FixedChapters: length.Fixed, PendingRequirements: pending,
			Goal: "设计可直接用于连续创作的卷、故事弧与章节大纲：只搭近处——第一卷的故事弧骨架（看得清时可再搭下一卷），更远的卷写到时按已发生的故事再定，终局只作为方向写进罗盘；立起长线（threads）：全书在追问、终局要回答的几个问题；这次只为第一个故事弧展开章节，这一弧多少章由你按故事走向决定，每章挂在所属故事弧下，用 threads 标明它推进哪几条长线，用 turn 写明它的转折；为 pending_requirements 中的要求安排落点，指明了章的就落在那几章；" + length.planningGoal(),
		},
		Reasons: creation.WorkReasons{
			Start:   "规划故事蓝图：搭起卷与故事弧，展开第一个故事弧的章节",
			Done:    "故事蓝图已生效",
			Waiting: "故事蓝图已拟好，等你确认后继续",
			Failure: "故事蓝图这次没能完成",
			Stuck:   "蓝图已定稿但没有产出任何章节节点，需要人工检查规划",
		},
	}
}

// extendWork 是先审后扩（§6.3）：窗口意见与待兑现要求随扩窗输入进入下一段规划；扩窗以覆盖
// 末章的裁定为基线（D51），裁定失效则扩窗任务失效。已覆盖的末章作为全书结局写成时是续写（D63）。
func extendWork(run model.CreationRun, length Length, ledger reviewLedger, covered int) creation.WorkItem {
	concluded := ledger.concluded(covered)
	goal := "展开下一个故事弧的章节：已有章节保持不变，只在末尾追加并挂在所属故事弧下；下一弧已有骨架就按已发生的故事修订后展开，没有就新建；本卷的故事弧都已写完时先判断故事是否到了终点，未到终点才新开一卷，新卷只搭它自己的故事弧骨架；这一弧多少章由你按故事走向决定；结合审阅意见调整后续走向，为 pending_requirements 中尚未兑现的要求安排落点；对照罗盘里各条长线停了几章（quiet）与下一次的安排（next）规划这一弧，新章用 threads 标明推进的长线、用 turn 写明转折，答案落在哪一章就给那条长线写上 answer，故事里出现新的长线就立起来；已有人物与事物的状态随章节正文记录，规划不改写；" + length.planningGoal()
	parts := []string{"plan", "extend", strconv.Itoa(covered), length.inputID()}
	start := fmt.Sprintf("扩展蓝图：前 %d 章已审完，展开下一个故事弧", covered)
	if concluded {
		goal += fmt.Sprintf("；第 %d 章已作为全书结局写成，这是续写：在这个结局之后开启新的篇章、接住已完成的故事，不重复收尾，并在 compass.ending 写下续写部分的终局方向", covered)
		parts = append(parts, "c")
		start = fmt.Sprintf("续写：第 %d 章已作为结局写成，在它之后展开新的篇章", covered)
	}
	return creation.WorkItem{
		ID: runQuickID(run.ID, parts...), Kind: model.OperationRevisePlan,
		Input: model.RevisePlanInput{
			FixedChapters: length.Fixed, ExistingChapters: covered,
			ReviewNotes: ledger.notes(covered), PendingRequirements: ledger.pending(covered),
			Basis: ledger.current[ledger.chapters[covered-1].ID].Verdict.Basis, Goal: goal,
		},
		Reasons: creation.WorkReasons{
			Start:   start,
			Done:    "后续章节的蓝图已生效",
			Waiting: "后续章节的蓝图已拟好（可能含预计篇幅调整），等你确认后继续",
			Failure: "后续章节的蓝图这次没能扩展完成",
			Stuck:   "蓝图扩展任务已结束但章节数没有增加，需要人工检查规划",
		},
	}
}

func writeWork(project projectdoc.Snapshot, run model.CreationRun, chapter chapterSlot) creation.WorkItem {
	return creation.WorkItem{
		ID: runQuickID(run.ID, "chapter", chapter.plan.ID), Kind: model.OperationWriteChapter,
		Input: model.WriteChapterInput{
			ChapterPlanID: chapter.plan.ID, Directives: chapter.directives(project), Basis: chapter.basis(project),
			Goal: "完成本章工作稿，连同本章的事实变化提交候选；严格满足 directives 列出的每条创作要求（用户原话），字数约束按 constraints 执行",
		},
		Reasons: creation.WorkReasons{
			Start:   "写" + chapter.label(),
			Done:    chapter.label() + "已入稿",
			Waiting: chapter.label() + "初稿完成，等你审阅后继续",
			Failure: chapter.label() + "这次没能写完",
			Stuck:   chapter.label() + "的任务已结束但正文缺失，需要人工检查",
		},
	}
}

// rewriteWork 按裁定重写一章；槽位绑定所依据裁定的 Revision。
func rewriteWork(project projectdoc.Snapshot, run model.CreationRun, chapter chapterSlot, findings []string, revision model.Revision) creation.WorkItem {
	return creation.WorkItem{
		ID: runQuickID(run.ID, "rewrite", chapter.chapterID, "r"+strconv.FormatInt(int64(revision), 10)), Kind: model.OperationRewriteChapter,
		Input: model.RewriteChapterInput{
			ChapterID: chapter.chapterID, ChapterPlanID: chapter.plan.ID,
			Findings: findings, Directives: chapter.directives(project), Basis: chapter.basis(project),
			Goal: "根据审阅意见重写本章：针对意见修改，不引入新的越界改动，与前后章节的衔接保持一致；重申报本章全部既有事实（确认、更新或删除）；严格满足 directives 列出的每条创作要求（用户原话），字数约束按 constraints 执行",
		},
		Reasons: creation.WorkReasons{
			Start:   fmt.Sprintf("按审阅意见重写%s：%d 条阻塞意见", chapter.label(), len(findings)),
			Done:    chapter.label() + "的重写稿已入稿",
			Waiting: chapter.label() + "已按审阅意见重写，等你过目后继续",
			Failure: chapter.label() + "按审阅意见重写失败",
			Stuck:   chapter.label() + "的重写已结束但审阅意见没有解决，需要人工检查",
		},
	}
}

// reworkWork 按返工要求重写一章（D78）：槽位绑定要求入账的最大 Revision，同一批要求只重写一次。
func reworkWork(project projectdoc.Snapshot, run model.CreationRun, chapter chapterSlot, rework Rework) creation.WorkItem {
	texts := make([]string, len(rework.Directives))
	for i, directive := range rework.Directives {
		texts[i] = directive.Text
	}
	return creation.WorkItem{
		ID: runQuickID(run.ID, "rework", chapter.chapterID, "r"+strconv.FormatInt(int64(rework.Revision), 10)), Kind: model.OperationReworkChapter,
		Input: model.ReworkChapterInput{
			ChapterID: chapter.chapterID, ChapterPlanID: chapter.plan.ID,
			Directives: chapter.directives(project), Basis: chapter.basis(project),
			Goal: "按用户的返工要求重写本章：directives 里 rework 为 true 的要求是用户对现有正文的意见，针对它修改，不引入新的越界改动，与前后章节的衔接保持一致；重申报本章全部既有事实（确认、更新或删除），以及本章的章题、章纲与转折（照重写后的正文，没变就原样给出）；严格满足 directives 列出的每条创作要求（用户原话），字数约束按 constraints 执行",
		},
		Reasons: creation.WorkReasons{
			Start:   "按你的要求重写" + chapter.label() + "：「" + strings.Join(texts, "」「") + "」",
			Done:    chapter.label() + "已按你的要求重写",
			Waiting: chapter.label() + "已按你的要求重写，等你过目后继续",
			Failure: chapter.label() + "按你的要求重写失败",
			Stuck:   chapter.label() + "的返工已结束但正文没有更新，需要人工检查",
		},
	}
}

// canonWork 核验一章的事实（D41）：正文改动后待核验，或这一章还没有入账。
func canonWork(run model.CreationRun, gap CanonGap) creation.WorkItem {
	reason := fmt.Sprintf("第 %d 章正文在事实入账后被修改，需要核验来源事实", gap.Number)
	start := fmt.Sprintf("核验第 %d 章的 %d 条事实：正文在事实入账后改过", gap.Number, len(gap.Pending))
	if len(gap.Pending) == 0 {
		reason = fmt.Sprintf("第 %d 章没有入账事实，需要补账", gap.Number)
		start = fmt.Sprintf("补记第 %d 章的事实：这一章还没有入账", gap.Number)
	}
	return creation.WorkItem{
		ID: runQuickID(run.ID, "canon", gap.ChapterID, "r"+strconv.FormatInt(int64(gap.Revision), 10)), Kind: model.OperationReviseCanon,
		Input: model.ReviseCanonInput{
			ChapterID: gap.ChapterID, FactIDs: gap.Pending, Reason: reason,
			Goal: "对照本章正文逐条核验 pending_facts 列出的事实：原样确认、更新或删除，并补记正文里新出现的关键事实；只提交事实（facts、confirm_facts、remove_facts），来源章就是本章",
		},
		Reasons: creation.WorkReasons{
			Start:   start,
			Done:    fmt.Sprintf("第 %d 章的事实已核验入账", gap.Number),
			Waiting: fmt.Sprintf("第 %d 章的事实核验已完成，等你确认后继续", gap.Number),
			Failure: fmt.Sprintf("第 %d 章的事实核验这次没能完成", gap.Number),
			Stuck:   fmt.Sprintf("第 %d 章的事实核验已结束但缺口仍在，需要人工检查", gap.Number),
		},
	}
}

// reviewWork 审阅闸门给出的窗口（D62）：要求以三态核验项投递，不走 directives；槽位绑定当前
// Revision 与输入摘要。
func reviewWork(project projectdoc.Snapshot, run model.CreationRun, ledger reviewLedger, step gateStep) (creation.WorkItem, error) {
	basis, err := ReviewBasis(project, step.review)
	if err != nil {
		return creation.WorkItem{}, err
	}
	requirements := ledger.window(step.review)
	// 上一份裁定判过满足、如今已不在要核验之列的要求（如已退役）不再提。
	satisfied := slices.DeleteFunc(slices.Clone(step.satisfied), func(id string) bool {
		return !slices.ContainsFunc(requirements, func(r model.Requirement) bool { return r.ID == id })
	})
	goal := "审阅本窗口正文：检查与上一章的衔接、窗口内的连续性与 Intent 方向，产出结构化裁定与发现；上一章正文只用于衔接检查，不在审阅范围；" +
		"每条 blocking 都由重写它所在的那一章来解决"
	if len(step.reviewed) > 0 {
		goal += "；reviewed 列出的章上一轮已审且正文未变，只作上下文：不再对它们记 blocking（违反 requirements 除外，但 satisfied 列出的要求上一轮已判满足，不再借它们阻塞），其中的问题记为 note；改动章与它们之间的矛盾记在改动章上"
	}
	if len(step.prior) > 0 {
		goal += "；逐条核对 prior_findings 列出的上一轮阻塞问题是否已解决，并检查改动有没有引入新问题"
	}
	if len(requirements) > 0 {
		goal += "；逐项核验 requirements：已兑现为 satisfied，被违反为 violated 并用阻塞发现链接，仅凭本窗口正文还无法判断为 pending；settle 为 true 的项必须给出 satisfied 或 violated"
	}
	input := model.ReviewRangeInput{
		ChapterIDs: step.review, Requirements: requirements, Reviewed: step.reviewed, Satisfied: satisfied, PriorFindings: step.prior,
		Basis: basis, Goal: goal,
	}
	first, last := ledger.numbers[step.review[0]], ledger.numbers[step.review[len(step.review)-1]]
	window := fmt.Sprintf("第 %d–%d 章", first, last)
	if first == last {
		window = fmt.Sprintf("第 %d 章", first)
	}
	start := "审阅" + window + "：先审后写，问题不带到后面"
	if len(step.reviewed) > 0 || len(step.prior) > 0 {
		start = "复审" + window + "：核对上一轮的意见是否已解决"
	}
	if len(requirements) > 0 {
		start += fmt.Sprintf("，并核验 %d 项要求", len(requirements))
	}
	return creation.WorkItem{
		ID: reviewOperationID(run.ID, project.Revision, input), Kind: model.OperationReviewRange, Input: input,
		Reasons: creation.WorkReasons{
			Start:   start,
			Done:    window + "审阅完成",
			Waiting: "审阅在等你确认后继续",
			Failure: "审阅这次没能完成",
			Stuck:   "审阅已结束但窗口仍未通过或要求仍未兑现，需要人工检查",
		},
	}, nil
}
