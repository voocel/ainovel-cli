package domain

// ChapterPlan 章节写作构思，Writer 自主生成。
// 不再强制场景拆分，Agent 自己决定如何组织内容。
type ChapterPlan struct {
	Chapter    int    `json:"chapter"`
	Title      string `json:"title"`
	Goal       string `json:"goal"`
	Conflict   string `json:"conflict"`
	Hook       string `json:"hook"`
	EmotionArc string `json:"emotion_arc,omitempty"`
	Notes      string `json:"notes,omitempty"` // Agent 的自由备忘
}

// ChapterSummary 章节摘要，供后续章节的上下文窗口使用。
type ChapterSummary struct {
	Chapter    int      `json:"chapter"`
	Summary    string   `json:"summary"`
	Characters []string `json:"characters"`
	KeyEvents  []string `json:"key_events"`
}

// ArcSummary 弧级摘要，弧结束时由 Editor 生成。
type ArcSummary struct {
	Volume    int      `json:"volume"`
	Arc       int      `json:"arc"`
	Title     string   `json:"title"`
	Summary   string   `json:"summary"`
	KeyEvents []string `json:"key_events"`
}

// VolumeSummary 卷级摘要，卷结束时生成。
type VolumeSummary struct {
	Volume    int      `json:"volume"`
	Title     string   `json:"title"`
	Summary   string   `json:"summary"`
	KeyEvents []string `json:"key_events"`
}

// CharacterSnapshot 角色状态快照，弧边界时记录。
type CharacterSnapshot struct {
	Volume     int    `json:"volume"`
	Arc        int    `json:"arc"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Power      string `json:"power,omitempty"`
	Motivation string `json:"motivation"`
	Relations  string `json:"relations,omitempty"`
}

// OutlineFeedback Writer 对大纲的反馈，提交章节时可选。
type OutlineFeedback struct {
	Deviation  string `json:"deviation"`  // 偏离描述
	Suggestion string `json:"suggestion"` // 调整建议
}

// CommitResult 是 commit_chapter 工具的结构化返回值。
// 宿主程序和 Coordinator 读取此信号做控制决策。
type CommitResult struct {
	Chapter        int              `json:"chapter"`
	Committed      bool             `json:"committed"`
	WordCount      int              `json:"word_count"`
	NextChapter    int              `json:"next_chapter"`
	ReviewRequired bool             `json:"review_required"`
	ReviewReason   string           `json:"review_reason,omitempty"`
	HookType       string           `json:"hook_type,omitempty"`
	DominantStrand string           `json:"dominant_strand,omitempty"`
	Feedback       *OutlineFeedback `json:"feedback,omitempty"`
	// 长篇分层信号
	ArcEnd    bool `json:"arc_end,omitempty"`
	VolumeEnd bool `json:"volume_end,omitempty"`
	Volume    int  `json:"volume,omitempty"`
	Arc       int  `json:"arc,omitempty"`
}
