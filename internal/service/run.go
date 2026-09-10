package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// 创作协调器（D27/D49）：内核只做机制——读快照与 Run、用户裁决优先、收集基线仍成立
// 的证据、向目标种类的推导器要恰好一个下一步、驱动 Operation、落盘落点。什么算完成、
// 下一步做什么，由推导器按目标种类决定；协调器不生产内容，也不认识任何小说概念。

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

// goalDeriver 是目标种类的推导契约（D49）：从项目快照、Run 与证据推导恰好一个
// 下一步。实现是纯函数——不触存储、不触执行器；新目标种类只登记一个推导器。
type goalDeriver interface {
	ValidateGoal(payload json.RawMessage) error
	Next(project ProjectSnapshot, run domain.CreationRun, evidence runEvidence) (step, error)
}

// runEvidence 是推导器可见的证据：基线仍成立的审阅裁定与工件，以及本轮已用的修订次数。
type runEvidence struct {
	verdicts    []storedVerdict
	artifacts   []domain.Artifact
	checks      []storedCheck
	repairsUsed int
}

// step 是推导结果，恰好一项非空：Work 驱动一个 Operation；Wait 停下等用户；
// Done 完成并绑定当前 Revision；Fail 是确定性失败，需要人工检查。
type step struct {
	Work *workItem
	Wait string
	Done string
	Fail string
}

// workItem 是要驱动的 Operation：ID 是创作槽位（后继链的基名），Reasons 是各落点的
// 创作语言文案。
type workItem struct {
	ID      string
	Kind    domain.OperationKind
	Input   domain.TaskInput
	Reasons workReasons
}

type workReasons struct {
	Waiting string // 等待用户裁决
	Failure string // 执行失败；内核补上诊断指引
	Stuck   string // 已成功却没有推进目标
}

func (s *Service) deriver(kind domain.GoalKind) (goalDeriver, error) {
	deriver, ok := s.derivers[kind]
	if !ok {
		return nil, fmt.Errorf("no deriver for goal kind %q: %w", kind, domain.ErrInvalid)
	}
	return deriver, nil
}

// driveOutcome 是一轮驱动的落点：Run、当时的项目快照与等待裁决的 Operation。
// 基础设施错误不带 Run 返回，落点已落盘的错误带着落盘后的 Run。
type driveOutcome struct {
	run     domain.CreationRun
	project ProjectSnapshot
	waiting string
}

