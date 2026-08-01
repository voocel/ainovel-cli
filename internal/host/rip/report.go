package rip

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// reportSchemaVersion 纳入报告工件 InputDigest。
const reportSchemaVersion = 1

// 反转类型枚举，含「无反转」——甜宠/喜剧/报应型故事本就没有反转，
// 强迫模型编一个出来只会污染产物。
var reversalTypes = []string{"视角反转", "身份反转", "动机反转", "时间线反转", "信息反转", "认知反转", "无反转"}

// ReversalTypes 返回反转类型词表副本，供契约与测试引用。
func ReversalTypes() []string { return append([]string(nil), reversalTypes...) }

const reversalNone = "无反转"

// 结构计数阈值。达不到即回喂重问：这些是「拆得够不够深」的客观下限，
// 不是给模型自评的建议值。
const (
	minBeats               = 4 // 结构段：开端/发展/高潮/结局
	minHooks               = 3
	minSetupClues          = 3 // reversal_type=无反转 时跳过（carve-out）
	minCharacterArchetypes = 2
	minReusableStructures  = 3
	minResonanceLayers     = 3
)

// Beat 是一个功能分段。
type Beat struct {
	Name         string `json:"name"` // 开端 / 发展 / 高潮 / 结局 等
	StartChapter int    `json:"start_chapter"`
	EndChapter   int    `json:"end_chapter"`
	Function     string `json:"function"`
}

// Score 是一个评分维度。
type Score struct {
	Dimension string `json:"dimension"`
	Value     int    `json:"value"` // 1-5
	Reason    string `json:"reason"`
}

// ReusableStructure 是一条可复用结构/手法。
type ReusableStructure struct {
	Name     string `json:"name"`
	How      string `json:"how"`       // 怎么用
	FailMode string `json:"fail_mode"` // 用错会怎样
	Example  string `json:"example"`   // 本书中的落点
}

// Reversal 是反转设计分析。
type Reversal struct {
	Type       string   `json:"type"` // 取自 reversalTypes
	Mechanism  string   `json:"mechanism,omitempty"`
	SetupClues []string `json:"setup_clues,omitempty"` // 铺垫线索；type=无反转 时可空
	Chapter    int      `json:"chapter,omitempty"`     // 引爆章
}

// StructureCounts 是报告的结构计数，验收的数值依据。
// 由 Go 从报告内容确定性算出（deriveCounts），不让模型自报——自报的数字与正文脱节是
// 参考实现里最常见的失真来源。
type StructureCounts struct {
	Beats               int    `json:"beats"`
	Hooks               int    `json:"hooks"`
	SetupClues          int    `json:"setup_clues"`
	CharacterArchetypes int    `json:"character_archetypes"`
	ReusableStructures  int    `json:"reusable_structures"`
	ResonanceLayers     int    `json:"resonance_layers"`
	ReversalType        string `json:"reversal_type"`
}

// Report 是拆文报告的结构化真源，拆文报告.md 是它的人类可读投影。
type Report struct {
	StoryCore           string              `json:"story_core"`
	Synopsis            string              `json:"synopsis"`
	Beats               []Beat              `json:"beats"`
	Hooks               []string            `json:"hooks"`
	Reversal            Reversal            `json:"reversal"`
	Scores              []Score             `json:"scores"`
	Resonance           []string            `json:"resonance"` // 共鸣层次
	ReusableStructures  []ReusableStructure `json:"reusable_structures"`
	CharacterArchetypes []string            `json:"character_archetypes"` // 有反差的人物原型
	Opening             string              `json:"opening"`              // 开头分析
	Ending              string              `json:"ending"`               // 结尾分析
	PacingBrief         string              `json:"pacing_brief"`         // 节奏速报
	Verdict             string              `json:"verdict"`
}

// deriveCounts 从报告内容确定性算出结构计数。
func deriveCounts(r *Report) StructureCounts {
	return StructureCounts{
		Beats:               len(r.Beats),
		Hooks:               len(nonEmpty(r.Hooks)),
		SetupClues:          len(nonEmpty(r.Reversal.SetupClues)),
		CharacterArchetypes: len(nonEmpty(r.CharacterArchetypes)),
		ReusableStructures:  len(r.ReusableStructures),
		ResonanceLayers:     len(nonEmpty(r.Resonance)),
		ReversalType:        r.Reversal.Type,
	}
}

