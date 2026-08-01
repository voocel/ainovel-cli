package rip

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/stylestat"
)

// styleSchemaVersion 纳入文风工件 InputDigest。
const styleSchemaVersion = 1

// 置信度由代码按样本量判定，不由模型自评——「我很有信心」是模型最廉价的输出。
const (
	confidenceHigh = "高"
	confidenceMid  = "中"
	confidenceLow  = "低"
)

// 置信度分档的章数门槛。低于 minChapters（stylestat 的统计下限）时统计为空，
// 只能靠 LLM 读原文定性，置信度必然是低。
const (
	styleHighChapters = 20
	styleMidChapters  = 8
)

// styleAnchorMinRunes 锚点切片的最小长度：太短的片段（「他说」）在原文里到处都是，
// 核对通过也证明不了模型真读到了那一处。
const styleAnchorMinRunes = 8

// StyleAnchor 是一条原文锚点：模型的判断必须落到具体文字上。
type StyleAnchor struct {
	Chapter int    `json:"chapter"`
	Quote   string `json:"quote"` // 原文片段，逐字复制
	Why     string `json:"why"`   // 这段体现了什么文风特征
}

// Style 是文风裁定结果。
// Stats 与 Confidence 两个字段由 Go 注入/判定，不进契约——句长分布这类数字让模型猜
// 只会得到编造的精确值。
type Style struct {
	Voice       string        `json:"voice"`       // 语感总述
	Sentence    string        `json:"sentence"`    // 句式特征
	Perspective string        `json:"perspective"` // 视角与人称
	Dialogue    string        `json:"dialogue"`    // 对话风格
	Narration   string        `json:"narration"`   // 叙述与描写配比
	Signatures  []string      `json:"signatures"`  // 标志性手法
	Avoid       []string      `json:"avoid"`       // 模仿时要避开的陷阱
	Anchors     []StyleAnchor `json:"anchors"`

	Stats            *stylestat.Stats `json:"stats,omitempty"`
	Confidence       string           `json:"confidence"`
	ConfidenceReason string           `json:"confidence_reason"`
}

// styleStatsInput 组装确定性统计的输入：逐章正文 + 标题。
func styleStatsInput(b *Boundaries, normalized []byte) stylestat.Input {
	in := stylestat.Input{
		Chapters: make([]string, 0, len(b.Chapters)),
		Titles:   make([]string, 0, len(b.Chapters)),
	}
	for i, c := range b.Chapters {
		in.Chapters = append(in.Chapters, b.Content(normalized, i))
		in.Titles = append(in.Titles, c.Title)
	}
	return in
}

// computeStyleStats 算出确定性文风统计；章数不足时返回 nil（stylestat 的行为）。
func computeStyleStats(b *Boundaries, normalized []byte) *stylestat.Stats {
	return stylestat.Compute(styleStatsInput(b, normalized))
}

// styleConfidence 按样本量判定置信度。纯函数，便于表驱动测试。
func styleConfidence(chapters int, stats *stylestat.Stats) (string, string) {
	switch {
	case stats == nil:
		return confidenceLow, fmt.Sprintf("仅 %d 章，不足以做全书级统计，以下判断只来自通读定性", chapters)
	case chapters >= styleHighChapters:
		return confidenceHigh, fmt.Sprintf("%d 章样本，句式与复读统计稳定", chapters)
	case chapters >= styleMidChapters:
		return confidenceMid, fmt.Sprintf("%d 章样本，统计可用但高频短语可能偏噪", chapters)
	default:
		return confidenceLow, fmt.Sprintf("仅 %d 章样本，频率类结论参考价值有限", chapters)
	}
}

// styleInputDigest 绑定边界身份 + 确定性统计输出 + prompt/schema 版本。
// 不绑报告：文风读的是原文本身，换报告 prompt 不该让文风重跑。
func styleInputDigest(boundIdentity string, b *Boundaries, normalized []byte) string {
	stats := computeStyleStats(b, normalized)
	statsJSON, _ := json.Marshal(stats)
	return Digest([]byte(strings.Join([]string{
		"style", stylePromptVersion, fmt.Sprintf("v%d", styleSchemaVersion),
		boundIdentity, Digest(statsJSON),
	}, "\x00")))
}

