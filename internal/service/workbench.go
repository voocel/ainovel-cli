package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

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
	Node   domain.PlanNode `json:"node"`
	Number int             `json:"number,omitempty"` // 章节序号，仅 chapter
	State  ChapterState    `json:"state,omitempty"`
	Detail string          `json:"detail,omitempty"` // 进行中细分：正在落笔/正在按意见重写
}

// ChapterCandidate 是待确认的章节候选稿及其血统。
type ChapterCandidate struct {
	OperationID  string                   `json:"operation_id"`
	BaseRevision domain.Revision          `json:"base_revision"`
	Chapter      domain.ManuscriptChapter `json:"chapter"`
}

// PendingDecision 是决定卡数据源：等待原因 + 可选的待裁决稿件。
// Stale 表示稿件基线已落后于当前 Revision：过期候选不得伪装成当前稿件（§4），
// 直接通过会撞版本冲突，应引导按意见重写或继续创作走后继迁移。
type PendingDecision struct {
	Reason      string          `json:"reason"`
	Proposal    domain.Proposal `json:"proposal"`
	HasProposal bool            `json:"has_proposal"`
	Stale       bool            `json:"stale,omitempty"`
}

type WorkbenchSnapshot struct {
	ProjectID string                 `json:"project_id"`
	Revision  domain.Revision        `json:"revision"`
	Intent    domain.Intent          `json:"intent"`
	Approval  domain.ApprovalPolicy  `json:"approval,omitempty"`
	Ownership []domain.OwnershipRule `json:"ownership,omitempty"`
	// Directives 只呈现 active 的用户创作要求（§4.9）。
	Directives []domain.Directive         `json:"directives,omitempty"`
	Canon      []domain.CanonFact         `json:"canon,omitempty"`
	Manuscript []domain.ManuscriptChapter `json:"manuscript,omitempty"`
	Outline    []OutlineNode              `json:"outline"`
	Candidates []ChapterCandidate         `json:"candidates,omitempty"`
	// Run 是最近一轮创作（含终态，落点呈现）；nil 表示还没开始过。
	Run *domain.CreationRun `json:"run,omitempty"`
	// CurrentPhase 是进行中的环节（创作语言），仅 Run 非终态时非空。
	CurrentPhase string           `json:"current_phase,omitempty"`
	Decision     *PendingDecision `json:"decision,omitempty"`
	// Findings 是基线仍成立的最新审阅发现（D48）中尚未被用户接受的部分；
	// Adjudications 是仍然有效的接受记录（D43）。
	Findings      []WorkbenchFinding    `json:"findings,omitempty"`
	Adjudications []domain.Adjudication `json:"adjudications,omitempty"`
	// PendingCanon 是正文改动后待核验的事实 ID（D41）。
	PendingCanon []string `json:"pending_canon,omitempty"`
	NextStep     string   `json:"next_step,omitempty"`
}

func (s *Service) WorkbenchSnapshot(ctx context.Context, projectID string) (WorkbenchSnapshot, error) {
	project, err := s.Project(ctx, projectID, domain.InitialRevision)
	if err != nil {
		return WorkbenchSnapshot{}, err
	}
	snapshot := WorkbenchSnapshot{
		ProjectID: project.ID, Revision: project.Revision,
		Intent: project.Intent, Approval: project.Approval, Ownership: project.Ownership,
		Directives: domain.ActiveDirectives(project.Directives),
		Canon:      project.Canon, Manuscript: project.Manuscript,
	}

	run, hasRun, err := s.LatestCreationRun(ctx, projectID)
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
	for _, gap := range canonGaps(project) {
		snapshot.PendingCanon = append(snapshot.PendingCanon, gap.Pending...)
	}
	snapshot.Outline = buildOutline(project, snapshot.Candidates, writingPlanID)
	snapshot.NextStep = workbenchNextStep(snapshot)
	return snapshot, nil
}

