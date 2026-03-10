package domain

// ChapterPlan 章节规划，写入 drafts/{ch}.plan.json。
type ChapterPlan struct {
	Chapter    int         `json:"chapter"`
	Title      string      `json:"title"`
	Goal       string      `json:"goal"`
	Conflict   string      `json:"conflict"`
	Scenes     []ScenePlan `json:"scenes"`
	Hook       string      `json:"hook"`
	EmotionArc string      `json:"emotion_arc,omitempty"`
}

// ScenePlan 场景规划。
type ScenePlan struct {
	Index    int    `json:"index"`
	Summary  string `json:"summary"`
	POV      string `json:"pov,omitempty"`
	Location string `json:"location,omitempty"`
}

// SceneDraft 场景草稿。
type SceneDraft struct {
	Chapter   int    `json:"chapter"`
	Scene     int    `json:"scene"`
	Content   string `json:"content"`
	WordCount int    `json:"word_count"`
}

// ChapterSummary 章节摘要，供后续章节的上下文窗口使用。
type ChapterSummary struct {
	Chapter    int      `json:"chapter"`
	Summary    string   `json:"summary"`
	Characters []string `json:"characters"`
	KeyEvents  []string `json:"key_events"`
}

// CommitResult 是 commit_chapter 工具的结构化返回值。
// 宿主程序和 Coordinator 读取此信号做控制决策。
type CommitResult struct {
	Chapter        int    `json:"chapter"`
	Committed      bool   `json:"committed"`
	WordCount      int    `json:"word_count"`
	SceneCount     int    `json:"scene_count"`
	NextChapter    int    `json:"next_chapter"`
	ReviewRequired bool   `json:"review_required"`
	ReviewReason   string `json:"review_reason,omitempty"`
	HookType       string `json:"hook_type,omitempty"`       // 钩子类型：crisis/mystery/desire/emotion/choice
	DominantStrand string `json:"dominant_strand,omitempty"` // 本章主导线：quest/fire/constellation
}
