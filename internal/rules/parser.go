package rules

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// 已知 front matter 字段集合，用于识别Không rõ字段并写入 conflicts。
var knownFrontMatterFields = map[string]struct{}{
	"genre":             {},
	"chapter_words":     {},
	"forbidden_chars":   {},
	"forbidden_phrases": {},
	"fatigue_words":     {},
}

// Parse 解析单份 rules.md 内容（front matter + Markdown）。
//
// 容错策略：
//   - front matter 整体解析Thất bại：不阻断，Chính văn仍作为偏好，conflicts 记录 parse_error
//   - Không rõ字段：丢弃，conflicts 记录 unknown_field
//   - 字段类型Lỗi：丢弃该字段，conflicts 记录 type_error
//   - 字段值非法（如 chapter_words Không thể解析为范围）：丢弃，conflicts 记录 invalid_value
//
// source 是Tập tinĐường dẫn，仅用于 conflicts.source；kind 决定优先级。
func Parse(source string, kind SourceKind, content []byte) Parsed {
	parsed := Parsed{Source: source, Kind: kind}

	fmText, bodyText := splitFrontMatter(content)
	parsed.Preference = strings.TrimSpace(bodyText)

	if strings.TrimSpace(fmText) == "" {
		return parsed
	}

	// 先 unmarshal 到 map[string]any，再逐字段强类型解析。
	// 这样能区分"字段不存在"和"字段类型Lỗi"，并能识别Không rõ字段。
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(fmText), &raw); err != nil {
		parsed.Conflicts = append(parsed.Conflicts, Conflict{
			Source: source,
			Kind:   ConflictParseError,
			Detail: fmt.Sprintf("front matter YAML 解析Thất bại: %v", err),
		})
		return parsed
	}

	for key, val := range raw {
		if _, ok := knownFrontMatterFields[key]; !ok {
			parsed.Conflicts = append(parsed.Conflicts, Conflict{
				Source: source,
				Kind:   ConflictUnknownField,
				Field:  key,
				Detail: fmt.Sprintf("Không rõ字段 %q，Phase 1 Không hỗ trợ；已忽略", key),
			})
			continue
		}
		applyField(&parsed, key, val)
	}

	return parsed
}

// splitFrontMatter 切分 `---` 包裹的 front matter 与Còn lạiChính văn。
//
// 约定：
//   - Tập tin以 `---` 起始（允许 BOM / Rỗng行）才认为有 front matter
//   - 第二个 `---` 之后是Chính văn
//   - 没有 front matter：全文作为Chính văn
//   - 只有起始 `---` 没有终止 `---`：视为Không có front matter（避免吞掉整篇Chính văn）
func splitFrontMatter(content []byte) (fm, body string) {
	text := string(bytes.TrimPrefix(content, []byte{0xEF, 0xBB, 0xBF})) // 去 UTF-8 BOM
	lines := strings.Split(text, "\n")

	// 找第一个非Rỗng行；不是 `---` 则全是Chính văn
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.TrimSpace(line) == "---" {
			start = i
		}
		break
	}
	if start < 0 {
		return "", text
	}

	// 找第二个 `---`
	end := -1
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		// 起始有 `---` 但没闭合：保守视为Không có front matter
		return "", text
	}

	fm = strings.Join(lines[start+1:end], "\n")
	body = strings.Join(lines[end+1:], "\n")
	return fm, body
}

