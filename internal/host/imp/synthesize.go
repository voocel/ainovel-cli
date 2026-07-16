package imp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// 故事状态闭集（RFC §10.4）。
const (
	storyOpen      = "open"
	storyClosed    = "closed"
	storyUncertain = "uncertain"
)

// synthesisSchemaVersion 纳入 RangeDigest / synthesis InputDigest，升级综合契约时递增以失效已落盘工件。
// synthesizePromptVersion 纳入 synthesis InputDigest，改综合 prompt 时递增，否则旧 synthesis 仍被误判有效。
const (
	synthesisSchemaVersion  = 1
	synthesizePromptVersion = "synthesize-v1"
	rangePromptVersion      = "range-v1" // 纳入 rangeInputDigest，改 Range prompt 时递增，否则旧区间摘要仍被误判有效
)

// ImportedArcRange / ImportedVolumeRange：综合只返回卷弧范围，不重复输出所有章节（RFC §10.3）。
type ImportedArcRange struct {
	Title        string `json:"title"`
	Goal         string `json:"goal"`
	StartChapter int    `json:"start_chapter"`
	EndChapter   int    `json:"end_chapter"`
}

type ImportedVolumeRange struct {
	Title string             `json:"title"`
	Theme string             `json:"theme"`
	Arcs  []ImportedArcRange `json:"arcs"`
}

// BookSynthesis 是最终综合结果：全局事实 + 卷弧范围（RFC §10.3）。
type BookSynthesis struct {
	Premise      string                `json:"premise"`
	Characters   []domain.Character    `json:"characters"`
	WorldRules   []domain.WorldRule    `json:"world_rules"`
	Structure    []ImportedVolumeRange `json:"structure"`
	Compass      domain.StoryCompass   `json:"compass"`
	PlanningTier domain.PlanningTier   `json:"planning_tier"`
	StoryStatus  string                `json:"story_status"`
	StatusReason string                `json:"status_reason,omitempty"`
}

// RangeDigest 是长书 Map 阶段的连续区间摘要，输出受单区间约束（RFC §10.2）。
type RangeDigest struct {
	StartChapter    int      `json:"start_chapter"`
	EndChapter      int      `json:"end_chapter"`
	Plot            string   `json:"plot"`
	Characters      []string `json:"characters,omitempty"`
	WorldFacts      []string `json:"world_facts,omitempty"`
	OpenedThreads   []string `json:"opened_threads,omitempty"`
	ResolvedThreads []string `json:"resolved_threads,omitempty"`
}

var validPlanningTiers = map[domain.PlanningTier]bool{
	domain.PlanningTierShort: true,
	domain.PlanningTierMid:   true,
	domain.PlanningTierLong:  true,
}

// planFactRanges 按字节预算把逐章事实分连续区间；短书一次容纳则单区间直接综合（RFC §10.2）。
func planFactRanges(facts []ImportedChapterFacts, budgetBytes int) [][2]int {
	if len(facts) == 0 {
		return nil
	}
	if budgetBytes <= 0 {
		return [][2]int{{0, len(facts)}}
	}
	var ranges [][2]int
	start, acc := 0, 0
	for i, f := range facts {
		size := len(compactFact(f))
		if i > start && acc+size > budgetBytes {
			ranges = append(ranges, [2]int{start, i})
			start, acc = i, 0
		}
		acc += size
	}
	ranges = append(ranges, [2]int{start, len(facts)})
	return ranges
}

// compactView 是送入综合的紧凑视图：保留跨章归纳需要的字段，不含全文。
// character/world evidence 是逐章反推时专为全书综合提取的观察，必须带进来——
// 否则综合器只能从摘要臆造正式角色与世界规则，白白浪费已提取的证据（RFC §9.1/§10）。
type compactView struct {
	Chapter           int                     `json:"chapter"`
	Title             string                  `json:"title"`
	CoreEvent         string                  `json:"core_event"`
	Summary           string                  `json:"summary"`
	Characters        []string                `json:"characters,omitempty"`
	CharacterEvidence []ImportedCharacterFact `json:"character_evidence,omitempty"`
	WorldEvidence     []ImportedWorldFact     `json:"world_evidence,omitempty"`
}

func toCompact(f ImportedChapterFacts) compactView {
	return compactView{
		Chapter:           f.Chapter,
		Title:             f.Title,
		CoreEvent:         f.CoreEvent,
		Summary:           f.Summary,
		Characters:        f.Characters,
		CharacterEvidence: f.CharacterEvidence,
		WorldEvidence:     f.WorldEvidence,
	}
}

func compactFact(f ImportedChapterFacts) string {
	data, _ := json.Marshal(toCompact(f))
	return string(data)
}

func compactFacts(facts []ImportedChapterFacts) string {
	views := make([]compactView, len(facts))
	for i, f := range facts {
		views[i] = toCompact(f)
	}
	data, _ := json.Marshal(views)
	return string(data)
}

