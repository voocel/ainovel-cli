package scan

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// analyzeSchemaVersion 纳入趋势与选题工件的 InputDigest，升级契约时递增以失效已落盘工件。
const analyzeSchemaVersion = 1

// rawEntry 是解析阶段的原始条目：字段可空（指针/零值），由清洗层做必填判定。
// 契约允许 null 而不是让模型编造——猜出来的排名和作者会让脏数据以干净的形态混进 entries.json。
type rawEntry struct {
	Rank        *int    `json:"rank"`
	Title       *string `json:"title"`
	Author      *string `json:"author"`
	Category    *string `json:"category"`
	Description *string `json:"description"`
	Stats       *string `json:"stats"`
}

// parseResult 是一份数据源的解析产物。
type parseResult struct {
	Entries []rawEntry `json:"entries"`
	Notes   []string   `json:"notes"`
}

// toEntry 把可空原始条目转成清洗层的 Entry；缺失字段留零值，交 CleanEntries 判定。
func (r rawEntry) toEntry() Entry {
	e := Entry{}
	if r.Rank != nil {
		e.Rank = *r.Rank
	}
	if r.Title != nil {
		e.Title = strings.TrimSpace(*r.Title)
	}
	if r.Author != nil {
		e.Author = strings.TrimSpace(*r.Author)
	}
	if r.Category != nil {
		e.Category = strings.TrimSpace(*r.Category)
	}
	if r.Description != nil {
		e.Description = strings.TrimSpace(*r.Description)
	}
	if r.Stats != nil {
		e.Stats = strings.TrimSpace(*r.Stats)
	}
	return e
}

// CategoryStat 是一个题材的分布统计。
type CategoryStat struct {
	Name     string   `json:"name"`
	Count    int      `json:"count"`
	Examples []string `json:"examples"`
	Note     string   `json:"note"`
}

// Trend 是一条趋势判断。
type Trend struct {
	Title    string   `json:"title"`
	Evidence []string `json:"evidence"`
	Reading  string   `json:"reading"`
}

// FreshElement 是一个新元素/新组合。
type FreshElement struct {
	Name   string   `json:"name"`
	SeenIn []string `json:"seen_in"`
	Why    string   `json:"why"`
}

// Analysis 是市场趋势分析产物。
type Analysis struct {
	Categories    []CategoryStat `json:"categories"`
	Trends        []Trend        `json:"trends"`
	FreshElements []FreshElement `json:"fresh_elements"`
	Saturated     []string       `json:"saturated"`
	Verdict       string         `json:"verdict"`
}

// entriesInputDigest 是扫榜的身份基石。
//
// 扫榜没有 imp/rip 那种字节精确的原文快照可用做身份（同一榜单每次复制粘贴的空白与
// 模板文本都可能不同），替代品是「归一化后的条目 payload + 采集日期」：
// 同一天同一份数据重跑复用，换数据或跨日重扫自然失效。
func entriesInputDigest(entries []Entry, scanDate, platform string) string {
	sorted := make([]Entry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Rank < sorted[j].Rank })
	var sb strings.Builder
	sb.WriteString("entries\x00")
	sb.WriteString(scanDate)
	sb.WriteString("\x00")
	sb.WriteString(strings.ToLower(platform))
	fmt.Fprintf(&sb, "\x00v%d\x00", analyzeSchemaVersion)
	for _, e := range sorted {
		data, _ := json.Marshal(e)
		sb.Write(data)
		sb.WriteByte(0)
	}
	return Digest([]byte(sb.String()))
}

// analysisInputDigest 绑定 entries 工件原始字节 + 趋势 prompt/schema 版本。
func analysisInputDigest(entriesRaw []byte) string {
	return Digest([]byte(strings.Join([]string{
		"analysis", analyzePromptVersion, fmt.Sprintf("v%d", analyzeSchemaVersion), Digest(entriesRaw),
	}, "\x00")))
}

// topicInputDigest 绑定趋势工件原始字节 + 选题 prompt/schema 版本。
func topicInputDigest(analysisRaw []byte) string {
	return Digest([]byte(strings.Join([]string{
		"topic", topicPromptVersion, fmt.Sprintf("v%d", analyzeSchemaVersion), Digest(analysisRaw),
	}, "\x00")))
}

