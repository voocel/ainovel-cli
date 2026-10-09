package workbench

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/app/decision"
	"github.com/voocel/ainovel-cli/internal/app/novel"
	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/creation"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

type Query struct {
	activity  ActivityFeed
	store     *store.Store
	projects  *projectdoc.Repository
	runs      *creation.Coordinator
	reviews   *novel.Reviews
	decisions *decision.Review
}

func New(authorityStore *store.Store, projects *projectdoc.Repository, runs *creation.Coordinator, reviews *novel.Reviews, decisions *decision.Review) *Query {
	return &Query{store: authorityStore, projects: projects, runs: runs, reviews: reviews, decisions: decisions}
}

// 工作台统一只读查询（v1-product-workbench-page.md §4）：TUI 与未来入口复用同一
// 模型，入口层不自行拼接权威数据。快照内全部权威投影绑定同一个 Revision；
// 候选稿保留 Operation 与 Base Revision 血统，过期候选不得伪装成当前稿件。

// ChapterState 是大纲章节的呈现状态，由权威文档与运行状态推导，不是新状态机。
type ChapterState string

const (
	ChapterConfirmed  ChapterState = "confirmed"   // 正文已入权威库
	ChapterPending    ChapterState = "pending"     // 候选稿等待用户确认
	ChapterInProgress ChapterState = "in_progress" // 正在写作/重写
	ChapterPlanned    ChapterState = "planned"     // 已规划、尚无正文
)

// OutlineNode 是大纲树节点：Plan 节点 + 章节呈现状态（仅 chapter 有）。
type OutlineNode struct {
	Node   model.PlanNode `json:"node"`
	Number int            `json:"number,omitempty"` // 章节序号，仅 chapter
	State  ChapterState   `json:"state,omitempty"`
	Detail string         `json:"detail,omitempty"` // 进行中细分：正在落笔/正在按意见重写
	// Proposed 表示节点来自等你确认的方案（新增或修订），尚未入库：大纲按方案生效后的
	// 样子呈现，确认前可逐章浏览。
	Proposed bool `json:"proposed,omitempty"`
}

// ChapterCandidate 是待确认的章节候选稿及其血统。
type ChapterCandidate struct {
	OperationID  string                  `json:"operation_id"`
	BaseRevision model.Revision          `json:"base_revision"`
	Chapter      model.ManuscriptChapter `json:"chapter"`
}

// PendingDecision 是决定卡数据源：等待原因 + 可选的待裁决稿件。
// Stale 表示稿件基线已落后于当前 Revision：过期候选不得伪装成当前稿件（§4），
// 直接通过会撞版本冲突，应引导按意见重写或继续创作走后继迁移。
type PendingDecision struct {
	Reason      string         `json:"reason"`
	Proposal    model.Proposal `json:"proposal"`
	HasProposal bool           `json:"has_proposal"`
	Stale       bool           `json:"stale,omitempty"`
	// View 是稿件里正文以外变更的故事语言描述，有稿件时才有。
	View ProposalView `json:"view,omitzero"`
}

type WorkbenchSnapshot struct {
	ProjectID string                `json:"project_id"`
	Revision  model.Revision        `json:"revision"`
	Intent    model.Intent          `json:"intent"`
	Approval  model.ApprovalPolicy  `json:"approval,omitempty"`
	Ownership []model.OwnershipRule `json:"ownership,omitempty"`
	// Directives 只呈现 active 的用户创作要求（§4.9）。
	Directives []model.Directive         `json:"directives,omitempty"`
	Manuscript []model.ManuscriptChapter `json:"manuscript,omitempty"`
	Outline    []OutlineNode             `json:"outline"`
	Candidates []ChapterCandidate        `json:"candidates,omitempty"`
	// Run 是最近一轮创作（含终态，落点呈现）；nil 表示还没开始过。
	Run *model.CreationRun `json:"run,omitempty"`
	// Usage 是创作团队在这本书上的累计用量与用时（从落盘的执行记录汇总）。
	Usage TeamUsage `json:"usage,omitzero"`
	// CurrentPhase 是进行中的环节（创作语言），仅 Run 非终态时非空。
	CurrentPhase string           `json:"current_phase,omitempty"`
	Decision     *PendingDecision `json:"decision,omitempty"`
	// Findings 是基线仍成立的最新审阅发现（D48）中尚未被用户接受的部分；
	// Adjudications 是仍然有效的接受记录（D43）。
	Findings      []WorkbenchFinding   `json:"findings,omitempty"`
	Adjudications []model.Adjudication `json:"adjudications,omitempty"`
	// Briefs 是各已规划章节的依据（键为章号）：覆盖它的要求与结论、它记下的设定。
	Briefs map[int]ChapterBrief `json:"briefs,omitempty"`
	// Length 是篇幅口径（D63）：固定章数、故事罗盘与全书章数，与推导器同一规则。
	Length novel.Length `json:"length"`
}

