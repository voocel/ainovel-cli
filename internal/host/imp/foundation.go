package imp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// FoundationResult 是 Foundation 反推的结构化产物。
type FoundationResult struct {
	Premise    string                 // Markdown 字符串
	Characters []domain.Character     // 角色档案
	WorldRules []domain.WorldRule     // 世界规则
	Outline    []domain.OutlineEntry  // 章节大纲，长度 = len(chapters)
}

// LLMChat 是 imp 包对 ChatModel 的最小依赖：仅需要一次普通文本生成。
// 抽出独立接口便于单测注入 mock，避免直接耦合 agentcore 客户端。
type LLMChat interface {
	Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error)
}

// ReverseFoundation 用一次 LLM 调用，从已切分的章节正文反推 foundation。
// 不调用 save_foundation，纯函数；持久化由调用方决定。
func ReverseFoundation(ctx context.Context, llm LLMChat, systemPrompt string, chapters []Chapter) (*FoundationResult, error) {
	if len(chapters) == 0 {
		return nil, fmt.Errorf("no chapters to analyze")
	}
	if llm == nil {
		return nil, fmt.Errorf("llm is nil")
	}

	system := strings.ReplaceAll(systemPrompt, "${chapter_count}", fmt.Sprintf("%d", len(chapters)))
	user := buildFoundationUserPrompt(chapters)

	resp, err := llm.Generate(ctx, []agentcore.Message{
		agentcore.SystemMsg(system),
		agentcore.UserMsg(user),
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("llm generate: %w", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("llm returned nil response")
	}

	return parseFoundationOutput(resp.Message.TextContent(), len(chapters))
}

// buildFoundationUserPrompt 拼装用户提示：所有章节顺序拼接，附章号锚点便于 LLM 引用。
func buildFoundationUserPrompt(chapters []Chapter) string {
	var sb strings.Builder
	sb.WriteString("以下是已完成的 ")
	fmt.Fprintf(&sb, "%d", len(chapters))
	sb.WriteString(" 章正文。请严格按系统提示反推 foundation，输出四个 === TAG === 段。\n\n")
	for i, ch := range chapters {
		fmt.Fprintf(&sb, "## 第 %d 章：%s\n\n", i+1, ch.Title)
		sb.WriteString(ch.Content)
		sb.WriteString("\n\n---\n\n")
	}
	return sb.String()
}

// parseFoundationOutput 解析 LLM 输出的 envelope 并校验关键约束。
func parseFoundationOutput(text string, expectChapters int) (*FoundationResult, error) {
	env := parseTaggedEnvelope(text)
	if env == nil {
		return nil, fmt.Errorf("no === TAG === envelope found in LLM output")
	}
	if err := requireTags(env, "PREMISE", "CHARACTERS", "WORLD_RULES", "OUTLINE"); err != nil {
		return nil, err
	}

	premise := stripFences(env["PREMISE"])
	if !strings.HasPrefix(strings.TrimLeft(premise, " \t\n"), "#") {
		return nil, fmt.Errorf("premise must start with a Markdown heading line (# 书名)")
	}

	var characters []domain.Character
	if err := decodeJSONArray("characters", env["CHARACTERS"], &characters); err != nil {
		return nil, err
	}
	if len(characters) == 0 {
		return nil, fmt.Errorf("characters array is empty")
	}

	var worldRules []domain.WorldRule
	if err := decodeJSONArray("world_rules", env["WORLD_RULES"], &worldRules); err != nil {
		return nil, err
	}

	var outline []domain.OutlineEntry
	if err := decodeJSONArray("outline", env["OUTLINE"], &outline); err != nil {
		return nil, err
	}
	if len(outline) != expectChapters {
		return nil, fmt.Errorf("outline length mismatch: got %d, want %d", len(outline), expectChapters)
	}
	for i := range outline {
		if outline[i].Chapter != i+1 {
			outline[i].Chapter = i + 1
		}
	}

	return &FoundationResult{
		Premise:    premise,
		Characters: characters,
		WorldRules: worldRules,
		Outline:    outline,
	}, nil
}

// PersistFoundation 把反推结果写入 Store，顺序与 Architect 短篇 prompt 一致：
// premise → characters → world_rules → outline。每步都触发 save_foundation 同款落盘逻辑。
//
// 不直接调 SaveFoundationTool 是因为这里是确定性回放，无需走 LLM 工具调度。
// 但保持与 SaveFoundationTool 相同的副作用：phase 推进、checkpoint 追加。
func PersistFoundation(ctx context.Context, st *store.Store, scale domain.PlanningTier, fr *FoundationResult) error {
	if fr == nil {
		return fmt.Errorf("nil foundation result")
	}
	if err := st.RunMeta.SetPlanningTier(scale); err != nil {
		return fmt.Errorf("save planning tier: %w", err)
	}

	// 1. premise
	if err := st.Outline.SavePremise(fr.Premise); err != nil {
		return fmt.Errorf("save premise: %w", err)
	}
	if name := domain.ExtractNovelNameFromPremise(fr.Premise); name != "" {
		_ = st.Progress.SetNovelName(name)
	}
	_ = st.Progress.UpdatePhase(domain.PhasePremise)
	_, _ = st.Checkpoints.Append(domain.GlobalScope(), "premise", "", "")

	// 2. characters
	if err := st.Characters.Save(fr.Characters); err != nil {
		return fmt.Errorf("save characters: %w", err)
	}
	_, _ = st.Checkpoints.Append(domain.GlobalScope(), "characters", "", "")

	// 3. world_rules
	if err := st.World.SaveWorldRules(fr.WorldRules); err != nil {
		return fmt.Errorf("save world_rules: %w", err)
	}
	_, _ = st.Checkpoints.Append(domain.GlobalScope(), "world_rules", "", "")

	// 4. outline (扁平模式，与短篇 architect 一致)
	if err := st.Outline.SaveOutline(fr.Outline); err != nil {
		return fmt.Errorf("save outline: %w", err)
	}
	_ = st.Progress.UpdatePhase(domain.PhaseOutline)
	_ = st.Progress.SetTotalChapters(len(fr.Outline))
	_ = st.Progress.SetLayered(false)
	_ = st.Progress.UpdateVolumeArc(0, 0)
	_ = st.Outline.ClearLayeredOutline()
	_, _ = st.Checkpoints.Append(domain.GlobalScope(), "outline", "", "")

	// 5. foundation 完整 → 推进到 writing 阶段（与 save_foundation 末尾逻辑一致）
	if len(st.FoundationMissing()) == 0 {
		if p, _ := st.Progress.Load(); p != nil &&
			p.Phase != domain.PhaseWriting && p.Phase != domain.PhaseComplete {
			_ = st.Progress.UpdatePhase(domain.PhaseWriting)
		}
	}
	return nil
}

// decodeJSONArray 解析 JSON 数组并附上行列错误，便于调试。
func decodeJSONArray(label, body string, out any) error {
	body = stripFences(body)
	if body == "" {
		return fmt.Errorf("%s body is empty", label)
	}
	if err := json.Unmarshal([]byte(body), out); err != nil {
		return fmt.Errorf("parse %s JSON: %w", label, err)
	}
	return nil
}

// stripFences 去掉首尾 ``` 代码围栏（含语言标签），LLM 偶尔会自作主张包一层。
func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if j := strings.LastIndex(s, "```"); j >= 0 {
		s = s[:j]
	}
	return strings.TrimSpace(s)
}
