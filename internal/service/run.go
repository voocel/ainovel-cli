package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// 创作协调器（D27）：从 Project 状态确定性推导下一个 Operation 并驱动 CreationRun，
// 直到完成契约满足（D29）或需要用户介入。协调器只做推导与验证，不生产任何内容。

// stepClock 产生严格递增的时间戳，源是 Service 注入的时钟。一轮驱动可能真实
// 耗时数分钟（模型执行），时间戳必须跟随真实时钟：租约有效期按真实时间校验，
// 纯逻辑时钟会让后创建的 Operation 拿到生来即过期的租约（心跳续租首跳即失败）。
type stepClock struct {
	now  func() time.Time
	last time.Time
}

func (c *stepClock) next() time.Time {
	now := c.now()
	if !now.After(c.last) {
		now = c.last.Add(time.Millisecond)
	}
	c.last = now
	return now
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
)

type workItem struct {
	kind      workKind
	covered   int             // extend：已覆盖章节数
	number    int             // chapter/rewrite：章节序号
	plan      domain.PlanNode // chapter/rewrite：章节计划
	chapterID string          // rewrite：目标正文章节
	chapters  []string        // review：范围内全部正文章节
	notes     []string        // rewrite：审阅意见；extend：上一窗口的审阅意见
	revision  domain.Revision // review/rewrite：绑定的 Revision
	// verifyIntent 标记终审（§6.4 完成条件 3）：pass 必须携带逐项意图核验声明；
	// 阶段审阅不要求（必须出现的要素可能落在后续章节）。
	verifyIntent bool
	// directives 是命中本任务作用域的用户要求（§4.9），随任务输入进入指令平面。
	directives []domain.Directive
}