func (s *Query) WorkbenchSnapshot(ctx context.Context, projectID string) (WorkbenchSnapshot, error) {
	project, err := s.projects.Project(ctx, projectID, model.InitialRevision)
	if err != nil {
		return WorkbenchSnapshot{}, err
	}
	return s.snapshotFromProject(ctx, project)
}

func (s *Query) snapshotFromProject(ctx context.Context, project projectdoc.Snapshot) (WorkbenchSnapshot, error) {
	projectID := project.ID
	snapshot := WorkbenchSnapshot{
		ProjectID: project.ID, Revision: project.Revision,
		Intent: project.Intent, Approval: project.Approval, Ownership: project.Ownership,
		Directives: model.ActiveDirectives(project.Directives),
		Manuscript: project.Manuscript,
	}

	run, hasRun, err := s.runs.LatestCreationRun(ctx, projectID)
	if err != nil {
		return WorkbenchSnapshot{}, err
	}
	attempts, err := s.store.ProjectAttempts(ctx, projectID)
	if err != nil {
		return WorkbenchSnapshot{}, err
	}
	snapshot.Usage = teamUsage(attempts, time.Now())
	writingPlanID := ""
	if hasRun {
		snapshot.Run = &run
		if snapshot.Candidates, snapshot.Decision, err = s.workbenchDecision(ctx, run, project); err != nil {
			return WorkbenchSnapshot{}, err
		}
		if snapshot.CurrentPhase, writingPlanID, err = s.workbenchPhase(ctx, run, project); err != nil {
			return WorkbenchSnapshot{}, err
		}
	}
	verdicts, err := s.reviews.ListVerdicts(ctx, project)
	if err != nil {
		return WorkbenchSnapshot{}, err
	}
	current := novel.CurrentVerdicts(verdicts)
	if snapshot.Findings, snapshot.Adjudications, err = s.validFindings(ctx, project, current); err != nil {
		return WorkbenchSnapshot{}, err
	}
	plan, proposed, err := proposedPlan(project.Plan, snapshot.Decision)
	if err != nil {
		return WorkbenchSnapshot{}, err
	}
	snapshot.Outline = buildOutline(plan, proposed, project.Manuscript, snapshot.Candidates, writingPlanID)
	snapshot.Length = novel.LengthOf(project, snapshot.Run)
	snapshot.Briefs = chapterBriefs(project, snapshot.Length.Final, current)
	return snapshot, nil
}

// DescribeProposal 用故事语言描述运行之外的稿件（如导入草案）相对作品当前内容的变更。
func (s *Query) DescribeProposal(ctx context.Context, projectID string, proposal model.Proposal) (ProposalView, error) {
	project, err := s.projects.Project(ctx, projectID, model.InitialRevision)
	if err != nil {
		return ProposalView{}, err
	}
	return proposalView(project, proposal)
}

