package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

type QuickWriteCommand struct {
	ProjectID string
	UserID    string
	Premise   string
	Chapters  int
	// Approval 请求更新 Project 的有效审批策略；Run 创建时只保留不可变预设摘要。
	Approval        domain.ApprovalPolicy
	Packs           []PackRef
	CreatorProfiles []CreatorProfileRef
	WorkerID        string
	LeaseDuration   time.Duration
	CreatedAt       time.Time
}

type QuickChapterResult struct {
	ID          string                `json:"id"`
	PlanNodeID  string                `json:"plan_node_id"`
	Number      int                   `json:"number"`
	Title       string                `json:"title"`
	OperationID string                `json:"operation_id"`
	State       domain.OperationState `json:"state"`
}

type QuickWriteResult struct {
	ProjectID           string                  `json:"project_id"`
	Revision            domain.Revision         `json:"revision"`
	RunID               string                  `json:"run_id"`
	RunState            domain.CreationRunState `json:"run_state"`
	RunReason           string                  `json:"run_reason,omitempty"`
	WaitingOperationID  string                  `json:"waiting_operation_id,omitempty"`
	RecoveredOperations []string                `json:"recovered_operations,omitempty"`
	Chapters            []QuickChapterResult    `json:"chapters"`
}

// QuickWrite 是“一句话写全书”的入口：它只是 CreationRun 的一个薄预设（D24/D27）——
// 建立 Project 与 Run 后，全部推进都由创作协调器驱动。等待与失败都会落在 Run 上，
// 再次执行同一命令即从落点继续。
func (s *Service) QuickWrite(ctx context.Context, command QuickWriteCommand) (QuickWriteResult, error) {
	if s.executor == nil {
		return QuickWriteResult{}, fmt.Errorf("quick write requires a configured model: %w", domain.ErrInvalid)
	}
	if strings.TrimSpace(command.ProjectID) == "" || strings.TrimSpace(command.UserID) == "" ||
		strings.TrimSpace(command.Premise) == "" || command.Chapters <= 0 ||
		strings.TrimSpace(command.WorkerID) == "" || command.LeaseDuration <= 0 || command.CreatedAt.IsZero() {
		return QuickWriteResult{}, fmt.Errorf("quick write project, user, premise, positive chapters, worker, lease and time are required: %w", domain.ErrInvalid)
	}
	switch command.Approval {
	case "", domain.ApprovalAuto, domain.ApprovalMilestone, domain.ApprovalManual:
	default:
		return QuickWriteResult{}, fmt.Errorf("quick write approval must be auto, milestone or manual: %w", domain.ErrInvalid)
	}

	result := QuickWriteResult{ProjectID: command.ProjectID}
	recovered, err := s.RecoverOperations(ctx, command.CreatedAt)
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
	return s.driveCreationRun(ctx, run, command, result)
}