// applyField 把单条 raw 字段塞进 Parsed.Structured，类型不匹配时写 conflicts。
func applyField(p *Parsed, key string, val any) {
	switch key {
	case "genre":
		s, ok := asString(val)
		if !ok {
			p.Conflicts = append(p.Conflicts, typeErr(p.Source, key, "string", val))
			return
		}
		p.Structured.Genre = strings.TrimSpace(s)

	case "chapter_words":
		rng, ok := parseChapterWords(val)
		if !ok {
			p.Conflicts = append(p.Conflicts, Conflict{
				Source: p.Source,
				Kind:   ConflictInvalidValue,
				Field:  key,
				Detail: fmt.Sprintf("chapter_words 期望区间 \"min-max\"（如 3000-6000）或单个目标值（如 2500），收到 %v", val),
			})
			return
		}
		p.Structured.ChapterWords = rng

	case "forbidden_chars":
		list, ok := asStringList(p, key, val)
		if !ok {
			p.Conflicts = append(p.Conflicts, typeErr(p.Source, key, "[]string", val))
			return
		}
		p.Structured.ForbiddenChars = list

	case "forbidden_phrases":
		list, ok := asStringList(p, key, val)
		if !ok {
			p.Conflicts = append(p.Conflicts, typeErr(p.Source, key, "[]string", val))
			return
		}
		p.Structured.ForbiddenPhrases = list

	case "fatigue_words":
		m, ok := parseFatigueWords(p, val)
		if !ok {
			p.Conflicts = append(p.Conflicts, typeErr(p.Source, key, "map[string]int 或 []string", val))
			return
		}
		p.Structured.FatigueWords = m
	}
}

// parseChapterWords 解析Chương字数范围为 *WordRange，接受三种写法：
//   - "min-max" 区间字符串（如 "3000-6000"）
//   - {min, max} 映射
//   - 单个正整数 N（裸数字 2500 或字符串 "2500"）——按"目标 N 字/章"理解，自动
//     Mở rộng为 N±20% 区间。否则用户凭直觉写单值会被静默丢弃、回落内置Mặc định（issue #41）。
func parseChapterWords(val any) (*WordRange, bool) {
	switch v := val.(type) {
	case string:
		s := strings.TrimSpace(v)
		if !strings.Contains(s, "-") { // 单值写法，如 "2500"
			if n, err := strconv.Atoi(s); err == nil && n > 0 {
				return wordBandAround(n), true
			}
			return nil, false
		}
		parts := strings.Split(s, "-")
		if len(parts) != 2 {
			return nil, false
		}
		minV, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
		maxV, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err1 != nil || err2 != nil || minV < 0 || maxV < 0 || minV > maxV {
			return nil, false
		}
		return &WordRange{Min: minV, Max: maxV}, true
	case map[string]any:
		minV, ok1 := asInt(v["min"])
		maxV, ok2 := asInt(v["max"])
		if !ok1 || !ok2 || minV < 0 || maxV < 0 || minV > maxV {
			return nil, false
		}
		return &WordRange{Min: minV, Max: maxV}, true
	default: // 裸数字，YAML 解析为 int / float64
		if n, ok := asInt(v); ok && n > 0 {
			return wordBandAround(n), true
		}
		return nil, false
	}
}

// wordBandAround 把"目标 N 字/章"Mở rộng为 ±20% 的舒适区间（如 2500 → 2000-3000），
// 让单值写法等价于一个合理区间，而不是 N-N 的硬墙（紧区间会逼出压缩死循环）。
func wordBandAround(n int) *WordRange {
	return &WordRange{Min: n * 4 / 5, Max: n * 6 / 5}
}

