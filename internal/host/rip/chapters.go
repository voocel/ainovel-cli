package rip

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// summarySchemaVersion 是逐章摘要 schema 版本，纳入 InputDigest。
const summarySchemaVersion = 1

// plotTones 是情节点基调词表。收敛成枚举而非自由文本：节奏聚合要按基调计数与画曲线，
// 自由文本会让「紧张/紧绷/绷紧」变成三种基调，聚合层直接失去可比性。
var plotTones = []string{"紧张", "压抑", "温情", "爽快", "悬疑", "悲怆", "幽默", "平缓"}

// PlotTones 返回情节点基调词表副本，供契约与测试引用。
func PlotTones() []string { return append([]string(nil), plotTones...) }

func validPlotTone(v string) bool {
	for _, t := range plotTones {
		if t == v {
			return true
		}
	}
	return false
}

// PlotPoint 是一个情节点：编号 + 事件 + 基调。
// 基调随情节点结构化在同一对象内，「情节点条数 == 基调条数」由结构保证，
// 不像参考实现那样靠 grep ^P[0-9]+ 计数事后核对。
type PlotPoint struct {
	ID   string `json:"id"`   // P1、P2……按序
	Beat string `json:"beat"` // 情节点事件
	Tone string `json:"tone"` // 基调，取自 plotTones
}

// ChapterSummary 是单章拆解产物。
type ChapterSummary struct {
	Chapter        int         `json:"chapter"`
	Title          string      `json:"title"`
	Summary        string      `json:"summary"`
	PlotPoints     []PlotPoint `json:"plot_points"`
	KeyFacts       []string    `json:"key_facts"` // 正文明确揭示的事实（至少 1 条）
	Payoffs        []string    `json:"payoffs"`   // 爽点/情绪支付
	Hook           string      `json:"hook,omitempty"`
	HookType       string      `json:"hook_type"`
	DominantStrand string      `json:"dominant_strand"`
	Characters     []string    `json:"characters"`
	EmotionArc     string      `json:"emotion_arc"` // 本章情绪走向（起→落点）
	Techniques     []string    `json:"techniques"`  // 本章可见的写作手法
}

// SummaryBatchResult 是一次批次调用的结构化返回，每元素是一章摘要。
type SummaryBatchResult struct {
	Chapters []ChapterSummary `json:"chapters"`
}

// ChapterSummaryPayload 是单章摘要工件载荷；同批次章节记录相同 BatchStart/BatchEnd。
type ChapterSummaryPayload struct {
	BatchStart int            `json:"batch_start"`
	BatchEnd   int            `json:"batch_end"`
	Summary    ChapterSummary `json:"summary"`
}

// SummaryBudget 是逐章摘要的输入/输出双预算。
// 输入以字节近似 context window；输出以每章保守预留近似 completion 上限。
type SummaryBudget struct {
	ContextBytes     int // 输入预算（正文 + overhead）
	MaxOutputTokens  int // 可见输出预算（completion 上限）
	PerChapterOutput int // 每章保守输出预留
	PromptOverhead   int // system 固定输入开销（字节）
}

func chapterPath(chapter int) string {
	return fmt.Sprintf("%s/%06d.json", dirChapters, chapter)
}

// summarizedChapters 返回从第 1 章起连续、且 InputDigest 与当前边界身份/版本/正文匹配的摘要工件数。
// 缺失、解析失败或 digest 失配都在此截断，使上游变化（重切、改 prompt/schema 版本）自然失效下游摘要。
// 已记录持久失败的章不阻断连续计数——它由 FailedChapters 承担，否则管线会卡在同一章无限重试。
func summarizedChapters(l *Library, b *Boundaries, normalized []byte, boundIdentity, promptVersion string, failed map[int]bool) int {
	n := 0
	for c := 1; c <= len(b.Chapters); c++ {
		if failed[c] {
			n++
			continue
		}
		a, err := readArtifact[ChapterSummaryPayload](l, chapterPath(c))
		if err != nil {
			break
		}
		if a.InputDigest != chapterInputDigest(boundIdentity, promptVersion, b, normalized, c-1) {
			break
		}
		n++
	}
	return n
}

