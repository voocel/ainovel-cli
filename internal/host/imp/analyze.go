package imp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"maps"
	"os"
	"slices"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// analysisSchemaVersion 是逐章事实 schema 版本，纳入 InputDigest。
const analysisSchemaVersion = 1

// validHookTypes / validStrands 与 commit_chapter schema 保持一致。
var (
	validHookTypes = map[string]bool{"crisis": true, "mystery": true, "desire": true, "emotion": true, "choice": true}
	validStrands   = map[string]bool{"quest": true, "fire": true, "constellation": true}
)

// ImportedCharacterFact / ImportedWorldFact 是用于全书综合的紧凑观察，不直接写正式角色或世界规则。
// 至少携带章节号，使综合结果有稳定来源（RFC §9.1）。
type ImportedCharacterFact struct {
	Chapter int    `json:"chapter"`
	Name    string `json:"name"`
	Note    string `json:"note,omitempty"`
}

type ImportedWorldFact struct {
	Chapter  int    `json:"chapter"`
	Category string `json:"category,omitempty"`
	Fact     string `json:"fact"`
}

// ImportedChapterFacts 是单章反推的结构化产物（RFC §9.1）。
type ImportedChapterFacts struct {
	Chapter             int                        `json:"chapter"`
	Title               string                     `json:"title"`
	Summary             string                     `json:"summary"`
	KeyEvents           []string                   `json:"key_events"`
	CoreEvent           string                     `json:"core_event"`
	Hook                string                     `json:"hook,omitempty"`
	Scenes              []string                   `json:"scenes,omitempty"`
	Characters          []string                   `json:"characters,omitempty"`
	CharacterEvidence   []ImportedCharacterFact    `json:"character_evidence,omitempty"`
	WorldEvidence       []ImportedWorldFact        `json:"world_evidence,omitempty"`
	TimelineEvents      []domain.TimelineEvent     `json:"timeline_events,omitempty"`
	ForeshadowUpdates   []domain.ForeshadowUpdate  `json:"foreshadow_updates,omitempty"`
	RelationshipChanges []domain.RelationshipEntry `json:"relationship_changes,omitempty"`
	StateChanges        []domain.StateChange       `json:"state_changes,omitempty"`
	HookType            string                     `json:"hook_type"`
	DominantStrand      string                     `json:"dominant_strand"`
}

// AnalysisBatchResult 是一次批次调用的结构化返回，每元素是一章事实。
type AnalysisBatchResult struct {
	Chapters []ImportedChapterFacts `json:"chapters"`
}

// ChapterAnalysisPayload 是单章分析工件载荷；同批次章节记录相同 BatchStart/BatchEnd。
type ChapterAnalysisPayload struct {
	BatchStart int                  `json:"batch_start"`
	BatchEnd   int                  `json:"batch_end"`
	Facts      ImportedChapterFacts `json:"facts"`
}

// AnalyzeBudget 是逐章分析的输入/输出双预算（RFC §9.2）。
// 输入以字节近似 context window；输出以每章保守事实预留近似 completion 上限。
type AnalyzeBudget struct {
	ContextBytes     int // 输入预算（正文 + ledger + overhead）
	MaxOutputTokens  int // 可见输出预算（completion 上限）
	PerChapterOutput int // 每章保守输出预留
	PromptOverhead   int // system/ledger 固定输入开销（字节）
}

func analysisPath(chapter int) string {
	return fmt.Sprintf("%s/%06d.json", dirAnalyses, chapter)
}

// analyzedChapters 返回从第 1 章起连续、且 InputDigest 与当前切分身份/版本/正文匹配的分析工件数（RFC §9.6）。
// 缺失、解析失败或 digest 失配都在此截断，使上游变化（重切、改 prompt/schema 版本）自然失效下游分析。
func analyzedChapters(w *Workspace, seg *Segmentation, normalized []byte, segIdentity, promptVersion string) int {
	n := 0
	for c := 1; c <= len(seg.Chapters); c++ {
		a, err := readArtifact[ChapterAnalysisPayload](w, analysisPath(c))
		if err != nil {
			break
		}
		if a.InputDigest != chapterInputDigest(segIdentity, promptVersion, seg, normalized, c-1) {
			break
		}
		n++
	}
	return n
}

