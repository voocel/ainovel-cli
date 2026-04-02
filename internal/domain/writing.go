package domain

// ChapterPlan 章节写作构思，Writer 自主生成。
// 不再强制场景拆分，Agent 自己决定如何组织内容。
type ChapterPlan struct {
	Chapter    int             `json:"chapter"`
	Title      string          `json:"title"`
	Goal       string          `json:"goal"`
	Conflict   string          `json:"conflict"`
	Hook       string          `json:"hook"`
	EmotionArc string          `json:"emotion_arc,omitempty"`
	Notes      string          `json:"notes,omitempty"` // Agent 的自由备忘
	Contract   ChapterContract `json:"contract,omitempty"`
}

// ChapterContract 是 Writer 和 Editor 共享的章节验收契约。
// 它定义本章必须完成的推进项、禁止越界项以及审阅关注点。
type ChapterContract struct {
	RequiredBeats    []string `json:"required_beats,omitempty"`    // 本章必须落地的推进项
	ForbiddenMoves   []string `json:"forbidden_moves,omitempty"`   // 本章明确不能发生的推进
	ContinuityChecks []string `json:"continuity_checks,omitempty"` // 本章需特别核对的连续性点
	EvaluationFocus  []string `json:"evaluation_focus,omitempty"`  // Editor 需要重点检查的点
	EmotionTarget    string   `json:"emotion_target,omitempty"`    // 可选：本章希望读者主要感受到的情绪
	PayoffPoints     []string `json:"payoff_points,omitempty"`     // 可选：关键章希望回应的情节点/兑现点
	HookGoal         string   `json:"hook_goal,omitempty"`         // 可选：章末钩子希望驱动的追读欲望
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

// WritingStyleRules 从已写章节中提炼的写作规则，弧边界时由 Editor 生成。
// 取代原文片段（style_anchors / voice_samples），用规则替代搬运原文。
type WritingStyleRules struct {
	Volume    int              `json:"volume"`
	Arc       int              `json:"arc"`
	Prose     []string         `json:"prose"`      // 3-5 条叙述风格规则，每条 ≤50 字
	Dialogue  []CharacterVoice `json:"dialogue"`   // 角色对话风格规则
	Taboos    []string         `json:"taboos"`     // 禁忌清单
	UpdatedAt string           `json:"updated_at"` // ISO8601 时间戳
}

// CharacterVoice 单个角色的对话风格规则。
type CharacterVoice struct {
	Name  string   `json:"name"`
	Rules []string `json:"rules"` // 2-3 条语言特征规则，每条 ≤30 字
}

// RelatedChapter 推荐回读的相关章节。
type RelatedChapter struct {
	Chapter int    `json:"chapter"`
	Reason  string `json:"reason"`
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
	ArcEnd         bool `json:"arc_end,omitempty"`
	VolumeEnd      bool `json:"volume_end,omitempty"`
	Volume         int  `json:"volume,omitempty"`
	Arc            int  `json:"arc,omitempty"`
	NeedsExpansion bool `json:"needs_expansion,omitempty"`  // 下一弧是骨架，需要展开章节
	NeedsNewVolume bool `json:"needs_new_volume,omitempty"` // 需要 Architect 创建下一卷
	NextVolume     int  `json:"next_volume,omitempty"`      // 下一弧/卷序号
	NextArc        int  `json:"next_arc,omitempty"`         // 下一弧序号
}
