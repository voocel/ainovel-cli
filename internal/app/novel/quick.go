package novel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/app/resource"
	"github.com/voocel/ainovel-cli/internal/app/task"
	"github.com/voocel/ainovel-cli/internal/domain/creation"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// KeepChapters 让 QuickWrite 沿用上一轮创作的篇幅设定；新作品按交给 AI 处理。
const KeepChapters = -1

// DefaultRepairBudget 是每章允许的自动重写次数（D63 按章计）。
const DefaultRepairBudget = 2

type QuickWriteCommand struct {
	ProjectID string
	UserID    string
	Premise   string
	// Chapters 是篇幅：正数固定全书章数，0 交给 AI（D63），KeepChapters 沿用上一轮。
	Chapters int
	// Extend 是续写：篇幅交给 AI 并撤回已有的收官承诺，由 AI 决定再写多少。它是显式意图，
	// 重复执行普通续跑不会隐式续写。
	Extend bool
	// Approval 请求更新 Project 的有效审批策略；Run 创建时只保留不可变预设摘要。
	Approval        model.ApprovalPolicy
	Packs           []resource.PackRef
	CreatorProfiles []resource.CreatorProfileRef
	WorkerID        string
	LeaseDuration   time.Duration
	CreatedAt       time.Time
}

type QuickChapterResult struct {
	ID          string               `json:"id"`
	PlanNodeID  string               `json:"plan_node_id"`
	Number      int                  `json:"number"`
	Title       string               `json:"title"`
	OperationID string               `json:"operation_id"`
	State       model.OperationState `json:"state"`
}

type QuickWriteResult struct {
	ProjectID           string                 `json:"project_id"`
	Revision            model.Revision         `json:"revision"`
	RunID               string                 `json:"run_id"`
	RunState            model.CreationRunState `json:"run_state"`
	RunReason           string                 `json:"run_reason,omitempty"`
	WaitingOperationID  string                 `json:"waiting_operation_id,omitempty"`
	RecoveredOperations []string               `json:"recovered_operations,omitempty"`
	Chapters            []QuickChapterResult   `json:"chapters"`
}

// QuickWrite 是“一句话写全书”的入口：它只是 CreationRun 的一个薄预设（D24/D27）——
// 建立 Project 与 Run 后，全部推进都由创作协调器驱动。等待与失败都会落在 Run 上，
// 再次执行同一命令即从落点继续。
func (s *Application) QuickWrite(ctx context.Context, command QuickWriteCommand) (QuickWriteResult, error) {
	command, run, err := s.quickRun(ctx, command)
	result := QuickWriteResult{ProjectID: command.ProjectID}
	if err != nil {
		return result, err
	}
	outcome, err := s.runs.Drive(ctx, run, s.creationTasks(command), command.CreatedAt)
	result = attachRun(outcome.Run, result)
	result.WaitingOperationID, result.RecoveredOperations = outcome.Waiting, outcome.Recovered
	if err != nil {
		return result, err
	}
	return s.finishQuickResult(ctx, outcome, result)
}

// quickRun 校验并补全命令，建立作品与这一轮创作。
func (s *Application) quickRun(ctx context.Context, command QuickWriteCommand) (QuickWriteCommand, model.CreationRun, error) {
	if !s.tasks.HasLLM() {
		return command, model.CreationRun{}, fmt.Errorf("quick write requires a configured model: %w", model.ErrInvalid)
	}
	if strings.TrimSpace(command.ProjectID) == "" || strings.TrimSpace(command.UserID) == "" ||
		strings.TrimSpace(command.Premise) == "" || command.Chapters < KeepChapters ||
		strings.TrimSpace(command.WorkerID) == "" || command.CreatedAt.IsZero() {
		return command, model.CreationRun{}, fmt.Errorf("quick write project, user, premise, chapters, worker and time are required: %w", model.ErrInvalid)
	}
	if command.LeaseDuration <= 0 {
		command.LeaseDuration = task.DefaultLease
	}
	switch command.Approval {
	case "", model.ApprovalAuto, model.ApprovalMilestone, model.ApprovalManual:
	default:
		return command, model.CreationRun{}, fmt.Errorf("quick write approval must be auto, milestone or manual: %w", model.ErrInvalid)
	}
	if command.Extend {
		if command.Chapters > 0 {
			return command, model.CreationRun{}, fmt.Errorf("extending leaves the length to the AI and cannot fix chapters: %w", model.ErrInvalid)
		}
		command.Chapters = 0
	}
	if command.Chapters == KeepChapters {
		chapters, err := s.currentChapters(ctx, command.ProjectID)
		if err != nil {
			return command, model.CreationRun{}, err
		}
		command.Chapters = chapters
	}

	project, err := s.ensureQuickProject(ctx, command)
	if err != nil {
		return command, model.CreationRun{}, err
	}
	run, err := s.ensureCreationRun(ctx, command, project.Approval)
	if err != nil {
		return command, model.CreationRun{}, err
	}
	return command, run, nil
}

