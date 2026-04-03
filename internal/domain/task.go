package domain

import "time"

// TaskKind 表示小说运行时中的领域任务类型。
type TaskKind string

const (
	TaskFoundationPlan      TaskKind = "foundation_plan"
	TaskChapterWrite        TaskKind = "chapter_write"
	TaskChapterReview       TaskKind = "chapter_review"
	TaskChapterRewrite      TaskKind = "chapter_rewrite"
	TaskChapterPolish       TaskKind = "chapter_polish"
	TaskArcExpand           TaskKind = "arc_expand"
	TaskVolumeAppend        TaskKind = "volume_append"
	TaskSteerApply          TaskKind = "steer_apply"
	TaskCoordinatorDecision TaskKind = "coordinator_decision"
)

// TaskStatus 表示任务生命周期状态。
type TaskStatus string

const (
	TaskQueued    TaskStatus = "queued"
	TaskRunning   TaskStatus = "running"
	TaskBlocked   TaskStatus = "blocked"
	TaskSucceeded TaskStatus = "succeeded"
	TaskFailed    TaskStatus = "failed"
	TaskCanceled  TaskStatus = "canceled"
)

// TaskProgress 表示任务的运行中进度。
type TaskProgress struct {
	Stage       string `json:"stage,omitempty"`
	Percent     int    `json:"percent,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Turn        int    `json:"turn,omitempty"`
	Tool        string `json:"tool,omitempty"`
	ToolSummary string `json:"tool_summary,omitempty"`
}

// TaskRecord 是运行时任务的持久化记录。
type TaskRecord struct {
	ID        string       `json:"id"`
	Kind      TaskKind     `json:"kind"`
	Owner     string       `json:"owner"`
	Title     string       `json:"title"`
	Status    TaskStatus   `json:"status"`
	Chapter   int          `json:"chapter,omitempty"`
	Volume    int          `json:"volume,omitempty"`
	Arc       int          `json:"arc,omitempty"`
	Input     string       `json:"input,omitempty"`
	OutputRef string       `json:"output_ref,omitempty"`
	Error     string       `json:"error,omitempty"`
	Progress  TaskProgress `json:"progress,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
	StartedAt time.Time    `json:"started_at,omitempty"`
	UpdatedAt time.Time    `json:"updated_at"`
	EndedAt   time.Time    `json:"ended_at,omitempty"`
}

// IsTerminal 返回任务是否已进入终态。
func (t TaskRecord) IsTerminal() bool {
	switch t.Status {
	case TaskSucceeded, TaskFailed, TaskCanceled:
		return true
	default:
		return false
	}
}
