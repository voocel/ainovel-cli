// Package skills 实现跨书 skill 库的存储与检索。
//
// skill 是 Markdown + frontmatter 文件，存放在 ~/.ainovel/skills/<category>/<name>.md。
// frontmatter 用一个极小的 YAML 子集解析（不引入外部依赖），仅支持：
//
//   - key: value              # scalar（字符串 / 整数）
//   - key: "double quoted"    # 带引号 scalar
//   - key: [a, b, c]          # inline 数组
//   - key:                    # 块数组（缩进 - 开头）
//       - a
//       - b
//   - key: |                  # 块 scalar（缩进多行）
//       多行
//       文本
//
// 仅 frontmatter、name 与 description 必填；其它字段可省略。
package skills

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// SkillMeta 是 skill 文件的元数据，作为索引和工具返回的基本单位。
// 字段 JSON tag 用 snake_case，方便工具返回直接 marshalling。
type SkillMeta struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	Tags        []string `json:"tags,omitempty"`
	Triggers    []string `json:"triggers,omitempty"`
	When        string   `json:"when,omitempty"`
	Do          string   `json:"do,omitempty"`
	Priority    int      `json:"priority"`
	Path        string   `json:"path,omitempty"`
}

// frontmatter 分隔符。
const fmDelim = "---"

// ParseSkill 解析 skill 文件全文，返回元数据、正文和错误。
//
// 行为：
//   - 空文件 → 报错
//   - 无 frontmatter（不以 --- 开头）→ 返回零元数据 + 整文为 body（不报错，
//     由调用方决定是否接受）
//   - frontmatter 缺闭合 --- → 报错
//   - frontmatter 缺 name 或 description → 报错
//   - priority 非整数 → 报错
//   - 字段值不符合 YAML 子集 → 报错并指明行号
//
// category 缺省时从 path 的父目录名推断（如 ~/.ainovel/skills/genres/x.md → "genres"）。
// priority 缺省时为 50（中等优先级）。
func ParseSkill(path, raw string) (SkillMeta, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return SkillMeta{}, "", fmt.Errorf("empty skill content")
	}

	meta := SkillMeta{Path: path, Priority: 50}

	// 无 frontmatter：零元数据 + 整文为 body
	if !strings.HasPrefix(raw, fmDelim+"\n") && raw != fmDelim {
		return meta, raw, nil
	}

	lines := strings.Split(raw, "\n")
	// 至少应有开头 --- + 内容 + 闭合 ---，少于 3 行肯定不完整
	if len(lines) < 3 {
		return meta, "", fmt.Errorf("invalid frontmatter: too short")
	}

	// 找闭合 ---
	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == fmDelim {
			closeIdx = i
			break
		}
	}
	if closeIdx == -1 {
		return meta, "", fmt.Errorf("invalid frontmatter: missing closing %s", fmDelim)
	}

	fmText := strings.Join(lines[1:closeIdx], "\n")
	body := strings.TrimSpace(strings.Join(lines[closeIdx+1:], "\n"))

	if err := parseFrontmatter(&meta, fmText); err != nil {
		return meta, body, fmt.Errorf("parse frontmatter: %w", err)
	}

	if strings.TrimSpace(meta.Name) == "" {
		return meta, body, fmt.Errorf("frontmatter: name is required")
	}
	if strings.TrimSpace(meta.Description) == "" {
		return meta, body, fmt.Errorf("frontmatter: description is required")
	}
	if meta.Category == "" {
		meta.Category = inferCategory(path)
	}
	return meta, body, nil
}

// parseFrontmatter 把 YAML 子集文本解析到 meta。详细语法见包注释。
func parseFrontmatter(meta *SkillMeta, text string) error {
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			i++
			continue
		}

		colonIdx := strings.Index(line, ":")
		if colonIdx == -1 {
			return fmt.Errorf("line %d: expected 'key: value', got %q", i+1, line)
		}
		key := strings.TrimSpace(line[:colonIdx])
		rest := strings.TrimSpace(line[colonIdx+1:])

		switch key {
		case "tags", "triggers":
			items, advance, err := parseListField(lines, i, rest, key)
			if err != nil {
				return err
			}
			if key == "tags" {
				meta.Tags = items
			} else {
				meta.Triggers = items
			}
			i += advance

		case "when", "do":
			body, advance, err := parseScalarField(lines, i, rest, key)
			if err != nil {
				return err
			}
			if key == "when" {
				meta.When = body
			} else {
				meta.Do = body
			}
			i += advance

		case "priority":
			n, err := strconv.Atoi(strings.TrimSpace(rest))
			if err != nil {
				return fmt.Errorf("line %d: priority must be integer, got %q", i+1, rest)
			}
			meta.Priority = n
			i++

		case "name", "description", "category":
			val := unquote(strings.TrimSpace(rest))
			switch key {
			case "name":
				meta.Name = val
			case "description":
				meta.Description = val
			case "category":
				meta.Category = val
			}
			i++

		default:
			// 未知字段忽略（向前兼容）
			i++
		}
	}
	return nil
}

