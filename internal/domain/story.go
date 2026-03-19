package domain

// Novel 小说元信息。
type Novel struct {
	Name          string `json:"name"`
	TotalChapters int    `json:"total_chapters"`
}

// OutlineEntry 大纲条目，对应一章。
type OutlineEntry struct {
	Chapter   int      `json:"chapter"`
	Title     string   `json:"title"`
	CoreEvent string   `json:"core_event"`
	Hook      string   `json:"hook"`
	Scenes    []string `json:"scenes"`
}

// Character 角色档案。
type Character struct {
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases,omitempty"` // 别名/称号/绰号（如"废物少年"、"炎哥"）
	Role        string   `json:"role"`
	Description string   `json:"description"`
	Arc         string   `json:"arc"`
	Traits      []string `json:"traits"`
	Tier        string   `json:"tier,omitempty"` // core / important / secondary / decorative（默认 important）
}

// VolumeOutline 卷级大纲（长篇分层模式）。
type VolumeOutline struct {
	Index int          `json:"index"`
	Title string       `json:"title"`
	Theme string       `json:"theme"` // 本卷核心冲突/主题
	Arcs  []ArcOutline `json:"arcs"`
}

// ArcOutline 弧级大纲。
type ArcOutline struct {
	Index    int            `json:"index"` // 卷内弧序号
	Title    string         `json:"title"`
	Goal     string         `json:"goal"` // 弧目标（起承转合）
	Chapters []OutlineEntry `json:"chapters"`
}

// TotalChapters 计算分层大纲的总章节数。
func TotalChapters(volumes []VolumeOutline) int {
	n := 0
	for _, v := range volumes {
		for _, a := range v.Arcs {
			n += len(a.Chapters)
		}
	}
	return n
}

// FlattenOutline 将分层大纲展开为扁平章节列表，保持全局章节号连续。
func FlattenOutline(volumes []VolumeOutline) []OutlineEntry {
	var result []OutlineEntry
	ch := 1
	for _, v := range volumes {
		for _, a := range v.Arcs {
			for _, e := range a.Chapters {
				e.Chapter = ch
				result = append(result, e)
				ch++
			}
		}
	}
	return result
}

// WorldRule 世界观规则条目。
type WorldRule struct {
	Category string `json:"category"` // magic / technology / geography / society / other
	Rule     string `json:"rule"`     // 规则描述
	Boundary string `json:"boundary"` // 不可违反的边界
}
