package skills

import (
	"fmt"
	"strings"
)

// parse 把 skill 文件内容解析成 Skill。
//
// 刻意手写而不引入 YAML 依赖：项目已在 rules 层砍掉 YAML（见 internal/rules/raw.go），
// 这里只需要 `key: value` 单行键值，没有嵌套、列表或多行标量的需求。多引一个解析器
// 会把"可以随手写"的资产重新变成"格式先得对"的资产。
//
// 格式：首行必须是 `---`，第二个 `---` 之前是键值区，之后全部是正文。
// 未知键忽略（向前兼容，便于未来加字段而不破旧文件）。
func parse(content, source string) (Skill, error) {
	// 统一行尾：Windows 上编辑器写出的 CRLF 不该让 `---` 匹配失败。
	content = strings.ReplaceAll(content, "\r\n", "\n")
	// 去掉 UTF-8 BOM：记事本另存为 UTF-8 会带上它，否则首行 `---` 判定必挂。
	content = strings.TrimPrefix(content, "\ufeff")

	rest, ok := cutFence(content)
	if !ok {
		return Skill{}, fmt.Errorf("缺少 frontmatter：首行必须是 ---")
	}
	lines := strings.Split(rest, "\n")
	end := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return Skill{}, fmt.Errorf("frontmatter 未闭合：缺少结束的 ---")
	}
	head := strings.Join(lines[:end], "\n")
	body := strings.Join(lines[end+1:], "\n")

	sk := Skill{Source: source, Body: strings.TrimSpace(body)}
	for i, line := range strings.Split(head, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return Skill{}, fmt.Errorf("frontmatter 第 %d 行不是 key: value 形式: %q", i+1, line)
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "name":
			sk.Name = value
		case "description":
			sk.Description = value
		case "agent":
			sk.Agent = value
		case "scope":
			sk.Scope = Scope(value)
		}
	}
	if err := sk.validate(); err != nil {
		return Skill{}, err
	}
	return sk, nil
}

// cutFence 剥掉开头的 `---` 行，返回其后的内容。
func cutFence(content string) (string, bool) {
	first, rest, ok := strings.Cut(content, "\n")
	if !ok || strings.TrimSpace(first) != "---" {
		return "", false
	}
	return rest, true
}
