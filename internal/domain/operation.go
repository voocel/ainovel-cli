package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type OperationKind string

const (
	OperationInitializeProject OperationKind = "initialize_project"
	OperationDevelopPlan       OperationKind = "develop_plan"
	OperationWriteChapter      OperationKind = "write_chapter"
	OperationRevisePlan        OperationKind = "revise_plan"
	OperationReviseCanon       OperationKind = "revise_canon"
	OperationRewriteChapter    OperationKind = "rewrite_chapter"
	OperationRewriteAffected   OperationKind = "rewrite_affected"
	OperationReviewRange       OperationKind = "review_range"
	OperationUpdateProfile     OperationKind = "update_creator_profile"
	OperationRebuildDerived    OperationKind = "rebuild_derived_data"
)

// RequiresCreationRun 把“影响故事内容的 AI Operation”收敛为一个领域判定；
// 维护派生数据与跨作品 Profile 更新可以独立存在（D27）。
func (k OperationKind) RequiresCreationRun() bool {
	switch k {
	case OperationInitializeProject, OperationDevelopPlan, OperationWriteChapter,
		OperationRevisePlan, OperationReviseCanon, OperationRewriteChapter,
		OperationRewriteAffected, OperationReviewRange:
		return true
	default:
		return false
	}
}

type OperationState string

const (
	OperationQueued           OperationState = "queued"
	OperationRunning          OperationState = "running"
	OperationPaused           OperationState = "paused"
	OperationAwaitingApproval OperationState = "awaiting_approval"
	OperationSucceeded        OperationState = "succeeded"
	OperationFailed           OperationState = "failed"
	OperationCancelled        OperationState = "cancelled"
	OperationStale            OperationState = "stale"
)

type ApprovalPolicy string

const (
	ApprovalAuto      ApprovalPolicy = "auto"
	ApprovalMilestone ApprovalPolicy = "milestone"
	ApprovalManual    ApprovalPolicy = "manual"
	ApprovalCustom    ApprovalPolicy = "custom"
)

// StricterApproval 取两份策略中更严格的一方（D23）：`auto < milestone < manual`；
// custom 依赖显式契约，视为最严格，绝不被静默降级。
func StricterApproval(left, right ApprovalPolicy) ApprovalPolicy {
	rank := func(policy ApprovalPolicy) int {
		switch policy {
		case ApprovalMilestone:
			return 1
		case ApprovalManual:
			return 2
		case ApprovalCustom:
			return 3
		default:
			return 0
		}
	}
	if rank(right) > rank(left) {
		return right
	}
	return left
}

type ExecutionSnapshot struct {
	ExecutionProfileDigest string         `json:"execution_profile_digest"`
	BaseRevision           Revision       `json:"base_project_revision"`
	CoreProtocolVersion    string         `json:"core_protocol_version"`
	WorkerProfileVersion   string         `json:"worker_profile_version"`
	ToolSchemaDigest       string         `json:"ordered_tool_schema_digest"`
	PromptDigest           string         `json:"compiled_prompt_digest"`
	PackSetDigest          string         `json:"pack_set_digest,omitempty"`
	CreatorProfileRev      Revision       `json:"creator_profile_revision,omitempty"`
	CreatorProfileDigest   string         `json:"creator_profile_digest,omitempty"`
	ProjectOverlayRev      Revision       `json:"project_overlay_revision,omitempty"`
	ModelConfigDigest      string         `json:"model_config_digest"`
	ApprovalPolicy         ApprovalPolicy `json:"approval_policy"`
	ApprovalPolicyDigest   string         `json:"approval_policy_digest"`
}