// summarizedChaptersStrict 与 summarizedChapters 新鲜度语义一致，但会暴露损坏或不可读的
// 既有工件。状态恢复使用严格版本，避免把真实读取错误当成「尚未摘要」后覆盖重做。
func summarizedChaptersStrict(l *Library, b *Boundaries, normalized []byte, boundIdentity, promptVersion string, failed map[int]bool) (int, error) {
	n := 0
	for c := 1; c <= len(b.Chapters); c++ {
		if failed[c] {
			n++
			continue
		}
		a, err := readArtifact[ChapterSummaryPayload](l, chapterPath(c))
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			return n, fmt.Errorf("读取第 %d 章摘要工件: %w", c, err)
		}
		if a.InputDigest != chapterInputDigest(boundIdentity, promptVersion, b, normalized, c-1) {
			break
		}
		n++
	}
	return n, nil
}

// discardSummariesAfter 删除章号 > keep 的逐章摘要工件，使「重摘某章即失效其后全部摘要」成立。
// 正常前向推进时 keep 之后本就无工件，为幂等无操作；仅在中途重摘（越过新鲜前缀）时清理陈旧尾部。
// 删除失败必须传播：吞掉会让陈旧尾部（逐章 digest 恒匹配）被当作新鲜前缀复用，聚合将消费新旧混拼的事实。
func discardSummariesAfter(l *Library, keep, total int) error {
	for c := keep + 1; c <= total; c++ {
		if err := os.Remove(l.path(chapterPath(c))); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("清理陈旧摘要工件 %s：%w", chapterPath(c), err)
		}
	}
	return nil
}

// loadSummaries 读取 1..count 章已落盘的摘要；缺口（持久失败章）以零值 Chapter=0 占位，
// 调用方据此区分「有摘要」与「该章失败」。
func loadSummaries(l *Library, count int) []ChapterSummary {
	out := make([]ChapterSummary, 0, count)
	for c := 1; c <= count; c++ {
		a, err := readArtifact[ChapterSummaryPayload](l, chapterPath(c))
		if err != nil {
			out = append(out, ChapterSummary{})
			continue
		}
		out = append(out, a.Payload.Summary)
	}
	return out
}

// chapterInputDigest 逐章绑定摘要工件身份：边界身份 + prompt/schema 版本 + 章号 + 单章正文。
// 逐章而非批次级绑定——批次划分是随模型能力变化的执行细节，不应让换模型后已摘要章节整体失效。
func chapterInputDigest(boundIdentity, promptVersion string, b *Boundaries, normalized []byte, i int) string {
	var sb strings.Builder
	sb.WriteString("summary\x00")
	sb.WriteString(promptVersion)
	fmt.Fprintf(&sb, "\x00v%d\x00", summarySchemaVersion)
	sb.WriteString(boundIdentity)
	fmt.Fprintf(&sb, "\x00ch%d\x00", b.Chapters[i].Number)
	sb.WriteString(b.Content(normalized, i))
	return Digest([]byte(sb.String()))
}

// validateSummaryBatch 分两层校验：批次级连续无缺无重，逐章级值域与引用。
// 参考实现把这些检查写在 prompt 里靠模型自觉（grep 计数、人工核对），这里全部机械执行。
func validateSummaryBatch(r *SummaryBatchResult, b *Boundaries, start, end int) error {
	want := end - start
	if len(r.Chapters) != want {
		return fmt.Errorf("批次章节数 %d != 预期 %d", len(r.Chapters), want)
	}
	for i, s := range r.Chapters {
		expect := b.Chapters[start+i]
		if s.Chapter != expect.Number {
			return fmt.Errorf("批次第 %d 项章号 %d != %d", i, s.Chapter, expect.Number)
		}
		if strings.TrimSpace(s.Summary) == "" {
			return fmt.Errorf("章 %d summary 不能为空", s.Chapter)
		}
		if len(s.PlotPoints) == 0 {
			return fmt.Errorf("章 %d 至少需要 1 个情节点", s.Chapter)
		}
		for j, p := range s.PlotPoints {
			wantID := fmt.Sprintf("P%d", j+1)
			if p.ID != wantID {
				return fmt.Errorf("章 %d 情节点[%d] id %q 应为 %q（按序编号，不跳号）", s.Chapter, j, p.ID, wantID)
			}
			if strings.TrimSpace(p.Beat) == "" {
				return fmt.Errorf("章 %d 情节点 %s 缺事件描述", s.Chapter, p.ID)
			}
			if !validPlotTone(p.Tone) {
				return fmt.Errorf("章 %d 情节点 %s 基调 %q 非法（只能是：%s）",
					s.Chapter, p.ID, p.Tone, strings.Join(plotTones, "、"))
			}
		}
		if len(nonEmpty(s.KeyFacts)) == 0 {
			return fmt.Errorf("章 %d 至少需要 1 条关键信息（key_facts）", s.Chapter)
		}
		if !domain.ValidHookType(strings.ToLower(s.HookType)) {
			return fmt.Errorf("章 %d hook_type 非法：%q", s.Chapter, s.HookType)
		}
		if !domain.ValidDominantStrand(strings.ToLower(s.DominantStrand)) {
			return fmt.Errorf("章 %d dominant_strand 非法：%q", s.Chapter, s.DominantStrand)
		}
		// 枚举按小写校验就按小写落盘：下游节奏/情绪聚合按精确串消费，大小写变体会被当成未知类型。
		r.Chapters[i].HookType = strings.ToLower(s.HookType)
		r.Chapters[i].DominantStrand = strings.ToLower(s.DominantStrand)
		// 标题以边界表为准：标题是坐标事实（已在边界阶段做过原文回显校验），
		// 让模型在这里复述一遍只会引入漂移。
		r.Chapters[i].Title = expect.Title
	}
	return nil
}

