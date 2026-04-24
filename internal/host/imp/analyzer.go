package imp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// validHookTypes / validStrands 与 commit_chapter schema 保持一致。
var (
	validHookTypes = map[string]bool{"crisis": true, "mystery": true, "desire": true, "emotion": true, "choice": true}
	validStrands   = map[string]bool{"quest": true, "fire": true, "constellation": true}
)

// ChapterAnalysis 是单章反推的结构化产物，字段直接对齐 commit_chapter 入参。
type ChapterAnalysis struct {
	Summary             string
	Characters          []string
	KeyEvents           []string
	TimelineEvents      []domain.TimelineEvent
	ForeshadowUpdates   []domain.ForeshadowUpdate
	RelationshipChanges []domain.RelationshipEntry
	StateChanges        []domain.StateChange
	HookType            string
	DominantStrand      string
}

// AnalyzeChapter 用一次 LLM 调用，从单章正文反推 commit_chapter 所需事实。
// hooksContext 是已知伏笔池的快照（可空），用于让 LLM 复用既有 ID。
func AnalyzeChapter(
	ctx context.Context,
	llm LLMChat,
	systemPrompt string,
	chapter int,
	chapterTitle, chapterContent string,
	premise, charactersBlock string,
	activeHooks []domain.ForeshadowEntry,
) (*ChapterAnalysis, error) {
	if llm == nil {
		return nil, fmt.Errorf("llm is nil")
	}
	if strings.TrimSpace(chapterContent) == "" {
		return nil, fmt.Errorf("chapter %d: empty content", chapter)
	}

	user := buildAnalyzerUserPrompt(chapter, chapterTitle, chapterContent, premise, charactersBlock, activeHooks)
	resp, err := llm.Generate(ctx, []agentcore.Message{
		agentcore.SystemMsg(systemPrompt),
		agentcore.UserMsg(user),
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("llm generate ch%d: %w", chapter, err)
	}
	if resp == nil {
		return nil, fmt.Errorf("ch%d: nil response", chapter)
	}
	return parseAnalyzerOutput(resp.Message.TextContent())
}

func buildAnalyzerUserPrompt(
	chapter int,
	title, content, premise, charactersBlock string,
	hooks []domain.ForeshadowEntry,
) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "请分析第 %d 章正文，输出 9 个 === TAG === 段。\n\n", chapter)
	if title != "" {
		fmt.Fprintf(&sb, "章节标题：%s\n\n", title)
	}

	if strings.TrimSpace(premise) != "" {
		sb.WriteString("## 故事前提（参考）\n\n")
		sb.WriteString(premise)
		sb.WriteString("\n\n")
	}
	if strings.TrimSpace(charactersBlock) != "" {
		sb.WriteString("## 已知角色（参考）\n\n")
		sb.WriteString(charactersBlock)
		sb.WriteString("\n\n")
	}

	if len(hooks) > 0 {
		sb.WriteString("## 已知伏笔池（请复用 ID，不要新造）\n\n")
		for _, h := range hooks {
			fmt.Fprintf(&sb, "- `%s` [%s]：%s（埋设于第 %d 章）\n",
				h.ID, h.Status, h.Description, h.PlantedAt)
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## 本章正文\n\n")
	sb.WriteString(content)
	sb.WriteString("\n")
	return sb.String()
}

func parseAnalyzerOutput(text string) (*ChapterAnalysis, error) {
	env := parseTaggedEnvelope(text)
	if env == nil {
		return nil, fmt.Errorf("no === TAG === envelope in analyzer output")
	}
	if err := requireTags(env, "SUMMARY", "CHARACTERS", "KEY_EVENTS", "HOOK_TYPE", "DOMINANT_STRAND"); err != nil {
		return nil, err
	}

	a := &ChapterAnalysis{
		Summary:        strings.TrimSpace(env["SUMMARY"]),
		HookType:       strings.ToLower(strings.TrimSpace(env["HOOK_TYPE"])),
		DominantStrand: strings.ToLower(strings.TrimSpace(env["DOMINANT_STRAND"])),
	}
	if a.Summary == "" {
		return nil, fmt.Errorf("summary is empty")
	}
	if !validHookTypes[a.HookType] {
		return nil, fmt.Errorf("invalid hook_type %q (want crisis/mystery/desire/emotion/choice)", a.HookType)
	}
	if !validStrands[a.DominantStrand] {
		return nil, fmt.Errorf("invalid dominant_strand %q (want quest/fire/constellation)", a.DominantStrand)
	}

	if err := decodeJSONArray("characters", env["CHARACTERS"], &a.Characters); err != nil {
		return nil, err
	}
	if len(a.Characters) == 0 {
		return nil, fmt.Errorf("characters array is empty")
	}
	if err := decodeJSONArray("key_events", env["KEY_EVENTS"], &a.KeyEvents); err != nil {
		return nil, err
	}
	if len(a.KeyEvents) == 0 {
		return nil, fmt.Errorf("key_events array is empty")
	}

	if err := decodeOptionalArray("timeline", env["TIMELINE"], &a.TimelineEvents); err != nil {
		return nil, err
	}
	if err := decodeOptionalArray("foreshadow", env["FORESHADOW"], &a.ForeshadowUpdates); err != nil {
		return nil, err
	}
	if err := decodeOptionalArray("relationships", env["RELATIONSHIPS"], &a.RelationshipChanges); err != nil {
		return nil, err
	}
	if err := decodeOptionalArray("state_changes", env["STATE_CHANGES"], &a.StateChanges); err != nil {
		return nil, err
	}
	for i, fu := range a.ForeshadowUpdates {
		if fu.Action == "plant" && strings.TrimSpace(fu.Description) == "" {
			return nil, fmt.Errorf("foreshadow[%d] action=plant requires description (id=%s)", i, fu.ID)
		}
	}
	return a, nil
}

// decodeOptionalArray 允许标签缺失或为空字符串；只在非空时解析。
func decodeOptionalArray(label, body string, out any) error {
	body = stripFences(body)
	if body == "" || body == "[]" {
		return nil
	}
	if err := json.Unmarshal([]byte(body), out); err != nil {
		return fmt.Errorf("parse %s JSON: %w", label, err)
	}
	return nil
}

// PersistChapter 把分析结果落盘：先写章节草稿，再调 commit_chapter 执行原子三件套。
// 已完成章节会被 commit_chapter 自身的幂等检查跳过，仍返回 nil 让循环继续。
func PersistChapter(
	ctx context.Context,
	st *store.Store,
	commitTool *tools.CommitChapterTool,
	chapter int,
	title, content string,
	a *ChapterAnalysis,
) error {
	if a == nil {
		return fmt.Errorf("nil analysis")
	}
	if commitTool == nil {
		return fmt.Errorf("nil commit tool")
	}

	// 1. 落盘草稿（commit_chapter 从 drafts/{ch}.draft.md 读正文）
	if err := st.Drafts.SaveDraft(chapter, content); err != nil {
		return fmt.Errorf("save draft ch%d: %w", chapter, err)
	}

	// 2. 标记进入写作中（ValidateChapterWork 在 FlowWriting 下不阻塞，但 progress 需要这一步保持一致）
	if err := st.Progress.StartChapter(chapter); err != nil {
		return fmt.Errorf("start chapter ch%d: %w", chapter, err)
	}

	// 3. 构造 commit_chapter 入参（注入 chapter title 仅记录用，commit_chapter 不读 title）
	args := map[string]any{
		"chapter":         chapter,
		"summary":         a.Summary,
		"characters":      a.Characters,
		"key_events":      a.KeyEvents,
		"hook_type":       a.HookType,
		"dominant_strand": a.DominantStrand,
	}
	if len(a.TimelineEvents) > 0 {
		args["timeline_events"] = a.TimelineEvents
	}
	if len(a.ForeshadowUpdates) > 0 {
		args["foreshadow_updates"] = a.ForeshadowUpdates
	}
	if len(a.RelationshipChanges) > 0 {
		args["relationship_changes"] = a.RelationshipChanges
	}
	if len(a.StateChanges) > 0 {
		args["state_changes"] = a.StateChanges
	}
	_ = title

	raw, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("marshal commit args ch%d: %w", chapter, err)
	}
	if _, err := commitTool.Execute(ctx, raw); err != nil {
		return fmt.Errorf("commit ch%d: %w", chapter, err)
	}
	return nil
}