func (s ExecutionSnapshot) Validate() error {
	if s.BaseRevision < InitialRevision || s.CreatorProfileRev < InitialRevision || s.ProjectOverlayRev < InitialRevision {
		return fmt.Errorf("snapshot revisions cannot be negative: %w", ErrInvalid)
	}
	required := map[string]string{
		"execution_profile_digest": s.ExecutionProfileDigest,
		"core_protocol_version":    s.CoreProtocolVersion,
		"worker_profile_version":   s.WorkerProfileVersion,
		"tool_schema_digest":       s.ToolSchemaDigest,
		"prompt_digest":            s.PromptDigest,
		"model_config_digest":      s.ModelConfigDigest,
		"approval_policy_digest":   s.ApprovalPolicyDigest,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("snapshot %s is required: %w", name, ErrInvalid)
		}
	}
	switch s.ApprovalPolicy {
	case ApprovalAuto, ApprovalMilestone, ApprovalManual, ApprovalCustom:
	default:
		return fmt.Errorf("unknown approval policy %q: %w", s.ApprovalPolicy, ErrInvalid)
	}
	return nil
}

type ExecutionProfileRecord struct {
	Digest           string            `json:"digest"`
	ProjectID        string            `json:"project_id"`
	WorkerProfile    string            `json:"worker_profile"`
	PromptDigest     string            `json:"prompt_digest"`
	ToolSchemaDigest string            `json:"tool_schema_digest"`
	Snapshot         ExecutionSnapshot `json:"snapshot"`
	StablePrefix     string            `json:"stable_prefix"`
	DynamicTail      string            `json:"dynamic_tail"`
	Tools            json.RawMessage   `json:"tools"`
	Sources          json.RawMessage   `json:"sources"`
	CreatedAt        time.Time         `json:"created_at"`
}

func (r ExecutionProfileRecord) Validate() error {
	if strings.TrimSpace(r.Digest) == "" || r.Digest != r.Snapshot.ExecutionProfileDigest ||
		strings.TrimSpace(r.ProjectID) == "" || strings.TrimSpace(r.WorkerProfile) == "" ||
		r.PromptDigest != r.Snapshot.PromptDigest || r.ToolSchemaDigest != r.Snapshot.ToolSchemaDigest ||
		strings.TrimSpace(r.StablePrefix) == "" || strings.TrimSpace(r.DynamicTail) == "" || r.CreatedAt.IsZero() {
		return fmt.Errorf("execution profile record is inconsistent: %w", ErrInvalid)
	}
	if len(r.Tools) == 0 || !json.Valid(r.Tools) || len(r.Sources) == 0 || !json.Valid(r.Sources) {
		return fmt.Errorf("execution profile tools and sources must be valid JSON: %w", ErrInvalid)
	}
	return r.Snapshot.Validate()
}