func (s *Service) driveCreationRun(
	ctx context.Context,
	run domain.CreationRun,
	command QuickWriteCommand,
	result QuickWriteResult,
) (QuickWriteResult, error) {
	clock := &stepClock{now: s.now, last: command.CreatedAt}
	if run.State == domain.RunWaitingUser || run.State == domain.RunPaused {
		resumed, err := s.store.TransitionCreationRun(ctx, run.ID, run.State, domain.RunRunning, "继续创作", 0, clock.next())
		if err != nil {
			return result, err
		}
		run = resumed
	}
	base := runBaseCommand(run, command)
	var lastSettled string
	for {
		project, err := s.Project(ctx, run.ProjectID, domain.InitialRevision)
		if err != nil {
			return result, err
		}
		// 用户可能在上一步执行期间暂停/取消（乐观并发）：用户裁决优先，
		// 本轮就此优雅收束，不再创建新的 Operation。
		current, err := s.store.GetCreationRun(ctx, run.ID)
		if err != nil {
			return result, err
		}
		if current.State != domain.RunRunning {
			return s.finishQuickResult(ctx, current, project, result)
		}
		run = current
		planCount := len(chapterPlansInOrder(project.Plan))
		if planCount > run.Goal.TargetChapters {
			reason := fmt.Sprintf(
				"蓝图包含 %d 个有效章节节点，但目标是 %d 章；请先裁剪蓝图或调整目标",
				planCount, run.Goal.TargetChapters,
			)
			run, settleErr := s.settleRun(ctx, run, domain.RunWaitingUser, reason, 0, clock.next())
			if settleErr != nil {
				return s.attachRun(run, result), settleErr
			}
			return s.finishQuickResult(ctx, run, project, result)
		}
		var item workItem
		var reviewScope []string
		unmet := completionUnmet(project, run.Goal)
		if unmet == "" {
			// 完成契约（§6.4）：内容齐全不等于完成，必须有绑定当前 Revision 的
			// 审阅通过证据；正文再变化时旧证据随 Revision 键自动失效。
			reviewScope = goalManuscriptIDs(project, run.Goal)
		} else {
			var ok bool
			if item, ok = nextWorkItem(project, run.Goal); !ok {
				return result, fmt.Errorf("creation run %q has unmet goal but no work item: %w", run.ID, domain.ErrInvalid)
			}
			// 先审后扩（§6.3）：扩窗前必须有覆盖当前窗口、绑定当前 Revision 的
			// 阶段审阅证据，让每段新规划都建立在已审阅的正文之上。
			if item.kind == workExtendPlan {
				reviewScope = manuscriptIDsUpTo(project, item.covered)
			}
		}
		if reviewScope != nil {
			verdict, reviewItem, err := s.resolveReviewFor(ctx, run.ID, run.ProjectID, project.Revision, reviewScope)
			if err != nil {
				run, settleErr := s.settleRun(ctx, run, domain.RunFailed, "审阅结果缺失或损坏，需要人工检查", 0, clock.next())
				return s.attachRun(run, result), errors.Join(err, settleErr)
			}
			switch {
			case verdict != nil && verdict.Status == domain.ReviewPass && unmet == "":
				// 完成条件 3（§6.4）：Intent 的必须出现/禁止出现/结局方向与用户要求
				// 必须被显式核验为满足——笼统的 pass 不构成完成证据。
				if !verdict.IntentSatisfied() || !verdict.DirectivesSatisfied() {
					reason := "审阅通过但未逐项核验意图或用户要求，需要人工检查"
					run, settleErr := s.settleRun(ctx, run, domain.RunFailed, reason, 0, clock.next())
					return s.attachRun(run, result), errors.Join(fmt.Errorf("%s: %w", reason, store.ErrStateConflict), settleErr)
				}
				run, err = s.settleRun(ctx, run, domain.RunCompleted,
					fmt.Sprintf("全书 %d 章完成并通过审阅", run.Goal.TargetChapters), project.Revision, clock.next())
				if err != nil {
					return s.attachRun(run, result), err
				}
				return s.finishQuickResult(ctx, run, project, result)
			case verdict != nil && verdict.Status == domain.ReviewPass:
				// 窗口审阅通过：意见随扩窗输入进入下一段规划（更新上下文 → 继续规划）。
				item.notes = verdictNotes(*verdict)
			case verdict != nil:
				repairs, err := s.autoRepairCount(ctx, run.ID)
				if err != nil {
					return result, err
				}
				if repairs >= run.Strategy.AutoRepairBudget {
					reason := fmt.Sprintf(
						"自动修订预算 %d 次已用尽，需要你查看审阅意见后决断",
						run.Strategy.AutoRepairBudget,
					)
					run, settleErr := s.settleRun(ctx, run, domain.RunWaitingUser, reason, 0, clock.next())
					if settleErr != nil {
						return s.attachRun(run, result), settleErr
					}
					return s.finishQuickResult(ctx, run, project, result)
				}
				item = rewriteWorkItem(project, *verdict)
				if item.chapterID == "" {
					reason := "审阅发现指向了不存在的章节，需要人工检查"
					run, settleErr := s.settleRun(ctx, run, domain.RunFailed, reason, 0, clock.next())
					return s.attachRun(run, result), errors.Join(fmt.Errorf("%s: %w", reason, store.ErrStateConflict), settleErr)
				}
			default:
				reviewItem.verifyIntent = unmet == ""
				item = reviewItem
			}
		}
		item.directives = coveringDirectives(project, item)
		operationCommand, err := itemOperationCommand(base, run, item, clock.next())
		if err != nil {
			return result, err
		}
		operation, err := s.ensureChainedOperation(ctx, run.ID, operationCommand)
		if err != nil {
			return result, err
		}
		if operation.State == domain.OperationQueued {
			runResult, err := s.RunOperation(ctx, operation.ID, command.WorkerID, command.LeaseDuration, clock.next())
			if runResult.Operation.ID != "" {
				operation = runResult.Operation
			}
			// stale 不是失败（§6.3）：基线在执行期间变化时由下一轮安全迁移到后继。
			if err != nil && operation.State != domain.OperationStale {
				run, settleErr := s.settleRun(ctx, run, domain.RunFailed, itemFailureReason(item, operation.ID), 0, clock.next())
				return s.attachRun(run, result), errors.Join(err, settleErr)
			}
		}
		switch operation.State {
		case domain.OperationSucceeded:
			// 同一个已完成 Operation 第二次出现说明目标没有被它推进（例如规划
			// 已定稿但章节数不足）：确定性失败，绝不无限重试。
			if operation.ID == lastSettled {
				reason := itemStuckReason(item)
				run, settleErr := s.settleRun(ctx, run, domain.RunFailed, reason, 0, clock.next())
				return s.attachRun(run, result), errors.Join(fmt.Errorf("%s: %w", reason, store.ErrStateConflict), settleErr)
			}
			lastSettled = operation.ID
		case domain.OperationAwaitingApproval:
			// 无人值守的等待（D26）：不是失败，Run 停在这里等用户裁决。
			run, settleErr := s.settleRun(ctx, run, domain.RunWaitingUser, itemWaitingReason(item), 0, clock.next())
			if settleErr != nil {
				return s.attachRun(run, result), settleErr
			}
			result.WaitingOperationID = operation.ID
			return s.finishQuickResult(ctx, run, project, result)
		case domain.OperationPaused:
			run, settleErr := s.settleRun(ctx, run, domain.RunPaused, "创作已暂停，恢复该任务后再继续", 0, clock.next())
			if settleErr != nil {
				return s.attachRun(run, result), settleErr
			}
			return s.finishQuickResult(ctx, run, project, result)
		case domain.OperationFailed:
			reason := itemFailureReason(item, operation.ID)
			run, settleErr := s.settleRun(ctx, run, domain.RunFailed, reason, 0, clock.next())
			return s.attachRun(run, result), errors.Join(fmt.Errorf("%s: %w", reason, store.ErrStateConflict), settleErr)
		case domain.OperationCancelled:
			run, settleErr := s.settleRun(ctx, run, domain.RunCancelled, "创作任务被取消", 0, clock.next())
			if settleErr != nil {
				return s.attachRun(run, result), settleErr
			}
			return s.finishQuickResult(ctx, run, project, result)
		case domain.OperationStale:
			// 控制收紧或基线漂移导致失效：下一轮由 ensureChainedOperation 安全迁移到后继。
		case domain.OperationRunning:
			return result, fmt.Errorf(
				"operation %q is held by another worker lease: %w", operation.ID, store.ErrStateConflict)
		}
	}
}