// discardAnalysesAfter 删除章号 > keep 的逐章分析工件，使"重分析某章即失效其后全部分析"成立（#4a）。
// 正常前向分析时 keep 之后本就无工件，为幂等无操作；仅在中途重分析（越过新鲜前缀）时清理陈旧尾部。
// 删除失败必须传播：这是该不变量的唯一执行点，吞掉错误会让陈旧尾部（逐章 digest 恒匹配）
// 被当作新鲜前缀复用，综合将消费新旧混拼的事实且无任何报错。
func discardAnalysesAfter(w *Workspace, keep, total int) error {
	for c := keep + 1; c <= total; c++ {
		if err := os.Remove(w.path(analysisPath(c))); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("清理陈旧分析工件 %s：%w", analysisPath(c), err)
		}
	}
	return nil
}

// loadPriorFacts 读取 1..count 章已落盘的事实，供 ledger 构造。
func loadPriorFacts(w *Workspace, count int) []ImportedChapterFacts {
	var out []ImportedChapterFacts
	for c := 1; c <= count; c++ {
		a, err := readArtifact[ChapterAnalysisPayload](w, analysisPath(c))
		if err != nil {
			break
		}
		out = append(out, a.Payload.Facts)
	}
	return out
}

// buildLedger 从已分析章节派生紧凑连续性上下文：人物别名 + 活跃伏笔 ID + 最近状态。
func buildLedger(prior []ImportedChapterFacts) string {
	if len(prior) == 0 {
		return ""
	}
	names := map[string]bool{}
	active := map[string]string{} // foreshadow id -> desc
	var recent []string
	for _, f := range prior {
		for _, c := range f.Characters {
			names[c] = true
		}
		for _, fu := range f.ForeshadowUpdates {
			switch fu.Action {
			case "plant", "advance":
				if fu.Description != "" {
					active[fu.ID] = fu.Description
				} else if _, ok := active[fu.ID]; !ok {
					active[fu.ID] = ""
				}
			case "resolve":
				delete(active, fu.ID)
			}
		}
	}
	if len(prior) > 0 {
		last := prior[len(prior)-1]
		for _, sc := range last.StateChanges {
			recent = append(recent, fmt.Sprintf("%s.%s=%s", sc.Entity, sc.Field, sc.NewValue))
		}
	}
	var b strings.Builder
	if len(names) > 0 {
		b.WriteString("已知人物：")
		b.WriteString(strings.Join(slices.Sorted(maps.Keys(names)), "、"))
		b.WriteString("\n")
	}
	if len(active) > 0 {
		b.WriteString("活跃伏笔（复用 ID，勿新造）：\n")
		for _, id := range slices.Sorted(maps.Keys(active)) {
			fmt.Fprintf(&b, "- %s：%s\n", id, active[id])
		}
	}
	if len(recent) > 0 {
		b.WriteString("最近状态：")
		b.WriteString(strings.Join(recent, "；"))
		b.WriteString("\n")
	}
	return b.String()
}