func (s *Service) driveCreationRun(
	ctx context.Context,
	run domain.CreationRun,
	command QuickWriteCommand,
) (driveOutcome, error) {
	deriver, err := s.deriver(run.Goal.Kind)
	if err != nil {
		return driveOutcome{}, err
	}
	clock := &stepClock{now: s.now, last: command.CreatedAt}
	if run.State == domain.RunWaitingUser || run.State == domain.RunPaused {
		resumed, err := s.store.TransitionCreationRun(ctx, run.ID, run.State, domain.RunRunning, "继续创作", 0, clock.next())
		if err != nil {
			return driveOutcome{}, err
		}
		run = resumed
	}
	base := s.runBaseCommand(run, command)
	var lastSettled string
	for {
		project, err := s.Project(ctx, run.ProjectID, domain.InitialRevision)
		if err != nil {
			return driveOutcome{}, err
		}
		// 用户可能在上一步执行期间暂停/取消（乐观并发）：用户裁决优先，
		// 本轮就此优雅收束，不再创建新的 Operation。
		current, err := s.store.GetCreationRun(ctx, run.ID)
		if err != nil {
			return driveOutcome{}, err
		}
		if current.State != domain.RunRunning {
			return driveOutcome{run: current, project: project}, nil
		}
		run = current
		verdicts, err := s.listVerdicts(ctx, project)
		if err != nil {
			settled, settleErr := s.settleRun(ctx, run, domain.RunFailed, "审阅结果缺失或损坏，需要人工检查", 0, clock.next())
			return driveOutcome{run: settled, project: project}, errors.Join(err, settleErr)
		}
		artifacts, err := s.validArtifacts(ctx, project)
		if err != nil {
			return driveOutcome{}, err
		}
		checks, err := s.validChecks(ctx, project)
		if err != nil {
			return driveOutcome{}, err
		}
		repairs, err := s.autoRepairCount(ctx, run.ID)
		if err != nil {
			return driveOutcome{}, err
		}
		next, err := deriver.Next(project, run, runEvidence{verdicts: verdicts, artifacts: artifacts, checks: checks, repairsUsed: repairs})
		if err != nil {
			return driveOutcome{}, err
		}
		switch {
		case next.Done != "":
			settled, err := s.settleRun(ctx, run, domain.RunCompleted, next.Done, project.Revision, clock.next())
			return driveOutcome{run: settled, project: project}, err
		case next.Wait != "":
			// 无人值守的等待（D26）：不是失败，Run 停在这里等用户裁决。
			settled, err := s.settleRun(ctx, run, domain.RunWaitingUser, next.Wait, 0, clock.next())
			return driveOutcome{run: settled, project: project}, err
		case next.Fail != "":
			return s.failRun(ctx, run, project, next.Fail, clock.next())
		}
		work := *next.Work
		operationCommand, err := workCommand(base, work, clock.next())
		if err != nil {
			return driveOutcome{}, err
		}
		operation, err := s.ensureChainedOperation(ctx, run.ID, operationCommand)
		if err != nil {
			return driveOutcome{}, err
		}
		if operation.State == domain.OperationQueued {
			runResult, err := s.RunOperation(ctx, operation.ID, command.WorkerID, command.LeaseDuration, clock.next())
			if runResult.Operation.ID != "" {
				operation = runResult.Operation
			}
			// stale 不是失败（§6.3）：基线在执行期间变化时由下一轮安全迁移到后继。
			if err != nil && operation.State != domain.OperationStale {
				settled, settleErr := s.settleRun(ctx, run, domain.RunFailed, failureReason(work, operation.ID), 0, clock.next())
				return driveOutcome{run: settled, project: project}, errors.Join(err, settleErr)
			}
		}
		switch operation.State {
		case domain.OperationSucceeded:
			// 同一个已完成 Operation 第二次出现说明目标没有被它推进（例如规划
			// 已定稿但章节数不足）：确定性失败，绝不无限重试。
			if operation.ID == lastSettled {
				return s.failRun(ctx, run, project, work.Reasons.Stuck, clock.next())
			}
			lastSettled = operation.ID
		case domain.OperationAwaitingApproval:
			// 无人值守的等待（D26）：不是失败，Run 停在这里等用户裁决。
			settled, err := s.settleRun(ctx, run, domain.RunWaitingUser, work.Reasons.Waiting, 0, clock.next())
			if err != nil {
				return driveOutcome{run: settled, project: project}, err
			}
			return driveOutcome{run: settled, project: project, waiting: operation.ID}, nil
		case domain.OperationPaused:
			settled, err := s.settleRun(ctx, run, domain.RunPaused, "创作已暂停，恢复该任务后再继续", 0, clock.next())
			return driveOutcome{run: settled, project: project}, err
		case domain.OperationFailed:
			return s.failRun(ctx, run, project, failureReason(work, operation.ID), clock.next())
		case domain.OperationCancelled:
			settled, err := s.settleRun(ctx, run, domain.RunCancelled, "创作任务被取消", 0, clock.next())
			return driveOutcome{run: settled, project: project}, err
		case domain.OperationStale:
			// 控制收紧或基线漂移导致失效：下一轮由 ensureChainedOperation 安全迁移到后继。
		case domain.OperationRunning:
			return driveOutcome{}, fmt.Errorf(
				"operation %q is held by another worker lease: %w", operation.ID, store.ErrStateConflict)
		}
	}
}

// failRun 落盘确定性失败并把原因作为错误上抛（§6.3）。
func (s *Service) failRun(
	ctx context.Context,
	run domain.CreationRun,
	project ProjectSnapshot,
	reason string,
	at time.Time,
) (driveOutcome, error) {
	settled, settleErr := s.settleRun(ctx, run, domain.RunFailed, reason, 0, at)
	return driveOutcome{run: settled, project: project}, errors.Join(fmt.Errorf("%s: %w", reason, store.ErrStateConflict), settleErr)
}

func failureReason(work workItem, operationID string) string {
	return fmt.Sprintf("%s；可展开 %s 的事件记录查看原始诊断", work.Reasons.Failure, operationID)
}

func workCommand(base StartOperationCommand, work workItem, at time.Time) (StartOperationCommand, error) {
	input, err := json.Marshal(work.Input)
	if err != nil {
		return StartOperationCommand{}, fmt.Errorf("encode %s task: %w", work.Kind, err)
	}
	base.OperationID, base.Kind, base.Input, base.CreatedAt = work.ID, work.Kind, input, at
	return base, nil
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
		if spec, err := domain.KindSpec(operation.Kind); err != nil {
			return 0, err
		} else if spec.Repair {
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
	deriver, err := s.deriver(command.Goal.Kind)
	if err != nil {
		return domain.CreationRun{}, err
	}
	if err := deriver.ValidateGoal(command.Goal.Payload); err != nil {
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

// runBaseCommand 是本轮全部 Operation 的公共启动参数：LLM 族带 Pack 与创作者档案，
// 外部族带执行器自报的配置摘要（D45）。
func (s *Service) runBaseCommand(run domain.CreationRun, command QuickWriteCommand) StartOperationCommand {
	base := StartOperationCommand{
		ProjectID: run.ProjectID, Packs: command.Packs, CreatorProfiles: command.CreatorProfiles,
		CoreProtocolVersion: "core-v1",
	}
	if configured, ok := s.executors.External.(interface{ ConfigDigest() string }); ok {
		base.ConfigDigest = configured.ConfigDigest()
	}
	return base
}

func runQuickID(runID string, parts ...string) string {
	return runID + ":" + strings.Join(parts, ":")
}
