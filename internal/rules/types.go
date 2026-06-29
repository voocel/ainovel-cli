// Package rules 实现用户偏好的持久化Nhập层（Policy）。
//
// Rule 是第四类事实，跟 Progress / Checkpoint / Artifact 并列，但性质相反：
// 前三类是系统输出，Rule 是用户意图的持久化Nhập。
//
// 设计约束（不可妥协）：
//   - 工具只返事实，不返指令（Violation 是事实，由 editor 决定Có czy không触发重写）
//   - 不引入Mới的 verdict Đường dẫn（复用 PendingRewrites）
//   - 不引入严格度字段（severity 由规则类型固定映射，editor 自主语义裁定）
//   - 不静默吞冲突（所有异常进 Bundle.Conflicts，让 LLM 与 /diag 可见）
//   - 不动 Flow Router（rule 不参与路由）
package rules

// SourceKind 标记规则来源，用于合并时的就近优先排序。
// 值越大越就近：Project > Global > Default。
//
// Phase 1.1 起只支持三层。Genre / Learned 层在实际题材库 / save_rule 落地前不开洞——
// 真要扩展时再加常量并补 loader 即可，不留Rỗng架子。
type SourceKind int

const (
	// SourceDefault — 项目内置Mặc định规则（assets/rules/default.md），优先级最低。
	SourceDefault SourceKind = iota
	// SourceGlobal — 用户全局偏好（~/.ainovel/rules/ Thư mục下所有 .md，按Tập tin名字典序合并），跨书复用。
	SourceGlobal
	// SourceProject — 本书规则（./.ainovel/rules/ Thư mục下所有 .md，按Tập tin名字典序合并），优先级最高。
	SourceProject
)

// String Quay lại来源的可读名称，用于 markdown 拼接时的来源Tiêu đề与 conflicts.detail。
func (k SourceKind) String() string {
	switch k {
	case SourceDefault:
		return "default"
	case SourceGlobal:
		return "global"
	case SourceProject:
		return "project"
	default:
		return "unknown"
	}
}

// WordRange 表示Chương字数的允许范围；nil 表示未声明。
type WordRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// Structured 装载 front matter 的结构化字段。
//
// 单Tập tin解析时，Parsed.Structured 只填该Tập tin声明的字段，其余保持零值。
// 合并后 Bundle.Structured 是各来源就近优先后的整体Kết quả。
type Structured struct {
	Genre            string         `json:"genre,omitempty"`
	ChapterWords     *WordRange     `json:"chapter_words,omitempty"`
	ForbiddenChars   []string       `json:"forbidden_chars,omitempty"`
	ForbiddenPhrases []string       `json:"forbidden_phrases,omitempty"`
	FatigueWords     map[string]int `json:"fatigue_words,omitempty"`
}

// IsEmpty 用于判定Có czy không完全没有结构化规则；checker 可据此Bỏ qua。
func (s Structured) IsEmpty() bool {
	return s.Genre == "" &&
		s.ChapterWords == nil &&
		len(s.ForbiddenChars) == 0 &&
		len(s.ForbiddenPhrases) == 0 &&
		len(s.FatigueWords) == 0
}

// ConflictKind 标记冲突或异常类型，便于 LLM 与诊断面板分类处理。
type ConflictKind string

const (
	// ConflictParseError — front matter 整体解析Thất bại；Chính văn仍作为偏好注入。
	ConflictParseError ConflictKind = "parse_error"
	// ConflictUnknownField — 用户写了 Phase 1 未支持的字段（forward-compatible）。
	ConflictUnknownField ConflictKind = "unknown_field"
	// ConflictTypeError — 字段类型Lỗi（如 forbidden_chars 写成字符串）；该字段丢弃。
	ConflictTypeError ConflictKind = "type_error"
	// ConflictFieldConflict — 多来源同一结构化字段值不一致；就近优先生效。
	ConflictFieldConflict ConflictKind = "field_conflict"
	// ConflictInvalidValue — 字段值格式非法（如 chapter_words: "abc"）；该字段丢弃。
	ConflictInvalidValue ConflictKind = "invalid_value"
)

// Conflict 一条冲突或异常记录。
//
// 永远不会阻断加载——所有异常都在这里暴露给 LLM 与 /diag，不静默处理。
type Conflict struct {
	Source string       `json:"source"`          // Tập tinĐường dẫn（绝对或相对，按来源记录）
	Kind   ConflictKind `json:"kind"`            // 冲突类型
	Field  string       `json:"field,omitempty"` // 受影响字段名（如 forbidden_chars）；parse_error 时为Rỗng
	Detail string       `json:"detail"`          // 人类可读的详情（含来源列表 / Lỗi信息）
}

// Parsed 是单份 rules.md 解析后的Kết quả。
type Parsed struct {
	Source     string     // Tập tinĐường dẫn
	Kind       SourceKind // 来源类型，用于合并优先级
	Structured Structured // 该Tập tin声明的 front matter 字段
	Preference string     // 该Tập tin的 Markdown Chính văn（front matter 之外的Phần）
	Conflicts  []Conflict // 该Tập tin解析期间产生的 conflicts（Không rõ字段 / 类型Lỗi）
}

// Bundle 是合并后注入 working_memory.user_rules 的最终形态。
//
// 字段映射到 JSON 输出：
//
//	{
//	  "structured": {...},
//	  "preferences": "...合并 markdown...",
//	  "sources": ["..."],
//	  "conflicts": [...]
//	}
type Bundle struct {
	Structured  Structured `json:"structured"`
	Preferences string     `json:"preferences"`
	Sources     []string   `json:"sources"`
	Conflicts   []Conflict `json:"conflicts"`
}

// IsEmpty 表示 Bundle 完全Không có内容（结构化字段为Rỗng + 偏好Chính văn为Rỗng）。
// 注入 user_rules 时仍应保留Rỗng Bundle，避免 LLM 处理 nil。
func (b Bundle) IsEmpty() bool {
	return b.Structured.IsEmpty() && b.Preferences == ""
}

// Severity 标记 Violation 的严重等级。
// 固定映射（用户不可Cấu hình）：
//
//	forbidden_chars 出现             -> Error
//	forbidden_phrases 出现           -> Error
//	fatigue_words 超阈值             -> Warning
//	chapter_words 偏差 < 20%         -> Warning
//	chapter_words 偏差 >= 20%        -> Error
type Severity string

const (
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// ChapterWordsDeviationThreshold 定义 chapter_words 偏差升级为 error 的临界值（20%）。
const ChapterWordsDeviationThreshold = 0.20

// Violation 是 checker 的输出：本章违反了某条机械规则的事实陈述。
//
// 注意：commit_chapter 把 violations 透传到Quay lại JSON，不阻断 commit；
// editor 在审阅时把这些事实映射到现有七维（aesthetic/pacing/character/consistency），
// 由 LLM 自主决定Có czy không升级 verdict 触发 polish/rewrite。
type Violation struct {
	Rule      string   `json:"rule"`                // forbidden_chars / forbidden_phrases / fatigue_words / chapter_words
	Target    string   `json:"target,omitempty"`    // 具体违规对象（哪个词/字符）；chapter_words 留Rỗng
	Limit     any      `json:"limit,omitempty"`     // 阈值；fatigue_words=int / chapter_words="3000-6000" / forbidden_*=Rỗng
	Actual    any      `json:"actual"`              // 实际值；fatigue_words/forbidden_*=出现次数 / chapter_words=本章字数
	Deviation float64  `json:"deviation,omitempty"` // chapter_words 偏差率（0~1），Khác规则留Rỗng
	Severity  Severity `json:"severity"`            // error / warning
}
