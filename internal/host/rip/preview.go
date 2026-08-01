package rip

import (
	"context"
	"fmt"
	"strings"
)

// previewChapters 是黄金三章的章数上限。长篇的开篇三章决定读者留存，
// 在付出全书逐章摘要的成本之前先深读这三章，让用户看到「值不值得继续拆」。
const previewChapters = 3

// PreviewChapter 是黄金三章中单章的深读结果。
type PreviewChapter struct {
	Chapter  int      `json:"chapter"`
	Title    string   `json:"title"`
	Opening  string   `json:"opening"`  // 开篇如何入场、怎么钩人
	Hooks    []string `json:"hooks"`    // 本章埋的钩子
	Conflict string   `json:"conflict"` // 本章核心冲突
	Promise  string   `json:"promise"`  // 本章给读者的承诺（读下去能得到什么）
	Risks    []string `json:"risks"`    // 可能劝退读者的地方
}

// Preview 是黄金三章深读结果，长篇的停靠点凭据。
type Preview struct {
	Chapters   []PreviewChapter `json:"chapters"`
	Verdict    string           `json:"verdict"`    // 三章整体判断
	Strengths  []string         `json:"strengths"`  // 值得学的地方
	Weaknesses []string         `json:"weaknesses"` // 明显短板
}

// previewSchemaVersion 纳入预览工件 InputDigest。
const previewSchemaVersion = 1

// previewInputDigest 绑定边界工件原始字节 + 预览 prompt/schema 版本。
// 预览走逐章摘要档位的模型，但它的身份只取决于边界与自己的 prompt 版本，
// 与摘要 prompt 无关——改摘要提示词不该让已看过的预览重跑。
func previewInputDigest(boundRaw []byte) string {
	return Digest([]byte(strings.Join([]string{
		"preview", previewPromptVersion, fmt.Sprintf("v%d", previewSchemaVersion), Digest(boundRaw),
	}, "\x00")))
}

// validatePreview 机械校验：章号必须与边界表前 N 章逐一对应，关键字段不得为空。
// 标题以边界表为准（坐标事实），不让模型复述引入漂移。
func validatePreview(p *Preview, b *Boundaries, want int) error {
	if len(p.Chapters) != want {
		return fmt.Errorf("深读章节数 %d != 预期 %d", len(p.Chapters), want)
	}
	for i := range p.Chapters {
		expect := b.Chapters[i]
		if p.Chapters[i].Chapter != expect.Number {
			return fmt.Errorf("深读第 %d 项章号 %d != %d", i, p.Chapters[i].Chapter, expect.Number)
		}
		if strings.TrimSpace(p.Chapters[i].Opening) == "" {
			return fmt.Errorf("章 %d 缺开篇分析", expect.Number)
		}
		if strings.TrimSpace(p.Chapters[i].Conflict) == "" {
			return fmt.Errorf("章 %d 缺核心冲突", expect.Number)
		}
		if strings.TrimSpace(p.Chapters[i].Promise) == "" {
			return fmt.Errorf("章 %d 缺读者承诺", expect.Number)
		}
		p.Chapters[i].Title = expect.Title
	}
	if strings.TrimSpace(p.Verdict) == "" {
		return fmt.Errorf("缺黄金三章整体判断（verdict）")
	}
	if len(nonEmpty(p.Strengths)) == 0 {
		return fmt.Errorf("至少需要 1 条值得学的地方（strengths）")
	}
	return nil
}

// PreviewGolden 深读开篇最多 previewChapters 章。
func PreviewGolden(ctx context.Context, m callModel, systemPrompt string, normalized []byte, b *Boundaries, maxTokens int, prof callProfile) (*Preview, error) {
	want := min(previewChapters, len(b.Chapters))
	if want == 0 {
		return nil, fmt.Errorf("边界表无章节，无法深读开篇")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "请深读开篇第 1-%d 章，逐章分析入场方式、钩子、核心冲突与读者承诺，并给出三章整体判断。\n\n", want)
	for i := 0; i < want; i++ {
		c := b.Chapters[i]
		fmt.Fprintf(&sb, "## 第 %d 章：%s\n\n", c.Number, c.Title)
		sb.WriteString(b.Content(normalized, i))
		sb.WriteString("\n\n---\n\n")
	}
	out, err := callStructured[Preview](ctx, m, previewContract, systemPrompt, sb.String(), maxTokens, prof, func(p *Preview) error {
		return validatePreview(p, b, want)
	})
	if err != nil {
		return nil, err
	}
	for _, c := range out.Chapters {
		prof.step(0, 0, "第 %d 章〈%s〉：%s", c.Chapter, snippet(c.Title, 24), snippet(c.Conflict, 48))
	}
	return &out, nil
}

