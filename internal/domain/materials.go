package domain

import "time"

// 素材分类约定。LLM 写入时按下列值归类，便于 novel_context 按 category 过滤；
// 不强制枚举，自定义分类也能存（add_materials 不校验 category 取值）。
const (
	MaterialCategoryNaming      = "naming"      // 命名表：人名/地名/组织名/技能名
	MaterialCategoryTerminology = "terminology" // 术语表：流派/技术/官职/职业
	MaterialCategoryVisual      = "visual"      // 视觉锚点：场景/服装/道具/色彩
	MaterialCategorySetting     = "setting"     // 设定资料：力量体系/历史/地理/经济
	MaterialCategoryReference   = "reference"   // 参考资料：流派套路/同类作品/真实事件
)

// MaterialItem 单条素材。
//
// 字段语义：
//   - ID：稳定标识，store.Add 时自动分配（mat-001/002/...）
//   - Category：见 MaterialCategory* 常量；空值按 "reference" 处理
//   - Title：一句话标题（"赛博朋克巨型企业命名候选"），便于检索与展示
//   - Content：素材正文，Markdown；novel_context 注入时原样返回
//   - Source：来源标记（"web_search:query=xxx" / "skill:<name>" / "builtin"），
//     便于追溯与未来去重
//   - AddedAt：首次写入时间，方便排序与回看
type MaterialItem struct {
	ID       string    `json:"id"`
	Category string    `json:"category"`
	Title    string    `json:"title"`
	Content  string    `json:"content"`
	Source   string    `json:"source,omitempty"`
	AddedAt  time.Time `json:"added_at"`
}

// MaterialLibrary 整本小说的素材集合。一本书一份，存于 meta/materials.json。
type MaterialLibrary struct {
	Items []MaterialItem `json:"items"`
}