// validateStyle 机械校验：关键判断非空、锚点能在原文里逐字找到。
// 锚点核对是这一阶段唯一的强主张检查——声称「原文这么写的」就必须真在原文里。
func validateStyle(s *Style, b *Boundaries, normalizedSquashed string) error {
	if strings.TrimSpace(s.Voice) == "" {
		return fmt.Errorf("缺语感总述（voice）")
	}
	if strings.TrimSpace(s.Sentence) == "" {
		return fmt.Errorf("缺句式特征（sentence）")
	}
	if strings.TrimSpace(s.Perspective) == "" {
		return fmt.Errorf("缺视角与人称（perspective）")
	}
	if len(nonEmpty(s.Signatures)) == 0 {
		return fmt.Errorf("至少需要 1 条标志性手法（signatures）")
	}
	if len(s.Anchors) < 2 {
		return fmt.Errorf("至少需要 2 条原文锚点（anchors）：每条判断都要落到具体文字上")
	}
	for i, a := range s.Anchors {
		if a.Chapter < 1 || a.Chapter > len(b.Chapters) {
			return fmt.Errorf("锚点[%d] 章号 %d 越界（全书 1-%d 章）", i, a.Chapter, len(b.Chapters))
		}
		quote := squashSpace(a.Quote)
		if len([]rune(quote)) < styleAnchorMinRunes {
			return fmt.Errorf("锚点[%d] 原文片段太短（%q）：至少 %d 字，否则无法确认出处",
				i, snippet(a.Quote, 20), styleAnchorMinRunes)
		}
		if !strings.Contains(normalizedSquashed, quote) {
			return fmt.Errorf("锚点[%d] 的原文片段 %q 在原文中找不到：必须逐字复制原文，不要转述或改写",
				i, snippet(a.Quote, 30))
		}
	}
	return nil
}

// BuildStyle 裁定文风：确定性统计由代码算好注入 payload，模型只做定性裁定与锚点挑选。
func BuildStyle(ctx context.Context, m callModel, systemPrompt string, normalized []byte, b *Boundaries, maxTokens int, prof callProfile) (*Style, error) {
	stats := computeStyleStats(b, normalized)
	var sb strings.Builder
	sb.WriteString("请裁定这本书的文风：语感、句式、视角、对话与叙述配比，并挑出原文锚点。\n\n")
	if stats != nil {
		sb.WriteString("## 确定性统计（由程序算出，是事实，不要复述数字，只据此判断）\n\n")
		statsJSON, _ := json.MarshalIndent(stats, "", "  ")
		sb.Write(statsJSON)
		sb.WriteString("\n\n")
	} else {
		fmt.Fprintf(&sb, "（全书仅 %d 章，样本不足以做全书统计，请只凭通读给出定性判断。）\n\n", len(b.Chapters))
	}
	sb.WriteString("## 原文样本\n\n")
	for _, i := range styleSampleIndexes(len(b.Chapters)) {
		c := b.Chapters[i]
		fmt.Fprintf(&sb, "### 第 %d 章：%s\n\n", c.Number, c.Title)
		sb.WriteString(b.Content(normalized, i))
		sb.WriteString("\n\n---\n\n")
	}
	squashed := squashSpace(string(normalized))
	out, err := callStructured[Style](ctx, m, styleContract, systemPrompt, sb.String(), maxTokens, prof, func(s *Style) error {
		return validateStyle(s, b, squashed)
	})
	if err != nil {
		return nil, err
	}
	out.Stats = stats
	out.Confidence, out.ConfidenceReason = styleConfidence(len(b.Chapters), stats)
	prof.step(0, 0, "文风裁定完成（置信度 %s）：%s", out.Confidence, snippet(out.Voice, 60))
	return &out, nil
}

// styleSampleChapters 是送入文风裁定的原文样本章数上限。
// 文风不需要全书正文——头/中/尾各取几章足够看出句式与语感，全书会直接撑爆上下文。
const styleSampleChapters = 6