// parseFatigueWords 同时接受 map[string]int（带阈值）与 []string（Mặc định阈值 1）。
//
// 单 key 类型错或阈值非法都会写 conflict 进 p.Conflicts，绝不静默吞。
// Quay lại (map, true) 表示存在合法项；(nil, false) 表示整体类型错或Tất cả项非法。
func parseFatigueWords(p *Parsed, val any) (map[string]int, bool) {
	switch v := val.(type) {
	case map[string]any:
		out := make(map[string]int, len(v))
		for k, raw := range v {
			trimmed := strings.TrimSpace(k)
			if trimmed == "" {
				p.Conflicts = append(p.Conflicts, Conflict{
					Source: p.Source,
					Kind:   ConflictInvalidValue,
					Field:  "fatigue_words",
					Detail: "fatigue_words 出现Rỗng白 key，Đã bỏ qua",
				})
				continue
			}
			n, ok := asInt(raw)
			if !ok {
				p.Conflicts = append(p.Conflicts, Conflict{
					Source: p.Source,
					Kind:   ConflictTypeError,
					Field:  "fatigue_words." + trimmed,
					Detail: fmt.Sprintf("fatigue_words[%q] 期望 int 阈值，收到 %T（%v）；已丢弃该 key", trimmed, raw, raw),
				})
				continue
			}
			if n <= 0 {
				p.Conflicts = append(p.Conflicts, Conflict{
					Source: p.Source,
					Kind:   ConflictInvalidValue,
					Field:  "fatigue_words." + trimmed,
					Detail: fmt.Sprintf("fatigue_words[%q] 阈值必须 > 0，收到 %d；已丢弃该 key", trimmed, n),
				})
				continue
			}
			out[trimmed] = n
		}
		if len(out) == 0 {
			return nil, false
		}
		return out, true
	case []any:
		out := make(map[string]int, len(v))
		for i, raw := range v {
			s, ok := raw.(string)
			if !ok {
				p.Conflicts = append(p.Conflicts, Conflict{
					Source: p.Source,
					Kind:   ConflictTypeError,
					Field:  fmt.Sprintf("fatigue_words[%d]", i),
					Detail: fmt.Sprintf("fatigue_words 列表元素期望 string，收到 %T（%v）；已丢弃该元素", raw, raw),
				})
				continue
			}
			s = strings.TrimSpace(s)
			if s == "" {
				p.Conflicts = append(p.Conflicts, Conflict{
					Source: p.Source,
					Kind:   ConflictInvalidValue,
					Field:  fmt.Sprintf("fatigue_words[%d]", i),
					Detail: "fatigue_words 列表元素为Rỗng白；已丢弃",
				})
				continue
			}
			out[s] = 1
		}
		if len(out) == 0 {
			return nil, false
		}
		return out, true
	default:
		return nil, false
	}
}

// asString / asInt / asStringList 是 yaml.v3 反序列化后类型归一化的小工具。
//
// 严格策略（Debug-First）：只接受目标类型，不自动转换Khác类型。
// 类型Lỗi由调用方写入 conflicts，不在工具内静默修复。

// asString 仅接受 string 标量。
// 注意：YAML 里 `genre: 42`（Không có引号）会被反序列化成 int，按本函数判定为类型Lỗi。
// 用户应写 `genre: "42"` 显式声明 string。
func asString(v any) (string, bool) {
	if s, ok := v.(string); ok {
		return s, true
	}
	return "", false
}

// asInt 接受所有整型；float64 仅在恰好是整数时接受（YAML 数字Mặc định解析为 float64）。
// 字符串数字不再自动转——避免与"字段错放成字符串"的失误混淆。
func asInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		// 仅当 float 恰好是整数时接受（如 yaml 解析的 `5` → float64(5.0)）
		if x == float64(int(x)) {
			return int(x), true
		}
		return 0, false
	default:
		return 0, false
	}
}

// asStringList 元素必须是 string；Khác类型该元素Bỏ qua并写入 conflicts。
// Quay lại (list, true) 表示存在合法元素；(nil, false) 表示整体类型错或Tất cả元素非法。
func asStringList(p *Parsed, field string, v any) ([]string, bool) {
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for i, raw := range arr {
		s, ok := raw.(string)
		if !ok {
			p.Conflicts = append(p.Conflicts, Conflict{
				Source: p.Source,
				Kind:   ConflictTypeError,
				Field:  fmt.Sprintf("%s[%d]", field, i),
				Detail: fmt.Sprintf("%s 列表元素期望 string，收到 %T（%v）；已丢弃该元素", field, raw, raw),
			})
			continue
		}
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

func typeErr(source, field, expected string, got any) Conflict {
	return Conflict{
		Source: source,
		Kind:   ConflictTypeError,
		Field:  field,
		Detail: fmt.Sprintf("字段 %s 类型Lỗi，期望 %s，收到 %T（%v）；已丢弃", field, expected, got, got),
	}
}
