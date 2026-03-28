package domain

// HandoffPack 是长会话切换或恢复时使用的结构化交接包。
type HandoffPack struct {
	Reason          string        `json:"reason,omitempty"`
	UpdatedAt       string        `json:"updated_at,omitempty"`
	NovelName       string        `json:"novel_name,omitempty"`
	Phase           string        `json:"phase,omitempty"`
	Flow            string        `json:"flow,omitempty"`
	PlanningTier    string        `json:"planning_tier,omitempty"`
	NextChapter     int           `json:"next_chapter,omitempty"`
	CompletedCount  int           `json:"completed_count,omitempty"`
	TotalChapters   int           `json:"total_chapters,omitempty"`
	TotalWordCount  int           `json:"total_word_count,omitempty"`
	PendingSteer    string        `json:"pending_steer,omitempty"`
	PendingRewrites []int         `json:"pending_rewrites,omitempty"`
	RewriteReason   string        `json:"rewrite_reason,omitempty"`
	LastCommit      string        `json:"last_commit,omitempty"`
	LastReview      string        `json:"last_review,omitempty"`
	RecentSummaries []string      `json:"recent_summaries,omitempty"`
	MemoryPolicy    *MemoryPolicy `json:"memory_policy,omitempty"`
	Guidance        []string      `json:"guidance,omitempty"`
}