// workbenchDecision 提取待确认候选与决定卡：Run 等待用户时才有。
func (s *Query) workbenchDecision(
	ctx context.Context, run model.CreationRun, project projectdoc.Snapshot,
) ([]ChapterCandidate, *PendingDecision, error) {
	if run.State != model.RunWaitingUser {
		return nil, nil, nil
	}
	proposal, ok, err := s.runs.WaitingProposal(ctx, run.ID)
	if err != nil {
		return nil, nil, err
	}
	decision := &PendingDecision{Reason: run.StateReason, Proposal: proposal, HasProposal: ok}
	if !ok {
		return nil, decision, nil
	}
	// 等待期间的漂移按 D51 判定：只有用户专属变化时候选可重定位、仍是当前稿件；
	// 正文/规划/相干要求变过则基线过期，不解出章节候选（大纲不得标 ◐），由界面
	// 引导重写而不是直接通过。
	relocated, _, err := s.decisions.Relocate(ctx, proposal)
	if errors.Is(err, model.ErrRevisionConflict) {
		decision.Stale = true
	} else if err != nil {
		return nil, nil, err
	}
	// 过期稿件也照实描述它提了什么，只是不再解出候选、不投到大纲。
	if decision.View, err = proposalView(project, proposal); err != nil {
		return nil, nil, err
	}
	if decision.Stale {
		return nil, decision, nil
	}
	proposal = relocated
	var candidates []ChapterCandidate
	for _, patch := range proposal.Patches {
		if patch.Document.Kind != model.DocumentManuscript || patch.Operation != model.PatchPut {
			continue
		}
		var chapter model.ManuscriptChapter
		if err := json.Unmarshal(patch.Content, &chapter); err != nil {
			return nil, nil, fmt.Errorf("decode pending chapter %q: %w", patch.Document.ID, err)
		}
		candidates = append(candidates, ChapterCandidate{
			OperationID: proposal.OperationID, BaseRevision: proposal.BaseRevision, Chapter: chapter,
		})
	}
	return candidates, decision, nil
}

// workbenchPhase 推导进行中的环节（创作语言）与正在落笔/重写的 Plan 节点；
// Run 终态或等待时为空。
func (s *Query) workbenchPhase(
	ctx context.Context, run model.CreationRun, project projectdoc.Snapshot,
) (string, string, error) {
	if run.State != model.RunRunning {
		return "", "", nil
	}
	ids, err := s.runs.RunOperationIDs(ctx, run.ID)
	if err != nil {
		return "", "", err
	}
	for i := len(ids) - 1; i >= 0; i-- {
		operation, err := s.store.GetOperation(ctx, ids[i])
		if err != nil {
			return "", "", err
		}
		switch operation.State {
		case model.OperationQueued, model.OperationRunning:
		default:
			continue
		}
		phase, planID := operationPhase(operation, project)
		return phase, planID, nil
	}
	return "持续创作中", "", nil
}

// operationPhase 推导进行中的环节文案：章节任务带章号与 Plan 节点，其余用种类登记的文案。
func operationPhase(operation model.Operation, project projectdoc.Snapshot) (string, string) {
	spec, err := model.KindSpec(operation.Kind)
	if err != nil {
		return "持续创作中", ""
	}
	input, err := model.DecodeTaskInput(operation.Kind, operation.Input)
	if err != nil {
		return spec.Label, ""
	}
	switch input := input.(type) {
	case *model.WriteChapterInput:
		return fmt.Sprintf("正在落笔第 %d 章", input.ChapterNumber), input.ChapterPlanID
	case *model.RewriteChapterInput:
		for _, chapter := range project.Manuscript {
			if chapter.ID == input.ChapterID {
				return fmt.Sprintf("正在按意见重写第 %d 章", chapter.Number), chapter.PlanNodeID
			}
		}
	case *model.ReviewRangeInput:
		var numbers []int
		for _, chapter := range project.Manuscript {
			if slices.Contains(input.ChapterIDs, chapter.ID) {
				numbers = append(numbers, chapter.Number)
			}
		}
		verb := "审阅"
		if len(input.Reviewed) > 0 || len(input.PriorFindings) > 0 {
			verb = "复审" // D68：上一轮审过，这次核对改动
		}
		switch len(numbers) {
		case 0:
		case 1:
			return fmt.Sprintf("正在%s第 %d 章", verb, numbers[0]), ""
		default:
			return fmt.Sprintf("正在%s第 %d–%d 章", verb, slices.Min(numbers), slices.Max(numbers)), ""
		}
	}
	return spec.Label, ""
}

// WorkbenchFinding 是带稳定标识的审阅发现：用户按 ID 接受（D43）。
type WorkbenchFinding struct {
	ID string `json:"id"`
	model.ReviewFinding
}

