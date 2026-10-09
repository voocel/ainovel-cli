package creation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// Coordinator drives application goals through the shared task and run contracts.
// Goals own snapshot loading, evidence selection, and the meaning of progress.
type Coordinator struct {
	store Store
	goals map[model.GoalKind]Goal
	now   func() time.Time
}

func New(st Store, goals map[model.GoalKind]Goal, now func() time.Time) *Coordinator {
	registered := make(map[model.GoalKind]Goal, len(goals))
	for kind, goal := range goals {
		registered[kind] = goal
	}
	return &Coordinator{store: st, goals: registered, now: now}
}

// stepClock 产生严格递增的时间戳，源是 Coordinator 注入的时钟。一轮驱动可能真实
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

// Goal loads the application state and derives exactly one next step.
type Goal interface {
	ValidateGoal(payload json.RawMessage) error
	Next(context.Context, model.CreationRun) (Decision, error)
}

type Decision struct {
	Revision model.Revision
	Step     Step
}

type DriveCommand struct {
	WorkerID      string
	LeaseDuration time.Duration
	CreatedAt     time.Time
}

// Step 是推导结果，恰好一项非空：Work 驱动一个 Operation；Wait 停下等用户；
// Done 完成并绑定当前 Revision；Fail 是确定性失败，需要人工检查。
type Step struct {
	Work *WorkItem
	Wait string
	Done string
	Fail string
}

