package rip

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// aggregateSchemaVersion 纳入聚合与角色设定工件的 InputDigest，升级契约时递增以失效已落盘工件。
const aggregateSchemaVersion = 1

// PlotUnit 是一个剧情单元：连续章节范围 + 功能定位。
// 范围必须落在 [1, ExpectedChapters]、相邻单元不重叠不跳空——由 Go 机械校验，不靠模型自觉。
type PlotUnit struct {
	Title        string   `json:"title"`
	Function     string   `json:"function"` // 该单元在全书中承担的功能（开端/推进/转折/收束等）
	StartChapter int      `json:"start_chapter"`
	EndChapter   int      `json:"end_chapter"`
	Summary      string   `json:"summary"`
	Turns        []string `json:"turns,omitempty"` // 单元内的关键转折
}

// PacingNote 是某个章节区间的节奏观察。
type PacingNote struct {
	StartChapter int    `json:"start_chapter"`
	EndChapter   int    `json:"end_chapter"`
	Tempo        string `json:"tempo"` // 快 / 中 / 慢
	Note         string `json:"note"`
}

// EmotionModule 是可复用的情绪模块：什么情绪、靠什么手段拉起、在哪些章出现。
type EmotionModule struct {
	Name     string `json:"name"`
	Trigger  string `json:"trigger"`  // 如何触发
	Payoff   string `json:"payoff"`   // 如何支付
	Chapters []int  `json:"chapters"` // 出现章号
}

// Aggregate 是逐章摘要之上的全书结构聚合。
type Aggregate struct {
	PlotUnits      []PlotUnit      `json:"plot_units"`
	Pacing         []PacingNote    `json:"pacing"`
	EmotionModules []EmotionModule `json:"emotion_modules"`
	MainConflict   string          `json:"main_conflict"`
	StoryCore      string          `json:"story_core"` // 故事核：一句话说清这本书在讲什么
}

// CharacterProfile 是一个角色的分层画像。
// HardFacts 是正文明确写出的硬事实，须能回溯到原文；Inferred 是分析师推断，明确分开。
type CharacterProfile struct {
	Name       string   `json:"name"`
	Aliases    []string `json:"aliases,omitempty"`
	Tier       string   `json:"tier"` // 主角 / 重要 / 配角 / 功能性
	Function   string   `json:"function"`
	HardFacts  []string `json:"hard_facts"`
	Inferred   []string `json:"inferred,omitempty"`
	Arc        string   `json:"arc"`
	FirstSeen  int      `json:"first_seen"`
	Motivation string   `json:"motivation"`
}

// SettingItem 是一条设定：世界规则、金手指、组织、地理等。
type SettingItem struct {
	Category string `json:"category"`
	Name     string `json:"name"`
	Rule     string `json:"rule"`
	Boundary string `json:"boundary,omitempty"` // 该设定的限制（不可违反处）
}

// RelationEdge 是一条人物关系。
type RelationEdge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Kind   string `json:"kind"`
	Change string `json:"change,omitempty"` // 全书中的关系变化
}

// Profile 是角色、设定与关系网。
type Profile struct {
	Characters []CharacterProfile `json:"characters"`
	Settings   []SettingItem      `json:"settings"`
	Relations  []RelationEdge     `json:"relations"`
}

// aggregateInputDigest 绑定有序逐章摘要：任一章重摘即令聚合失配。
// 失败章以零值参与摘要，其「缺口」也是身份的一部分——补齐失败章后聚合自然重跑。
func aggregateInputDigest(summaries []ChapterSummary) string {
	var sb strings.Builder
	sb.WriteString("aggregate\x00")
	sb.WriteString(aggregatePromptVersion)
	fmt.Fprintf(&sb, "\x00v%d\x00", aggregateSchemaVersion)
	for _, s := range summaries {
		data, _ := json.Marshal(compactSummaryOf(s))
		sb.Write(data)
		sb.WriteByte(0)
	}
	return Digest([]byte(sb.String()))
}

// profileInputDigest 绑定聚合工件原始字节 + 角色设定 prompt/schema 版本。
func profileInputDigest(aggRaw []byte) string {
	return Digest([]byte(strings.Join([]string{
		"profile", profilePromptVersion, fmt.Sprintf("v%d", aggregateSchemaVersion), Digest(aggRaw),
	}, "\x00")))
}

// compactSummary 是送入聚合的紧凑视图：保留跨章归纳需要的字段，不含全文。
type compactSummary struct {
	Chapter    int         `json:"chapter"`
	Title      string      `json:"title"`
	Summary    string      `json:"summary"`
	PlotPoints []PlotPoint `json:"plot_points,omitempty"`
	KeyFacts   []string    `json:"key_facts,omitempty"`
	Payoffs    []string    `json:"payoffs,omitempty"`
	HookType   string      `json:"hook_type,omitempty"`
	Characters []string    `json:"characters,omitempty"`
	EmotionArc string      `json:"emotion_arc,omitempty"`
	Techniques []string    `json:"techniques,omitempty"`
}

