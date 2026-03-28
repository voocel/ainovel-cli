package domain

// CommitStage 表示章节提交 Saga 的当前阶段。
type CommitStage string

const (
	CommitStageStarted        CommitStage = "started"
	CommitStageStateApplied   CommitStage = "state_applied"
	CommitStageProgressMarked CommitStage = "progress_marked"
	CommitStageSignalSaved    CommitStage = "signal_saved"
)

// PendingCommit 记录章节提交中断时的恢复信息。
type PendingCommit struct {
	Chapter        int           `json:"chapter"`
	Stage          CommitStage   `json:"stage"`
	Summary        string        `json:"summary,omitempty"`
	HookType       string        `json:"hook_type,omitempty"`
	DominantStrand string        `json:"dominant_strand,omitempty"`
	Result         *CommitResult `json:"result,omitempty"`
	StartedAt      string        `json:"started_at,omitempty"`
	UpdatedAt      string        `json:"updated_at,omitempty"`
}