// Validate rejects ambiguous or empty decisions before the driver performs work.
func (s Step) Validate() error {
	count := 0
	if s.Work != nil {
		count++
	}
	for _, reason := range []string{s.Wait, s.Done, s.Fail} {
		if reason != "" {
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("goal decision must contain exactly one of work, wait, done, fail: %w", model.ErrInvalid)
	}
	return nil
}

// WorkItem 是要驱动的 Operation：ID 是创作槽位（后继链的基名），Reasons 是这项工作在
// 各节点上的创作语言文案。
type WorkItem struct {
	ID      string
	Kind    model.OperationKind
	Input   model.TaskInput
	Reasons WorkReasons
}

// WorkReasons 的开工与做成由任务驱动方转述给用户，其余是运行落点；内核只传递不解读。
type WorkReasons struct {
	Start   string // 开工：这一步做什么、为什么是现在
	Done    string // 做成：这一步落下了什么
	Waiting string // 等待用户裁决
	Failure string // 执行失败；内核补上诊断指引
	Stuck   string // 已成功却没有推进目标
}

func (s *Coordinator) deriver(kind model.GoalKind) (Goal, error) {
	deriver, ok := s.goals[kind]
	if !ok {
		return nil, fmt.Errorf("no deriver for goal kind %q: %w", kind, model.ErrInvalid)
	}
	return deriver, nil
}

// Outcome records the persisted stopping point and the revision evaluated by the goal.
// Recovered 是驱动开始前放回队列的过期执行。
type Outcome struct {
	Run       model.CreationRun
	Revision  model.Revision
	Waiting   string
	Recovered []string
}

// 运行状态文案分工：应用经 WorkReasons 说明"在做什么工作"（只有它知道是规划还是
// 写第几章），内核说明"这轮运行发生了什么"（被暂停、被取消、重试了几次）。两边各写
// 自己那半，拼成用户看到的一句话；内核不猜工作内容，应用不描述运行机制。
const (
	reasonResumed   = "继续创作"
	reasonRunning   = "持续创作中"
	reasonPausedRun = "创作已暂停，随时可以继续"
	reasonPausedJob = "创作已暂停，恢复该任务后再继续"
	reasonCancelled = "创作已停止"
	reasonCancelJob = "创作任务被取消"
	// reasonDiagnostics 接在失败文案后面，指向事件日志里的原始诊断。
	reasonDiagnostics = "可展开 %s 的事件记录查看原始诊断"
	reasonSuperseded  = "已被新的推导取代，这份待审批稿件作废"
)

// Drive 推进一轮创作直到落点。开始前回收过期租约：崩溃留下的 running 任务放回队列，
// 驱动才能续跑它。
func (s *Coordinator) Drive(
	ctx context.Context,
	run model.CreationRun,
	tasks Tasks,
	command DriveCommand,
) (Outcome, error) {
	clock := &stepClock{now: s.now, last: command.CreatedAt}
	recovered, err := s.store.RecoverExpiredOperations(ctx, clock.next())
	if err != nil {
		return Outcome{}, err
	}
	outcome, err := s.drive(ctx, run, tasks, command, clock)
	outcome.Recovered = recovered
	return outcome, err
}

func (s *Coordinator) drive(
	ctx context.Context,
	run model.CreationRun,
	tasks Tasks,
	command DriveCommand,
	clock *stepClock,
) (Outcome, error) {
	if run.State == model.RunWaitingUser || run.State == model.RunPaused {
		resumed, err := s.store.TransitionCreationRun(ctx, run.ID,
			model.RunTransition{From: run.State, To: model.RunRunning, Reason: reasonResumed}, clock.next())
		if err != nil {
			return Outcome{}, err
		}
		run = resumed
	}
	var lastSettled string
	for {
		// 用户可能在上一步执行期间暂停/取消（乐观并发）：用户裁决优先，
		// 本轮就此优雅收束，不再创建新的 Operation。
		current, err := s.store.GetCreationRun(ctx, run.ID)
		if err != nil {
			return Outcome{}, err
		}
		if current.State != model.RunRunning {
			return Outcome{Run: current}, nil
		}
		run = current
		deriver, err := s.deriver(run.Goal.Kind)
		if err != nil {
			return Outcome{}, err
		}
		decision, decisionErr := deriver.Next(ctx, run)
		// An application failure is still a decision about observed state.
		// Infrastructure errors have no decision and leave the run resumable.
		if decisionErr != nil && decision.Step.Fail == "" {
			return Outcome{}, decisionErr
		}
		next := decision.Step
		if err := next.Validate(); err != nil {
			return Outcome{}, errors.Join(decisionErr, err)
		}
		// Goal evaluation can overlap user control and source edits. Decisions
		// may advance only the revision they actually observed.
		current, err = s.store.GetCreationRun(ctx, run.ID)
		if err != nil {
			return Outcome{}, err
		}
		if current.State != model.RunRunning {
			return Outcome{Run: current, Revision: decision.Revision}, nil
		}
		if !run.Goal.Equal(current.Goal) || run.Strategy != current.Strategy {
			continue
		}
		run = current
		revision, err := s.store.CurrentRevision(ctx, model.AuthorityTarget{Kind: model.AuthorityProject, ID: run.ProjectID})
		if err != nil {
			return Outcome{}, err
		}
		if decision.Revision <= 0 || decision.Revision > revision {
			return Outcome{}, fmt.Errorf("goal observed invalid revision %d (current %d): %w", decision.Revision, revision, model.ErrInvalid)
		}
		if decision.Revision < revision {
			continue
		}
		if next.Work == nil {
			state, reason := model.RunWaitingUser, next.Wait
			if next.Done != "" {
				state, reason = model.RunCompleted, next.Done
			} else if next.Fail != "" {
				state, reason = model.RunFailed, next.Fail
			}
			settled, err := s.store.SettleCreationRun(ctx, run, decision.Revision, state, reason, clock.next())
			if errors.Is(err, model.ErrRevisionConflict) || errors.Is(err, model.ErrStateConflict) {
				continue // Source, goal or user control changed before the atomic write.
			}
			if err != nil {
				return Outcome{Run: run, Revision: decision.Revision}, errors.Join(decisionErr, err)
			}
			if state == model.RunFailed && decisionErr == nil {
				decisionErr = fmt.Errorf("%s: %w", reason, model.ErrStateConflict)
			}
			return Outcome{Run: settled, Revision: decision.Revision}, decisionErr
		}
		work := *next.Work
		if err := s.supersede(ctx, run.ID, work, clock.next()); err != nil {
			return Outcome{}, err
		}
		operation, err := s.ensureChainedOperation(ctx, tasks, run, work, clock.next())
		if err != nil {
			return Outcome{}, err
		}
		if operation.State == model.OperationQueued {
			ran, err := tasks.Run(ctx, work, operation, command.WorkerID, command.LeaseDuration, clock.next())
			// 进程退出等取消不是创作失败：引擎已把任务放回队列，Run 保持 running 可续跑。
			if err != nil && ctx.Err() != nil {
				return Outcome{Run: run, Revision: decision.Revision}, err
			}
			// 执行按持久化结局落点（引擎在围栏拒绝写入时回读真实状态）。没有落到任何结局
			// 属于基础设施错误（执行器缺失、存储失败）：不作决定，Run 保持可续跑。
			if ran.ID != operation.ID || ran.State == model.OperationQueued || ran.State == model.OperationRunning {
				if err == nil {
					err = fmt.Errorf("operation %q did not conclude: %w", operation.ID, model.ErrStateConflict)
				}
				return Outcome{Run: run, Revision: decision.Revision}, err
			}
			operation = ran
		}
		// 单点分派：结局的原因已记在任务上，执行错误不再另行判断。
		switch operation.State {
		case model.OperationSucceeded:
			// 同一个已完成 Operation 第二次出现说明目标没有被它推进（例如规划
			// 已定稿但章节数不足）：确定性失败，绝不无限重试。
			if operation.ID == lastSettled {
				return s.failRun(ctx, run, decision.Revision, work.Reasons.Stuck, clock.next())
			}
			lastSettled = operation.ID
		case model.OperationAwaitingApproval:
			// 无人值守的等待（D26）：不是失败，Run 停在这里等用户裁决。
			settled, err := s.settleRun(ctx, run, model.RunTransition{
				To: model.RunWaitingUser, Reason: work.Reasons.Waiting, WaitingOperationID: operation.ID,
			}, clock.next())
			if err != nil {
				return Outcome{Run: settled, Revision: decision.Revision}, err
			}
			return Outcome{Run: settled, Revision: decision.Revision, Waiting: operation.ID}, nil
		case model.OperationPaused:
			settled, err := s.settleRun(ctx, run, model.RunTransition{To: model.RunPaused, Reason: reasonPausedJob}, clock.next())
			return Outcome{Run: settled, Revision: decision.Revision}, err
		case model.OperationFailed:
			outcome, reopen, err := s.reopenOrWait(ctx, tasks, run, decision.Revision, work, operation, clock.next())
			if reopen {
				continue
			}
			return outcome, err
		case model.OperationCancelled:
			settled, err := s.settleRun(ctx, run, model.RunTransition{To: model.RunCancelled, Reason: reasonCancelJob}, clock.next())
			return Outcome{Run: settled, Revision: decision.Revision}, err
		case model.OperationStale:
			// 控制收紧或基线漂移导致失效：下一轮由 ensureChainedOperation 安全迁移到后继。
		case model.OperationRunning:
			return Outcome{}, fmt.Errorf("operation %q: %w", operation.ID, model.ErrOperationHeld)
		}
	}
}

// failRun 落盘确定性失败并把原因作为错误上抛（§6.3）。
func (s *Coordinator) failRun(
	ctx context.Context,
	run model.CreationRun,
	revision model.Revision,
	reason string,
	at time.Time,
) (Outcome, error) {
	settled, settleErr := s.settleRun(ctx, run, model.RunTransition{To: model.RunFailed, Reason: reason}, at)
	return Outcome{Run: settled, Revision: revision}, errors.Join(fmt.Errorf("%s: %w", reason, model.ErrStateConflict), settleErr)
}

// autoReopenBudget 是执行失败的会话级重开策略（D56）：一个 Operation 最多自动重开
// 这么多次。重开走 ensureChainedOperation 的 Resume/后继路径，对话、工作区与失败
// 原因全部带回给模型；用尽落 waiting_user，用户续跑再授予一次尝试，绝不无限重跑。
// 预算按失败次数计（Tasks.Failures），不按 attempt：进程退出释放的执行不算失败。
// 结果未知与提交受阻不自动重开；provider 的调用级重试不在此计数。
const autoReopenBudget = 3

// reopenOrWait 决定失败任务的去向：预算内返回 reopen=true 让驱动循环下一轮重开；
// 用尽则把 Run 停在 waiting_user 并说明原因，草稿与上下文保留。
func (s *Coordinator) reopenOrWait(
	ctx context.Context,
	tasks Tasks,
	run model.CreationRun,
	revision model.Revision,
	work WorkItem,
	operation model.Operation,
	at time.Time,
) (Outcome, bool, error) {
	if operation.FailureCode == model.FailureSubmissionBlocked || operation.FailureCode == model.FailureResultUnknown {
		reason := fmt.Sprintf("%s：%s。已停止自动重试并保留工作区，请处理原因后显式续跑；"+reasonDiagnostics,
			work.Reasons.Failure, operation.Error, operation.ID)
		settled, err := s.settleRun(ctx, run, model.RunTransition{To: model.RunWaitingUser, Reason: reason}, at)
		return Outcome{Run: settled, Revision: revision}, false, err
	}
	failures, err := tasks.Failures(ctx, operation.ID)
	if err != nil {
		return Outcome{}, false, err
	}
	if failures <= autoReopenBudget {
		return Outcome{}, true, nil
	}
	reason := fmt.Sprintf("%s：已自动重试 %d 次仍未成功，停下等你处理（最近一次原因：%s）。续跑会带着已有草稿再试一次；"+reasonDiagnostics,
		work.Reasons.Failure, failures-1, operation.Error, operation.ID)
	settled, err := s.settleRun(ctx, run, model.RunTransition{To: model.RunWaitingUser, Reason: reason}, at)
	return Outcome{Run: settled, Revision: revision}, false, err
}

// ensureChainedOperation 解析创作槽位当前有效的 Operation：沿确定性后继链
// （base、base:r2、base:r3…）找到第一个可用者；失效或被否决的前任由后继继承
// 工作区（§5.5 安全迁移），failed 在用户显式重新发起时先尝试原地重排。
func (s *Coordinator) ensureChainedOperation(
	ctx context.Context,
	tasks Tasks,
	run model.CreationRun,
	work WorkItem,
	at time.Time,
) (model.Operation, error) {
	baseID := work.ID
	var predecessor model.Operation
	for attempt := 1; ; attempt++ {
		id := chainID(baseID, attempt)
		operation, err := s.store.GetOperation(ctx, id)
		if errors.Is(err, model.ErrNotFound) {
			// Run 归属与血统事件由存储层在创建事务内一并落盘（§6.3），无孤儿窗口。
			var created model.Operation
			work.ID = id
			if predecessor.ID == "" {
				created, err = tasks.Start(ctx, run.ID, work, at)
			} else {
				created, err = tasks.Restart(ctx, predecessor.ID, run.ID, work, at)
			}
			if err != nil {
				return model.Operation{}, err
			}
			return created, nil
		}
		if err != nil {
			return model.Operation{}, err
		}
		if operation.Target.Kind != model.AuthorityProject || operation.Target.ID != run.ProjectID ||
			operation.Kind != work.Kind || operation.RunID != run.ID {
			return model.Operation{}, fmt.Errorf("operation %q identity changed: %w", id, model.ErrIdempotencyConflict)
		}
		switch operation.State {
		case model.OperationStale, model.OperationCancelled:
			predecessor = operation
		case model.OperationFailed:
			resumed, err := tasks.Resume(ctx, operation.ID, at)
			if err == nil {
				return resumed, nil
			}
			if !errors.Is(err, model.ErrStateConflict) {
				return model.Operation{}, err
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

// inChain 报告 id 是否是槽位 base 后继链上的成员。
func inChain(base, id string) bool {
	if id == base {
		return true
	}
	suffix, ok := strings.CutPrefix(id, base+":r")
	attempt, err := strconv.Atoi(suffix)
	return ok && err == nil && chainID(base, attempt) == id && attempt > 1
}

// supersede 作废被新推导取代的待审批任务（D64）：推导给出同种类的另一项工作（不在它的
// 后继链上）时，旧稿不再是这一步的候选，转 stale——之后既不呈现也不能被批准入账。推导
// 暂时去做别的（审阅、核验）或停下等待都不算取代。用户同时裁决了它就以用户为准。
func (s *Coordinator) supersede(ctx context.Context, runID string, work WorkItem, at time.Time) error {
	awaiting, err := s.store.AwaitingRunOperations(ctx, runID)
	if err != nil {
		return err
	}
	for _, operation := range awaiting {
		if operation.Kind != work.Kind || inChain(work.ID, operation.ID) {
			continue
		}
		_, err := s.store.TransitionOperation(ctx, operation.ID,
			model.OperationAwaitingApproval, model.OperationStale, reasonSuperseded, at)
		if err != nil && !errors.Is(err, model.ErrStateConflict) {
			return err
		}
	}
	return nil
}

// LatestChainOperation 只读地取槽位后继链上最新的 Operation，用于结果汇报。
func (s *Coordinator) LatestChainOperation(
	ctx context.Context,
	baseID string,
) (model.Operation, bool, error) {
	var found model.Operation
	ok := false
	for attempt := 1; ; attempt++ {
		operation, err := s.store.GetOperation(ctx, chainID(baseID, attempt))
		if errors.Is(err, model.ErrNotFound) {
			return found, ok, nil
		}
		if err != nil {
			return model.Operation{}, false, err
		}
		found, ok = operation, true
	}
}

// RunOperationIDs 按事件顺序列出 Run 创建过的全部 Operation（含后继链成员）。
func (s *Coordinator) RunOperationIDs(ctx context.Context, runID string) ([]string, error) {
	events, err := s.store.ListCreationRunEvents(ctx, runID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(events))
	for _, event := range events {
		if event.Kind != model.RunEventOperationCreated {
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

// WaitingProposal 取 Run 停下等待审批的稿件，用于恢复落点时重建决定卡。ok=false 表示
// 等待不携带稿件（例如预算用尽），或那个任务已不在等待（被裁决或被新的推导取代）。
func (s *Coordinator) WaitingProposal(ctx context.Context, runID string) (model.Proposal, bool, error) {
	run, err := s.store.GetCreationRun(ctx, runID)
	if err != nil || run.WaitingOperationID == "" {
		return model.Proposal{}, false, err
	}
	operation, err := s.store.GetOperation(ctx, run.WaitingOperationID)
	if err != nil || operation.State != model.OperationAwaitingApproval {
		return model.Proposal{}, false, err
	}
	proposal, err := s.store.GetProposalByOperation(ctx, operation.ID)
	if err != nil {
		return model.Proposal{}, false, err
	}
	return proposal, true, nil
}

// settleRun 从运行现状落盘落点（transition.From 取 run.State）；落盘失败返回原 Run 与
// 错误，调用方与主错误一并上抛，不吞错（§6.3）。
func (s *Coordinator) settleRun(
	ctx context.Context,
	run model.CreationRun,
	transition model.RunTransition,
	at time.Time,
) (model.CreationRun, error) {
	transition.From = run.State
	settled, err := s.store.TransitionCreationRun(ctx, run.ID, transition, at)
	if err == nil {
		return settled, nil
	}
	// 状态冲突且现状是用户设定的暂停/取消：用户裁决优先，以其为落点不报错。
	if errors.Is(err, model.ErrStateConflict) {
		if current, getErr := s.store.GetCreationRun(ctx, run.ID); getErr == nil &&
			(current.State == model.RunPaused || current.State == model.RunCancelled) {
			return current, nil
		}
	}
	return run, fmt.Errorf("settle creation run %q to %s: %w", run.ID, transition.To, err)
}

func (s *Coordinator) CreationRunEvents(ctx context.Context, runID string) ([]model.CreationRunEvent, error) {
	return s.store.ListCreationRunEvents(ctx, runID)
}

// RunOperations 按创建序取一轮创作的全部 Operation。诊断视图靠它下钻：
// run 事件表只有编排流水，真实的失败原因在 Operation.Error 与 operation_events。
func (s *Coordinator) RunOperations(ctx context.Context, runID string) ([]model.Operation, error) {
	ids, err := s.RunOperationIDs(ctx, runID)
	if err != nil {
		return nil, err
	}
	operations := make([]model.Operation, 0, len(ids))
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
	Goal      model.CreationRunGoal
	Strategy  model.CreationRunStrategy
	Preset    model.CreationRunPreset
	CreatedAt time.Time
}

// StartCreationRun 是精细协作入口与 quick 预设共用的运行内核入口。
func (s *Coordinator) StartCreationRun(
	ctx context.Context,
	command StartCreationRunCommand,
) (model.CreationRun, error) {
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: command.ProjectID}
	if _, err := s.store.CurrentRevision(ctx, target); err != nil {
		return model.CreationRun{}, err
	}
	deriver, err := s.deriver(command.Goal.Kind)
	if err != nil {
		return model.CreationRun{}, err
	}
	if err := deriver.ValidateGoal(command.Goal.Payload); err != nil {
		return model.CreationRun{}, err
	}
	run := model.CreationRun{
		ID: command.RunID, ProjectID: command.ProjectID,
		Goal: command.Goal, Strategy: command.Strategy, Preset: command.Preset,
		State: model.RunRunning, StateReason: reasonRunning,
		CreatedAt: command.CreatedAt, UpdatedAt: command.CreatedAt,
	}
	return s.store.CreateCreationRun(ctx, run)
}

func (s *Coordinator) CreationRun(ctx context.Context, runID string) (model.CreationRun, error) {
	return s.store.GetCreationRun(ctx, runID)
}

// ActiveCreationRun 取项目当前非终态的创作运行；ok=false 表示没有进行中的创作。
func (s *Coordinator) ActiveCreationRun(ctx context.Context, projectID string) (model.CreationRun, bool, error) {
	run, err := s.store.ActiveCreationRun(ctx, projectID)
	if errors.Is(err, model.ErrNotFound) {
		return model.CreationRun{}, false, nil
	}
	if err != nil {
		return model.CreationRun{}, false, err
	}
	return run, true, nil
}

// LatestCreationRun 取项目最近一轮创作（含终态）；ok=false 表示还没写过。
// 界面用它呈现落点：失败/取消/完成的上一轮同样是用户需要看到的状态。
func (s *Coordinator) LatestCreationRun(ctx context.Context, projectID string) (model.CreationRun, bool, error) {
	run, err := s.store.LatestCreationRun(ctx, projectID)
	if errors.Is(err, model.ErrNotFound) {
		return model.CreationRun{}, false, nil
	}
	if err != nil {
		return model.CreationRun{}, false, err
	}
	return run, true, nil
}

// PauseCreationRun 把创作挂起；与活跃驱动循环竞争时由 TransitionCreationRun
// 的乐观并发裁决，先落盘者为准。
func (s *Coordinator) PauseCreationRun(ctx context.Context, runID string, at time.Time) (model.CreationRun, error) {
	run, err := s.store.GetCreationRun(ctx, runID)
	if err != nil {
		return model.CreationRun{}, err
	}
	if run.State == model.RunPaused {
		return run, nil
	}
	return s.store.TransitionCreationRun(ctx, runID, model.RunTransition{From: run.State, To: model.RunPaused, Reason: reasonPausedRun}, at)
}

// CancelCreationRun 终止本轮创作；已写内容保留在权威流，重新开始会开启
// 新一轮并从现状续写。
func (s *Coordinator) CancelCreationRun(ctx context.Context, runID string, at time.Time) (model.CreationRun, error) {
	run, err := s.store.GetCreationRun(ctx, runID)
	if err != nil {
		return model.CreationRun{}, err
	}
	if run.State == model.RunCancelled {
		return run, nil
	}
	return s.store.TransitionCreationRun(ctx, runID, model.RunTransition{From: run.State, To: model.RunCancelled, Reason: reasonCancelled}, at)
}

// UpdateCreationRunStrategy 让用户中途调整自动化边界（§6.3）：只影响之后创建
// 的 Operation，在途任务要采用新策略必须显式取消或重启。
func (s *Coordinator) UpdateCreationRunStrategy(
	ctx context.Context,
	runID string,
	strategy model.CreationRunStrategy,
	now time.Time,
) (model.CreationRun, error) {
	return s.store.UpdateCreationRunStrategy(ctx, runID, strategy, now)
}