// ensureChainedOperation 解析创作槽位当前有效的 Operation：沿确定性后继链
// （base、base:r2、base:r3…）找到第一个可用者；失效或被否决的前任由后继继承
// 工作区（§5.5 安全迁移），failed 在用户显式重新发起时先尝试原地重排。
func (s *Service) ensureChainedOperation(
	ctx context.Context,
	runID string,
	command StartOperationCommand,
) (domain.Operation, error) {
	baseID := command.OperationID
	var predecessor domain.Operation
	for attempt := 1; ; attempt++ {
		id := chainID(baseID, attempt)
		operation, err := s.store.GetOperation(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			// Run 归属与血统事件由存储层在创建事务内一并落盘（§6.3），无孤儿窗口。
			var created domain.Operation
			if predecessor.ID == "" {
				command.OperationID, command.RunID = id, runID
				created, err = s.StartOperation(ctx, command)
			} else {
				created, err = s.RestartOperation(ctx, RestartOperationCommand{
					FromOperationID: predecessor.ID, OperationID: id, Input: command.Input,
					Packs: command.Packs, CreatorProfiles: command.CreatorProfiles,
					CoreProtocolVersion: command.CoreProtocolVersion,
					ApprovalPolicy:      command.ApprovalPolicy,
					RunID:               runID,
					CreatedAt:           command.CreatedAt,
				})
			}
			if err != nil {
				return domain.Operation{}, err
			}
			return created, nil
		}
		if err != nil {
			return domain.Operation{}, err
		}
		if operation.Target.Kind != domain.AuthorityProject || operation.Target.ID != command.ProjectID ||
			operation.Kind != command.Kind || operation.RunID != runID {
			return domain.Operation{}, fmt.Errorf("operation %q identity changed: %w", id, store.ErrIdempotencyConflict)
		}
		switch operation.State {
		case domain.OperationStale, domain.OperationCancelled:
			predecessor = operation
		case domain.OperationFailed:
			resumed, err := s.ResumeOperation(ctx, operation.ID, command.CreatedAt)
			if err == nil {
				return resumed, nil
			}
			if !errors.Is(err, store.ErrStateConflict) {
				return domain.Operation{}, err
			}
			// 被否决的候选不能原地续跑：由后继带着否决前的工作区重写。
			predecessor = operation
		default:
			return operation, nil
		}
	}
}

func chainID(base string, attempt int) string {
	if attempt == 1 {
		return base
	}
	return fmt.Sprintf("%s:r%d", base, attempt)
}

// latestChainOperation 只读地取槽位后继链上最新的 Operation，用于结果汇报。
func (s *Service) latestChainOperation(
	ctx context.Context,
	baseID string,
) (domain.Operation, bool, error) {
	var found domain.Operation
	ok := false
	for attempt := 1; ; attempt++ {
		operation, err := s.store.GetOperation(ctx, chainID(baseID, attempt))
		if errors.Is(err, store.ErrNotFound) {
			return found, ok, nil
		}
		if err != nil {
			return domain.Operation{}, false, err
		}
		found, ok = operation, true
	}
}