// renderPreviewMarkdown 渲染 快速预览.md：用户在停靠点看的就是这份投影。
func renderPreviewMarkdown(m *Manifest, b *Boundaries, p *Preview) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# 快速预览：%s\n\n", m.BookName)
	fmt.Fprintf(&sb, "- 原文：%s（编码 %s，%d 字）\n", m.SourceName, m.Encoding, m.Runes)
	fmt.Fprintf(&sb, "- 章节：%d 章\n", len(b.Chapters))
	if len(b.Uncertain) > 0 {
		fmt.Fprintf(&sb, "- 存疑章节：%d 章（%s）\n", len(b.Uncertain), joinInts(b.Uncertain, 12))
	}
	sb.WriteString("\n## 黄金三章\n\n")
	for _, c := range p.Chapters {
		fmt.Fprintf(&sb, "### 第 %d 章 %s\n\n", c.Chapter, c.Title)
		fmt.Fprintf(&sb, "- 入场：%s\n", c.Opening)
		fmt.Fprintf(&sb, "- 核心冲突：%s\n", c.Conflict)
		fmt.Fprintf(&sb, "- 读者承诺：%s\n", c.Promise)
		writeBullets(&sb, "钩子", c.Hooks)
		writeBullets(&sb, "劝退风险", c.Risks)
		sb.WriteString("\n")
	}
	sb.WriteString("## 整体判断\n\n")
	sb.WriteString(p.Verdict)
	sb.WriteString("\n\n")
	writeBullets(&sb, "值得学", p.Strengths)
	writeBullets(&sb, "明显短板", p.Weaknesses)
	if len(b.Notes) > 0 {
		sb.WriteString("\n## 待人工核对\n\n")
		for _, n := range b.Notes {
			fmt.Fprintf(&sb, "- %s\n", n)
		}
	}
	return sb.String()
}

// writeBullets 写一个「标签 + 条目」块；条目全空时整块省略。
func writeBullets(sb *strings.Builder, label string, items []string) {
	items = nonEmpty(items)
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(sb, "- %s：\n", label)
	for _, it := range items {
		fmt.Fprintf(sb, "  - %s\n", it)
	}
}

// joinInts 把章号列表压成一行，超过 limit 个折叠计数。
func joinInts(xs []int, limit int) string {
	parts := make([]string, 0, limit)
	for i, x := range xs {
		if i == limit {
			return strings.Join(parts, "、") + fmt.Sprintf(" 等 %d 章", len(xs))
		}
		parts = append(parts, fmt.Sprintf("%d", x))
	}
	return strings.Join(parts, "、")
}

// buildPreviewPause 组装停靠点文案：用户据此决定是否放行后续全书拆解。
// 只写事实，操作提示（y 放行 / --guide 重切 / Esc）由 TUI 暂停块统一渲染，避免双份文案漂移。
func buildPreviewPause(b *Boundaries, p *Preview, previewPath string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "已识别 %d 章", len(b.Chapters))
	if len(b.Matter) > 0 {
		fmt.Fprintf(&sb, "、%d 个附属区域", len(b.Matter))
	}
	if len(b.Uncertain) > 0 {
		fmt.Fprintf(&sb, "（%d 章存疑）", len(b.Uncertain))
	}
	sb.WriteString("，黄金三章深读完成：\n")
	for _, c := range p.Chapters {
		fmt.Fprintf(&sb, "  第%d章 %s\n", c.Chapter, c.Title)
		fmt.Fprintf(&sb, "    冲突：%s\n", snippet(c.Conflict, 60))
		fmt.Fprintf(&sb, "    承诺：%s\n", snippet(c.Promise, 60))
	}
	fmt.Fprintf(&sb, "  整体：%s\n", snippet(p.Verdict, 100))
	for _, n := range b.Notes {
		fmt.Fprintf(&sb, "  ! %s\n", n)
	}
	fmt.Fprintf(&sb, "  完整预览：%s\n", previewPath)
	fmt.Fprintf(&sb, "  放行后将逐章拆解全书 %d 章（按当前档位计费）\n", len(b.Chapters))
	return sb.String()
}