func nonEmpty(xs []string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			out = append(out, x)
		}
	}
	return out
}

// planBatch 从 start 章起，按输入/输出双预算返回连续批次终点 end（[start,end)，章索引 0 起）。
// 至少 1 章；单章即便超预算也单独成批，由执行方在截断时报告容量不足。
func planBatch(chapters []ChapterSpan, start int, b SummaryBudget) int {
	end := start + 1
	if b.ContextBytes <= 0 || b.MaxOutputTokens <= 0 || b.PerChapterOutput <= 0 {
		return end // 预算未配置：逐章
	}
	inAcc := b.PromptOverhead + chapterBytes(chapters, start)
	outAcc := b.PerChapterOutput
	for end < len(chapters) {
		cb := chapterBytes(chapters, end)
		if inAcc+cb > b.ContextBytes {
			break
		}
		if outAcc+b.PerChapterOutput > b.MaxOutputTokens {
			break
		}
		inAcc += cb
		outAcc += b.PerChapterOutput
		end++
	}
	return end
}

func chapterBytes(chapters []ChapterSpan, i int) int {
	return chapters[i].End - chapters[i].Start
}

// SummarizeNext 从第一份缺失摘要起组一个批次并原子落盘，返回本次提交的章节数。
// 截断即「打捞前缀 + 缩小重组批」；批次已缩到单章仍截断则显式报告容量不足。
func SummarizeNext(ctx context.Context, m callModel, systemPrompt string, l *Library, normalized []byte, b *Boundaries, boundIdentity, promptVersion string, start, end int, budget SummaryBudget, prof callProfile) (int, error) {
	for {
		payload := buildSummaryPayload(normalized, b, start, end)
		res, err := callStructured[SummaryBatchResult](ctx, m, summaryContract, systemPrompt, payload, budget.MaxOutputTokens, prof, func(r *SummaryBatchResult) error {
			return validateSummaryBatch(r, b, start, end)
		})
		if err != nil {
			var tr *errTruncated
			if errors.As(err, &tr) {
				// 截断优先打捞从批次首章起的最大连续合法前缀，已完成部分不重做。
				if salvaged := salvagePrefix(tr.Raw, b, start); len(salvaged) > 0 {
					for i, s := range salvaged {
						ch := start + i + 1
						digest := chapterInputDigest(boundIdentity, promptVersion, b, normalized, start+i)
						art := ChapterSummaryPayload{BatchStart: start + 1, BatchEnd: end, Summary: s}
						if werr := writeArtifact(l, chapterPath(ch), digest, art); werr != nil {
							return i, fmt.Errorf("落盘打捞章 %d：%w", ch, werr)
						}
						l.clearChapterFailure(ch)
					}
					l.writeFailure(FailureMeta{Stage: "summarize", Detail: fmt.Sprintf("批次 %d-%d 长度截断", start+1, end),
						StopReason: "length", PrefixSalvage: fmt.Sprintf("available:%d", len(salvaged))}, tr.Raw)
					prof.logger().Info("rip 摘要截断，打捞连续前缀", "batch_start", start+1, "salvaged", len(salvaged))
					echoChapterSummaries(prof, salvaged)
					return len(salvaged), nil
				}
				l.writeFailure(FailureMeta{Stage: "summarize", Detail: fmt.Sprintf("批次 %d-%d 长度截断，无可打捞前缀", start+1, end),
					StopReason: "length", PrefixSalvage: "unavailable"}, tr.Raw)
				if end-start > 1 {
					prof.logger().Warn("rip 摘要截断，缩小重组批", "batch", fmt.Sprintf("%d-%d", start+1, end), "prefix_salvage", "unavailable")
					end = start + (end-start)/2
					prof.step(0, 0, "输出被长度截断且无可打捞前缀，缩小批次为第 %d-%d 章重试", start+1, end)
					continue
				}
				return 0, fmt.Errorf("章 %d 单章批次仍被长度截断，模型可见输出能力不足", start+1)
			}
			return 0, err
		}
		for i, s := range res.Chapters {
			ch := start + i + 1
			digest := chapterInputDigest(boundIdentity, promptVersion, b, normalized, start+i)
			art := ChapterSummaryPayload{BatchStart: start + 1, BatchEnd: end, Summary: s}
			if err := writeArtifact(l, chapterPath(ch), digest, art); err != nil {
				return i, fmt.Errorf("落盘章 %d 摘要：%w", ch, err)
			}
			l.clearChapterFailure(ch)
		}
		echoChapterSummaries(prof, res.Chapters)
		return end - start, nil
	}
}