// styleSampleIndexes 取头中尾均匀分布的样本章下标，升序且不重复。
func styleSampleIndexes(total int) []int {
	if total <= 0 {
		return nil
	}
	if total <= styleSampleChapters {
		idx := make([]int, total)
		for i := range idx {
			idx[i] = i
		}
		return idx
	}
	idx := make([]int, 0, styleSampleChapters)
	last := -1
	for k := 0; k < styleSampleChapters; k++ {
		// 均匀取点：k=0 落在首章，k=styleSampleChapters-1 落在末章。
		i := k * (total - 1) / (styleSampleChapters - 1)
		if i <= last {
			i = last + 1
		}
		if i >= total {
			break
		}
		idx = append(idx, i)
		last = i
	}
	return idx
}

// renderStyleMarkdown 渲染 文风.md：定性裁定 + 确定性统计 + 原文锚点。
func renderStyleMarkdown(m *Manifest, s *Style) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# 文风：%s\n\n", m.BookName)
	fmt.Fprintf(&sb, "- 置信度：%s（%s）\n", s.Confidence, s.ConfidenceReason)
	fmt.Fprintf(&sb, "\n## 语感\n\n%s\n", s.Voice)
	fmt.Fprintf(&sb, "\n## 句式\n\n%s\n", s.Sentence)
	fmt.Fprintf(&sb, "\n## 视角与人称\n\n%s\n", s.Perspective)
	if strings.TrimSpace(s.Dialogue) != "" {
		fmt.Fprintf(&sb, "\n## 对话\n\n%s\n", s.Dialogue)
	}
	if strings.TrimSpace(s.Narration) != "" {
		fmt.Fprintf(&sb, "\n## 叙述与描写\n\n%s\n", s.Narration)
	}

	sb.WriteString("\n## 标志性手法\n\n")
	for _, x := range nonEmpty(s.Signatures) {
		fmt.Fprintf(&sb, "- %s\n", x)
	}
	if items := nonEmpty(s.Avoid); len(items) > 0 {
		sb.WriteString("\n## 模仿时要避开\n\n")
		for _, x := range items {
			fmt.Fprintf(&sb, "- %s\n", x)
		}
	}

	sb.WriteString("\n## 原文锚点\n\n")
	for _, a := range s.Anchors {
		fmt.Fprintf(&sb, "- 第 %d 章：「%s」\n", a.Chapter, oneLine(a.Quote))
		if strings.TrimSpace(a.Why) != "" {
			fmt.Fprintf(&sb, "  - %s\n", a.Why)
		}
	}

	if s.Stats != nil {
		writeStyleStats(&sb, s.Stats)
	}
	return sb.String()
}

// writeStyleStats 把确定性统计渲染成表格。数字全部来自 stylestat，不经模型。
func writeStyleStats(sb *strings.Builder, st *stylestat.Stats) {
	fmt.Fprintf(sb, "\n## 确定性统计（%d 章）\n\n", st.Chapters)
	if len(st.Patterns) > 0 {
		sb.WriteString("### 句式模式\n\n| 模式 | 全书次数 | 章均 |\n|---|---|---|\n")
		for _, p := range st.Patterns {
			fmt.Fprintf(sb, "| %s | %d | %.1f |\n", p.Name, p.Total, p.PerChapter)
		}
		sb.WriteByte('\n')
	}
	if len(st.TopPhrases) > 0 {
		sb.WriteString("### 高频短语\n\n")
		for _, p := range st.TopPhrases {
			fmt.Fprintf(sb, "- %s（%d 次）\n", p.Text, p.Count)
		}
		sb.WriteByte('\n')
	}
	if len(st.RepeatedSentences) > 0 {
		sb.WriteString("### 跨章重复句\n\n")
		for _, r := range st.RepeatedSentences {
			fmt.Fprintf(sb, "- %s（%d 章 %d 次）\n", r.Text, r.Chapters, r.Count)
		}
		sb.WriteByte('\n')
	}
	fmt.Fprintf(sb, "### 章末形态\n\n- 短结尾占比：%.2f\n- 末行中位字数：%d\n- 开篇写时间占比：%.2f\n",
		st.Ending.ShortRatio, st.Ending.MedianRunes, st.OpeningTimeRate)
	if st.TitleFormats != nil {
		fmt.Fprintf(sb, "- 标题「第N章」前缀混用：带前缀 %d 章 / 不带 %d 章\n",
			st.TitleFormats.WithPrefix, st.TitleFormats.WithoutPrefix)
	}
}