// nonEmpty 过滤掉空白项：模型常用空串占位满足「至少 N 条」的字面要求。
func nonEmpty(xs []string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if s := strings.TrimSpace(x); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// validateParse 机械校验解析产物：至少读出 1 条，且不允许全字段皆空的占位条目。
func validateParse(r *parseResult) error {
	if len(r.Entries) == 0 {
		return fmt.Errorf("没有解析出任何条目：若这段文本确实不是榜单，请说明而不是返回空数组")
	}
	blank := 0
	for _, e := range r.Entries {
		got := e.toEntry()
		if got.Rank == 0 && got.Title == "" && got.Author == "" && got.Description == "" {
			blank++
		}
	}
	if blank == len(r.Entries) {
		return fmt.Errorf("%d 条条目全部字段为空：请逐条摘录原文中真实存在的字段", blank)
	}
	return nil
}

// ParseSource 把一份半结构化榜单文本读成结构化条目。
func ParseSource(ctx context.Context, m callModel, systemPrompt string, src Source, contextBytes, maxTokens int, prof callProfile) ([]Entry, []string, error) {
	header := fmt.Sprintf("平台：%s\n榜单：%s\n来源：%s\n\n以下是榜单页的原始文本，请逐条读成结构化条目。原文没写的字段留 null，不要推测。\n\n",
		fallback(src.Platform, "未指明"), fallback(src.RankName, "未指明"), src.Origin)
	if contextBytes <= len(header)+512 {
		return nil, nil, fmt.Errorf("解析输入预算过小：contextBytes=%d，固定提示占用=%d", contextBytes, len(header))
	}
	chunks := splitTextByBytes(src.Raw, contextBytes-len(header))
	var entries []Entry
	var notes []string
	for i, chunk := range chunks {
		if len(chunks) > 1 {
			prof.step(i, len(chunks), "解析 %s 的第 %d/%d 块", src.Origin, i+1, len(chunks))
		}
		out, err := callStructured[parseResult](ctx, m, parseContract, systemPrompt, header+chunk, maxTokens, prof, validateParse)
		if err != nil {
			return nil, nil, fmt.Errorf("第 %d/%d 块: %w", i+1, len(chunks), err)
		}
		for _, raw := range out.Entries {
			entries = append(entries, raw.toEntry())
		}
		for _, note := range nonEmpty(out.Notes) {
			if len(chunks) > 1 {
				note = fmt.Sprintf("第 %d/%d 块：%s", i+1, len(chunks), note)
			}
			notes = append(notes, note)
		}
	}
	return entries, notes, nil
}

// splitTextByBytes 优先在换行处分块；超长单行按 UTF-8 rune 边界硬切。
func splitTextByBytes(raw string, maxBytes int) []string {
	if len(raw) <= maxBytes {
		return []string{raw}
	}
	var chunks []string
	var current strings.Builder
	flush := func() {
		if current.Len() > 0 {
			chunks = append(chunks, current.String())
			current.Reset()
		}
	}
	appendPiece := func(piece string) {
		for len(piece) > maxBytes {
			flush()
			cut := maxBytes
			for cut > 0 && !utf8.RuneStart(piece[cut]) {
				cut--
			}
			if cut == 0 {
				cut = maxBytes
			}
			chunks = append(chunks, piece[:cut])
			piece = piece[cut:]
		}
		if current.Len()+len(piece) > maxBytes {
			flush()
		}
		current.WriteString(piece)
	}
	for _, line := range strings.SplitAfter(raw, "\n") {
		appendPiece(line)
	}
	flush()
	return chunks
}

func fallback(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// validateAnalysis 机械校验趋势产物：
// 题材条目数不得超过实际有效条目数（模型放大分母会让分布看起来更有说服力），
// 证据必须引用输入里真实存在的书名（不许凭空造书）。
func validateAnalysis(a *Analysis, entries []Entry) error {
	if len(a.Trends) == 0 {
		return fmt.Errorf("至少需要 1 条趋势判断")
	}
	if strings.TrimSpace(a.Verdict) == "" {
		return fmt.Errorf("缺一句话总评（verdict）")
	}
	titles := make(map[string]bool, len(entries))
	for _, e := range entries {
		titles[squash(e.Title)] = true
	}
	total := len(entries)
	sum := 0
	for i, c := range a.Categories {
		if strings.TrimSpace(c.Name) == "" {
			return fmt.Errorf("题材[%d] 缺名称", i)
		}
		if c.Count < 0 || c.Count > total {
			return fmt.Errorf("题材 %q 的条目数 %d 越界（有效条目共 %d 条）", c.Name, c.Count, total)
		}
		sum += c.Count
		if err := checkTitles(titles, c.Examples, fmt.Sprintf("题材 %q 的代表作", c.Name)); err != nil {
			return err
		}
	}
	if sum > total {
		return fmt.Errorf("各题材条目数之和 %d 超过有效条目总数 %d：一本书只能计入一个题材", sum, total)
	}
	for i, t := range a.Trends {
		if strings.TrimSpace(t.Title) == "" {
			return fmt.Errorf("趋势[%d] 缺标题", i)
		}
		if len(nonEmpty(t.Evidence)) == 0 {
			return fmt.Errorf("趋势 %q 没给榜单证据：每条判断都必须落到具体书名上", t.Title)
		}
		if err := checkTitles(titles, t.Evidence, fmt.Sprintf("趋势 %q 的证据", t.Title)); err != nil {
			return err
		}
	}
	for i, f := range a.FreshElements {
		if strings.TrimSpace(f.Name) == "" {
			return fmt.Errorf("新元素[%d] 缺名称", i)
		}
		if err := checkTitles(titles, f.SeenIn, fmt.Sprintf("新元素 %q 的出处", f.Name)); err != nil {
			return err
		}
	}
	return nil
}

// checkTitles 校验一组引用里提到的书名确实来自输入条目。
// 引用文本可能是「《书名》体现了…」这种句子，故用包含匹配而非全等。
func checkTitles(titles map[string]bool, refs []string, what string) error {
	for _, ref := range nonEmpty(refs) {
		s := squash(ref)
		hit := false
		for t := range titles {
			if t != "" && strings.Contains(s, t) {
				hit = true
				break
			}
		}
		if !hit {
			return fmt.Errorf("%s 提到 %q，但这本书不在榜单条目里：只能引用输入中真实存在的书名", what, snippet(ref, 30))
		}
	}
	return nil
}

// squash 去掉全部空白，便于跨排版做包含匹配。
func squash(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// AnalyzeRanks 从清洗后的条目归纳市场趋势。
func AnalyzeRanks(ctx context.Context, m callModel, systemPrompt string, entries []Entry, q QualityReport, platform, rankName string, maxTokens int, prof callProfile) (*Analysis, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "平台：%s\n榜单：%s\n有效条目：%d\n",
		fallback(platform, "未指明"), fallback(rankName, "未指明"), q.ValidEntries)
	if q.Sparse {
		// 稀疏事实必须进 payload：让模型知道样本不足而收敛措辞，
		// 但结论的硬约束仍在 Go 侧（ClampFeasibility），不依赖它自觉。
		sb.WriteString("数据质量：[数据稀疏] —— 样本不足，请如实收敛结论强度，不要给出超出样本支撑的判断\n")
	}
	sb.WriteString("\n以下是榜单条目：\n\n")
	for _, e := range entries {
		data, _ := json.Marshal(e)
		sb.Write(data)
		sb.WriteByte('\n')
	}

	out, err := callStructured[Analysis](ctx, m, analyzeContract, systemPrompt, sb.String(), maxTokens, prof, func(a *Analysis) error {
		return validateAnalysis(a, entries)
	})
	if err != nil {
		return nil, err
	}
	prof.step(0, 0, "总评：%s", snippet(out.Verdict, 60))
	for _, t := range out.Trends {
		prof.step(0, 0, "趋势〈%s〉%s", snippet(t.Title, 20), snippet(t.Reading, 40))
	}
	return &out, nil
}

// RenderReportMarkdown 渲染扫榜报告 Markdown。质量头强制在最前面：
// 结论的可信度先于结论本身，读者不该翻到底才发现样本只有 8 条。
func RenderReportMarkdown(a *Analysis, entries []Entry, q QualityReport, platform, rankName, scanDate string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# 扫榜报告：%s %s（%s）\n\n", fallback(platform, "未指明平台"), fallback(rankName, "榜单"), scanDate)
	sb.WriteString(RenderQualityHeader(q))

	fmt.Fprintf(&sb, "## 总评\n\n%s\n\n", a.Verdict)

	if len(a.Categories) > 0 {
		sb.WriteString("## 题材分布\n\n")
		sb.WriteString("| 题材 | 条目数 | 代表作 | 观察 |\n|---|---|---|---|\n")
		for _, c := range a.Categories {
			fmt.Fprintf(&sb, "| %s | %d | %s | %s |\n",
				c.Name, c.Count, strings.Join(nonEmpty(c.Examples), "、"), c.Note)
		}
		sb.WriteString("\n")
	}

	if len(a.Trends) > 0 {
		sb.WriteString("## 趋势\n\n")
		for i, t := range a.Trends {
			fmt.Fprintf(&sb, "### %d. %s\n\n%s\n\n", i+1, t.Title, t.Reading)
			if ev := nonEmpty(t.Evidence); len(ev) > 0 {
				sb.WriteString("榜单证据：\n\n")
				for _, e := range ev {
					fmt.Fprintf(&sb, "- %s\n", e)
				}
				sb.WriteString("\n")
			}
		}
	}

	if len(a.FreshElements) > 0 {
		sb.WriteString("## 新元素\n\n")
		for _, f := range a.FreshElements {
			fmt.Fprintf(&sb, "- **%s**：%s", f.Name, f.Why)
			if seen := nonEmpty(f.SeenIn); len(seen) > 0 {
				fmt.Fprintf(&sb, "（见 %s）", strings.Join(seen, "、"))
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	if sat := nonEmpty(a.Saturated); len(sat) > 0 {
		sb.WriteString("## 已饱和方向\n\n")
		for _, s := range sat {
			fmt.Fprintf(&sb, "- %s\n", s)
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## 条目明细\n\n")
	sb.WriteString("| 排名 | 书名 | 作者 | 分类 | 数据 |\n|---|---|---|---|---|\n")
	for _, e := range sortByRank(entries) {
		fmt.Fprintf(&sb, "| %d | %s | %s | %s | %s |\n", e.Rank, e.Title, e.Author, e.Category, e.Stats)
	}
	sb.WriteString("\n")
	return sb.String()
}

// sortByRank 按排名排序副本，不改调用方的切片。
func sortByRank(entries []Entry) []Entry {
	sorted := make([]Entry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Rank < sorted[j].Rank })
	return sorted
}
