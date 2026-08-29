package skills

import (
	"fmt"
	"strings"
)

// InjectRequest 描述一次 /skill-name 主动调用展开。
// 调用方（TUI / cocreate）从用户原始输入解析出 SkillName 和 UserMsg，
// 交给 Store 展开。
type InjectRequest struct {
	SkillName string // 不含前缀 /
	UserMsg   string // 用户在 skill name 之后输入的额外说明，可为空
}

// InjectResult 是 InjectSkill 的返回。
type InjectResult struct {
	Expanded bool   // true=命中 skill 并已展开；false=未命中
	Text     string // 展开后的完整消息（命中时）或原始回退（未命中时）
	Hint     string // 未命中时的错误描述（供 UI 显示）
}

// InjectSkill 把 /skill-name 用户消息展开为最终发给 LLM 的文本。
//
// 命中时格式：
//
//	【已主动加载本地 skill：<name>】
//
//	<skill 全文（含 frontmatter）>
//
//	---
//
//	用户补充要求：<UserMsg>
//	（UserMsg 为空时使用通用提示）
//
// 未命中（name 不存在）：返回 Expanded=false、Hint 描述错误。调用方决定如何处理
// ——TUI 一般是显示错误并保留输入框，cocreate 同理。
//
// store 为 nil（禁用）时直接返回未命中 + 禁用提示。
func (s *Store) InjectSkill(req InjectRequest) InjectResult {
	name := strings.TrimSpace(req.SkillName)
	if name == "" {
		return InjectResult{Expanded: false, Hint: "skill 名称为空"}
	}
	if s == nil || s.root == "" {
		return InjectResult{
			Expanded: false,
			Hint:     "本地 skill 库未启用（路径解析失败或禁用）。",
		}
	}
	content, err := s.Read(name)
	if err != nil {
		// 给出近似建议：取前 5 个 name 供用户参考
		hint := fmt.Sprintf("skill %q 不存在。", name)
		all := s.List("")
		if len(all) > 0 {
			limit := 5
			if len(all) < limit {
				limit = len(all)
			}
			suggestions := make([]string, 0, limit)
			for _, m := range all[:limit] {
				suggestions = append(suggestions, m.Name)
			}
			hint += " 可用 skill（前 " + fmt.Sprintf("%d", limit) + " 个）：" + strings.Join(suggestions, ", ") + "。完整列表用 'ainovel skill list'。"
		} else {
			hint += " 当前 skill 库为空。"
		}
		return InjectResult{Expanded: false, Hint: hint}
	}

	var sb strings.Builder
	sb.WriteString("【已主动加载本地 skill：" + name + "】\n\n")
	sb.WriteString(content)
	sb.WriteString("\n\n---\n\n")
	userMsg := strings.TrimSpace(req.UserMsg)
	if userMsg != "" {
		sb.WriteString("用户补充要求：" + userMsg)
	} else {
		sb.WriteString("（用户未补充具体要求。请基于上述 skill 内容给出建议、确认理解、或直接开始创作。）")
	}
	return InjectResult{Expanded: true, Text: sb.String()}
}

// ParseSkillRef 从用户原始输入解析出 InjectRequest。
// 输入形如 "/cyberpunk-noir-checklist 帮我写开篇"。
// 返回 ok=false 表示不是 skill 引用（不以 / 开头，或 / 后内容为空）。
func ParseSkillRef(raw string) (InjectRequest, bool) {
	text := strings.TrimSpace(raw)
	if !strings.HasPrefix(text, "/") {
		return InjectRequest{}, false
	}
	rest := strings.TrimPrefix(text, "/")
	if strings.TrimSpace(rest) == "" {
		return InjectRequest{}, false
	}
	parts := strings.SplitN(rest, " ", 2)
	req := InjectRequest{SkillName: parts[0]}
	if len(parts) > 1 {
		req.UserMsg = strings.TrimSpace(parts[1])
	}
	return req, true
}