type Operation struct {
	ID        string          `json:"id"`
	Kind      OperationKind   `json:"kind"`
	Target    AuthorityTarget `json:"target"`
	DependsOn []string        `json:"depends_on,omitempty"`
	Priority  int             `json:"priority"`
	State     OperationState  `json:"state"`
	Attempt   int             `json:"attempt"`
	// RunID 是创作血统归属（§6.3）：影响故事内容的 AI Operation 必须归属唯一
	// CreationRun；纯维护类 Operation 留空。RunPolicyVersion 由存储层在创建
	// 事务内解析为当时最近一次策略事件的序号，调用方不填。
	RunID            string            `json:"run_id,omitempty"`
	RunPolicyVersion int64             `json:"run_policy_version,omitempty"`
	Snapshot         ExecutionSnapshot `json:"execution_snapshot"`
	Input            json.RawMessage   `json:"input"`
	Error            string            `json:"error,omitempty"`
	LeaseOwner       string            `json:"lease_owner,omitempty"`
	LeaseUntil       *time.Time        `json:"lease_until,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
}

func (o Operation) Validate() error {
	if strings.TrimSpace(o.ID) == "" {
		return fmt.Errorf("operation id is required: %w", ErrInvalid)
	}
	switch o.Kind {
	case OperationInitializeProject, OperationDevelopPlan, OperationWriteChapter,
		OperationRevisePlan, OperationReviseCanon, OperationRewriteChapter, OperationRewriteAffected,
		OperationReviewRange, OperationUpdateProfile, OperationRebuildDerived:
	default:
		return fmt.Errorf("unknown operation kind %q: %w", o.Kind, ErrInvalid)
	}
	if err := o.Target.Validate(); err != nil {
		return err
	}
	seenDependencies := make(map[string]struct{}, len(o.DependsOn))
	for i, dependency := range o.DependsOn {
		if strings.TrimSpace(dependency) == "" || dependency == o.ID {
			return fmt.Errorf("operation dependency %d is invalid: %w", i, ErrInvalid)
		}
		if _, ok := seenDependencies[dependency]; ok {
			return fmt.Errorf("duplicate operation dependency %q: %w", dependency, ErrInvalid)
		}
		seenDependencies[dependency] = struct{}{}
	}
	switch o.State {
	case OperationQueued, OperationRunning, OperationPaused, OperationAwaitingApproval,
		OperationSucceeded, OperationFailed, OperationCancelled, OperationStale:
	default:
		return fmt.Errorf("unknown operation state %q: %w", o.State, ErrInvalid)
	}
	if o.Attempt < 0 {
		return fmt.Errorf("operation attempt cannot be negative: %w", ErrInvalid)
	}
	if err := o.Snapshot.Validate(); err != nil {
		return err
	}
	if len(o.Input) == 0 || !json.Valid(o.Input) {
		return fmt.Errorf("operation input must be valid JSON: %w", ErrInvalid)
	}
	if o.CreatedAt.IsZero() || o.UpdatedAt.IsZero() || o.UpdatedAt.Before(o.CreatedAt) {
		return fmt.Errorf("operation timestamps are invalid: %w", ErrInvalid)
	}
	if o.State == OperationRunning {
		if strings.TrimSpace(o.LeaseOwner) == "" || o.LeaseUntil == nil || o.LeaseUntil.IsZero() {
			return fmt.Errorf("running operation requires lease: %w", ErrInvalid)
		}
	} else if o.LeaseOwner != "" || o.LeaseUntil != nil {
		return fmt.Errorf("non-running operation cannot hold lease: %w", ErrInvalid)
	}
	return nil
}

func CanTransitionOperation(from, to OperationState) bool {
	switch from {
	case OperationQueued:
		return to == OperationRunning || to == OperationPaused || to == OperationCancelled || to == OperationStale
	case OperationRunning:
		return to == OperationPaused || to == OperationAwaitingApproval || to == OperationSucceeded ||
			to == OperationFailed || to == OperationCancelled || to == OperationStale || to == OperationQueued
	case OperationPaused:
		return to == OperationQueued || to == OperationCancelled || to == OperationStale
	case OperationAwaitingApproval:
		return to == OperationQueued || to == OperationSucceeded || to == OperationFailed ||
			to == OperationCancelled || to == OperationStale
	case OperationFailed:
		return to == OperationQueued || to == OperationCancelled
	default:
		return false
	}
}

func OperationTerminal(state OperationState) bool {
	return state == OperationSucceeded || state == OperationCancelled || state == OperationStale
}

type WorkspaceArtifact struct {
	OperationID string    `json:"operation_id"`
	Key         string    `json:"key"`
	Version     int64     `json:"version"`
	MediaType   string    `json:"media_type"`
	Content     []byte    `json:"content"`
	Digest      string    `json:"digest"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type OperationEvent struct {
	OperationID    string          `json:"operation_id"`
	Sequence       int64           `json:"sequence"`
	StepID         string          `json:"step_id"`
	Attempt        int             `json:"attempt"`
	IdempotencyKey string          `json:"idempotency_key"`
	Kind           string          `json:"kind"`
	Payload        json.RawMessage `json:"payload"`
	CreatedAt      time.Time       `json:"created_at"`
}
