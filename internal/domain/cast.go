package domain

// CastEntry 是配角名册中一条配角记录。
//
// 与 Character（characters.json，Architect 维护的核心档案）解耦：
//   - CastEntry 由 commit_chapter 工具自动累加，记录"出现过的有名字的次要角色"
//   - Character 由 Architect 显式设计，记录主角和关键配角的人格弧线/特质/tier
//
// 同名时以 Character 为准（核心角色不进 cast_ledger），避免重复。
type CastEntry struct {
	Name string `json:"name"`
	// Aliases 当前没有写入通道；预留给将来的"用户 steer 合并别名"工具
	// （如把'李掌柜'与'老李'声明为同一人）。MergeAppearances 已支持别名查找。
	Aliases          []string `json:"aliases,omitempty"`
	BriefRole        string   `json:"brief_role,omitempty"` // 一句话定位（首次出场由 Writer 填，可后续补全；不被覆盖）
	FirstSeenChapter int      `json:"first_seen_chapter"`
	LastSeenChapter  int      `json:"last_seen_chapter"`
	// AppearanceCount 派生自 len(AppearanceChapters)，merge 时保持同步。
	// 保留显式字段方便 UI/JSON 直接读，无需每次重算。
	AppearanceCount    int   `json:"appearance_count"`
	AppearanceChapters []int `json:"appearance_chapters"`
	// Promoted 标记此条目已升格到 characters.json。RecentActive 会跳过这些条目，
	// 避免与核心档案重复召回。当前升格通道未实现，字段为预留 hook。
	Promoted bool `json:"promoted,omitempty"`
}

// CastIntro 是 Writer 在 commit_chapter 时对新出场角色的简介声明。
// 仅在该名字首次出现或 ledger 中 BriefRole 仍为空时才被采用。
type CastIntro struct {
	Name      string `json:"name"`
	BriefRole string `json:"brief_role"`
}
