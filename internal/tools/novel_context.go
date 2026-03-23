package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// References 嵌入的参考资料。
type References struct {
	// V0
	ChapterGuide      string
	HookTechniques    string
	QualityChecklist  string
	OutlineTemplate   string
	CharacterTemplate string
	ChapterTemplate   string
	// V1
	Consistency      string
	ContentExpansion string
	DialogueWriting  string
	// V2
	StyleReference   string // 风格补充参考（可为空）
	LongformPlanning string // 通用长篇规划参考
	Differentiation  string // 通用差异化设计参考
}

// ContextTool 组装当前章节所需上下文。
type ContextTool struct {
	store *store.Store
	refs  References
	style string
}

func NewContextTool(store *store.Store, refs References, style string) *ContextTool {
	return &ContextTool{store: store, refs: refs, style: style}
}

func (t *ContextTool) Name() string { return "novel_context" }
func (t *ContextTool) Description() string {
	return "获取小说创作上下文，包括基础设定、状态数据、前情摘要和写作参考资料"
}
func (t *ContextTool) Label() string { return "加载上下文" }

func (t *ContextTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("chapter", schema.Int("章节号。不传则返回基础设定和模板（供 Architect 使用）")),
	)
}

func (t *ContextTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapter int `json:"chapter"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}

	result := make(map[string]any)
	var warnings []string
	seenWarnings := make(map[string]struct{})
	warn := func(scope string, err error) {
		if err == nil || os.IsNotExist(err) {
			return
		}
		msg := fmt.Sprintf("%s 读取失败: %v", scope, err)
		if _, ok := seenWarnings[msg]; ok {
			return
		}
		seenWarnings[msg] = struct{}{}
		warnings = append(warnings, msg)
	}

	// 加载基础设定
	if premise, err := t.store.LoadPremise(); err == nil && premise != "" {
		result["premise"] = premise
	} else {
		warn("premise", err)
	}
	if outline, err := t.store.LoadOutline(); err == nil && outline != nil {
		result["outline"] = outline
	} else {
		warn("outline", err)
	}
	if rules, err := t.store.LoadWorldRules(); err == nil && len(rules) > 0 {
		result["world_rules"] = rules
	} else {
		warn("world_rules", err)
	}

	if a.Chapter > 0 {
		// 根据总章节数计算上下文策略
		profile := domain.NewContextProfile(0)
		progress, err := t.store.LoadProgress()
		warn("progress", err)
		runMeta, err := t.store.LoadRunMeta()
		warn("run_meta", err)
		if runMeta != nil && runMeta.PlanningTier != "" {
			result["planning_tier"] = runMeta.PlanningTier
		}
		if progress != nil && progress.TotalChapters > 0 {
			profile = domain.NewContextProfile(progress.TotalChapters)
		}
		// Layered 以 Progress 的显式标志为准，而非章节数推断
		if progress == nil || !progress.Layered {
			profile.Layered = false
		}

		// 角色加载：Layered 模式优先用快照，回退到原始设定
		if profile.Layered {
			t.loadLayeredCharacters(result, a.Chapter, warn)
		} else {
			t.loadFilteredCharacters(result, a.Chapter, warn)
		}

		// Writer/Editor 模式：加载章节相关上下文
		currentEntry, currentEntryErr := t.store.GetChapterOutline(a.Chapter)
		if currentEntryErr == nil {
			result["current_chapter_outline"] = currentEntry
		} else {
			warn("current_chapter_outline", currentEntryErr)
		}

		// 下一章预告：让 Writer 知道后续走向，便于设计伏笔和章末钩子
		if next, err := t.store.GetChapterOutline(a.Chapter + 1); err == nil && next != nil {
			result["next_chapter_outline"] = next
		}

		// 状态数据统一加载（供 result 赋值和相关章节推荐共用，避免重复 IO）
		foreshadow, foreshadowErr := t.store.LoadActiveForeshadow()
		warn("foreshadow_ledger", foreshadowErr)
		if len(foreshadow) > 0 {
			result["foreshadow_ledger"] = foreshadow
		}
		relationships, relErr := t.store.LoadRelationships()
		warn("relationship_state", relErr)
		if len(relationships) > 0 {
			result["relationship_state"] = relationships
		}
		allStateChanges, scErr := t.store.LoadStateChanges()
		warn("recent_state_changes", scErr)
		if len(allStateChanges) > 0 {
			start := max(a.Chapter-2, 1)
			var recent []domain.StateChange
			for _, c := range allStateChanges {
				if c.Chapter >= start && c.Chapter < a.Chapter {
					recent = append(recent, c)
				}
			}
			if len(recent) > 0 {
				result["recent_state_changes"] = recent
			}
		}

		// 相关章节推荐：长篇模式下根据结构化数据反查与当前章相关的历史章节
		if progress != nil && progress.TotalChapters > 30 && currentEntry != nil {
			if related := t.buildRelatedChapters(a.Chapter, currentEntry, foreshadow, relationships, allStateChanges); len(related) > 0 {
				result["related_chapters"] = related
			}
		}

		// 摘要加载：分层 vs 扁平窗口
		if profile.Layered {
			t.loadLayeredSummaries(result, a.Chapter, profile.SummaryWindow, warn)
		} else {
			if summaries, err := t.store.LoadRecentSummaries(a.Chapter, profile.SummaryWindow); err == nil && len(summaries) > 0 {
				result["recent_summaries"] = summaries
			} else {
				warn("recent_summaries", err)
			}
		}

		// 时间线：窗口加载
		if timeline, err := t.store.LoadRecentTimeline(a.Chapter, profile.TimelineWindow); err == nil && len(timeline) > 0 {
			result["timeline"] = timeline
		} else {
			warn("timeline", err)
		}

		// Layered 模式：注入当前卷弧位置 + 弧目标/卷主题
		if profile.Layered && progress != nil {
			pos := map[string]any{
				"volume": progress.CurrentVolume,
				"arc":    progress.CurrentArc,
			}
			if volumes, err := t.store.LoadLayeredOutline(); err == nil {
				for _, v := range volumes {
					if v.Index == progress.CurrentVolume {
						pos["volume_title"] = v.Title
						pos["volume_theme"] = v.Theme
						for _, arc := range v.Arcs {
							if arc.Index == progress.CurrentArc {
								pos["arc_title"] = arc.Title
								pos["arc_goal"] = arc.Goal
								break
							}
						}
						break
					}
				}
			} else {
				warn("layered_outline", err)
			}
			result["position"] = pos
		}

		// 加载进度状态和节奏追踪
		if progress != nil {
			checkpoint := map[string]any{
				"in_progress_chapter": progress.InProgressChapter,
			}
			if len(progress.StrandHistory) > 0 {
				checkpoint["strand_history"] = progress.StrandHistory
			}
			if len(progress.HookHistory) > 0 {
				checkpoint["hook_history"] = progress.HookHistory
			}
			result["checkpoint"] = checkpoint
		}
		// 加载已有的章节构思
		if plan, err := t.store.LoadChapterPlan(a.Chapter); err == nil && plan != nil {
			result["chapter_plan"] = plan
		} else {
			warn("chapter_plan", err)
		}

		// 前章尾部：嵌入前一章末尾 ~800 字，Writer 无需额外调用 read_chapter 获取衔接上文
		if a.Chapter > 1 {
			if prevText, err := t.store.LoadChapterText(a.Chapter - 1); err == nil && prevText != "" {
				runes := []rune(prevText)
				if len(runes) > 800 {
					runes = runes[len(runes)-800:]
				}
				result["previous_tail"] = string(runes)
			}
		}

		// 写作风格：规则优先，无规则时回退到原文片段
		styleRules, styleErr := t.store.LoadStyleRules()
		warn("style_rules", styleErr)
		if styleRules != nil {
			result["style_rules"] = styleRules
		} else {
			// 风格锚点：从前文提取代表性段落
			if anchors := t.store.ExtractStyleAnchors(3); len(anchors) > 0 {
				result["style_anchors"] = anchors
			}

			// 角色声纹：提取出场角色的对话原文片段
			if currentEntry != nil {
				var voiceSamples []map[string]any
				chars, _ := t.store.LoadCharacters()
				for _, c := range chars {
					// 只为 core/important 角色提取声纹
					if c.Tier == "secondary" || c.Tier == "decorative" {
						continue
					}
					samples := t.store.ExtractDialogue(c.Name, c.Aliases, 3)
					if len(samples) > 0 {
						voiceSamples = append(voiceSamples, map[string]any{
							"character": c.Name,
							"samples":   samples,
						})
					}
					if len(voiceSamples) >= 5 {
						break
					}
				}
				if len(voiceSamples) > 0 {
					result["voice_samples"] = voiceSamples
				}
			}
		}

		// 写作参考资料分阶段加载
		result["references"] = t.writerReferences(a.Chapter)
	} else {
		runMeta, err := t.store.LoadRunMeta()
		warn("run_meta", err)
		if runMeta != nil && runMeta.PlanningTier != "" {
			result["planning_tier"] = runMeta.PlanningTier
		}
		// Architect 模式：全量角色 + 模板
		if chars, err := t.store.LoadCharacters(); err == nil && chars != nil {
			result["characters"] = chars
		} else {
			warn("characters", err)
		}
		// Architect 模式下也加载分层大纲（弧级规划需要看全貌）
		if layered, err := t.store.LoadLayeredOutline(); err == nil && len(layered) > 0 {
			result["layered_outline"] = layered
		} else {
			warn("layered_outline", err)
		}
		// 加载已有的弧摘要（弧级规划时需要参考前续弧的内容）
		if volSummaries, err := t.store.LoadAllVolumeSummaries(); err == nil && len(volSummaries) > 0 {
			result["volume_summaries"] = volSummaries
		} else {
			warn("volume_summaries", err)
		}
		result["references"] = t.architectReferences()

		// 基础设定完备性检查
		result["foundation_status"] = t.foundationStatus()
	}

	if len(warnings) > 0 {
		result["_warnings"] = warnings
	}

	// 优先级预算：总大小超过阈值时自动裁剪低优先级数据
	if a.Chapter > 0 {
		trimByBudget(result, 100*1024) // 100KB 预算
	}

	result["_loading_summary"] = buildLoadingSummary(result, a.Chapter)
	return json.Marshal(result)
}

// buildLoadingSummary 从已组装的 result 中统计各项数据量，生成一行可读摘要。
func buildLoadingSummary(result map[string]any, chapter int) string {
	var parts []string

	if chapter > 0 {
		parts = append(parts, fmt.Sprintf("ch=%d", chapter))
	} else {
		parts = append(parts, "architect")
	}
	if tier, ok := result["planning_tier"].(domain.PlanningTier); ok && tier != "" {
		parts = append(parts, fmt.Sprintf("tier=%s", tier))
	}

	// 卷弧位置
	if pos, ok := result["position"].(map[string]any); ok {
		parts = append(parts, fmt.Sprintf("V%dA%d", pos["volume"], pos["arc"]))
	}

	var items []string
	countSlice := func(key string) int {
		if v, ok := result[key]; ok {
			if s, ok := v.([]domain.Character); ok {
				return len(s)
			}
			// 通用 slice 反射
			return sliceLen(v)
		}
		return 0
	}

	// 角色
	if n := countSlice("character_snapshots"); n > 0 {
		items = append(items, fmt.Sprintf("角色:%d(快照)", n))
	} else if n := countSlice("characters"); n > 0 {
		items = append(items, fmt.Sprintf("角色:%d", n))
	}

	// 分层摘要
	if n := countSlice("volume_summaries"); n > 0 {
		items = append(items, fmt.Sprintf("卷摘要:%d", n))
	}
	if n := countSlice("arc_summaries"); n > 0 {
		items = append(items, fmt.Sprintf("弧摘要:%d", n))
	}
	if n := countSlice("recent_summaries"); n > 0 {
		items = append(items, fmt.Sprintf("章摘要:%d", n))
	}

	// 分层大纲
	if n := countSlice("layered_outline"); n > 0 {
		items = append(items, fmt.Sprintf("分层大纲:%d卷", n))
	}

	// 状态数据
	if n := countSlice("timeline"); n > 0 {
		items = append(items, fmt.Sprintf("时间线:%d", n))
	}
	if n := countSlice("foreshadow_ledger"); n > 0 {
		items = append(items, fmt.Sprintf("伏笔:%d", n))
	}
	if n := countSlice("relationship_state"); n > 0 {
		items = append(items, fmt.Sprintf("关系:%d", n))
	}
	if n := countSlice("recent_state_changes"); n > 0 {
		items = append(items, fmt.Sprintf("状态变化:%d", n))
	}
	if _, ok := result["previous_tail"]; ok {
		items = append(items, "前章尾部:ok")
	}
	if _, ok := result["style_rules"]; ok {
		items = append(items, "风格规则:ok")
	}
	if n := sliceLen(result["related_chapters"]); n > 0 {
		items = append(items, fmt.Sprintf("相关章:%d", n))
	}

	// 参考资料
	if refs, ok := result["references"].(map[string]string); ok && len(refs) > 0 {
		items = append(items, fmt.Sprintf("参考:%d项", len(refs)))
	}
	if warnings, ok := result["_warnings"].([]string); ok && len(warnings) > 0 {
		items = append(items, fmt.Sprintf("告警:%d", len(warnings)))
	}
	if trimmed, ok := result["_trimmed"].([]string); ok && len(trimmed) > 0 {
		items = append(items, fmt.Sprintf("裁剪:%s", strings.Join(trimmed, ",")))
	}

	if len(items) > 0 {
		parts = append(parts, strings.Join(items, " "))
	}
	return strings.Join(parts, " | ")
}

// sliceLen 对 any 类型尝试取 slice 长度。
func sliceLen(v any) int {
	switch s := v.(type) {
	case []domain.ChapterSummary:
		return len(s)
	case []domain.ArcSummary:
		return len(s)
	case []domain.VolumeSummary:
		return len(s)
	case []domain.CharacterSnapshot:
		return len(s)
	case []domain.TimelineEvent:
		return len(s)
	case []domain.ForeshadowEntry:
		return len(s)
	case []domain.RelationshipEntry:
		return len(s)
	case []domain.StateChange:
		return len(s)
	case []domain.VolumeOutline:
		return len(s)
	case []domain.Character:
		return len(s)
	case []domain.RelatedChapter:
		return len(s)
	default:
		return 0
	}
}

// loadFilteredCharacters 按 Tier 和场景出场过滤角色。
// core/important 始终返回；secondary/decorative 只在当前章节大纲提及时返回。
func (t *ContextTool) loadFilteredCharacters(result map[string]any, chapter int, warn func(string, error)) {
	chars, err := t.store.LoadCharacters()
	if err != nil {
		warn("characters", err)
		return
	}
	if len(chars) == 0 {
		return
	}

	// 获取当前章节大纲的场景描述，用于匹配次要角色
	entry, err := t.store.GetChapterOutline(chapter)
	if err != nil {
		warn("current_chapter_outline", err)
		result["characters"] = chars
		return
	}
	sceneText := strings.Join(entry.Scenes, " ") + " " + entry.CoreEvent + " " + entry.Title

	var filtered []domain.Character
	for _, c := range chars {
		switch c.Tier {
		case "secondary", "decorative":
			if matchCharacter(sceneText, c) {
				filtered = append(filtered, c)
			}
		default: // core, important, 或未设置
			filtered = append(filtered, c)
		}
	}
	result["characters"] = filtered
}

// matchCharacter 检查场景文本中是否包含角色的正式名或任一别名。
func matchCharacter(text string, c domain.Character) bool {
	if strings.Contains(text, c.Name) {
		return true
	}
	for _, alias := range c.Aliases {
		if strings.Contains(text, alias) {
			return true
		}
	}
	return false
}

// loadLayeredSummaries 分层摘要加载：卷摘要 + 当前卷弧摘要 + 弧内章摘要。
func (t *ContextTool) loadLayeredSummaries(result map[string]any, chapter, summaryWindow int, warn func(string, error)) {
	vol, arc, err := t.store.LocateChapter(chapter)
	if err != nil {
		warn("layered_outline_position", err)
		// 回退到扁平模式
		if summaries, err := t.store.LoadRecentSummaries(chapter, summaryWindow); err == nil && len(summaries) > 0 {
			result["recent_summaries"] = summaries
		} else {
			warn("recent_summaries", err)
		}
		return
	}

	// 1. 已完成卷的卷摘要
	if volSummaries, err := t.store.LoadAllVolumeSummaries(); err == nil && len(volSummaries) > 0 {
		result["volume_summaries"] = volSummaries
	} else {
		warn("volume_summaries", err)
	}

	// 2. 当前卷内已完成弧的弧摘要（不含当前弧）
	if arcSummaries, err := t.store.LoadArcSummaries(vol); err == nil && len(arcSummaries) > 0 {
		var prior []domain.ArcSummary
		for _, s := range arcSummaries {
			if s.Arc < arc {
				prior = append(prior, s)
			}
		}
		if len(prior) > 0 {
			result["arc_summaries"] = prior
		}
	} else {
		warn("arc_summaries", err)
	}

	// 3. 当前弧内最近 N 章的章摘要
	if summaries, err := t.store.LoadRecentSummaries(chapter, summaryWindow); err == nil && len(summaries) > 0 {
		result["recent_summaries"] = summaries
	} else {
		warn("recent_summaries", err)
	}
}

// loadLayeredCharacters Layered 模式下的角色加载：优先用最近快照，回退到原始设定 + Tier 过滤。
func (t *ContextTool) loadLayeredCharacters(result map[string]any, chapter int, warn func(string, error)) {
	snapshots, err := t.store.LoadLatestSnapshots()
	if err == nil && len(snapshots) > 0 {
		result["character_snapshots"] = snapshots
		// 同时保留原始设定中的 core/important 角色（快照可能不含新登场角色）
		t.loadFilteredCharacters(result, chapter, warn)
		return
	}
	warn("character_snapshots", err)
	// 无快照时回退到原始设定
	t.loadFilteredCharacters(result, chapter, warn)
}

// writerReferences 返回写作参考资料。章节 1 返回全量，后续章节裁剪掉不再需要的模板。
func (t *ContextTool) writerReferences(chapter int) map[string]string {
	refs := map[string]string{}
	add := func(k, v string) {
		if v != "" {
			refs[k] = v
		}
	}
	// 渐进式加载：始终保留核心参考，前 3 章额外加载完整写作指南
	add("consistency", t.refs.Consistency)
	add("hook_techniques", t.refs.HookTechniques)
	add("quality_checklist", t.refs.QualityChecklist)
	if chapter <= 3 {
		add("chapter_guide", t.refs.ChapterGuide)
		add("dialogue_writing", t.refs.DialogueWriting)
		add("style_reference", t.refs.StyleReference)
	}

	// 仅首章加载的补充参考
	if chapter <= 1 {
		add("chapter_template", t.refs.ChapterTemplate)
		add("content_expansion", t.refs.ContentExpansion)
	}
	return refs
}

func (t *ContextTool) architectReferences() map[string]string {
	refs := map[string]string{}
	add := func(k, v string) {
		if v != "" {
			refs[k] = v
		}
	}
	add("outline_template", t.refs.OutlineTemplate)
	add("character_template", t.refs.CharacterTemplate)
	add("longform_planning", t.refs.LongformPlanning)
	add("differentiation", t.refs.Differentiation)
	add("style_reference", t.refs.StyleReference)
	return refs
}

// foundationStatus 检查基础设定的完备性，返回缺失项列表。
func (t *ContextTool) foundationStatus() map[string]any {
	status := map[string]any{"ready": true}
	var missing []string
	if p, _ := t.store.LoadPremise(); p == "" {
		missing = append(missing, "premise")
	}
	if o, _ := t.store.LoadOutline(); len(o) == 0 {
		missing = append(missing, "outline")
	}
	if c, _ := t.store.LoadCharacters(); len(c) == 0 {
		missing = append(missing, "characters")
	}
	if r, _ := t.store.LoadWorldRules(); len(r) == 0 {
		missing = append(missing, "world_rules")
	}
	if len(missing) > 0 {
		status["ready"] = false
		status["missing"] = missing
	}
	return status
}

// ContextSummary 返回当前状态的简要摘要（供日志使用）。
func (t *ContextTool) ContextSummary() string {
	var parts []string
	if p, _ := t.store.LoadPremise(); p != "" {
		parts = append(parts, "premise:ok")
	}
	if o, _ := t.store.LoadOutline(); o != nil {
		parts = append(parts, fmt.Sprintf("outline:%d chapters", len(o)))
	}
	if c, _ := t.store.LoadCharacters(); c != nil {
		parts = append(parts, fmt.Sprintf("characters:%d", len(c)))
	}
	if len(parts) == 0 {
		return "empty"
	}
	return strings.Join(parts, ", ")
}

// trimByBudget 按优先级裁剪 result，使 JSON 总大小不超过 budget 字节。
// 优先级（从低到高）：references < voice_samples < style_anchors < previous_tail < timeline
//
//	< recent_state_changes < foreshadow_ledger < relationship_state < 其余（不裁剪）
//
// 裁剪的 key 会记录到 result["_trimmed"] 供日志排查。
func trimByBudget(result map[string]any, budget int) {
	// 先测量当前大小
	data, err := json.Marshal(result)
	if err != nil || len(data) <= budget {
		return
	}

	// 按优先级从低到高列出可裁剪的 key
	trimOrder := []string{
		"references",
		"voice_samples",
		"style_anchors",
		"style_rules",
		"previous_tail",
		"timeline",
		"recent_state_changes",
		"foreshadow_ledger",
		"relationship_state",
	}

	var trimmed []string
	for _, key := range trimOrder {
		if _, ok := result[key]; !ok {
			continue
		}
		delete(result, key)
		trimmed = append(trimmed, key)
		data, err = json.Marshal(result)
		if err != nil || len(data) <= budget {
			break
		}
	}
	if len(trimmed) > 0 {
		result["_trimmed"] = trimmed
	}
}

// buildRelatedChapters 根据结构化数据反查与当前章相关的历史章节。
// 从伏笔、角色出场、状态变化、关系四个维度推荐，去重后最多返回 5 条。
// 所有数据通过参数传入，不做额外 IO。
func (t *ContextTool) buildRelatedChapters(
	chapter int,
	entry *domain.OutlineEntry,
	foreshadow []domain.ForeshadowEntry,
	relationships []domain.RelationshipEntry,
	stateChanges []domain.StateChange,
) []domain.RelatedChapter {
	const recentWindow = 10
	const maxResults = 5

	seen := make(map[int]struct{})
	var results []domain.RelatedChapter
	add := func(ch int, reason string) {
		if ch <= 0 || ch >= chapter {
			return
		}
		// 最近几章太近，不推荐
		if ch > chapter-recentWindow {
			return
		}
		if _, ok := seen[ch]; ok {
			return
		}
		seen[ch] = struct{}{}
		results = append(results, domain.RelatedChapter{Chapter: ch, Reason: reason})
	}

	// 拼接大纲文本用于关键词匹配
	outlineText := entry.Title + " " + entry.CoreEvent
	for _, s := range entry.Scenes {
		outlineText += " " + s
	}

	// 1. 伏笔反查：活跃伏笔的描述是否与当前章大纲相关
	for _, f := range foreshadow {
		if strings.Contains(outlineText, f.ID) || containsAny(outlineText, strings.Fields(f.Description)) {
			add(f.PlantedAt, fmt.Sprintf("伏笔%s(%s)埋设章", f.ID, truncateRunes(f.Description, 15)))
		}
		if len(results) >= maxResults {
			break
		}
	}

	// 2. 角色出场反查：批量单次遍历，IO 从 O(角色数×章节数) 降为 O(章节数)
	chars, _ := t.store.LoadCharacters()
	outlineChars := matchOutlineCharacters(outlineText, chars)
	if len(outlineChars) > 0 {
		appearances := t.store.FindCharacterAppearances(outlineChars, chapter, recentWindow)
		for _, name := range outlineChars {
			if len(results) >= maxResults {
				break
			}
			if ch, ok := appearances[name]; ok {
				add(ch, fmt.Sprintf("角色'%s'最后出场章", name))
			}
		}
	}

	// 3. 状态变化反查：在已加载的 slice 上操作，零 IO
	for _, name := range outlineChars {
		if len(results) >= maxResults {
			break
		}
		ch := findLastStateChange(stateChanges, name, chapter)
		if ch > 0 && ch <= chapter-recentWindow {
			add(ch, fmt.Sprintf("'%s'状态变化章", name))
		}
	}

	// 4. 关系反查：当前章涉及的角色对之间关系最后变化
	if len(relationships) > 0 && len(outlineChars) >= 2 {
		charSet := make(map[string]struct{}, len(outlineChars))
		for _, c := range outlineChars {
			charSet[c] = struct{}{}
		}
		for _, r := range relationships {
			if len(results) >= maxResults {
				break
			}
			_, aIn := charSet[r.CharacterA]
			_, bIn := charSet[r.CharacterB]
			if aIn && bIn {
				add(r.Chapter, fmt.Sprintf("%s-%s关系变化", r.CharacterA, r.CharacterB))
			}
		}
	}

	return results
}

// findLastStateChange 在已加载的状态变化列表中查找实体最近一次变化的章节号。
func findLastStateChange(changes []domain.StateChange, entity string, currentChapter int) int {
	for i := len(changes) - 1; i >= 0; i-- {
		if changes[i].Entity == entity && changes[i].Chapter < currentChapter {
			return changes[i].Chapter
		}
	}
	return 0
}

// matchOutlineCharacters 从大纲文本中匹配出场角色名。
func matchOutlineCharacters(text string, chars []domain.Character) []string {
	var matched []string
	for _, c := range chars {
		if strings.Contains(text, c.Name) {
			matched = append(matched, c.Name)
			continue
		}
		for _, alias := range c.Aliases {
			if strings.Contains(text, alias) {
				matched = append(matched, c.Name)
				break
			}
		}
	}
	return matched
}

// containsAny 检查 text 是否包含 words 中的任一词（至少 2 字才匹配，避免噪音）。
func containsAny(text string, words []string) bool {
	for _, w := range words {
		if len([]rune(w)) >= 2 && strings.Contains(text, w) {
			return true
		}
	}
	return false
}

// truncateRunes 截断字符串到指定 rune 数。
func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}
