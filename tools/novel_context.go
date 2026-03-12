package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/domain"
	"github.com/voocel/ainovel-cli/state"
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
	store *state.Store
	refs  References
	style string
}

func NewContextTool(store *state.Store, refs References, style string) *ContextTool {
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
		if entry, err := t.store.GetChapterOutline(a.Chapter); err == nil {
			result["current_chapter_outline"] = entry
		} else {
			warn("current_chapter_outline", err)
		}

		// 摘要加载：分层 vs 扁平
		if profile.Layered {
			t.loadLayeredSummaries(result, a.Chapter, profile.SummaryWindow, warn)
		} else if profile.FullContext {
			if summaries, err := t.store.LoadAllSummaries(a.Chapter); err == nil && len(summaries) > 0 {
				result["recent_summaries"] = summaries
			} else {
				warn("recent_summaries", err)
			}
		} else {
			if summaries, err := t.store.LoadRecentSummaries(a.Chapter, profile.SummaryWindow); err == nil && len(summaries) > 0 {
				result["recent_summaries"] = summaries
			} else {
				warn("recent_summaries", err)
			}
		}

		// 时间线：Layered 用窗口，其他按策略
		if profile.FullContext {
			if timeline, err := t.store.LoadTimeline(); err == nil && len(timeline) > 0 {
				result["timeline"] = timeline
			} else {
				warn("timeline", err)
			}
		} else {
			if timeline, err := t.store.LoadRecentTimeline(a.Chapter, profile.TimelineWindow); err == nil && len(timeline) > 0 {
				result["timeline"] = timeline
			} else {
				warn("timeline", err)
			}
		}
		// foreshadow：短篇全量，否则只取未回收条目
		if profile.FullContext {
			if foreshadow, err := t.store.LoadForeshadowLedger(); err == nil && len(foreshadow) > 0 {
				result["foreshadow_ledger"] = foreshadow
			} else {
				warn("foreshadow_ledger", err)
			}
		} else {
			if foreshadow, err := t.store.LoadActiveForeshadow(); err == nil && len(foreshadow) > 0 {
				result["foreshadow_ledger"] = foreshadow
			} else {
				warn("foreshadow_ledger", err)
			}
		}
		// relationships：保持全量（pair-key 去重，数据量天然可控）
		if relationships, err := t.store.LoadRelationships(); err == nil && len(relationships) > 0 {
			result["relationship_state"] = relationships
		} else {
			warn("relationship_state", err)
		}
		// 状态变化：最近 5 章的角色/实体状态变化
		if changes, err := t.store.LoadRecentStateChanges(a.Chapter, 5); err == nil && len(changes) > 0 {
			result["recent_state_changes"] = changes
		} else {
			warn("recent_state_changes", err)
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

		// 加载场景级恢复状态 + 节奏追踪
		if progress != nil {
			checkpoint := map[string]any{
				"in_progress_chapter": progress.InProgressChapter,
				"completed_scenes":    progress.CompletedScenes,
			}
			if len(progress.StrandHistory) > 0 {
				checkpoint["strand_history"] = progress.StrandHistory
			}
			if len(progress.HookHistory) > 0 {
				checkpoint["hook_history"] = progress.HookHistory
			}
			result["checkpoint"] = checkpoint
		}
		// 加载已有的章节规划（支持场景恢复跳过已完成场景）
		if plan, err := t.store.LoadChapterPlan(a.Chapter); err == nil && plan != nil {
			result["chapter_plan"] = plan
		} else {
			warn("chapter_plan", err)
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
	}

	if len(warnings) > 0 {
		result["_warnings"] = warnings
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

	// 参考资料
	if refs, ok := result["references"].(map[string]string); ok && len(refs) > 0 {
		items = append(items, fmt.Sprintf("参考:%d项", len(refs)))
	}
	if warnings, ok := result["_warnings"].([]string); ok && len(warnings) > 0 {
		items = append(items, fmt.Sprintf("告警:%d", len(warnings)))
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
	// 始终加载的核心参考
	add("chapter_guide", t.refs.ChapterGuide)
	add("hook_techniques", t.refs.HookTechniques)
	add("quality_checklist", t.refs.QualityChecklist)
	add("consistency", t.refs.Consistency)
	add("dialogue_writing", t.refs.DialogueWriting)
	add("style_reference", t.refs.StyleReference)

	// 仅首章加载的补充参考（后续章节不再需要）
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