// currentChapters 解析"沿用篇幅设定"：取最近一轮创作的设定，没有时交给 AI。
func (s *Application) currentChapters(ctx context.Context, projectID string) (int, error) {
	run, ok, err := s.runs.LatestCreationRun(ctx, projectID)
	if err != nil || !ok {
		return 0, err
	}
	goal, err := model.DecodeNovelGoal(run.Goal)
	return goal.TargetChapters, err
}

func attachRun(run model.CreationRun, result QuickWriteResult) QuickWriteResult {
	result.RunID, result.RunState, result.RunReason = run.ID, run.State, run.StateReason
	return result
}

// finishQuickResult 把驱动落点投影成 quick 结果：全书章数内（未收官时为整个蓝图）每章的
// 状态与来源 Operation。
func (s *Application) finishQuickResult(
	ctx context.Context,
	outcome creation.Outcome,
	result QuickWriteResult,
) (QuickWriteResult, error) {
	run := outcome.Run
	project, err := s.projects.Project(ctx, run.ProjectID, outcome.Revision)
	if err != nil {
		return result, err
	}
	goal, err := model.DecodeNovelGoal(run.Goal)
	if err != nil {
		return result, err
	}
	result.Revision = project.Revision
	plans := model.ChapterPlansInOrder(project.Plan)
	if final := lengthOf(project, goal.TargetChapters).Final; final > 0 && len(plans) > final {
		plans = plans[:final]
	}
	written := manuscriptsByPlanNode(project.Manuscript)
	result.Chapters = result.Chapters[:0]
	for index, plan := range plans {
		chapter, ok := written[plan.ID]
		if !ok {
			operation, found, err := s.runs.LatestChainOperation(
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
			model.AuthorityTarget{Kind: model.AuthorityProject, ID: run.ProjectID},
			manuscriptRef(chapter.ID), project.Revision,
		)
		if err != nil {
			return result, err
		}
		changeSet, err := s.store.GetChangeSet(ctx, version.ChangeSetID)
		if err != nil {
			return result, err
		}
		operationID := changeSet.OperationID
		state := model.OperationSucceeded
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

// ensureQuickProject 建立或对账作品：首次调用在初始化事务里把 Intent 与审批
// 策略写入权威（§6.3）；此后同一命令重入时按需更新审批策略与罗盘，走唯一写路径。
// 篇幅属于运行目标与罗盘（D63），不写 Intent：改篇幅不作废任何证据。
func (s *Application) ensureQuickProject(ctx context.Context, command QuickWriteCommand) (projectdoc.Snapshot, error) {
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: command.ProjectID}
	_, err := s.store.CurrentRevision(ctx, target)
	if errors.Is(err, model.ErrNotFound) {
		return s.projects.CreateProject(ctx, projectdoc.CreateProjectCommand{
			ProjectID: command.ProjectID,
			ChangeID:  quickID(command.ProjectID, "create"),
			UserID:    command.UserID,
			Reason:    "一句话创建作品",
			Draft: projectdoc.ProjectDraft{
				Intent:   model.Intent{Premise: command.Premise},
				Approval: command.Approval,
			},
			CreatedAt: command.CreatedAt,
		})
	}
	if err != nil {
		return projectdoc.Snapshot{}, err
	}
	project, err := s.projects.Project(ctx, command.ProjectID, model.InitialRevision)
	if err != nil {
		return projectdoc.Snapshot{}, err
	}
	if project.Intent.Premise != command.Premise {
		return projectdoc.Snapshot{}, fmt.Errorf(
			"project %q already has a different premise: %w", command.ProjectID, model.ErrIdempotencyConflict)
	}
	if command.Approval != "" && command.Approval != project.Approval {
		if _, err := s.projects.SetApprovalPolicy(ctx, projectdoc.SetApprovalPolicyCommand{
			ProjectID: command.ProjectID,
			ChangeID:  fmt.Sprintf("%s@r%d", quickID(command.ProjectID, "approval", string(command.Approval)), project.Revision),
			UserID:    command.UserID, Policy: command.Approval,
			Reason: "调整创作的审批预设", CreatedAt: command.CreatedAt,
		}); err != nil {
			return projectdoc.Snapshot{}, err
		}
		if project, err = s.projects.Project(ctx, command.ProjectID, model.InitialRevision); err != nil {
			return projectdoc.Snapshot{}, err
		}
	}
	return s.reconcileCompass(ctx, command, project)
}

// reconcileCompass 让罗盘跟随用户的篇幅意图（D63），罗盘进每个任务的上下文，篇幅只能
// 有一个口径：固定篇幅时撤下 AI 的预计篇幅与收官，罗盘只留终局（D70）；续写时撤回收官
// 承诺，预计篇幅不动——还有余量就直接续写，没有余量由 AI 修订预计（护栏以上需用户确认，D73）。
// 罗盘不在任何证据基线里，这些用户变更不作废审阅。
func (s *Application) reconcileCompass(ctx context.Context, command QuickWriteCommand, project projectdoc.Snapshot) (projectdoc.Snapshot, error) {
	if project.Compass == nil {
		return project, nil
	}
	compass, action, reason := *project.Compass, "", ""
	switch {
	// 固定篇幅后篇幅只有用户的一个口径（D70）：罗盘撤下 AI 的预计篇幅与收官，只留终局。
	case command.Chapters > 0 && (compass.ScaleMax != 0 || compass.Final != 0):
		compass.Final, compass.ScaleMax = 0, 0
		action, reason = "length:"+strconv.Itoa(command.Chapters), fmt.Sprintf("篇幅固定为 %d 章，罗盘只保留终局方向", command.Chapters)
	case command.Extend && compass.Final > 0:
		compass.Final = 0
		action, reason = "extend", fmt.Sprintf("撤回第 %d 章的收官承诺，由 AI 决定续写多少", project.Compass.Final)
	default:
		return project, nil
	}
	content, err := json.Marshal(compass)
	if err != nil {
		return projectdoc.Snapshot{}, fmt.Errorf("encode compass: %w", err)
	}
	committed, err := s.changes.CommitUser(ctx, model.Proposal{
		ID:     fmt.Sprintf("%s@r%d", quickID(command.ProjectID, action), project.Revision),
		Target: model.AuthorityTarget{Kind: model.AuthorityProject, ID: command.ProjectID}, BaseRevision: project.Revision,
		Author: model.Author{Kind: model.AuthorUser, ID: command.UserID},
		Reason: reason,
		Patches: []model.Patch{{
			Document:  model.DocumentRef{Kind: model.DocumentCompass, ID: model.SingletonDocumentID},
			Operation: model.PatchPut, Content: content,
		}},
		ApprovalState: model.ApprovalPending, CreatedAt: command.CreatedAt,
	})
	if err != nil {
		return projectdoc.Snapshot{}, err
	}
	return s.projects.Project(ctx, command.ProjectID, committed.NewRevision)
}

// ensureCreationRun 复用进行中的 Run（目标变化时更新当前 Run，不启动竞争 Run，
// §6.3），否则开启新一轮。终态的历史轮次留在原地：已完成的书再次执行会开启
// 一轮只做完成验证的 Run，缺章时则真正续写。
func (s *Application) ensureCreationRun(
	ctx context.Context,
	command QuickWriteCommand,
	approval model.ApprovalPolicy,
) (model.CreationRun, error) {
	goal := model.NovelGoal{TargetChapters: command.Chapters}
	active, ok, err := s.runs.ActiveCreationRun(ctx, command.ProjectID)
	if err != nil {
		return model.CreationRun{}, err
	}
	if ok {
		current, err := model.DecodeNovelGoal(active.Goal)
		if err != nil {
			return model.CreationRun{}, err
		}
		if current != goal {
			if active, err = s.store.UpdateCreationRunGoal(ctx, active.ID, goal.Goal(), command.CreatedAt); err != nil {
				return model.CreationRun{}, err
			}
		}
		return active, nil
	}
	count, err := s.store.CountCreationRuns(ctx, command.ProjectID)
	if err != nil {
		return model.CreationRun{}, err
	}
	strategy := model.CreationRunStrategy{AutoRepairBudget: DefaultRepairBudget}
	preset, err := model.NewCreationRunPreset("quick", approval, strategy)
	if err != nil {
		return model.CreationRun{}, err
	}
	return s.runs.StartCreationRun(ctx, creation.StartCreationRunCommand{
		RunID: fmt.Sprintf("run:%s:%d", command.ProjectID, count+1), ProjectID: command.ProjectID,
		Goal: goal.Goal(), Strategy: strategy, Preset: preset, CreatedAt: command.CreatedAt,
	})
}

func quickID(projectID string, parts ...string) string {
	return "quick:" + projectID + ":" + strings.Join(parts, ":")
}