// validateCounts 是结构计数阈值校验，含「无反转」carve-out。
// 独立成纯函数便于表驱动测试。
func validateCounts(c StructureCounts) error {
	if c.Beats < minBeats {
		return fmt.Errorf("功能分段只有 %d 段，至少需要 %d 段（须含开端/发展/高潮/结局）", c.Beats, minBeats)
	}
	if c.Hooks < minHooks {
		return fmt.Errorf("钩子只有 %d 条，至少需要 %d 条", c.Hooks, minHooks)
	}
	if !validReversalType(c.ReversalType) {
		return fmt.Errorf("反转类型 %q 非法（只能是：%s）", c.ReversalType, strings.Join(reversalTypes, "、"))
	}
	// carve-out：无反转的故事没有铺垫线索可数，跳过该行而非逼模型编造。
	if c.ReversalType != reversalNone && c.SetupClues < minSetupClues {
		return fmt.Errorf("反转铺垫线索只有 %d 条，至少需要 %d 条（若本篇确实没有反转，请把 type 定为「%s」）",
			c.SetupClues, minSetupClues, reversalNone)
	}
	if c.CharacterArchetypes < minCharacterArchetypes {
		return fmt.Errorf("有反差的人物原型只有 %d 个，至少需要 %d 个", c.CharacterArchetypes, minCharacterArchetypes)
	}
	if c.ReusableStructures < minReusableStructures {
		return fmt.Errorf("可复用结构只有 %d 条，至少需要 %d 条", c.ReusableStructures, minReusableStructures)
	}
	if c.ResonanceLayers < minResonanceLayers {
		return fmt.Errorf("共鸣层次只有 %d 层，至少需要 %d 层", c.ResonanceLayers, minResonanceLayers)
	}
	return nil
}

func validReversalType(v string) bool {
	for _, t := range reversalTypes {
		if t == v {
			return true
		}
	}
	return false
}

// validateReport 校验报告：结构计数阈值 + 分段范围 + 评分值域。
func validateReport(r *Report, total int) error {
	if strings.TrimSpace(r.StoryCore) == "" {
		return fmt.Errorf("缺故事核")
	}
	if strings.TrimSpace(r.Synopsis) == "" {
		return fmt.Errorf("缺故事梗概")
	}
	prevEnd := 0
	for i, b := range r.Beats {
		if strings.TrimSpace(b.Name) == "" {
			return fmt.Errorf("功能分段[%d] 缺名称", i)
		}
		if b.StartChapter < 1 || b.EndChapter > total || b.StartChapter > b.EndChapter {
			return fmt.Errorf("功能分段 %q 范围 %d-%d 非法（全书 1-%d 章）", b.Name, b.StartChapter, b.EndChapter, total)
		}
		if b.StartChapter <= prevEnd {
			return fmt.Errorf("功能分段 %q 与前一段（止于第 %d 章）重叠", b.Name, prevEnd)
		}
		prevEnd = b.EndChapter
	}
	if r.Reversal.Chapter != 0 && (r.Reversal.Chapter < 1 || r.Reversal.Chapter > total) {
		return fmt.Errorf("反转引爆章 %d 越界（全书 1-%d 章）", r.Reversal.Chapter, total)
	}
	for _, s := range r.Scores {
		if s.Value < 1 || s.Value > 5 {
			return fmt.Errorf("维度 %q 评分 %d 越界（1-5）", s.Dimension, s.Value)
		}
		if strings.TrimSpace(s.Reason) == "" {
			return fmt.Errorf("维度 %q 缺评分理由", s.Dimension)
		}
	}
	for i, rs := range r.ReusableStructures {
		if strings.TrimSpace(rs.Name) == "" || strings.TrimSpace(rs.How) == "" {
			return fmt.Errorf("可复用结构[%d] 缺名称或用法", i)
		}
	}
	return validateCounts(deriveCounts(r))
}