// validFindings 返回各章当前裁定里未被接受的审阅发现（按章节顺序、每份裁定一次），
// 以及仍然有效的接受记录。"当前裁定"与协调器共用 novel.CurrentVerdicts，两处不分叉。
func (s *Query) validFindings(
	ctx context.Context, project projectdoc.Snapshot, current map[string]*novel.StoredVerdict,
) ([]WorkbenchFinding, []model.Adjudication, error) {
	adjudications, err := s.reviews.ValidAdjudications(ctx, project)
	if err != nil {
		return nil, nil, err
	}
	accepted := model.AcceptedFindings(adjudications)
	var effective []model.Adjudication
	for _, record := range adjudications {
		if _, ok := accepted[record.Finding]; ok && record.Finding != "" {
			effective = append(effective, record)
		}
	}
	written := make(map[string]string, len(project.Manuscript)) // plan node id → 正文章节 id
	for _, chapter := range project.Manuscript {
		written[chapter.PlanNodeID] = chapter.ID
	}
	seen := make(map[string]bool)
	var findings []WorkbenchFinding
	for _, plan := range model.ChapterPlansInOrder(project.Plan) {
		stored := current[written[plan.ID]]
		if stored == nil || seen[stored.Key] {
			continue
		}
		seen[stored.Key] = true
		for index, finding := range stored.Verdict.Findings {
			id := model.FindingID(stored.Key, index)
			if _, ok := accepted[id]; ok && finding.Severity == model.FindingBlocking {
				continue
			}
			findings = append(findings, WorkbenchFinding{ID: id, ReviewFinding: finding})
		}
	}
	return findings, effective, nil
}

// buildOutline 组装大纲树（先序，按 parent、再按 order 与 ID 排序，与章号同一口径）并
// 推导章节呈现状态；proposed 是来自等你确认的方案、尚未入库的节点。
func buildOutline(
	plan []model.PlanNode, proposed map[string]struct{}, manuscript []model.ManuscriptChapter,
	candidates []ChapterCandidate, writingPlanID string,
) []OutlineNode {
	confirmed := make(map[string]int, len(manuscript)) // plan node id → 章节号
	for _, chapter := range manuscript {
		confirmed[chapter.PlanNodeID] = chapter.Number
	}
	pending := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		pending[candidate.Chapter.PlanNodeID] = struct{}{}
	}

	children := make(map[string][]model.PlanNode)
	for _, node := range plan {
		children[node.ParentID] = append(children[node.ParentID], node)
	}
	for parent := range children {
		slices.SortFunc(children[parent], func(a, b model.PlanNode) int {
			return cmp.Or(a.Order-b.Order, strings.Compare(a.ID, b.ID))
		})
	}

	var outline []OutlineNode
	chapterNumber := 0
	var walk func(parentID string)
	walk = func(parentID string) {
		for _, node := range children[parentID] {
			entry := OutlineNode{Node: node, Proposed: hasKey(proposed, node.ID)}
			if node.Kind == model.PlanChapter {
				chapterNumber++
				entry.Number = chapterNumber
				switch {
				case hasKey(pending, node.ID):
					entry.State = ChapterPending
					entry.Detail = "候选稿等你验收"
				case confirmed[node.ID] > 0:
					entry.State = ChapterConfirmed
				case node.ID == writingPlanID:
					entry.State = ChapterInProgress
					entry.Detail = "正在落笔"
				default:
					entry.State = ChapterPlanned
				}
				if entry.Proposed && entry.State == ChapterPlanned {
					entry.Detail = "方案待你确认"
				}
			}
			outline = append(outline, entry)
			walk(node.ID)
		}
	}
	walk("")
	return outline
}

// proposedPlan 把等你确认的方案里的大纲改动投到当前大纲上，确认前就能在大纲里逐章
// 浏览；过期稿件不投。
func proposedPlan(plan []model.PlanNode, decision *PendingDecision) ([]model.PlanNode, map[string]struct{}, error) {
	proposed := make(map[string]struct{})
	if decision == nil || !decision.HasProposal || decision.Stale {
		return plan, proposed, nil
	}
	byID := make(map[string]model.PlanNode, len(plan))
	for _, node := range plan {
		byID[node.ID] = node
	}
	for _, patch := range decision.Proposal.Patches {
		if patch.Document.Kind != model.DocumentPlan {
			continue
		}
		if patch.Operation != model.PatchPut {
			delete(byID, patch.Document.ID)
			continue
		}
		var node model.PlanNode
		if err := json.Unmarshal(patch.Content, &node); err != nil {
			return nil, nil, fmt.Errorf("decode proposed plan node %q: %w", patch.Document.ID, err)
		}
		byID[node.ID], proposed[node.ID] = node, struct{}{}
	}
	return slices.Collect(maps.Values(byID)), proposed, nil
}

func hasKey(set map[string]struct{}, id string) bool {
	_, ok := set[id]
	return ok
}