// compactSummaryOf 是 compactSummary 的唯一构造入口，供 digest 与 payload 共用——
// 两者必须看到同一份视图，否则聚合身份与实际送入模型的内容会脱钩。
func compactSummaryOf(s ChapterSummary) compactSummary {
	return compactSummary{
		Chapter: s.Chapter, Title: s.Title, Summary: s.Summary,
		PlotPoints: s.PlotPoints, KeyFacts: s.KeyFacts, Payoffs: s.Payoffs,
		HookType: s.HookType, Characters: s.Characters,
		EmotionArc: s.EmotionArc, Techniques: s.Techniques,
	}
}

// validateAggregate 机械校验剧情单元的章节范围：必须落在 [1,total]、单元内 start<=end、
// 按序相邻不重叠不跳空、整体覆盖全书。参考实现靠模型自觉，这里代码执行、不通过就回喂重问。
func validateAggregate(a *Aggregate, total int) error {
	if len(a.PlotUnits) == 0 {
		return fmt.Errorf("至少需要 1 个剧情单元")
	}
	if strings.TrimSpace(a.StoryCore) == "" {
		return fmt.Errorf("缺故事核（story_core）")
	}
	if strings.TrimSpace(a.MainConflict) == "" {
		return fmt.Errorf("缺主线冲突（main_conflict）")
	}
	prevEnd := 0
	for i, u := range a.PlotUnits {
		if strings.TrimSpace(u.Title) == "" {
			return fmt.Errorf("剧情单元[%d] 缺标题", i)
		}
		if u.StartChapter < 1 || u.EndChapter > total {
			return fmt.Errorf("剧情单元 %q 范围 %d-%d 越界（全书 1-%d 章）", u.Title, u.StartChapter, u.EndChapter, total)
		}
		if u.StartChapter > u.EndChapter {
			return fmt.Errorf("剧情单元 %q 起始章 %d 大于结束章 %d", u.Title, u.StartChapter, u.EndChapter)
		}
		if u.StartChapter != prevEnd+1 {
			if u.StartChapter <= prevEnd {
				return fmt.Errorf("剧情单元 %q 起始章 %d 与前一单元（止于第 %d 章）重叠：单元必须连续且不重叠",
					u.Title, u.StartChapter, prevEnd)
			}
			return fmt.Errorf("剧情单元 %q 起始章 %d 与前一单元（止于第 %d 章）之间跳空了第 %d-%d 章：单元必须完整覆盖全书",
				u.Title, u.StartChapter, prevEnd, prevEnd+1, u.StartChapter-1)
		}
		prevEnd = u.EndChapter
	}
	if prevEnd != total {
		return fmt.Errorf("剧情单元只覆盖到第 %d 章，全书共 %d 章：请补齐剩余章节", prevEnd, total)
	}
	for i, p := range a.Pacing {
		if p.StartChapter < 1 || p.EndChapter > total || p.StartChapter > p.EndChapter {
			return fmt.Errorf("节奏观察[%d] 范围 %d-%d 非法（全书 1-%d 章）", i, p.StartChapter, p.EndChapter, total)
		}
	}
	for i, e := range a.EmotionModules {
		if strings.TrimSpace(e.Name) == "" {
			return fmt.Errorf("情绪模块[%d] 缺名称", i)
		}
		for _, c := range e.Chapters {
			if c < 1 || c > total {
				return fmt.Errorf("情绪模块 %q 引用了不存在的第 %d 章（全书 1-%d 章）", e.Name, c, total)
			}
		}
	}
	return nil
}

// AggregateBook 把逐章摘要归纳为剧情单元、节奏与情绪模块。
// 输入是紧凑视图而非全文：全书正文远超任何上下文窗口，摘要层已是为此准备的压缩。
func AggregateBook(ctx context.Context, m callModel, systemPrompt string, summaries []ChapterSummary, total, maxTokens int, prof callProfile) (*Aggregate, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "以下是全书 %d 章的逐章拆解。请归纳剧情单元（必须连续、不重叠、完整覆盖 1-%d 章）、节奏与可复用情绪模块。\n\n", total, total)
	for _, s := range summaries {
		if s.Chapter == 0 {
			continue // 持久失败章：无摘要可用，跳过而非编造
		}
		data, _ := json.Marshal(compactSummaryOf(s))
		sb.Write(data)
		sb.WriteByte('\n')
	}
	out, err := callStructured[Aggregate](ctx, m, aggregateContract, systemPrompt, sb.String(), maxTokens, prof, func(a *Aggregate) error {
		return validateAggregate(a, total)
	})
	if err != nil {
		return nil, err
	}
	prof.step(0, 0, "故事核：%s", snippet(out.StoryCore, 60))
	for _, u := range out.PlotUnits {
		prof.step(0, 0, "单元〈%s〉第 %d-%d 章：%s", snippet(u.Title, 20), u.StartChapter, u.EndChapter, snippet(u.Function, 40))
	}
	return &out, nil
}