// reportInputDigest 绑定角色设定工件原始字节 + 报告 prompt/schema 版本。
func reportInputDigest(profileRaw []byte) string {
	return Digest([]byte(strings.Join([]string{
		"report", reportPromptVersion, fmt.Sprintf("v%d", reportSchemaVersion), Digest(profileRaw),
	}, "\x00")))
}

// BuildReport 生成拆文报告的结构化真源。
func BuildReport(ctx context.Context, m callModel, systemPrompt string, agg *Aggregate, prof *Profile, summaries []ChapterSummary, total, maxTokens int, callProf callProfile) (*Report, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "以下是全书 %d 章的结构聚合、人物设定与逐章拆解。请产出完整拆文报告。\n\n", total)
	sb.WriteString("## 结构聚合\n\n")
	aggData, _ := json.MarshalIndent(agg, "", "  ")
	sb.Write(aggData)
	sb.WriteString("\n\n## 人物与设定\n\n")
	profData, _ := json.MarshalIndent(prof, "", "  ")
	sb.Write(profData)
	sb.WriteString("\n\n## 逐章拆解\n\n")
	for _, s := range summaries {
		if s.Chapter == 0 {
			continue
		}
		data, _ := json.Marshal(compactSummaryOf(s))
		sb.Write(data)
		sb.WriteByte('\n')
	}
	out, err := callStructured[Report](ctx, m, reportContract, systemPrompt, sb.String(), maxTokens, callProf, func(r *Report) error {
		return validateReport(r, total)
	})
	if err != nil {
		return nil, err
	}
	c := deriveCounts(&out)
	callProf.step(0, 0, "报告完成：%d 段结构、%d 条钩子、反转 %s、%d 条可复用结构",
		c.Beats, c.Hooks, c.ReversalType, c.ReusableStructures)
	return &out, nil
}

