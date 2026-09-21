package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

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
}

type WorkbenchSnapshot struct {
	ProjectID string                `json:"project_id"`
	Revision  model.Revision        `json:"revision"`
	Intent    model.Intent          `json:"intent"`
	Approval  model.ApprovalPolicy  `json:"approval,omitempty"`
	Ownership []model.OwnershipRule `json:"ownership,omitempty"`
	// Directives 只呈现 active 的用户创作要求（§4.9）。
	Directives []model.Directive         `json:"directives,omitempty"`
	Canon      []model.CanonFact         `json:"canon,omitempty"`
	Manuscript []model.ManuscriptChapter `json:"manuscript,omitempty"`
	Outline    []OutlineNode             `json:"outline"`
	Candidates []ChapterCandidate        `json:"candidates,omitempty"`
	// Run 是最近一轮创作（含终态，落点呈现）；nil 表示还没开始过。
	Run *model.CreationRun `json:"run,omitempty"`
	// CurrentPhase 是进行中的环节（创作语言），仅 Run 非终态时非空。
	CurrentPhase string           `json:"current_phase,omitempty"`
	Decision     *PendingDecision `json:"decision,omitempty"`
	// Findings 是基线仍成立的最新审阅发现（D48）中尚未被用户接受的部分；
	// Adjudications 是仍然有效的接受记录（D43）。
	Findings      []WorkbenchFinding   `json:"findings,omitempty"`
	Adjudications []model.Adjudication `json:"adjudications,omitempty"`
	// PendingCanon 是正文改动后待核验的事实 ID（D41）。
	PendingCanon []string `json:"pending_canon,omitempty"`
	NextStep     string   `json:"next_step,omitempty"`
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
		Canon:      project.Canon, Manuscript: project.Manuscript,
	}

	run, hasRun, err := s.runs.LatestCreationRun(ctx, projectID)
	if err != nil {
		return WorkbenchSnapshot{}, err
	}
	writingPlanID := ""
	if hasRun {
		snapshot.Run = &run
		if snapshot.Candidates, snapshot.Decision, err = s.workbenchDecision(ctx, run); err != nil {
			return WorkbenchSnapshot{}, err
		}
		if snapshot.CurrentPhase, writingPlanID, err = s.workbenchPhase(ctx, run, project); err != nil {
			return WorkbenchSnapshot{}, err
		}
	}
	if snapshot.Findings, snapshot.Adjudications, err = s.validFindings(ctx, project); err != nil {
		return WorkbenchSnapshot{}, err
	}
	for _, gap := range novel.CanonGaps(project) {
		snapshot.PendingCanon = append(snapshot.PendingCanon, gap.Pending...)
	}
	snapshot.Outline = buildOutline(project, snapshot.Candidates, writingPlanID)
	snapshot.NextStep = workbenchNextStep(snapshot)
	return snapshot, nil
}

// workbenchDecision 提取待确认候选与决定卡：Run 等待用户时才有。
func (s *Query) workbenchDecision(
	ctx context.Context, run model.CreationRun,
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
	relocated, _, err := s.decisions.RelocateProposal(ctx, proposal)
	if errors.Is(err, model.ErrRevisionConflict) {
		decision.Stale = true
		return nil, decision, nil
	}
	if err != nil {
		return nil, nil, err
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
	}
	return spec.Label, ""
}

// WorkbenchFinding 是带稳定标识的审阅发现：用户按 ID 接受（D43）。
type WorkbenchFinding struct {
	ID string `json:"id"`
	model.ReviewFinding
}

// validFindings 返回基线仍成立的最新审阅发现里未被接受的部分，以及仍然有效的接受记录。
// 有效性与"最新"的裁决规则同协调器（listVerdicts/latestVerdict），两处不分叉。
func (s *Query) validFindings(ctx context.Context, project projectdoc.Snapshot) ([]WorkbenchFinding, []model.Adjudication, error) {
	verdicts, err := s.reviews.ListVerdicts(ctx, project)
	if err != nil {
		return nil, nil, err
	}
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
	latest := novel.LatestStoredVerdict(verdicts, func(model.ReviewVerdict) bool { return true })
	if latest == nil {
		return nil, effective, nil
	}
	var findings []WorkbenchFinding
	for index, finding := range latest.Verdict.Findings {
		id := model.FindingID(latest.Key, index)
		if _, ok := accepted[id]; ok && finding.Severity == model.FindingBlocking {
			continue
		}
		findings = append(findings, WorkbenchFinding{ID: id, ReviewFinding: finding})
	}
	return findings, effective, nil
}

// buildOutline 组装大纲树（先序，按 parent/order 排序）并推导章节呈现状态。
func buildOutline(project projectdoc.Snapshot, candidates []ChapterCandidate, writingPlanID string) []OutlineNode {
	confirmed := make(map[string]int, len(project.Manuscript)) // plan node id → 章节号
	for _, chapter := range project.Manuscript {
		confirmed[chapter.PlanNodeID] = chapter.Number
	}
	pending := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		pending[candidate.Chapter.PlanNodeID] = struct{}{}
	}

	children := make(map[string][]model.PlanNode)
	for _, node := range project.Plan {
		children[node.ParentID] = append(children[node.ParentID], node)
	}
	for parent := range children {
		slices.SortStableFunc(children[parent], func(a, b model.PlanNode) int { return a.Order - b.Order })
	}

	var outline []OutlineNode
	chapterNumber := 0
	var walk func(parentID string)
	walk = func(parentID string) {
		for _, node := range children[parentID] {
			entry := OutlineNode{Node: node}
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
			}
			outline = append(outline, entry)
			walk(node.ID)
		}
	}
	walk("")
	return outline
}

func hasKey(set map[string]struct{}, id string) bool {
	_, ok := set[id]
	return ok
}

// workbenchNextStep 生成创作语言的下一步引导（键名属于产品键位表，随文案下发）。
func workbenchNextStep(snapshot WorkbenchSnapshot) string {
	if snapshot.CurrentPhase != "" {
		return ""
	}
	if snapshot.Decision != nil {
		if snapshot.Decision.HasProposal {
			return "有稿件待验收。进入 F3 审阅后，输入 y 回车通过，或提交修改意见。"
		}
		return "创作在等你的决定：" + snapshot.Decision.Reason
	}
	if snapshot.Run == nil {
		return "输入 /continue 回车开始创作。"
	}
	switch snapshot.Run.State {
	case model.RunFailed:
		return "输入 /diag 查看诊断，/continue 重试。"
	case model.RunCancelled:
		return "创作已取消。输入 /continue 从现有内容继续。"
	case model.RunCompleted:
		return "续写：/goal <总章数>，回车即继续。"
	case model.RunPaused:
		return "创作已暂停。输入 /continue 回车继续。"
	}
	return ""
}
