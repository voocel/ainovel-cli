package rules

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Merge 把 loader Quay lại的多个来源合并成最终 Bundle。
//
// 合并规则：
//   - 普通结构化字段：就近优先（后者覆盖前者），多来源声明同一字段且值不一致写 field_conflict
//   - fatigue_words：按词合并；同一词多来源声明且阈值不一致时，就近优先并写 field_conflict
//   - Markdown Chính văn：按来源顺序拼接，每段加来源Tiêu đề，不覆盖
//   - sources：所有Thành công加载的Tập tinĐường dẫn
//   - conflicts：解析期 conflicts + 合并期 field_conflict
//
// 入参 layers 应已按 SourceKind 升序排好（loader.Load 的输出形态）。
func Merge(layers []Parsed) Bundle {
	bundle := Bundle{
		Structured:  Structured{},
		Preferences: "",
		Sources:     make([]string, 0, len(layers)),
		Conflicts:   nil,
	}

	// 阶段 A：收集每个字段的所有声明来源，便于后续冲突判定
	declarations := map[string][]Parsed{}
	declare := func(field string, p Parsed) {
		declarations[field] = append(declarations[field], p)
	}
	for _, p := range layers {
		if p.Structured.Genre != "" {
			declare("genre", p)
		}
		if p.Structured.ChapterWords != nil {
			declare("chapter_words", p)
		}
		if len(p.Structured.ForbiddenChars) > 0 {
			declare("forbidden_chars", p)
		}
		if len(p.Structured.ForbiddenPhrases) > 0 {
			declare("forbidden_phrases", p)
		}
		if len(p.Structured.FatigueWords) > 0 {
			declare("fatigue_words", p)
		}
	}

	// 阶段 B：合并结构化字段，得到最终结构化字段。
	// 标量/list 字段保持就近覆盖；fatigue_words 是 map，按词叠加，便于用户只Mới增少量疲劳词。
	for _, p := range layers {
		if p.Structured.Genre != "" {
			bundle.Structured.Genre = p.Structured.Genre
		}
		if p.Structured.ChapterWords != nil {
			bundle.Structured.ChapterWords = p.Structured.ChapterWords
		}
		if len(p.Structured.ForbiddenChars) > 0 {
			bundle.Structured.ForbiddenChars = p.Structured.ForbiddenChars
		}
		if len(p.Structured.ForbiddenPhrases) > 0 {
			bundle.Structured.ForbiddenPhrases = p.Structured.ForbiddenPhrases
		}
		if len(p.Structured.FatigueWords) > 0 {
			bundle.Structured.FatigueWords = mergeFatigueWords(bundle.Structured.FatigueWords, p.Structured.FatigueWords)
		}
	}

	// 阶段 C：构造 field_conflict（多来源 + 值不一致才算冲突）
	for field, sources := range declarations {
		if len(sources) < 2 {
			continue
		}
		if field == "fatigue_words" {
			bundle.Conflicts = append(bundle.Conflicts, fatigueWordConflicts(sources)...)
			continue
		}
		if allEqual(field, sources) {
			continue
		}
		bundle.Conflicts = append(bundle.Conflicts, Conflict{
			Source: sources[len(sources)-1].Source,
			Kind:   ConflictFieldConflict,
			Field:  field,
			Detail: describeFieldConflict(field, sources),
		})
	}

	// 阶段 D：合并 Markdown 偏好Chính văn
	var sb strings.Builder
	for _, p := range layers {
		if strings.TrimSpace(p.Preference) == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		fmt.Fprintf(&sb, "## [%s] %s\n\n", p.Kind, p.Source)
		sb.WriteString(p.Preference)
	}
	bundle.Preferences = sb.String()

	// 阶段 E：汇总 sources 与解析期 conflicts
	for _, p := range layers {
		bundle.Sources = append(bundle.Sources, p.Source)
		bundle.Conflicts = append(bundle.Conflicts, p.Conflicts...)
	}

	return bundle
}