// renderReportMarkdown 渲染 拆文报告.md。
// failed 非空时在首部写降级提示：产物不完整这件事必须写在报告里，不能只在事件流里一闪而过。
func renderReportMarkdown(m *Manifest, b *Boundaries, agg *Aggregate, p *Profile, r *Report, failed []int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# 拆文报告：%s\n\n", m.BookName)
	fmt.Fprintf(&sb, "- 原文：%s（编码 %s，%d 字，%d 章）\n", m.SourceName, m.Encoding, m.Runes, len(b.Chapters))
	fmt.Fprintf(&sb, "- 形态：%s\n", formLabel(resolveForm(m.Runes)))
	if len(failed) > 0 {
		fmt.Fprintf(&sb, "\n> 本次拆解有 %d 章失败（第 %s 章），报告基于其余章节产出，结论可能不完整。\n",
			len(failed), joinInts(failed, 12))
	}
	fmt.Fprintf(&sb, "\n## 故事核\n\n%s\n", r.StoryCore)
	fmt.Fprintf(&sb, "\n## 故事梗概\n\n%s\n", r.Synopsis)

	sb.WriteString("\n## 功能分段\n\n")
	sb.WriteString("| 分段 | 章节 | 功能 |\n|---|---|---|\n")
	for _, bt := range r.Beats {
		fmt.Fprintf(&sb, "| %s | %d-%d | %s |\n", bt.Name, bt.StartChapter, bt.EndChapter, bt.Function)
	}

	sb.WriteString("\n## 剧情单元\n\n")
	sb.WriteString("| 单元 | 章节 | 功能 | 概要 |\n|---|---|---|---|\n")
	for _, u := range agg.PlotUnits {
		fmt.Fprintf(&sb, "| %s | %d-%d | %s | %s |\n", u.Title, u.StartChapter, u.EndChapter, u.Function, oneLine(u.Summary))
	}

	sb.WriteString("\n## 钩子\n\n")
	for _, h := range nonEmpty(r.Hooks) {
		fmt.Fprintf(&sb, "- %s\n", h)
	}

	sb.WriteString("\n## 反转设计\n\n")
	fmt.Fprintf(&sb, "- 类型：%s\n", r.Reversal.Type)
	if r.Reversal.Mechanism != "" {
		fmt.Fprintf(&sb, "- 机制：%s\n", r.Reversal.Mechanism)
	}
	if r.Reversal.Chapter > 0 {
		fmt.Fprintf(&sb, "- 引爆章：第 %d 章\n", r.Reversal.Chapter)
	}
	writeBullets(&sb, "铺垫线索", r.Reversal.SetupClues)

	sb.WriteString("\n## 情绪模块\n\n")
	for _, e := range agg.EmotionModules {
		fmt.Fprintf(&sb, "- **%s**：%s → %s", e.Name, e.Trigger, e.Payoff)
		if len(e.Chapters) > 0 {
			fmt.Fprintf(&sb, "（第 %s 章）", joinInts(e.Chapters, 8))
		}
		sb.WriteByte('\n')
	}

	sb.WriteString("\n## 人物\n\n")
	sb.WriteString("| 人物 | 层级 | 功能 | 动机 | 首次出场 |\n|---|---|---|---|---|\n")
	for _, c := range p.Characters {
		fmt.Fprintf(&sb, "| %s | %s | %s | %s | 第 %d 章 |\n", c.Name, c.Tier, oneLine(c.Function), oneLine(c.Motivation), c.FirstSeen)
	}

	if len(p.Settings) > 0 {
		sb.WriteString("\n## 设定\n\n")
		for _, s := range p.Settings {
			fmt.Fprintf(&sb, "- [%s] **%s**：%s", s.Category, s.Name, s.Rule)
			if s.Boundary != "" {
				fmt.Fprintf(&sb, "（边界：%s）", s.Boundary)
			}
			sb.WriteByte('\n')
		}
	}

	fmt.Fprintf(&sb, "\n## 开头\n\n%s\n", r.Opening)
	fmt.Fprintf(&sb, "\n## 结尾\n\n%s\n", r.Ending)
	fmt.Fprintf(&sb, "\n## 节奏速报\n\n%s\n", r.PacingBrief)

	sb.WriteString("\n## 评分\n\n")
	sb.WriteString("| 维度 | 分数 | 理由 |\n|---|---|---|\n")
	for _, s := range r.Scores {
		fmt.Fprintf(&sb, "| %s | %d/5 | %s |\n", s.Dimension, s.Value, oneLine(s.Reason))
	}

	sb.WriteString("\n## 共鸣层次\n\n")
	for i, x := range nonEmpty(r.Resonance) {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, x)
	}

	sb.WriteString("\n## 可复用结构\n\n")
	for _, rs := range r.ReusableStructures {
		fmt.Fprintf(&sb, "### %s\n\n", rs.Name)
		fmt.Fprintf(&sb, "- 怎么用：%s\n", rs.How)
		if rs.FailMode != "" {
			fmt.Fprintf(&sb, "- 用错会怎样：%s\n", rs.FailMode)
		}
		if rs.Example != "" {
			fmt.Fprintf(&sb, "- 本书落点：%s\n", rs.Example)
		}
		sb.WriteByte('\n')
	}

	fmt.Fprintf(&sb, "## 一句话\n\n%s\n", r.Verdict)
	return sb.String()
}