// parseListField 解析 tags/triggers。支持两种形式：
//
//	key: [a, b, c]       inline
//	key:                 block（后续缩进 - 开头的行）
func parseListField(lines []string, i int, rest, key string) ([]string, int, error) {
	if rest != "" {
		items, err := parseInlineArray(rest)
		if err != nil {
			return nil, 0, fmt.Errorf("line %d: %s: %w", i+1, key, err)
		}
		return items, 1, nil
	}

	items, advance := parseBlockList(lines, i+1)
	return items, 1 + advance, nil
}

// parseScalarField 解析 when/do。支持：
//
//	key: "single line"   单行带引号
//	key: single line     单行裸
//	key: |               块 scalar（后续缩进行）
//	key: |-              块 scalar，去除末尾换行（YAML 语义，与 | 等价处理）
func parseScalarField(lines []string, i int, rest, key string) (string, int, error) {
	rest = strings.TrimSpace(rest)
	if rest == "|" || rest == "|-" || rest == "|+" {
		body, advance := parseBlockScalar(lines, i+1)
		return body, 1 + advance, nil
	}
	if rest == "" {
		// 空 value 视为块 scalar 容许（无内容）
		body, advance := parseBlockScalar(lines, i+1)
		return body, 1 + advance, nil
	}
	return unquote(rest), 1, nil
}

// parseBlockList 解析块数组。返回 items 和消耗的非空行数。
func parseBlockList(lines []string, start int) ([]string, int) {
	var items []string
	i := start
	for i < len(lines) {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			break
		}
		if countIndent(line) == 0 {
			break
		}
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- ") && trimmed != "-" {
			break
		}
		var item string
		if trimmed == "-" {
			item = ""
		} else {
			item = unquote(strings.TrimSpace(trimmed[2:]))
		}
		items = append(items, item)
		i++
	}
	return items, i - start
}

// parseBlockScalar 解析块 scalar（|）。返回内容和消耗的非空行数。
// 缩进保留：剥离所有行共同的最小缩进。
func parseBlockScalar(lines []string, start int) (string, int) {
	var sb strings.Builder
	baseIndent := -1
	i := start
	for i < len(lines) {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			sb.WriteString("\n")
			i++
			continue
		}
		indent := countIndent(line)
		if indent == 0 {
			break
		}
		if baseIndent == -1 {
			baseIndent = indent
		}
		if indent < baseIndent {
			break
		}
		sb.WriteString(line[baseIndent:])
		sb.WriteString("\n")
		i++
	}
	return strings.TrimRight(sb.String(), "\n"), i - start
}

// parseInlineArray 解析 [a, b, c] 形式内联数组。
func parseInlineArray(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil, fmt.Errorf("array must be [a, b, c] form: %q", s)
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return nil, nil
	}
	parts := strings.Split(inner, ",")
	items := make([]string, 0, len(parts))
	for _, p := range parts {
		v := unquote(strings.TrimSpace(p))
		if v == "" {
			continue
		}
		items = append(items, v)
	}
	return items, nil
}

func countIndent(s string) int {
	n := 0
	for n < len(s) && (s[n] == ' ' || s[n] == '\t') {
		n++
	}
	return n
}

// unquote 去掉包围引号（双或单）。无引号原样返回。
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return s
	}
	if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
		inner := s[1 : len(s)-1]
		if s[0] == '"' {
			// 双引号走 JSON unescape，支持 \"、\n 等
			var out string
			if err := json.Unmarshal([]byte(s), &out); err == nil {
				return out
			}
		}
		return inner
	}
	return s
}

// inferCategory 从文件路径推断分类。取父目录名；不合法时退回 "misc"。
func inferCategory(path string) string {
	parts := strings.Split(strings.ReplaceAll(path, "\\", "/"), "/")
	if len(parts) < 2 {
		return "misc"
	}
	parent := parts[len(parts)-2]
	if !isValidName(parent) {
		return "misc"
	}
	return parent
}

// isValidName 校验 name/category 必须为 [a-z0-9-]+。
func isValidName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
			return false
		}
	}
	return true
}