// workbenchDecision 提取待确认候选与决定卡：Run 等待用户时才有。
func (s *Service) workbenchDecision(
	ctx context.Context, run domain.CreationRun,
) ([]ChapterCandidate, *PendingDecision, error) {
	if run.State != domain.RunWaitingUser {
		return nil, nil, nil
	}
	proposal, ok, err := s.WaitingProposal(ctx, run.ID)
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
	relocated, _, err := s.relocateProposal(ctx, proposal)
	if errors.Is(err, store.ErrRevisionConflict) {
		decision.Stale = true
		return nil, decision, nil
	}
	if err != nil {
		return nil, nil, err
	}
	proposal = relocated
	var candidates []ChapterCandidate
	for _, patch := range proposal.Patches {
		if patch.Document.Kind != domain.DocumentManuscript || patch.Operation != domain.PatchPut {
			continue
		}
		var chapter domain.ManuscriptChapter
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
func (s *Service) workbenchPhase(
	ctx context.Context, run domain.CreationRun, project ProjectSnapshot,
) (string, string, error) {
	if run.State != domain.RunRunning {
		return "", "", nil
	}
	ids, err := s.runOperationIDs(ctx, run.ID)
	if err != nil {
		return "", "", err
	}
	for i := len(ids) - 1; i >= 0; i-- {
		operation, err := s.store.GetOperation(ctx, ids[i])
		if err != nil {
			return "", "", err
		}
		switch operation.State {
		case domain.OperationQueued, domain.OperationRunning:
		default:
			continue
		}
		phase, planID := operationPhase(operation, project)
		return phase, planID, nil
	}
	return "持续创作中", "", nil
}

// operationPhase 推导进行中的环节文案：章节任务带章号与 Plan 节点，其余用种类登记的文案。
func operationPhase(operation domain.Operation, project ProjectSnapshot) (string, string) {
	spec, err := domain.KindSpec(operation.Kind)
	if err != nil {
		return "持续创作中", ""
	}
	input, err := domain.DecodeTaskInput(operation.Kind, operation.Input)
	if err != nil {
		return spec.Label, ""
	}
	switch input := input.(type) {
	case *domain.WriteChapterInput:
		return fmt.Sprintf("正在落笔第 %d 章", input.ChapterNumber), input.ChapterPlanID
	case *domain.RewriteChapterInput:
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
	domain.ReviewFinding
}

// validFindings 返回基线仍成立的最新审阅发现里未被接受的部分，以及仍然有效的接受记录。
// 有效性与"最新"的裁决规则同协调器（listVerdicts/latestVerdict），两处不分叉。
func (s *Service) validFindings(ctx context.Context, project ProjectSnapshot) ([]WorkbenchFinding, []domain.Adjudication, error) {
	verdicts, err := s.listVerdicts(ctx, project)
	if err != nil {
		return nil, nil, err
	}
	adjudications, err := s.validAdjudications(ctx, project)
	if err != nil {
		return nil, nil, err
	}
	accepted := domain.AcceptedFindings(adjudications)
	var effective []domain.Adjudication
	for _, record := range adjudications {
		if _, ok := accepted[record.Finding]; ok && record.Finding != "" {
			effective = append(effective, record)
		}
	}
	latest := latestStoredVerdict(verdicts, func(domain.ReviewVerdict) bool { return true })
	if latest == nil {
		return nil, effective, nil
	}
	var findings []WorkbenchFinding
	for index, finding := range latest.verdict.Findings {
		id := domain.FindingID(latest.key, index)
		if _, ok := accepted[id]; ok && finding.Severity == domain.FindingBlocking {
			continue
		}
		findings = append(findings, WorkbenchFinding{ID: id, ReviewFinding: finding})
	}
	return findings, effective, nil
}

// buildOutline 组装大纲树（先序，按 parent/order 排序）并推导章节呈现状态。
func buildOutline(project ProjectSnapshot, candidates []ChapterCandidate, writingPlanID string) []OutlineNode {
	confirmed := make(map[string]int, len(project.Manuscript)) // plan node id → 章节号
	for _, chapter := range project.Manuscript {
		confirmed[chapter.PlanNodeID] = chapter.Number
	}
	pending := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		pending[candidate.Chapter.PlanNodeID] = struct{}{}
	}

	children := make(map[string][]domain.PlanNode)
	for _, node := range project.Plan {
		children[node.ParentID] = append(children[node.ParentID], node)
	}
	for parent := range children {
		slices.SortStableFunc(children[parent], func(a, b domain.PlanNode) int { return a.Order - b.Order })
	}

	var outline []OutlineNode
	chapterNumber := 0
	var walk func(parentID string)
	walk = func(parentID string) {
		for _, node := range children[parentID] {
			entry := OutlineNode{Node: node}
			if node.Kind == domain.PlanChapter {
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
			return "有稿件等你验收：按 y 确认通过，或输入修改意见让它重写。"
		}
		return "创作在等你的决定：" + snapshot.Decision.Reason
	}
	if snapshot.Run == nil {
		return "这本书还没开始创作。按 c 开始：先拟故事蓝图，再逐章写作与审阅。"
	}
	switch snapshot.Run.State {
	case domain.RunFailed:
		return "上一轮创作没能完成。按 d 看原始诊断，按 c 重试继续。"
	case domain.RunCancelled:
		return "上一轮创作已取消。按 c 开启新一轮，从现有内容继续。"
	case domain.RunCompleted:
		return "全书已完成。想继续写：按 g 提高目标章数，再按 c 继续。"
	case domain.RunPaused:
		return "创作已暂停。按 c 继续。"
	}
	return ""
}