// validateProfile 机械校验角色与设定。
// 硬事实必须能在原文中找到支撑：逐条按「去空白后子串命中」核对，找不到就回喂重问要求
// 改写成推断或标注「原文未明确」——这是参考实现里最容易被模型绕过的一条，必须代码执行。
func validateProfile(p *Profile, total int, normalizedSquashed string) error {
	if len(p.Characters) == 0 {
		return fmt.Errorf("至少需要 1 个角色")
	}
	for i, c := range p.Characters {
		if strings.TrimSpace(c.Name) == "" {
			return fmt.Errorf("角色[%d] 缺姓名", i)
		}
		if c.FirstSeen < 1 || c.FirstSeen > total {
			return fmt.Errorf("角色 %q 首次出场章 %d 越界（全书 1-%d 章）", c.Name, c.FirstSeen, total)
		}
		if !strings.Contains(normalizedSquashed, squashSpace(c.Name)) {
			return fmt.Errorf("角色名 %q 在原文中找不到：请使用原文中出现的称呼，不要改写或音译", c.Name)
		}
		for _, f := range nonEmpty(c.HardFacts) {
			if fact := hardFactKernel(f); fact != "" && !strings.Contains(normalizedSquashed, fact) {
				return fmt.Errorf("角色 %q 的硬事实 %q 在原文中找不到对应文字：若这是你的推断请移到 inferred，若原文确实没写请写「原文未明确」",
					c.Name, snippet(f, 30))
			}
		}
	}
	for i, s := range p.Settings {
		if strings.TrimSpace(s.Name) == "" || strings.TrimSpace(s.Rule) == "" {
			return fmt.Errorf("设定[%d] 缺名称或规则", i)
		}
	}
	known := make(map[string]bool, len(p.Characters))
	for _, c := range p.Characters {
		known[squashSpace(c.Name)] = true
		for _, a := range c.Aliases {
			known[squashSpace(a)] = true
		}
	}
	for i, r := range p.Relations {
		if !known[squashSpace(r.From)] {
			return fmt.Errorf("关系[%d] 的 from %q 不在角色列表中：请先把它列入 characters", i, r.From)
		}
		if !known[squashSpace(r.To)] {
			return fmt.Errorf("关系[%d] 的 to %q 不在角色列表中：请先把它列入 characters", i, r.To)
		}
	}
	return nil
}

// hardFactKernel 提取硬事实中可核对的「原文核心」。
// 整句照抄原文极少见（分析师会转述），所以只核对被引号包裹的直引部分——那是声称
// 「原文这么写的」的唯一强主张。无直引时返回空串表示不核对，把裁量留给模型。
func hardFactKernel(fact string) string {
	for _, pair := range [][2]string{{"「", "」"}, {"“", "”"}, {"『", "』"}, {`"`, `"`}} {
		i := strings.Index(fact, pair[0])
		if i < 0 {
			continue
		}
		rest := fact[i+len(pair[0]):]
		j := strings.Index(rest, pair[1])
		if j <= 0 {
			continue
		}
		if kernel := squashSpace(rest[:j]); len([]rune(kernel)) >= 4 {
			return kernel
		}
	}
	return ""
}

// ProfileBook 从逐章摘要与聚合结果归纳角色、设定与关系网。
func ProfileBook(ctx context.Context, m callModel, systemPrompt string, summaries []ChapterSummary, agg *Aggregate, normalized []byte, total, maxTokens int, prof callProfile) (*Profile, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "以下是全书 %d 章的逐章拆解与结构聚合。请归纳角色分层、设定与关系网。\n\n", total)
	sb.WriteString("## 结构聚合\n\n")
	aggData, _ := json.MarshalIndent(agg, "", "  ")
	sb.Write(aggData)
	sb.WriteString("\n\n## 逐章拆解\n\n")
	for _, s := range summaries {
		if s.Chapter == 0 {
			continue
		}
		data, _ := json.Marshal(compactSummaryOf(s))
		sb.Write(data)
		sb.WriteByte('\n')
	}
	squashed := squashSpace(string(normalized))
	out, err := callStructured[Profile](ctx, m, profileContract, systemPrompt, sb.String(), maxTokens, prof, func(p *Profile) error {
		return validateProfile(p, total, squashed)
	})
	if err != nil {
		return nil, err
	}
	prof.step(0, 0, "归纳出 %d 个角色、%d 条设定、%d 条关系", len(out.Characters), len(out.Settings), len(out.Relations))
	return &out, nil
}