// completionUnmet 是完成契约的最小落点（D29）：在同一 Revision 上验证蓝图覆盖
// 目标章数且每章有正文。队列为空不等于完成。返回空串表示契约满足。
func completionUnmet(project ProjectSnapshot, goal domain.CreationRunGoal) string {
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

func nextWorkItem(project ProjectSnapshot, goal domain.CreationRunGoal) (workItem, bool) {
	plans := chapterPlansInOrder(project.Plan)
	if len(plans) == 0 {
		return workItem{kind: workDevelopPlan}, true
	}
	// 先写满当前窗口再扩窗（§6.3）：窗口末尾的阶段审阅意见要能流进下一段规划。
	covered := min(len(plans), goal.TargetChapters)
	written := manuscriptsByPlanNode(project.Manuscript)
	for index, plan := range plans[:covered] {
		if _, ok := written[plan.ID]; !ok {
			return workItem{kind: workWriteChapter, number: index + 1, plan: plan}, true
		}
	}
	if len(plans) < goal.TargetChapters {
		return workItem{kind: workExtendPlan, covered: covered}, true
	}
	return workItem{}, false
}

// storedVerdict 是一条已校验的裁定及其派生记录坐标（选"最新"用）。
type storedVerdict struct {
	verdict   domain.ReviewVerdict
	key       string
	createdAt time.Time
}

// listVerdicts 取绑定 revision 的全部有效裁定：操作必须已成功、内容可解码且
// 结构合法——缺失或损坏是数据错误，一律上抛，不得静默当作"没有裁定"。
// 协调器与工作台共用，保证两处"最新裁定"语义一致。
func (s *Service) listVerdicts(
	ctx context.Context, projectID string, revision domain.Revision,
) ([]storedVerdict, error) {
	derived, err := s.store.ListDerivedDocuments(ctx, projectID, revision)
	if err != nil {
		return nil, err
	}
	var verdicts []storedVerdict
	for _, document := range derived {
		if document.Kind != domain.DerivedVerdictKind {
			continue
		}
		operation, err := s.store.GetOperation(ctx, document.Key)
		if err != nil {
			return nil, fmt.Errorf("review verdict %q has no operation: %w", document.Key, err)
		}
		if operation.State != domain.OperationSucceeded {
			continue
		}
		var verdict domain.ReviewVerdict
		if err := json.Unmarshal(document.Content, &verdict); err != nil {
			return nil, fmt.Errorf("decode review verdict %q: %w", document.Key, err)
		}
		if err := verdict.Validate(); err != nil {
			return nil, fmt.Errorf("review verdict %q: %w", document.Key, err)
		}
		verdicts = append(verdicts, storedVerdict{verdict: verdict, key: document.Key, createdAt: document.CreatedAt})
	}
	return verdicts, nil
}

// latestVerdict 在满足条件的裁定里选最新：CreatedAt 优先，相同则 Key 决胜。
func latestVerdict(verdicts []storedVerdict, matches func(domain.ReviewVerdict) bool) *domain.ReviewVerdict {
	var best *storedVerdict
	for index := range verdicts {
		candidate := &verdicts[index]
		if !matches(candidate.verdict) {
			continue
		}
		if best == nil || candidate.createdAt.After(best.createdAt) ||
			(candidate.createdAt.Equal(best.createdAt) && candidate.key > best.key) {
			best = candidate
		}
	}
	if best == nil {
		return nil
	}
	verdict := best.verdict
	return &verdict
}

// resolveReviewFor 解析当前 Revision 上覆盖指定正文范围的审阅证据：返回有效
// 裁定，或需要发起的审阅任务。裁定必须覆盖全部范围且绑定当前 Revision，
// 否则视为缺失；阶段审阅与终审共用 Revision 键的操作 ID，天然互不碰撞。
func (s *Service) resolveReviewFor(
	ctx context.Context,
	runID string,
	projectID string,
	revision domain.Revision,
	chapters []string,
) (*domain.ReviewVerdict, workItem, error) {
	reviewItem := workItem{kind: workReview, chapters: chapters, revision: revision}
	verdicts, err := s.listVerdicts(ctx, projectID, revision)
	if err != nil {
		return nil, workItem{}, err
	}
	matched := latestVerdict(verdicts, func(verdict domain.ReviewVerdict) bool {
		return verdictCovers(verdict, revision, chapters)
	})
	if matched != nil {
		return matched, workItem{}, nil
	}
	operation, ok, err := s.latestChainOperation(ctx, reviewOperationID(runID, revision))
	if err != nil {
		return nil, workItem{}, err
	}
	if !ok || operation.State != domain.OperationSucceeded {
		return nil, reviewItem, nil
	}
	return nil, workItem{}, fmt.Errorf(
		"review operation %q succeeded without a matching verdict: %w", operation.ID, store.ErrStateConflict)
}

func verdictCovers(verdict domain.ReviewVerdict, revision domain.Revision, chapters []string) bool {
	if verdict.Revision != revision || len(verdict.ChapterIDs) != len(chapters) {
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

func rewriteWorkItem(project ProjectSnapshot, verdict domain.ReviewVerdict) workItem {
	byID := make(map[string]domain.ManuscriptChapter, len(project.Manuscript))
	for _, chapter := range project.Manuscript {
		byID[chapter.ID] = chapter
	}
	planByID := make(map[string]domain.PlanNode, len(project.Plan))
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
		return workItem{
			kind: workRewrite, chapterID: chapterID, number: chapter.Number,
			plan: plan, notes: notes, revision: verdict.Revision,
		}
	}
	return workItem{}
}

// runOperationIDs 按事件顺序列出 Run 创建过的全部 Operation（含后继链成员）。
func (s *Service) runOperationIDs(ctx context.Context, runID string) ([]string, error) {
	events, err := s.store.ListCreationRunEvents(ctx, runID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(events))
	for _, event := range events {
		if event.Kind != domain.RunEventOperationCreated {
			continue
		}
		var payload struct {
			OperationID string `json:"operation_id"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return nil, fmt.Errorf("run %q event %d payload is corrupt: %w", runID, event.Sequence, err)
		}
		ids = append(ids, payload.OperationID)
	}
	return ids, nil
}

// autoRepairCount 统计本轮 Run 已创建的自动重写槽位。预算属于显式 RunStrategy；
// 审阅本身不设隐式轮次上限，只有实际自动修改内容才消耗预算。
func (s *Service) autoRepairCount(ctx context.Context, runID string) (int, error) {
	ids, err := s.runOperationIDs(ctx, runID)
	if err != nil {
		return 0, err
	}
	repairs := 0
	for _, id := range ids {
		operation, err := s.store.GetOperation(ctx, id)
		if err != nil {
			return 0, fmt.Errorf("run %q references operation %q: %w", runID, id, err)
		}
		if operation.Kind == domain.OperationRewriteChapter {
			repairs++
		}
	}
	return repairs, nil
}

// WaitingProposal 定位 Run 当前等待用户裁决的稿件，用于恢复落点时重建决定卡；
// ok=false 表示等待不携带稿件（例如预算用尽）。
func (s *Service) WaitingProposal(ctx context.Context, runID string) (domain.Proposal, bool, error) {
	ids, err := s.runOperationIDs(ctx, runID)
	if err != nil {
		return domain.Proposal{}, false, err
	}
	for i := len(ids) - 1; i >= 0; i-- {
		operation, err := s.store.GetOperation(ctx, ids[i])
		if err != nil {
			return domain.Proposal{}, false, err
		}
		if operation.State != domain.OperationAwaitingApproval {
			continue
		}
		proposal, err := s.store.GetProposalByOperation(ctx, operation.ID)
		if err != nil {
			return domain.Proposal{}, false, err
		}
		return proposal, true, nil
	}
	return domain.Proposal{}, false, nil
}

func reviewOperationID(runID string, revision domain.Revision) string {
	return runQuickID(runID, "review", "r"+strconv.FormatInt(int64(revision), 10))
}

func goalManuscriptIDs(project ProjectSnapshot, goal domain.CreationRunGoal) []string {
	return manuscriptIDsUpTo(project, goal.TargetChapters)
}

// manuscriptIDsUpTo 取前 count 个章节计划对应的正文 ID，按章节顺序排列。
func manuscriptIDsUpTo(project ProjectSnapshot, count int) []string {
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
func verdictNotes(verdict domain.ReviewVerdict) []string {
	var notes []string
	for _, finding := range verdict.Findings {
		notes = append(notes, finding.Note)
	}
	return notes
}

func chapterPlansInOrder(plan []domain.PlanNode) []domain.PlanNode {
	chapters := make([]domain.PlanNode, 0, len(plan))
	for _, node := range plan {
		if node.Kind == domain.PlanChapter {
			chapters = append(chapters, node)
		}
	}
	slices.SortFunc(chapters, func(left, right domain.PlanNode) int {
		if left.Order != right.Order {
			return left.Order - right.Order
		}
		return strings.Compare(left.ID, right.ID)
	})
	return chapters
}

func manuscriptsByPlanNode(manuscript []domain.ManuscriptChapter) map[string]domain.ManuscriptChapter {
	byPlan := make(map[string]domain.ManuscriptChapter, len(manuscript))
	for _, chapter := range manuscript {
		byPlan[chapter.PlanNodeID] = chapter
	}
	return byPlan
}

// runBaseCommand 不携带审批策略：策略权威在 Project 文档（§6.3），
// 由 Execution Profile 编译时解析；Run 只保留不可变预设摘要。
// coveringDirectives 装配命中本任务的用户要求（§4.9）：写/重写按目标章命中，
// 审阅取范围内各章的并集，规划面向未来取全部 active。它只做装配，不参与推导。
func coveringDirectives(project ProjectSnapshot, item workItem) []domain.Directive {
	target := func(number int, planID string) domain.DirectiveTarget {
		return domain.DirectiveTarget{ChapterNumber: number, PlanNodeIDs: domain.PlanAncestry(project.Plan, planID)}
	}
	switch item.kind {
	case workWriteChapter, workRewrite:
		return domain.ActiveDirectivesFor(project.Directives, target(item.number, item.plan.ID))
	case workReview:
		chapters := make(map[string]domain.ManuscriptChapter, len(project.Manuscript))
		for _, chapter := range project.Manuscript {
			chapters[chapter.ID] = chapter
		}
		union := make(map[string]domain.Directive)
		for _, id := range item.chapters {
			if chapter, ok := chapters[id]; ok {
				for _, directive := range domain.ActiveDirectivesFor(project.Directives, target(chapter.Number, chapter.PlanNodeID)) {
					union[directive.ID] = directive
				}
			}
		}
		merged := make([]domain.Directive, 0, len(union))
		for _, directive := range union {
			merged = append(merged, directive)
		}
		domain.SortDirectives(merged)
		return merged
	default:
		return domain.ActiveDirectives(project.Directives)
	}
}

func runBaseCommand(run domain.CreationRun, command QuickWriteCommand) StartOperationCommand {
	return StartOperationCommand{
		ProjectID: run.ProjectID, Packs: command.Packs, CreatorProfiles: command.CreatorProfiles,
		CoreProtocolVersion: "core-v1",
	}
}

func itemOperationCommand(
	base StartOperationCommand,
	run domain.CreationRun,
	item workItem,
	at time.Time,
) (StartOperationCommand, error) {
	var input []byte
	var err error
	switch item.kind {
	case workDevelopPlan:
		input, err = json.Marshal(struct {
			Intent            string `json:"intent"`
			TargetChapters    int    `json:"target_chapters"`
			RequestedChapters int    `json:"requested_chapters"`
			Goal              string `json:"goal"`
		}{
			Intent: run.Goal.Premise, TargetChapters: run.Goal.TargetChapters,
			RequestedChapters: min(run.Strategy.PlanWindowChapters, run.Goal.TargetChapters),
			Goal:              "设计可直接用于连续创作的卷、故事弧与章节节点；先展开请求数量的 chapter 节点（滚动规划的首个窗口），保持稳定 ID 和合法父子关系",
		})
		base.OperationID, base.Kind = runQuickID(run.ID, "plan"), domain.OperationDevelopPlan
	case workExtendPlan:
		input, err = json.Marshal(struct {
			Intent            string   `json:"intent"`
			TargetChapters    int      `json:"target_chapters"`
			ExistingChapters  int      `json:"existing_chapters"`
			RequestedChapters int      `json:"requested_chapters"`
			ReviewNotes       []string `json:"review_notes,omitempty"`
			Goal              string   `json:"goal"`
		}{
			Intent: run.Goal.Premise, TargetChapters: run.Goal.TargetChapters,
			ExistingChapters:  item.covered,
			RequestedChapters: min(item.covered+run.Strategy.PlanWindowChapters, run.Goal.TargetChapters),
			ReviewNotes:       item.notes,
			Goal:              "增量扩展章节计划到请求数量：保持已有节点与 ID 稳定，只补充后续 chapter 节点并挂在合法父节点下；结合上一窗口的审阅意见调整后续走向",
		})
		base.OperationID = runQuickID(run.ID, "plan", "extend", strconv.Itoa(item.covered))
		base.Kind = domain.OperationRevisePlan
	case workWriteChapter:
		input, err = json.Marshal(struct {
			ChapterPlanID string             `json:"chapter_plan_id"`
			ChapterNumber int                `json:"chapter_number"`
			Directives    []domain.Directive `json:"directives,omitempty"`
			Goal          string             `json:"goal"`
		}{
			ChapterPlanID: item.plan.ID, ChapterNumber: item.number, Directives: item.directives,
			Goal: "完成本章工作稿并提交带稳定章节 ID、稳定 block_id 与大纲依赖的正式候选；严格满足 directives 列出的每条创作要求（用户原话），字数约束按 constraints 执行",
		})
		base.OperationID, base.Kind = runQuickID(run.ID, "chapter", item.plan.ID), domain.OperationWriteChapter
	case workReview:
		goal := "阶段审阅范围内全部正文：检查跨章连续性与 Intent 方向一致性，产出结构化裁定与发现；意见将进入下一段规划"
		if item.verifyIntent {
			goal = "终审范围内全部正文：逐项核验 Intent 的必须出现、禁止出现与结局方向并在裁定中声明，检查跨章连续性；意图未满足即为阻塞发现"
		}
		if len(item.directives) > 0 {
			goal += "；逐项核验 directives 中的每条用户要求是否满足并在裁定 directives 中声明，未满足即为阻塞发现"
		}
		input, err = json.Marshal(struct {
			Range struct {
				ChapterIDs []string `json:"chapter_ids"`
			} `json:"range"`
			VerifyIntent bool               `json:"verify_intent"`
			Directives   []domain.Directive `json:"directives,omitempty"`
			Goal         string             `json:"goal"`
		}{
			Range: struct {
				ChapterIDs []string `json:"chapter_ids"`
			}{item.chapters},
			VerifyIntent: item.verifyIntent, Directives: item.directives,
			Goal: goal,
		})
		base.OperationID, base.Kind = reviewOperationID(run.ID, item.revision), domain.OperationReviewRange
	case workRewrite:
		input, err = json.Marshal(struct {
			ChapterID     string             `json:"chapter_id"`
			ChapterPlanID string             `json:"chapter_plan_id"`
			ChapterNumber int                `json:"chapter_number"`
			BaseRevision  domain.Revision    `json:"base_revision"`
			Findings      []string           `json:"findings"`
			Directives    []domain.Directive `json:"directives,omitempty"`
			Goal          string             `json:"goal"`
		}{
			ChapterID: item.chapterID, ChapterPlanID: item.plan.ID, ChapterNumber: item.number,
			BaseRevision: item.revision, Findings: item.notes, Directives: item.directives,
			Goal: "根据审阅意见重写本章：保持章节 ID 与 block 结构稳定，针对意见修改，不引入新的越界改动；严格满足 directives 列出的每条创作要求（用户原话），字数约束按 constraints 执行",
		})
		base.OperationID = runQuickID(run.ID, "rewrite", item.chapterID,
			"r"+strconv.FormatInt(int64(item.revision), 10))
		base.Kind = domain.OperationRewriteChapter
	default:
		return StartOperationCommand{}, fmt.Errorf("unknown work item kind %d: %w", item.kind, domain.ErrInvalid)
	}
	if err != nil {
		return StartOperationCommand{}, fmt.Errorf("encode %s task: %w", base.Kind, err)
	}
	base.Input, base.CreatedAt = input, at
	return base, nil
}

func itemWaitingReason(item workItem) string {
	switch item.kind {
	case workDevelopPlan:
		return "故事蓝图已拟好，等你确认后继续"
	case workExtendPlan:
		return "后续章节的蓝图已拟好，等你确认后继续"
	case workReview:
		return "审阅在等你确认后继续"
	case workRewrite:
		return fmt.Sprintf("第 %d 章《%s》已按审阅意见重写，等你过目后继续", item.number, item.plan.Title)
	default:
		return fmt.Sprintf("第 %d 章《%s》初稿完成，等你审阅后继续", item.number, item.plan.Title)
	}
}

func itemFailureReason(item workItem, operationID string) string {
	switch item.kind {
	case workDevelopPlan:
		return fmt.Sprintf("故事蓝图这次没能完成；可展开 %s 的事件记录查看原始诊断", operationID)
	case workExtendPlan:
		return fmt.Sprintf("后续章节的蓝图这次没能扩展完成；可展开 %s 的事件记录查看原始诊断", operationID)
	case workReview:
		return fmt.Sprintf("审阅这次没能完成；可展开 %s 的事件记录查看原始诊断", operationID)
	case workRewrite:
		return fmt.Sprintf("第 %d 章《%s》按审阅意见重写失败；可展开 %s 的事件记录查看原始诊断", item.number, item.plan.Title, operationID)
	default:
		return fmt.Sprintf("第 %d 章《%s》这次没能写完；可展开 %s 的事件记录查看原始诊断", item.number, item.plan.Title, operationID)
	}
}

func itemStuckReason(item workItem) string {
	switch item.kind {
	case workDevelopPlan:
		return "蓝图已定稿但没有产出任何章节节点，需要人工检查规划"
	case workExtendPlan:
		return "蓝图扩展任务已结束但章节数没有增加，需要人工检查规划"
	case workReview:
		return "审阅任务已结束但没有留下有效裁定，需要人工检查"
	case workRewrite:
		return fmt.Sprintf("第 %d 章《%s》的重写已结束但审阅意见没有解决，需要人工检查", item.number, item.plan.Title)
	default:
		return fmt.Sprintf("第 %d 章《%s》的任务已结束但正文缺失，需要人工检查", item.number, item.plan.Title)
	}
}

// settleRun 落盘 Run 的落点状态；落盘失败返回原 Run 与错误，调用方与主错误
// 一并上抛，不吞错（§6.3）。
func (s *Service) settleRun(
	ctx context.Context,
	run domain.CreationRun,
	to domain.CreationRunState,
	reason string,
	completedRevision domain.Revision,
	at time.Time,
) (domain.CreationRun, error) {
	settled, err := s.store.TransitionCreationRun(ctx, run.ID, run.State, to, reason, completedRevision, at)
	if err == nil {
		return settled, nil
	}
	// 状态冲突且现状是用户设定的暂停/取消：用户裁决优先，以其为落点不报错。
	if errors.Is(err, store.ErrStateConflict) {
		if current, getErr := s.store.GetCreationRun(ctx, run.ID); getErr == nil &&
			(current.State == domain.RunPaused || current.State == domain.RunCancelled) {
			return current, nil
		}
	}
	return run, fmt.Errorf("settle creation run %q to %s: %w", run.ID, to, err)
}

func (s *Service) CreationRunEvents(ctx context.Context, runID string) ([]domain.CreationRunEvent, error) {
	return s.store.ListCreationRunEvents(ctx, runID)
}

// RunOperations 按创建序取一轮创作的全部 Operation。诊断视图靠它下钻：
// run 事件表只有编排流水，真实的失败原因在 Operation.Error 与 operation_events。
func (s *Service) RunOperations(ctx context.Context, runID string) ([]domain.Operation, error) {
	ids, err := s.runOperationIDs(ctx, runID)
	if err != nil {
		return nil, err
	}
	operations := make([]domain.Operation, 0, len(ids))
	for _, id := range ids {
		operation, err := s.store.GetOperation(ctx, id)
		if err != nil {
			return nil, err
		}
		operations = append(operations, operation)
	}
	return operations, nil
}

type StartCreationRunCommand struct {
	RunID     string
	ProjectID string
	Goal      domain.CreationRunGoal
	Strategy  domain.CreationRunStrategy
	Preset    domain.CreationRunPreset
	CreatedAt time.Time
}

// StartCreationRun 是精细协作入口与 quick 预设共用的运行内核入口。
func (s *Service) StartCreationRun(
	ctx context.Context,
	command StartCreationRunCommand,
) (domain.CreationRun, error) {
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: command.ProjectID}
	if _, err := s.store.CurrentRevision(ctx, target); err != nil {
		return domain.CreationRun{}, err
	}
	run := domain.CreationRun{
		ID: command.RunID, ProjectID: command.ProjectID,
		Goal: command.Goal, Strategy: command.Strategy, Preset: command.Preset,
		State: domain.RunRunning, StateReason: "持续创作中",
		CreatedAt: command.CreatedAt, UpdatedAt: command.CreatedAt,
	}
	return s.store.CreateCreationRun(ctx, run)
}

func (s *Service) CreationRun(ctx context.Context, runID string) (domain.CreationRun, error) {
	return s.store.GetCreationRun(ctx, runID)
}

// ActiveCreationRun 取项目当前非终态的创作运行；ok=false 表示没有进行中的创作。
func (s *Service) ActiveCreationRun(ctx context.Context, projectID string) (domain.CreationRun, bool, error) {
	run, err := s.store.ActiveCreationRun(ctx, projectID)
	if errors.Is(err, store.ErrNotFound) {
		return domain.CreationRun{}, false, nil
	}
	if err != nil {
		return domain.CreationRun{}, false, err
	}
	return run, true, nil
}

// LatestCreationRun 取项目最近一轮创作（含终态）；ok=false 表示还没写过。
// 界面用它呈现落点：失败/取消/完成的上一轮同样是用户需要看到的状态。
func (s *Service) LatestCreationRun(ctx context.Context, projectID string) (domain.CreationRun, bool, error) {
	run, err := s.store.LatestCreationRun(ctx, projectID)
	if errors.Is(err, store.ErrNotFound) {
		return domain.CreationRun{}, false, nil
	}
	if err != nil {
		return domain.CreationRun{}, false, err
	}
	return run, true, nil
}

// PauseCreationRun 把创作挂起；与活跃驱动循环竞争时由 TransitionCreationRun
// 的乐观并发裁决，先落盘者为准。
func (s *Service) PauseCreationRun(ctx context.Context, runID string, at time.Time) (domain.CreationRun, error) {
	run, err := s.store.GetCreationRun(ctx, runID)
	if err != nil {
		return domain.CreationRun{}, err
	}
	if run.State == domain.RunPaused {
		return run, nil
	}
	return s.store.TransitionCreationRun(ctx, runID, run.State, domain.RunPaused, "创作已暂停，随时可以继续", 0, at)
}

// CancelCreationRun 终止本轮创作；已写内容保留在权威流，重新开始会开启
// 新一轮并从现状续写。
func (s *Service) CancelCreationRun(ctx context.Context, runID string, at time.Time) (domain.CreationRun, error) {
	run, err := s.store.GetCreationRun(ctx, runID)
	if err != nil {
		return domain.CreationRun{}, err
	}
	if run.State == domain.RunCancelled {
		return run, nil
	}
	return s.store.TransitionCreationRun(ctx, runID, run.State, domain.RunCancelled, "本轮创作已取消", 0, at)
}

// UpdateCreationRunStrategy 让用户中途调整自动化边界（§6.3）：只影响之后创建
// 的 Operation，在途任务要采用新策略必须显式取消或重启。
func (s *Service) UpdateCreationRunStrategy(
	ctx context.Context,
	runID string,
	strategy domain.CreationRunStrategy,
	now time.Time,
) (domain.CreationRun, error) {
	return s.store.UpdateCreationRunStrategy(ctx, runID, strategy, now)
}

func (s *Service) attachRun(run domain.CreationRun, result QuickWriteResult) QuickWriteResult {
	result.RunID, result.RunState, result.RunReason = run.ID, run.State, run.StateReason
	return result
}

func (s *Service) finishQuickResult(
	ctx context.Context,
	run domain.CreationRun,
	project ProjectSnapshot,
	result QuickWriteResult,
) (QuickWriteResult, error) {
	result = s.attachRun(run, result)
	result.Revision = project.Revision
	plans := chapterPlansInOrder(project.Plan)
	if len(plans) > run.Goal.TargetChapters {
		plans = plans[:run.Goal.TargetChapters]
	}
	written := manuscriptsByPlanNode(project.Manuscript)
	result.Chapters = result.Chapters[:0]
	for index, plan := range plans {
		chapter, ok := written[plan.ID]
		if !ok {
			operation, found, err := s.latestChainOperation(
				ctx, runQuickID(run.ID, "chapter", plan.ID),
			)
			if err != nil {
				return result, err
			}
			if !found {
				continue
			}
			result.Chapters = append(result.Chapters, QuickChapterResult{
				ID: plan.ID, PlanNodeID: plan.ID, Number: index + 1,
				Title: plan.Title, OperationID: operation.ID, State: operation.State,
			})
			continue
		}
		version, err := s.store.GetDocument(ctx,
			domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: run.ProjectID},
			domain.DocumentRef{Kind: domain.DocumentManuscript, ID: chapter.ID}, project.Revision,
		)
		if err != nil {
			return result, err
		}
		changeSet, err := s.store.GetChangeSet(ctx, version.ChangeSetID)
		if err != nil {
			return result, err
		}
		operationID := changeSet.OperationID
		state := domain.OperationSucceeded
		if operationID != "" {
			operation, err := s.store.GetOperation(ctx, operationID)
			if err != nil {
				return result, err
			}
			state = operation.State
		}
		result.Chapters = append(result.Chapters, QuickChapterResult{
			ID: plan.ID, PlanNodeID: plan.ID, Number: index + 1,
			Title: plan.Title, OperationID: operationID, State: state,
		})
	}
	return result, nil
}

func runQuickID(runID string, parts ...string) string {
	return runID + ":" + strings.Join(parts, ":")
}