func mergeFatigueWords(dst, src map[string]int) map[string]int {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string]int, len(src))
	}
	for word, limit := range src {
		dst[word] = limit
	}
	return dst
}

func fatigueWordConflicts(sources []Parsed) []Conflict {
	type declaration struct {
		source string
		limit  int
	}
	byWord := make(map[string][]declaration)
	for _, p := range sources {
		for word, limit := range p.Structured.FatigueWords {
			if word == "" {
				continue
			}
			byWord[word] = append(byWord[word], declaration{source: p.Source, limit: limit})
		}
	}

	words := make([]string, 0, len(byWord))
	for word := range byWord {
		words = append(words, word)
	}
	sort.Strings(words)

	var conflicts []Conflict
	for _, word := range words {
		ds := byWord[word]
		if len(ds) < 2 {
			continue
		}
		first := ds[0].limit
		allSame := true
		for _, d := range ds[1:] {
			if d.limit != first {
				allSame = false
				break
			}
		}
		if allSame {
			continue
		}
		parts := make([]string, 0, len(ds))
		for _, d := range ds {
			parts = append(parts, fmt.Sprintf("%s=%d", d.source, d.limit))
		}
		winner := ds[len(ds)-1]
		conflicts = append(conflicts, Conflict{
			Source: winner.source,
			Kind:   ConflictFieldConflict,
			Field:  "fatigue_words." + word,
			Detail: fmt.Sprintf("字段 fatigue_words[%q] 在多个来源声明且阈值不一致：%s；就近优先生效：%s",
				word, strings.Join(parts, " | "), winner.source),
		})
	}
	return conflicts
}

// allEqual 判定同一字段在多个来源中的值Có czy không完全一致；一致则不报冲突。
//
// list 字段语义上不关心顺序，但实现上 yaml 反序列化已保留声明顺序，
// 完全相同的两份Cấu hình reflect.DeepEqual 即Quay lại true，已满足"值一致"的判定。
// 顺序不同但元素相同的特殊情况按"不一致"处理是可接受的（仍然 just info，不阻断）。
func allEqual(field string, sources []Parsed) bool {
	if len(sources) < 2 {
		return true
	}
	first := extractField(field, sources[0].Structured)
	for _, p := range sources[1:] {
		if !reflect.DeepEqual(first, extractField(field, p.Structured)) {
			return false
		}
	}
	return true
}

func extractField(field string, s Structured) any {
	switch field {
	case "genre":
		return s.Genre
	case "chapter_words":
		if s.ChapterWords == nil {
			return nil
		}
		return *s.ChapterWords
	case "forbidden_chars":
		return s.ForbiddenChars
	case "forbidden_phrases":
		return s.ForbiddenPhrases
	case "fatigue_words":
		return s.FatigueWords
	default:
		return nil
	}
}

// describeFieldConflict 用人类可读的方式描述冲突：列出所有来源 + 每来源的值。
// 末尾标注最终生效的来源（就近优先）。
func describeFieldConflict(field string, sources []Parsed) string {
	var parts []string
	for _, p := range sources {
		parts = append(parts, fmt.Sprintf("%s=%s", p.Source, formatFieldValue(field, p.Structured)))
	}
	winner := sources[len(sources)-1]
	return fmt.Sprintf(
		"字段 %s 在多个来源声明且值不一致：%s；就近优先生效：%s",
		field, strings.Join(parts, " | "), winner.Source,
	)
}

func formatFieldValue(field string, s Structured) string {
	switch field {
	case "genre":
		return s.Genre
	case "chapter_words":
		if s.ChapterWords == nil {
			return "<nil>"
		}
		return fmt.Sprintf("%d-%d", s.ChapterWords.Min, s.ChapterWords.Max)
	case "forbidden_chars":
		return fmt.Sprintf("%v", s.ForbiddenChars)
	case "forbidden_phrases":
		return fmt.Sprintf("%v", s.ForbiddenPhrases)
	case "fatigue_words":
		return fmt.Sprintf("%v", s.FatigueWords)
	default:
		return "<unknown>"
	}
}
