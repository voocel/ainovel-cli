package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/agentcore/schema"
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
	StyleReference string // 风格补充参考（可为空）
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

	// 加载基础设定
	if premise, err := t.store.LoadPremise(); err == nil && premise != "" {
		result["premise"] = premise
	}
	if outline, err := t.store.LoadOutline(); err == nil && outline != nil {
		result["outline"] = outline
	}
	if chars, err := t.store.LoadCharacters(); err == nil && chars != nil {
		result["characters"] = chars
	}

	if rules, err := t.store.LoadWorldRules(); err == nil && len(rules) > 0 {
		result["world_rules"] = rules
	}

	if a.Chapter > 0 {
		// Writer/Editor 模式：加载章节相关上下文
		if entry, err := t.store.GetChapterOutline(a.Chapter); err == nil {
			result["current_chapter_outline"] = entry
		}
		if summaries, err := t.store.LoadRecentSummaries(a.Chapter, 2); err == nil && len(summaries) > 0 {
			result["recent_summaries"] = summaries
		}
		// V1: 加载状态数据
		if timeline, err := t.store.LoadTimeline(); err == nil && len(timeline) > 0 {
			result["timeline"] = timeline
		}
		if foreshadow, err := t.store.LoadForeshadowLedger(); err == nil && len(foreshadow) > 0 {
			result["foreshadow_ledger"] = foreshadow
		}
		if relationships, err := t.store.LoadRelationships(); err == nil && len(relationships) > 0 {
			result["relationship_state"] = relationships
		}
		// V2: 加载场景级恢复状态
		if progress, err := t.store.LoadProgress(); err == nil && progress != nil {
			checkpoint := map[string]any{
				"in_progress_chapter": progress.InProgressChapter,
				"completed_scenes":    progress.CompletedScenes,
			}
			result["checkpoint"] = checkpoint
		}
		// V2: 加载已有的章节规划（支持场景恢复跳过已完成场景）
		if plan, err := t.store.LoadChapterPlan(a.Chapter); err == nil && plan != nil {
			result["chapter_plan"] = plan
		}
		// 写作参考资料
		result["references"] = t.writerReferences()
	} else {
		// Architect 模式：加载模板
		result["references"] = t.architectReferences()
	}

	return json.Marshal(result)
}

func (t *ContextTool) writerReferences() map[string]string {
	refs := map[string]string{}
	add := func(k, v string) {
		if v != "" {
			refs[k] = v
		}
	}
	add("chapter_guide", t.refs.ChapterGuide)
	add("hook_techniques", t.refs.HookTechniques)
	add("quality_checklist", t.refs.QualityChecklist)
	add("chapter_template", t.refs.ChapterTemplate)
	add("consistency", t.refs.Consistency)
	add("content_expansion", t.refs.ContentExpansion)
	add("dialogue_writing", t.refs.DialogueWriting)
	add("style_reference", t.refs.StyleReference)
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