// ensureQuickProject 建立或对账作品：首次调用在初始化事务里把 Intent 与审批
// 策略写入权威（§6.3）；此后同一命令重入时按需更新目标章数（Intent 变更）与
// 审批策略，全部走唯一写路径。
func (s *Service) ensureQuickProject(ctx context.Context, command QuickWriteCommand) (ProjectSnapshot, error) {
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: command.ProjectID}
	_, err := s.store.CurrentRevision(ctx, target)
	if errors.Is(err, store.ErrNotFound) {
		approval := command.Approval
		if approval == "" {
			approval = domain.ApprovalAuto
		}
		return s.CreateProject(ctx, CreateProjectCommand{
			ProjectID: command.ProjectID,
			ChangeID:  quickID(command.ProjectID, "create"),
			UserID:    command.UserID,
			Reason:    "一句话创建作品",
			Draft: ProjectDraft{
				Intent:   domain.Intent{Premise: command.Premise, TargetChapters: command.Chapters},
				Approval: approval,
			},
			CreatedAt: command.CreatedAt,
		})
	}
	if err != nil {
		return ProjectSnapshot{}, err
	}
	project, err := s.Project(ctx, command.ProjectID, domain.InitialRevision)
	if err != nil {
		return ProjectSnapshot{}, err
	}
	if project.Intent.Premise != command.Premise {
		return ProjectSnapshot{}, fmt.Errorf(
			"project %q was started from a different premise: %w", command.ProjectID, store.ErrIdempotencyConflict)
	}
	if command.Approval != "" && command.Approval != project.Approval {
		if _, err := s.SetApprovalPolicy(ctx, SetApprovalPolicyCommand{
			ProjectID: command.ProjectID,
			ChangeID:  fmt.Sprintf("%s@r%d", quickID(command.ProjectID, "approval", string(command.Approval)), project.Revision),
			UserID:    command.UserID, Policy: command.Approval,
			Reason: "调整创作的审批预设", CreatedAt: command.CreatedAt,
		}); err != nil {
			return ProjectSnapshot{}, err
		}
		if project, err = s.Project(ctx, command.ProjectID, domain.InitialRevision); err != nil {
			return ProjectSnapshot{}, err
		}
	}
	if project.Intent.TargetChapters != command.Chapters {
		intent := project.Intent
		intent.TargetChapters = command.Chapters
		content, err := json.Marshal(intent)
		if err != nil {
			return ProjectSnapshot{}, fmt.Errorf("encode intent goal update: %w", err)
		}
		committed, err := s.commitUserProposal(ctx, domain.Proposal{
			ID:           fmt.Sprintf("%s@r%d", quickID(command.ProjectID, "goal", strconv.Itoa(command.Chapters)), project.Revision),
			Target:       target,
			BaseRevision: project.Revision,
			Author:       domain.Author{Kind: domain.AuthorUser, ID: command.UserID},
			Reason:       fmt.Sprintf("把目标章数调整为 %d", command.Chapters),
			Patches: []domain.Patch{{
				Document:  domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"},
				Operation: domain.PatchPut, Content: content,
			}},
			ApprovalState: domain.ApprovalPending, CreatedAt: command.CreatedAt,
		}, command.CreatedAt)
		if err != nil {
			return ProjectSnapshot{}, err
		}
		return s.Project(ctx, command.ProjectID, committed.NewRevision)
	}
	return project, nil
}

// ensureCreationRun 复用进行中的 Run（目标变化时更新当前 Run，不启动竞争 Run，
// §6.3），否则开启新一轮。终态的历史轮次留在原地：已完成的书再次执行会开启
// 一轮只做完成验证的 Run，缺章时则真正续写。
func (s *Service) ensureCreationRun(
	ctx context.Context,
	command QuickWriteCommand,
	projectApproval domain.ApprovalPolicy,
) (domain.CreationRun, error) {
	goal := domain.CreationRunGoal{Premise: command.Premise, TargetChapters: command.Chapters}
	approval := projectApproval
	if approval == "" {
		approval = domain.ApprovalAuto
	}
	active, err := s.store.ActiveCreationRun(ctx, command.ProjectID)
	if err == nil {
		if active.Goal.Premise != goal.Premise {
			return domain.CreationRun{}, fmt.Errorf(
				"project %q already has an active creation run with a different premise: %w",
				command.ProjectID, store.ErrIdempotencyConflict)
		}
		if active.Goal != goal {
			if active, err = s.store.UpdateCreationRunGoal(ctx, active.ID, goal, command.CreatedAt); err != nil {
				return domain.CreationRun{}, err
			}
		}
		return active, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return domain.CreationRun{}, err
	}
	count, err := s.store.CountCreationRuns(ctx, command.ProjectID)
	if err != nil {
		return domain.CreationRun{}, err
	}
	strategy := domain.CreationRunStrategy{
		PlanWindowChapters: 3,
		ReviewCadence:      domain.ReviewPerPlanWindow,
		AutoRepairBudget:   command.Chapters,
	}
	preset, err := domain.NewCreationRunPreset("quick", approval, strategy)
	if err != nil {
		return domain.CreationRun{}, err
	}
	return s.StartCreationRun(ctx, StartCreationRunCommand{
		RunID: fmt.Sprintf("run:%s:%d", command.ProjectID, count+1), ProjectID: command.ProjectID,
		Goal: goal, Strategy: strategy, Preset: preset, CreatedAt: command.CreatedAt,
	})
}

func quickID(projectID string, parts ...string) string {
	return "quick:" + projectID + ":" + strings.Join(parts, ":")
}