// echoChapterSummaries 把模型对每章的核心理解回显到面板——用户应看见模型读懂了什么，
// 而非只有机械的批次计数。
func echoChapterSummaries(prof callProfile, summaries []ChapterSummary) {
	for _, s := range summaries {
		beat := ""
		if len(s.PlotPoints) > 0 {
			beat = s.PlotPoints[0].Beat
		}
		prof.step(0, 0, "第 %d 章〈%s〉：%d 个情节点，%s", s.Chapter, snippet(s.Title, 24), len(s.PlotPoints), snippet(beat, 48))
	}
}

// buildSummaryPayload 组装批次输入：连续章节原文。
// 逐章摘要不带前序 ledger：拆文是只读分析，每章的情节点/基调/手法都是本章内可判定的事实，
// 不需要跨章连续性推断——省下的输入预算直接换成更大的批次。
func buildSummaryPayload(normalized []byte, b *Boundaries, start, end int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "请拆解第 %d-%d 章，返回 {\"chapters\":[每章一个对象]}，数组顺序与章号一致。\n\n", start+1, end)
	for i := start; i < end; i++ {
		c := b.Chapters[i]
		fmt.Fprintf(&sb, "## 第 %d 章：%s\n\n", c.Number, c.Title)
		sb.WriteString(b.Content(normalized, i))
		sb.WriteString("\n\n---\n\n")
	}
	return sb.String()
}

// salvagePrefix 从长度截断的批次响应中解析最大连续合法前缀。
// 只保存从批次首章起连续、逐章校验通过的对象；遇首个不完整/非法/跳号即停，之后字节不解释。
func salvagePrefix(raw string, b *Boundaries, start int) []ChapterSummary {
	arr := extractChaptersArray(raw)
	if arr == "" {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(arr))
	if _, err := dec.Token(); err != nil { // 消费 '['
		return nil
	}
	var out []ChapterSummary
	for dec.More() {
		var s ChapterSummary
		if err := dec.Decode(&s); err != nil {
			break // 首个不完整对象，停止
		}
		idx := start + len(out)
		if idx >= len(b.Chapters) || s.Chapter != b.Chapters[idx].Number {
			break // 跳号/越界
		}
		one := SummaryBatchResult{Chapters: []ChapterSummary{s}}
		if err := validateSummaryBatch(&one, b, idx, idx+1); err != nil {
			break
		}
		out = append(out, one.Chapters[0]) // validateSummaryBatch 已就地归一化，取校验后的值
	}
	return out
}

// extractChaptersArray 截取 "chapters" 后的 JSON 数组文本（可被尾部截断）。
func extractChaptersArray(raw string) string {
	i := strings.Index(raw, "\"chapters\"")
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(raw[i:], '[')
	if j < 0 {
		return ""
	}
	return raw[i+j:]
}
