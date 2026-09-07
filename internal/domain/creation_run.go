package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// CreationRun 是一次持续创作的运行主体（D27）：从目标出发驱动一串 Operation，
// 直到完成契约满足（D29）或被显式终止。它记录目标、运行策略与预设摘要；
// Ownership/Approval 等控制权威仍以 Project 最新已批准 Revision 为准。
type CreationRunState string

const (
	RunRunning     CreationRunState = "running"
	RunWaitingUser CreationRunState = "waiting_user"
	RunPaused      CreationRunState = "paused"
	RunCompleted   CreationRunState = "completed"
	RunFailed      CreationRunState = "failed"
	RunCancelled   CreationRunState = "cancelled"
)

type CreationRunGoal struct {
	Premise        string `json:"premise"`
	TargetChapters int    `json:"target_chapters"`
}

type ReviewCadence string

const ReviewPerPlanWindow ReviewCadence = "per_plan_window"

// CreationRunStrategy 是连续创作的显式自动化边界。预设只负责给出初值，
// Coordinator 此后只读取这份版本化策略，不再判断预设身份（D24/D27）。
type CreationRunStrategy struct {
	PlanWindowChapters int           `json:"plan_window_chapters"`
	ReviewCadence      ReviewCadence `json:"review_cadence"`
	AutoRepairBudget   int           `json:"auto_repair_budget"`
}

func (s CreationRunStrategy) Validate() error {
	if s.PlanWindowChapters <= 0 || s.AutoRepairBudget < 0 {
		return fmt.Errorf("creation run strategy requires a positive plan window and non-negative repair budget: %w", ErrInvalid)
	}
	if s.ReviewCadence != ReviewPerPlanWindow {
		return fmt.Errorf("unknown review cadence %q: %w", s.ReviewCadence, ErrInvalid)
	}
	return nil
}

// CreationRunPreset 只用于追溯启动边界，不是当前控制策略。Approval 的唯一
// 有效来源始终是 Project 最新已批准 Revision。
type CreationRunPreset struct {
	Source   string         `json:"source"`
	Digest   string         `json:"digest"`
	Approval ApprovalPolicy `json:"approval"`
}

// NewCreationRunPreset 生成可追溯的启动预设：Source 只标记入口，身份摘要由
// approval 与 strategy 决定，各入口不得自行拼装。
func NewCreationRunPreset(
	source string,
	approval ApprovalPolicy,
	strategy CreationRunStrategy,
) (CreationRunPreset, error) {
	digest, err := DigestJSON(struct {
		Source   string              `json:"source"`
		Approval ApprovalPolicy      `json:"approval"`
		Strategy CreationRunStrategy `json:"strategy"`
	}{Source: source, Approval: approval, Strategy: strategy})
	if err != nil {
		return CreationRunPreset{}, fmt.Errorf("encode creation run preset: %w", err)
	}
	return CreationRunPreset{Source: source, Digest: digest, Approval: approval}, nil
}

func (p CreationRunPreset) Validate() error {
	if strings.TrimSpace(p.Source) == "" || strings.TrimSpace(p.Digest) == "" {
		return fmt.Errorf("creation run preset source and digest are required: %w", ErrInvalid)
	}
	switch p.Approval {
	case ApprovalAuto, ApprovalMilestone, ApprovalManual, ApprovalCustom:
		return nil
	default:
		return fmt.Errorf("unknown preset approval policy %q: %w", p.Approval, ErrInvalid)
	}
}

func (g CreationRunGoal) Validate() error {
	if strings.TrimSpace(g.Premise) == "" || g.TargetChapters <= 0 {
		return fmt.Errorf("creation run premise and positive target chapters are required: %w", ErrInvalid)
	}
	return nil
}

type CreationRun struct {
	ID          string              `json:"id"`
	ProjectID   string              `json:"project_id"`
	Goal        CreationRunGoal     `json:"goal"`
	Strategy    CreationRunStrategy `json:"strategy"`
	Preset      CreationRunPreset   `json:"preset"`
	State       CreationRunState    `json:"state"`
	StateReason string              `json:"state_reason,omitempty"`
	// CompletedRevision 把“完成”绑定到具体 Revision（D29）：之后的任何修改都是新一轮创作。
	CompletedRevision Revision  `json:"completed_revision,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func (r CreationRun) Validate() error {
	if strings.TrimSpace(r.ID) == "" || strings.TrimSpace(r.ProjectID) == "" {
		return fmt.Errorf("creation run id and project are required: %w", ErrInvalid)
	}
	if err := r.Goal.Validate(); err != nil {
		return err
	}
	if err := r.Strategy.Validate(); err != nil {
		return err
	}
	if err := r.Preset.Validate(); err != nil {
		return err
	}
	switch r.State {
	case RunRunning, RunWaitingUser, RunPaused, RunCompleted, RunFailed, RunCancelled:
	default:
		return fmt.Errorf("unknown creation run state %q: %w", r.State, ErrInvalid)
	}
	if r.State == RunCompleted && r.CompletedRevision <= InitialRevision {
		return fmt.Errorf("completed creation run must bind a revision: %w", ErrInvalid)
	}
	if r.State != RunCompleted && r.CompletedRevision != InitialRevision {
		return fmt.Errorf("only completed creation runs bind a revision: %w", ErrInvalid)
	}
	if r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() || r.UpdatedAt.Before(r.CreatedAt) {
		return fmt.Errorf("creation run timestamps are invalid: %w", ErrInvalid)
	}
	return nil
}

// Run 事件（§6.3）：目标与运行策略的每次修改都是版本化事件；
// Operation 创建时绑定当时的策略版本（最近一次策略事件的序号）。
const (
	RunEventCreated          = "created"
	RunEventGoalUpdated      = "goal_updated"
	RunEventStrategyUpdated  = "strategy_updated"
	RunEventOperationCreated = "operation_created"
)

type CreationRunEvent struct {
	RunID     string          `json:"run_id"`
	Sequence  int64           `json:"sequence"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

func CanTransitionCreationRun(from, to CreationRunState) bool {
	switch from {
	case RunRunning:
		return to == RunWaitingUser || to == RunPaused || to == RunCompleted ||
			to == RunFailed || to == RunCancelled
	case RunWaitingUser:
		return to == RunRunning || to == RunPaused || to == RunFailed || to == RunCancelled
	case RunPaused:
		return to == RunRunning || to == RunCancelled
	default:
		return false
	}
}
