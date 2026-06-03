// internal/tools/draft_persona.go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// DraftPersonaTool 是竞稿模式下的章节草稿工具。它绑定一个 persona slug，
// 候选阶段写隔离候选槽（drafts/NN.cand-<slug>.md），
// 润色阶段（本 persona 中选且已提升）写正式 draft.md。
// Name 仍为 "draft_chapter"，使 writer prompt 与 StopAfterTools 无需改动。
// 阶段每次 Execute 独立判定：Promoted 置位前写候选槽，置位后（且本 persona 中选）写正式 draft.md。
type DraftPersonaTool struct {
	store   *store.Store
	persona string
}

// NewDraftPersonaTool 创建绑定指定 persona 的草稿工具。
func NewDraftPersonaTool(store *store.Store, persona string) *DraftPersonaTool {
	return &DraftPersonaTool{store: store, persona: persona}
}

func (t *DraftPersonaTool) Name() string  { return "draft_chapter" }
func (t *DraftPersonaTool) Label() string { return "写入章节(竞稿)" }
func (t *DraftPersonaTool) Description() string {
	return "写入章节正文。竞稿候选阶段保存为你的候选稿；中选润色阶段保存为正式草稿。mode=write 覆盖，mode=append 追加。"
}

// 写工具，禁止并发（读-改-写竞态）。
func (t *DraftPersonaTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *DraftPersonaTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

// StrictSchema 启用 OpenAI strict tool calling，与 DraftChapterTool 保持一致。
func (t *DraftPersonaTool) StrictSchema() bool { return true }

func (t *DraftPersonaTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("chapter", schema.Int("章节号")).Required(),
		schema.Property("content", schema.String("章节正文")).Required(),
		schema.Property("mode", schema.Enum("写入模式", "write", "append")).Required(),
	)
}

func (t *DraftPersonaTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapter int    `json:"chapter"`
		Content string `json:"content"`
		Mode    string `json:"mode"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if a.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}
	if a.Content == "" {
		return nil, fmt.Errorf("content must not be empty: %w", errs.ErrToolArgs)
	}

	// 判定阶段：本 persona 是否为已提升的中选稿 → 润色阶段写 draft.md。
	// 其余情况（无 verdict / 未提升 / 非中选 persona）均为候选阶段。
	polish := false
	if v, _ := t.store.Contest.LoadVerdict(a.Chapter); v != nil && v.Promoted && v.Winner == t.persona {
		polish = true
	}

	var artifact string
	if polish {
		// 润色阶段：写正式草稿
		artifact = fmt.Sprintf("drafts/%02d.draft.md", a.Chapter)
		if a.Mode == "append" {
			if err := t.store.Drafts.AppendDraft(a.Chapter, a.Content); err != nil {
				return nil, fmt.Errorf("append draft: %w", err)
			}
		} else if err := t.store.Drafts.SaveDraft(a.Chapter, a.Content); err != nil {
			return nil, fmt.Errorf("save draft: %w", err)
		}
	} else {
		// 候选阶段：写隔离候选槽
		artifact = fmt.Sprintf("drafts/%02d.cand-%s.md", a.Chapter, t.persona)
		content := a.Content
		if a.Mode == "append" {
			// append 时先读旧内容拼接，再整体覆盖写入
			if old, _ := t.store.Contest.LoadCandidate(a.Chapter, t.persona); old != "" {
				content = old + "\n\n" + a.Content
			}
		}
		if err := t.store.Contest.SaveCandidate(a.Chapter, t.persona, content); err != nil {
			return nil, fmt.Errorf("save candidate: %w", err)
		}
	}

	// 追加 draft checkpoint（与 DraftChapterTool 保持一致）
	if _, err := t.store.Checkpoints.AppendArtifact(domain.ChapterScope(a.Chapter), "draft", artifact); err != nil {
		return nil, fmt.Errorf("checkpoint draft: %w", err)
	}

	phase := "candidate"
	// 候选阶段必须就此结束等待评审，绝不能引导 writer 调 commit_chapter，
	// 否则与 CandidateStopGuard 冲突造成无效 LLM 调用。
	nextStep := "候选稿已保存。本轮到此结束，等待评审。不要调用 check_consistency 或 commit_chapter。"
	if polish {
		phase = "polish"
		nextStep = "润色稿已保存。请继续 check_consistency，然后 commit_chapter 提交终稿。"
	}
	return json.Marshal(map[string]any{
		"written":    true,
		"chapter":    a.Chapter,
		"persona":    t.persona,
		"phase":      phase,
		"mode":       a.Mode,
		"word_count": utf8.RuneCountInString(a.Content),
		"next_step":  nextStep,
	})
}
