package domain

// Novel 小说元信息。
type Novel struct {
	Name          string `json:"name"`
	TotalChapters int    `json:"total_chapters"`
}

// OutlineEntry Đại cương条目，对应一章。
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
	Tier        string   `json:"tier,omitempty"` // core / important / secondary / decorative（Mặc định important）
}

// VolumeOutline 卷级Đại cương（长篇分层模式）。
type VolumeOutline struct {
	Index int          `json:"index"`
	Title string       `json:"title"`
	Theme string       `json:"theme"` // 本卷核心冲突/主题
	Arcs  []ArcOutline `json:"arcs"`
}

// IsExpanded 判断卷Có czy không已Mở rộng（有弧级结构）。
func (v *VolumeOutline) IsExpanded() bool { return len(v.Arcs) > 0 }

// StoryCompass 终局方向指南针，替代固定的骨架卷列表。
// Architect 在每次卷边界时可更Mới，允许故事方向随创作演化。
type StoryCompass struct {
	EndingDirection string   `json:"ending_direction"`          // 终局方向（主题性描述）
	OpenThreads     []string `json:"open_threads,omitempty"`    // 活跃长线（需收束才能结局）
	EstimatedScale  string   `json:"estimated_scale,omitempty"` // 模糊规模（如"预计 4-6 卷"）
	LastUpdated     int      `json:"last_updated,omitempty"`    // 更Mới时的Đã hoàn thànhChương数
}

// ArcOutline 弧级Đại cương。
type ArcOutline struct {
	Index             int            `json:"index"` // 卷内弧序号
	Title             string         `json:"title"`
	Goal              string         `json:"goal"`                         // 弧目标（起承转合）
	EstimatedChapters int            `json:"estimated_chapters,omitempty"` // 骨架弧的预估章数（Mở rộng后清零）
	Chapters          []OutlineEntry `json:"chapters"`
}

// IsExpanded 判断弧Có czy không已Mở rộng（有详细Chương）。
func (a *ArcOutline) IsExpanded() bool { return len(a.Chapters) > 0 }

// TotalChapters 计算分层Đại cương的Hiện tại规划总章数。
// 已Mở rộng弧按真实Chương数计，骨架弧按 EstimatedChapters 计。
// Progress.TotalChapters 用它判断长篇Ngữ cảnh策略；真正可写Chương仍来自 FlattenOutline。
func TotalChapters(volumes []VolumeOutline) int {
	n := 0
	for _, v := range volumes {
		for _, a := range v.Arcs {
			if a.IsExpanded() {
				n += len(a.Chapters)
			} else {
				n += a.EstimatedChapters
			}
		}
	}
	return n
}

// FlattenOutline 将分层Đại cươngMở rộng为扁平Chương列表，保持全局Chương号连续。
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

// WorldRule Thế giới quan规则条目。
type WorldRule struct {
	Category string `json:"category"` // magic / technology / geography / society / other
	Rule     string `json:"rule"`     // 规则描述
	Boundary string `json:"boundary"` // 不可违反的边界
}