// planBatch 从 start 章起，按输入/输出双预算返回连续批次终点 end（[start,end)，章索引 0 起）。
// 至少 1 章；单章即便超预算也单独成批，由执行方在截断时报告容量不足（RFC §9.2）。
func planBatch(chapters []ChapterSpan, start, ledgerBytes int, b AnalyzeBudget) int {
	end := start + 1
	if b.ContextBytes <= 0 || b.MaxOutputTokens <= 0 || b.PerChapterOutput <= 0 {
		return end // 预算未配置：逐章
	}
	inAcc := ledgerBytes + b.PromptOverhead + chapterBytes(chapters, start)
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

// chapterInputDigest 逐章绑定分析工件身份：切分身份 + prompt/schema 版本 + 章号 + 单章正文。
// 逐章而非批次级绑定——批次划分是随模型能力变化的执行细节，不应让换模型后已分析章节整体失效；
// 绑定 segIdentity（segmentation 工件的 InputDigest）确保重切后所有分析自然失配（RFC §9.1/§6.3）。
func chapterInputDigest(segIdentity, promptVersion string, seg *Segmentation, normalized []byte, i int) string {
	var b strings.Builder
	b.WriteString("analyze\x00")
	b.WriteString(promptVersion)
	fmt.Fprintf(&b, "\x00v%d\x00", analysisSchemaVersion)
	b.WriteString(segIdentity)
	fmt.Fprintf(&b, "\x00ch%d\x00", seg.Chapters[i].Number)
	b.WriteString(seg.Content(normalized, i))
	return Digest([]byte(b.String()))
}

// validateBatch 分两层校验：批次级连续无缺无重，逐章级值域与引用（RFC §9.4）。
func validateBatch(r *AnalysisBatchResult, seg *Segmentation, start, end int) error {
	want := end - start
	if len(r.Chapters) != want {
		return fmt.Errorf("批次章节数 %d != 预期 %d", len(r.Chapters), want)
	}
	for i, f := range r.Chapters {
		want := seg.Chapters[start+i]
		if f.Chapter != want.Number {
			return fmt.Errorf("批次第 %d 项章号 %d != %d", i, f.Chapter, want.Number)
		}
		if strings.TrimSpace(f.Summary) == "" || strings.TrimSpace(f.CoreEvent) == "" {
			return fmt.Errorf("章 %d summary/core_event 不能为空", f.Chapter)
		}
		if !validHookTypes[strings.ToLower(f.HookType)] {
			return fmt.Errorf("章 %d hook_type 非法：%q", f.Chapter, f.HookType)
		}
		if !validStrands[strings.ToLower(f.DominantStrand)] {
			return fmt.Errorf("章 %d dominant_strand 非法：%q", f.Chapter, f.DominantStrand)
		}
		for j, fu := range f.ForeshadowUpdates {
			if fu.Action == "plant" && strings.TrimSpace(fu.Description) == "" {
				return fmt.Errorf("章 %d foreshadow[%d] plant 需 description", f.Chapter, j)
			}
		}
		// 枚举按小写校验就按小写落盘：commit_chapter 不复验枚举，大小写变体会直通正式状态
		//（HookHistory 等按精确串消费，变体被视为未知类型），校验通过即归一化。
		r.Chapters[i].HookType = strings.ToLower(f.HookType)
		r.Chapters[i].DominantStrand = strings.ToLower(f.DominantStrand)
	}
	return nil
}

// AnalyzeNext 从第一份缺失分析起组一个批次并原子落盘，返回本次提交的章节数。
// 截断即「失败 + 缩小重组批」（默认，§9.5）；批次已缩到单章仍截断则显式报告容量不足。
func AnalyzeNext(ctx context.Context, m callModel, systemPrompt string, w *Workspace, normalized []byte, seg *Segmentation, segIdentity, promptVersion string, budget AnalyzeBudget, prof callProfile) (int, error) {
	total := len(seg.Chapters)
	start := analyzedChapters(w, seg, normalized, segIdentity, promptVersion)
	if start >= total {
		return 0, nil
	}
	ledger := buildLedger(loadPriorFacts(w, start))
	end := planBatch(seg.Chapters, start, len(ledger), budget)

	for {
		payload := buildAnalyzePayload(normalized, seg, ledger, start, end)
		res, err := callStructured[AnalysisBatchResult](ctx, m, systemPrompt, payload, budget.MaxOutputTokens, prof, func(r *AnalysisBatchResult) error {
			return validateBatch(r, seg, start, end)
		})
		if err != nil {
			var tr *errTruncated
			if errors.As(err, &tr) {
				// 截断优先打捞从批次首章起的最大连续合法前缀，已提交部分不重做（§9.5）。
				if salvaged := salvagePrefix(tr.Raw, seg, start); len(salvaged) > 0 {
					for i, f := range salvaged {
						ch := start + i + 1
						digest := chapterInputDigest(segIdentity, promptVersion, seg, normalized, start+i)
						art := ChapterAnalysisPayload{BatchStart: start + 1, BatchEnd: end, Facts: f}
						if werr := writeArtifact(w, analysisPath(ch), digest, art); werr != nil {
							return i, fmt.Errorf("落盘打捞章 %d：%w", ch, werr)
						}
					}
					w.writeFailure(FailureMeta{Stage: "analyze", Detail: fmt.Sprintf("批次 %d-%d 长度截断", start+1, end),
						StopReason: "length", PrefixSalvage: fmt.Sprintf("available:%d", len(salvaged))}, tr.Raw)
					prof.logger().Info("imp 分析截断，打捞连续前缀", "batch_start", start+1, "salvaged", len(salvaged))
					echoChapterFacts(prof, salvaged)
					return len(salvaged), nil
				}
				// 无可打捞前缀：记录不可用并「失败 + 缩小重组批」，单章仍截断则报容量不足。
				w.writeFailure(FailureMeta{Stage: "analyze", Detail: fmt.Sprintf("批次 %d-%d 长度截断，无可打捞前缀", start+1, end),
					StopReason: "length", PrefixSalvage: "unavailable"}, tr.Raw)
				if end-start > 1 {
					prof.logger().Warn("imp 分析截断，缩小重组批", "batch", fmt.Sprintf("%d-%d", start+1, end), "prefix_salvage", "unavailable")
					end = start + (end-start)/2
					// 无 Key 的进度行：既让用户看见缩批动作，也隔断前后两次独立调用的
					// 退避行按同 Key 误合并（Key 契约只覆盖同一调用内的瞬态退避）。
					prof.step(0, 0, "输出被长度截断且无可打捞前缀，缩小批次为第 %d-%d 章重试", start+1, end)
					continue
				}
				return 0, fmt.Errorf("章 %d 单章批次仍被长度截断，模型可见输出能力不足", start+1)
			}
			return 0, err
		}
		for i, f := range res.Chapters {
			ch := start + i + 1
			digest := chapterInputDigest(segIdentity, promptVersion, seg, normalized, start+i)
			payloadArt := ChapterAnalysisPayload{BatchStart: start + 1, BatchEnd: end, Facts: f}
			if err := writeArtifact(w, analysisPath(ch), digest, payloadArt); err != nil {
				return i, fmt.Errorf("落盘章 %d 分析：%w", ch, err)
			}
		}
		echoChapterFacts(prof, res.Chapters)
		return end - start, nil
	}
}

// echoChapterFacts 把模型对每章的核心理解回显到面板——用户应看见模型读懂了什么，
// 而非只有机械的批次计数（§14.1）。
func echoChapterFacts(prof callProfile, facts []ImportedChapterFacts) {
	for _, f := range facts {
		prof.step(0, 0, "第 %d 章〈%s〉：%s", f.Chapter, snippet(f.Title, 24), snippet(f.CoreEvent, 60))
	}
}

// buildAnalyzePayload 组装批次输入：连续章节原文 + 批次前 ledger。
func buildAnalyzePayload(normalized []byte, seg *Segmentation, ledger string, start, end int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "请分析第 %d-%d 章，返回 {\"chapters\":[每章一个事实对象]}，数组顺序与章号一致。\n\n", start+1, end)
	if ledger != "" {
		b.WriteString("## 连续性 ledger（参考）\n\n")
		b.WriteString(ledger)
		b.WriteString("\n")
	}
	for i := start; i < end; i++ {
		c := seg.Chapters[i]
		fmt.Fprintf(&b, "## 第 %d 章：%s\n\n", c.Number, c.Title)
		b.WriteString(seg.Content(normalized, i))
		b.WriteString("\n\n---\n\n")
	}
	return b.String()
}

// salvagePrefix 从长度截断的批次响应中解析最大连续合法前缀（RFC §9.5）。
// 只保存从批次首章起连续、逐章校验通过的对象；遇首个不完整/非法/跳号即停，之后字节不解释。
// 纯函数，由 AnalyzeNext 在容量截断时优先调用，避免丢弃已完整生成的前缀章节。
func salvagePrefix(raw string, seg *Segmentation, start int) []ImportedChapterFacts {
	arr := extractChaptersArray(raw)
	if arr == "" {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(arr))
	if _, err := dec.Token(); err != nil { // 消费 '['
		return nil
	}
	var out []ImportedChapterFacts
	for dec.More() {
		var f ImportedChapterFacts
		if err := dec.Decode(&f); err != nil {
			break // 首个不完整对象，停止
		}
		idx := start + len(out)
		if idx >= len(seg.Chapters) || f.Chapter != seg.Chapters[idx].Number {
			break // 跳号/越界
		}
		one := AnalysisBatchResult{Chapters: []ImportedChapterFacts{f}}
		if err := validateBatch(&one, seg, idx, idx+1); err != nil {
			break
		}
		out = append(out, one.Chapters[0]) // validateBatch 已就地归一化枚举，取校验后的值
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
