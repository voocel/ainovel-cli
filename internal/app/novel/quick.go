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
	"github.com/voocel/ainovel-cli/internal/domain/creation"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

type QuickWriteCommand struct {
	ProjectID string
	UserID    string
	Premise   string
	Chapters  int
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
	if !s.tasks.HasLLM() {
		return QuickWriteResult{}, fmt.Errorf("quick write requires a configured model: %w", model.ErrInvalid)
	}
	if strings.TrimSpace(command.ProjectID) == "" || strings.TrimSpace(command.UserID) == "" ||
		strings.TrimSpace(command.Premise) == "" || command.Chapters <= 0 ||
		strings.TrimSpace(command.WorkerID) == "" || command.LeaseDuration <= 0 || command.CreatedAt.IsZero() {
		return QuickWriteResult{}, fmt.Errorf("quick write project, user, premise, positive chapters, worker, lease and time are required: %w", model.ErrInvalid)
	}
	switch command.Approval {
	case "", model.ApprovalAuto, model.ApprovalMilestone, model.ApprovalManual:
	default:
		return QuickWriteResult{}, fmt.Errorf("quick write approval must be auto, milestone or manual: %w", model.ErrInvalid)
	}

	result := QuickWriteResult{ProjectID: command.ProjectID}
	recovered, err := s.tasks.RecoverOperations(ctx, command.CreatedAt)
	if err != nil {
		return result, err
	}
	result.RecoveredOperations = recovered
	project, err := s.ensureQuickProject(ctx, command)
	if err != nil {
		return result, err
	}
	run, err := s.ensureCreationRun(ctx, command, project.Approval)
	if err != nil {
		return result, err
	}
	outcome, err := s.runs.Drive(ctx, run, s.creationTasks(command), driveCommand(command))
	result = attachRun(outcome.Run, result)
	result.WaitingOperationID = outcome.Waiting
	if err != nil {
		return result, err
	}
	return s.finishQuickResult(ctx, outcome, result)
}

func attachRun(run model.CreationRun, result QuickWriteResult) QuickWriteResult {
	result.RunID, result.RunState, result.RunReason = run.ID, run.State, run.StateReason
	return result
}

// finishQuickResult 把驱动落点投影成 quick 结果：目标章数内每章的状态与来源 Operation。
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
	plans := chapterPlansInOrder(project.Plan)
	if len(plans) > goal.TargetChapters {
		plans = plans[:goal.TargetChapters]
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
			model.DocumentRef{Kind: model.DocumentManuscript, ID: chapter.ID}, project.Revision,
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
// 策略写入权威（§6.3）；此后同一命令重入时按需更新目标章数（Intent 变更）与
// 审批策略，全部走唯一写路径。
func (s *Application) ensureQuickProject(ctx context.Context, command QuickWriteCommand) (projectdoc.Snapshot, error) {
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: command.ProjectID}
	_, err := s.store.CurrentRevision(ctx, target)
	if errors.Is(err, model.ErrNotFound) {
		approval := command.Approval
		if approval == "" {
			approval = model.ApprovalAuto
		}
		return s.projects.CreateProject(ctx, projectdoc.CreateProjectCommand{
			ProjectID: command.ProjectID,
			ChangeID:  quickID(command.ProjectID, "create"),
			UserID:    command.UserID,
			Reason:    "一句话创建作品",
			Draft: projectdoc.ProjectDraft{
				Intent:   model.Intent{Premise: command.Premise, TargetChapters: command.Chapters},
				Approval: approval,
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
			"project %q was started from a different premise: %w", command.ProjectID, model.ErrIdempotencyConflict)
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
	if project.Intent.TargetChapters != command.Chapters {
		intent := project.Intent
		intent.TargetChapters = command.Chapters
		content, err := json.Marshal(intent)
		if err != nil {
			return projectdoc.Snapshot{}, fmt.Errorf("encode intent goal update: %w", err)
		}
		committed, err := s.changes.CommitUser(ctx, model.Proposal{
			ID:           fmt.Sprintf("%s@r%d", quickID(command.ProjectID, "goal", strconv.Itoa(command.Chapters)), project.Revision),
			Target:       target,
			BaseRevision: project.Revision,
			Author:       model.Author{Kind: model.AuthorUser, ID: command.UserID},
			Reason:       fmt.Sprintf("把目标章数调整为 %d", command.Chapters),
			Patches: []model.Patch{{
				Document:  model.DocumentRef{Kind: model.DocumentIntent, ID: "root"},
				Operation: model.PatchPut, Content: content,
			}},
			ApprovalState: model.ApprovalPending, CreatedAt: command.CreatedAt,
		}, command.CreatedAt)
		if err != nil {
			return projectdoc.Snapshot{}, err
		}
		return s.projects.Project(ctx, command.ProjectID, committed.NewRevision)
	}
	return project, nil
}

// ensureCreationRun 复用进行中的 Run（目标变化时更新当前 Run，不启动竞争 Run，
// §6.3），否则开启新一轮。终态的历史轮次留在原地：已完成的书再次执行会开启
// 一轮只做完成验证的 Run，缺章时则真正续写。
func (s *Application) ensureCreationRun(
	ctx context.Context,
	command QuickWriteCommand,
	projectApproval model.ApprovalPolicy,
) (model.CreationRun, error) {
	goal := model.NovelGoal{Premise: command.Premise, TargetChapters: command.Chapters}
	approval := projectApproval
	if approval == "" {
		approval = model.ApprovalAuto
	}
	active, err := s.store.ActiveCreationRun(ctx, command.ProjectID)
	if err == nil {
		current, err := model.DecodeNovelGoal(active.Goal)
		if err != nil {
			return model.CreationRun{}, err
		}
		if current.Premise != goal.Premise {
			return model.CreationRun{}, fmt.Errorf(
				"project %q already has an active creation run with a different premise: %w",
				command.ProjectID, model.ErrIdempotencyConflict)
		}
		if current != goal {
			if active, err = s.store.UpdateCreationRunGoal(ctx, active.ID, goal.Goal(), command.CreatedAt); err != nil {
				return model.CreationRun{}, err
			}
		}
		return active, nil
	}
	if !errors.Is(err, model.ErrNotFound) {
		return model.CreationRun{}, err
	}
	count, err := s.store.CountCreationRuns(ctx, command.ProjectID)
	if err != nil {
		return model.CreationRun{}, err
	}
	strategy := model.CreationRunStrategy{
		PlanWindowChapters: 3,
		ReviewCadence:      model.ReviewPerPlanWindow,
		AutoRepairBudget:   command.Chapters,
	}
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