// Synthesize 分层综合：短书直接出 BookSynthesis；长书先出 RangeDigest 再归并（RFC §10）。
// bookPrompt 描述 BookSynthesis 契约，rangePrompt 描述 RangeDigest 契约——两阶段输出结构不同，
// 必须各用对应系统提示词，否则模型收到 BookSynthesis 指令却被要求 RangeDigest，指令自相矛盾。
func Synthesize(ctx context.Context, m callModel, bookPrompt, rangePrompt string, w *Workspace, facts []ImportedChapterFacts, budgetBytes, maxTokens int, prof callProfile) (*BookSynthesis, error) {
	ranges := planFactRanges(facts, budgetBytes)
	if len(ranges) <= 1 {
		return synthesizeBook(ctx, m, bookPrompt, compactFacts(facts), len(facts), maxTokens, prof)
	}
	digests := make([]RangeDigest, 0, len(ranges))
	for ri, r := range ranges {
		rangeFacts := facts[r[0]:r[1]]
		startCh, endCh := rangeFacts[0].Chapter, rangeFacts[len(rangeFacts)-1].Chapter
		want := rangeInputDigest(rangeFacts)
		rel := rangeDigestPath(startCh, endCh)
		// InputDigest 匹配的已落盘区间摘要直接复用，长书任一区间崩溃后不重复收费（RFC §6/§10.2）。
		if art, err := readArtifact[RangeDigest](w, rel); err == nil && art.InputDigest == want {
			digests = append(digests, art.Payload)
			continue
		}
		prof.step(ri+1, len(ranges), "区间摘要 %d/%d（第 %d-%d 章）...", ri+1, len(ranges), startCh, endCh)
		rd, err := callStructured[RangeDigest](ctx, m, rangePrompt, buildRangePayload(rangeFacts), maxTokens, prof, func(d *RangeDigest) error {
			if strings.TrimSpace(d.Plot) == "" {
				return fmt.Errorf("range digest plot 为空")
			}
			// 区间边界必须与请求一致，否则归并时会把错位区间当作本区间摘要（RFC §10.2）。
			if d.StartChapter != startCh || d.EndChapter != endCh {
				return fmt.Errorf("range digest 章范围 %d-%d 与请求 %d-%d 不符", d.StartChapter, d.EndChapter, startCh, endCh)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("range %d-%d 综合：%w", startCh, endCh, err)
		}
		if err := writeArtifact(w, rel, want, rd); err != nil {
			return nil, fmt.Errorf("落盘 range digest：%w", err)
		}
		digests = append(digests, rd)
	}
	// 递归 Reduce：区间摘要总量仍可能超过最终综合输入预算（把 #83 从"全部章节"推迟到"全部区间摘要"）。
	// 逐层归并到可容纳，才真正无界扩展（RFC §10.2）。
	digests, err := reduceToFit(ctx, m, rangePrompt, digests, budgetBytes, maxTokens, prof)
	if err != nil {
		return nil, err
	}
	data, _ := json.Marshal(digests)
	return synthesizeBook(ctx, m, bookPrompt, string(data), len(facts), maxTokens, prof)
}

// reduceToFit 反复把连续区间摘要按预算分组归并，直到序列化后可容纳最终 BookSynthesis 输入预算。
// 每轮严格减少摘要数量，故必然收敛；单个摘要即便超预算也不再拆（下层已是最小语义单元），
// 交最终调用，若因此截断由 callStructured 显式报错而非静默溢出。
func reduceToFit(ctx context.Context, m callModel, rangePrompt string, digests []RangeDigest, budgetBytes, maxTokens int, prof callProfile) ([]RangeDigest, error) {
	round := 0
	for len(digests) > 1 {
		if budgetBytes <= 0 {
			return digests, nil
		}
		data, _ := json.Marshal(digests)
		if len(data) <= budgetBytes {
			return digests, nil
		}
		groups := groupDigestsByBudget(digests, budgetBytes)
		if len(groups) >= len(digests) {
			return digests, nil // 无法再合并（每组仅一个摘要）
		}
		round++
		merged := make([]RangeDigest, 0, len(groups))
		for gi, g := range groups {
			startCh, endCh := g[0].StartChapter, g[len(g)-1].EndChapter
			prof.step(gi+1, len(groups), "归并区间摘要（第 %d 轮 %d/%d，第 %d-%d 章）...",
				round, gi+1, len(groups), startCh, endCh)
			rd, err := callStructured[RangeDigest](ctx, m, rangePrompt, buildDigestReducePayload(g), maxTokens, prof, func(d *RangeDigest) error {
				if strings.TrimSpace(d.Plot) == "" {
					return fmt.Errorf("合并区间 plot 为空")
				}
				if d.StartChapter != startCh || d.EndChapter != endCh {
					return fmt.Errorf("合并区间范围 %d-%d 与请求 %d-%d 不符", d.StartChapter, d.EndChapter, startCh, endCh)
				}
				return nil
			})
			if err != nil {
				return nil, fmt.Errorf("合并区间 %d-%d：%w", startCh, endCh, err)
			}
			merged = append(merged, rd)
		}
		digests = merged
	}
	return digests, nil
}

// groupDigestsByBudget 把连续区间摘要按字节预算分成连续分组；单个摘要即便超预算也单独成组。
func groupDigestsByBudget(digests []RangeDigest, budgetBytes int) [][]RangeDigest {
	var groups [][]RangeDigest
	var cur []RangeDigest
	acc := 0
	for _, d := range digests {
		b, _ := json.Marshal(d)
		if len(cur) > 0 && acc+len(b) > budgetBytes {
			groups = append(groups, cur)
			cur, acc = nil, 0
		}
		cur = append(cur, d)
		acc += len(b)
	}
	if len(cur) > 0 {
		groups = append(groups, cur)
	}
	return groups
}

// buildDigestReducePayload 组装"把若干下层区间摘要合并为一个 RangeDigest"的输入。
func buildDigestReducePayload(digests []RangeDigest) string {
	data, _ := json.Marshal(digests)
	return fmt.Sprintf("请把第 %d-%d 章的多个下层区间摘要合并为一个 RangeDigest（连续区间摘要）。下层摘要：\n%s",
		digests[0].StartChapter, digests[len(digests)-1].EndChapter, string(data))
}

// rangeDigestPath 返回连续区间摘要工件相对路径。
func rangeDigestPath(startChapter, endChapter int) string {
	return fmt.Sprintf("%s/%06d-%06d.json", dirRangeDigests, startChapter, endChapter)
}

// rangeInputDigest 绑定该连续区间的紧凑事实与 Range prompt/schema 版本（RFC §6.3）。
func rangeInputDigest(facts []ImportedChapterFacts) string {
	return Digest([]byte(fmt.Sprintf("range\x00%s\x00v%d\x00%s", rangePromptVersion, synthesisSchemaVersion, compactFacts(facts))))
}

func synthesizeBook(ctx context.Context, m callModel, systemPrompt, payload string, n, maxTokens int, prof callProfile) (*BookSynthesis, error) {
	prof.step(0, 0, "生成全书综合（premise/characters/大纲结构）...")
	s, err := callStructured[BookSynthesis](ctx, m, systemPrompt, buildBookPayload(payload, n), maxTokens, prof, func(s *BookSynthesis) error {
		return validateSynthesis(s, n)
	})
	if err != nil {
		return nil, err
	}
	// 回显模型的全书理解：这是导入最核心的语义产出，值得让用户第一时间看见。
	prof.step(0, 0, "模型概括全书：%s", snippet(s.Premise, 80))
	return &s, nil
}

func buildRangePayload(facts []ImportedChapterFacts) string {
	return fmt.Sprintf("请为第 %d-%d 章生成一个 RangeDigest（连续区间摘要）。逐章事实：\n%s",
		facts[0].Chapter, facts[len(facts)-1].Chapter, compactFacts(facts))
}

func buildBookPayload(inner string, n int) string {
	return fmt.Sprintf("以下是全书 %d 章的紧凑事实/区间摘要。请生成 BookSynthesis：premise、characters、world_rules、卷弧范围 structure、compass、planning_tier、story_status。\n\n%s", n, inner)
}

// validateSynthesis 校验综合结果的结构约束（值域/闭集/范围），不复判文学质量。
func validateSynthesis(s *BookSynthesis, n int) error {
	if strings.TrimSpace(s.Premise) == "" {
		return fmt.Errorf("premise 为空")
	}
	if len(s.Characters) == 0 {
		return fmt.Errorf("characters 为空")
	}
	if !validPlanningTiers[s.PlanningTier] {
		return fmt.Errorf("planning_tier 非法：%q", s.PlanningTier)
	}
	switch s.StoryStatus {
	case storyOpen, storyClosed, storyUncertain:
	default:
		return fmt.Errorf("story_status 非法：%q", s.StoryStatus)
	}
	if strings.TrimSpace(s.Compass.EndingDirection) == "" {
		return fmt.Errorf("compass.ending_direction 为空")
	}
	return validateStructure(s.Structure, n)
}

// validateStructure 校验卷弧范围连续、无重叠、完整覆盖 1..N（RFC §11 / 不变量 5）。
func validateStructure(structure []ImportedVolumeRange, n int) error {
	if len(structure) == 0 {
		return fmt.Errorf("structure 为空")
	}
	next := 1
	for vi, v := range structure {
		if len(v.Arcs) == 0 {
			return fmt.Errorf("卷[%d] %q 无弧", vi, v.Title)
		}
		for ai, a := range v.Arcs {
			if a.StartChapter != next {
				return fmt.Errorf("卷[%d]弧[%d] 起点 %d 应为 %d（须连续无缺口）", vi, ai, a.StartChapter, next)
			}
			if a.EndChapter < a.StartChapter {
				return fmt.Errorf("卷[%d]弧[%d] 范围倒置 %d..%d", vi, ai, a.StartChapter, a.EndChapter)
			}
			next = a.EndChapter + 1
		}
	}
	if next-1 != n {
		return fmt.Errorf("卷弧范围覆盖 %d 章，应为 %d 章", next-1, n)
	}
	return nil
}

// synthesisInputDigest 绑定有序逐章分析集合的紧凑事实 + 综合 prompt/schema 版本（RFC §6.3 / 不变量 6）。
// 纳入版本，改综合契约后旧 synthesis 自然失效重做。
func synthesisInputDigest(facts []ImportedChapterFacts) string {
	var b strings.Builder
	b.WriteString("synthesize\x00")
	b.WriteString(synthesizePromptVersion)
	fmt.Fprintf(&b, "\x00v%d", synthesisSchemaVersion)
	for _, f := range facts {
		b.WriteByte(0)
		b.WriteString(compactFact(f))
	}
	return Digest([]byte(b.String()))
}

// Foundation 是从 BookSynthesis + 逐章事实组装出的正式领域对象集（发布前完整校验，RFC §11）。
type Foundation struct {
	PlanningTier domain.PlanningTier
	Premise      string
	Characters   []domain.Character
	WorldRules   []domain.WorldRule
	Volumes      []domain.VolumeOutline
	Compass      domain.StoryCompass
	Closed       bool
}

// AssembleFoundation 用综合语义 + 逐章事实组装正式 Foundation 并完整校验。
// closed 是 story_status 裁定后的收束事实；fallbackName 用于正文无法确认书名时的推断标题。
func AssembleFoundation(s *BookSynthesis, facts []ImportedChapterFacts, closed bool, fallbackName string) (*Foundation, error) {
	n := len(facts)
	if err := validateSynthesis(s, n); err != nil {
		return nil, err
	}
	byChapter := make(map[int]ImportedChapterFacts, n)
	for _, f := range facts {
		byChapter[f.Chapter] = f
	}

	volumes := make([]domain.VolumeOutline, 0, len(s.Structure))
	for vi, v := range s.Structure {
		vol := domain.VolumeOutline{Index: vi + 1, Title: v.Title, Theme: v.Theme}
		for ai, a := range v.Arcs {
			arc := domain.ArcOutline{Index: ai + 1, Title: a.Title, Goal: a.Goal}
			for ch := a.StartChapter; ch <= a.EndChapter; ch++ {
				f, ok := byChapter[ch]
				if !ok {
					return nil, fmt.Errorf("弧范围引用不存在的章 %d", ch)
				}
				arc.Chapters = append(arc.Chapters, domain.OutlineEntry{
					Chapter: ch, Title: f.Title, CoreEvent: f.CoreEvent, Hook: f.Hook, Scenes: f.Scenes,
				})
			}
			vol.Arcs = append(vol.Arcs, arc)
		}
		volumes = append(volumes, vol)
	}
	if closed && len(volumes) > 0 {
		volumes[len(volumes)-1].Final = true
	}

	// FlattenOutline 后章数为 N，且标题与逐章事实一致（RFC §11.5）。
	flat := domain.FlattenOutline(volumes)
	if len(flat) != n {
		return nil, fmt.Errorf("FlattenOutline 章数 %d != %d", len(flat), n)
	}
	for _, e := range flat {
		if e.Title != byChapter[e.Chapter].Title {
			return nil, fmt.Errorf("章 %d 标题与逐章事实不一致", e.Chapter)
		}
	}

	premise := ensurePremiseTitle(s.Premise, fallbackName)
	return &Foundation{
		PlanningTier: s.PlanningTier,
		Premise:      premise,
		Characters:   s.Characters,
		WorldRules:   s.WorldRules,
		Volumes:      volumes,
		Compass:      s.Compass,
		Closed:       closed,
	}, nil
}

// ensurePremiseTitle 保证 premise 以书名标题行开头；正文无法确认书名时用文件 basename 作推断标题（RFC §11.1）。
func ensurePremiseTitle(premise, fallbackName string) string {
	if strings.HasPrefix(strings.TrimLeft(premise, " \t\n"), "#") {
		return premise
	}
	name := strings.TrimSuffix(fallbackName, ".txt")
	name = strings.TrimSuffix(name, ".md")
	if name == "" {
		name = "未命名导入"
	}
	return fmt.Sprintf("# %s（书名据文件名推断）\n\n%s", name, premise)
}