// renderPlotPointsMarkdown 渲染 情节节点.md：逐章情节点清单，方便定位节奏锚点。
func renderPlotPointsMarkdown(m *Manifest, summaries []ChapterSummary, failed []int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# 情节节点：%s\n\n", m.BookName)
	if len(failed) > 0 {
		fmt.Fprintf(&sb, "> 第 %s 章拆解失败，下表缺这些章。\n\n", joinInts(failed, 12))
	}
	for _, s := range summaries {
		if s.Chapter == 0 {
			continue
		}
		fmt.Fprintf(&sb, "## 第 %d 章 %s\n\n", s.Chapter, s.Title)
		for _, pp := range s.PlotPoints {
			fmt.Fprintf(&sb, "- %s [%s] %s\n", pp.ID, pp.Tone, pp.Beat)
		}
		if s.Hook != "" {
			fmt.Fprintf(&sb, "- 章末钩子（%s）：%s\n", s.HookType, s.Hook)
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// renderTechniquesMarkdown 渲染 写作手法.md：按手法归并出现章节，方便复用。
func renderTechniquesMarkdown(m *Manifest, summaries []ChapterSummary, r *Report) string {
	type entry struct {
		name     string
		chapters []int
	}
	index := map[string]*entry{}
	var order []string
	for _, s := range summaries {
		if s.Chapter == 0 {
			continue
		}
		for _, t := range nonEmpty(s.Techniques) {
			key := squashSpace(t)
			e := index[key]
			if e == nil {
				e = &entry{name: t}
				index[key] = e
				order = append(order, key)
			}
			e.chapters = append(e.chapters, s.Chapter)
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# 写作手法：%s\n\n", m.BookName)
	sb.WriteString("## 逐章手法归并\n\n")
	sb.WriteString("| 手法 | 出现章数 | 章节 |\n|---|---|---|\n")
	for _, key := range order {
		e := index[key]
		fmt.Fprintf(&sb, "| %s | %d | %s |\n", e.name, len(e.chapters), joinInts(e.chapters, 10))
	}
	sb.WriteString("\n## 可复用结构\n\n")
	for _, rs := range r.ReusableStructures {
		fmt.Fprintf(&sb, "### %s\n\n- 怎么用：%s\n", rs.Name, rs.How)
		if rs.FailMode != "" {
			fmt.Fprintf(&sb, "- 用错会怎样：%s\n", rs.FailMode)
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// renderPacingMarkdown 渲染 节奏.md：区间节奏 + 逐章基调序列。
func renderPacingMarkdown(m *Manifest, agg *Aggregate, summaries []ChapterSummary) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# 节奏：%s\n\n", m.BookName)
	sb.WriteString("## 区间节奏\n\n")
	sb.WriteString("| 章节 | 速度 | 说明 |\n|---|---|---|\n")
	for _, p := range agg.Pacing {
		fmt.Fprintf(&sb, "| %d-%d | %s | %s |\n", p.StartChapter, p.EndChapter, p.Tempo, oneLine(p.Note))
	}
	sb.WriteString("\n## 逐章基调序列\n\n")
	sb.WriteString("| 章 | 情节点数 | 基调序列 | 钩子类型 |\n|---|---|---|---|\n")
	for _, s := range summaries {
		if s.Chapter == 0 {
			continue
		}
		tones := make([]string, 0, len(s.PlotPoints))
		for _, pp := range s.PlotPoints {
			tones = append(tones, pp.Tone)
		}
		fmt.Fprintf(&sb, "| %d | %d | %s | %s |\n", s.Chapter, len(s.PlotPoints), strings.Join(tones, "→"), s.HookType)
	}
	return sb.String()
}

// renderEmotionMarkdown 渲染 情绪模块.md。
func renderEmotionMarkdown(m *Manifest, agg *Aggregate, summaries []ChapterSummary) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# 情绪模块：%s\n\n", m.BookName)
	for _, e := range agg.EmotionModules {
		fmt.Fprintf(&sb, "## %s\n\n", e.Name)
		fmt.Fprintf(&sb, "- 触发：%s\n", e.Trigger)
		fmt.Fprintf(&sb, "- 支付：%s\n", e.Payoff)
		if len(e.Chapters) > 0 {
			fmt.Fprintf(&sb, "- 出现：第 %s 章\n", joinInts(e.Chapters, 12))
		}
		sb.WriteByte('\n')
	}
	sb.WriteString("## 逐章情绪走向\n\n")
	for _, s := range summaries {
		if s.Chapter == 0 || strings.TrimSpace(s.EmotionArc) == "" {
			continue
		}
		fmt.Fprintf(&sb, "- 第 %d 章：%s\n", s.Chapter, s.EmotionArc)
	}
	return sb.String()
}

// oneLine 把多行文本压成表格单元格可用的单行。
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "|", "｜")), " ")
}

func formLabel(f Form) string {
	switch f {
	case FormLong:
		return "长篇"
	case FormShort:
		return "短篇"
	default:
		return "灰区（需裁定）"
	}
}
