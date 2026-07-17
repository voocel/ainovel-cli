package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/voocel/agentcore/schema"
	agentcoretools "github.com/voocel/agentcore/tools"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// EditChapterTool 对章节草稿做定点字符串替换，适用于打磨场景。
// 相比 draft_chapter 整章重写，token 节省 10x+。
//
// 落盘契约：只改 drafts/{ch:02d}.draft.md，禁止直接改 chapters/（终稿由 commit_chapter 独占）。
// Seed 语义：drafts 不存在但 chapters 有 → 自动把 chapters 复制到 drafts 作为起点。
// 归属检查：章节已完成时必须在 PendingRewrites 队列中，否则拒绝。
//
// 本工具是 agentcore.EditTool 的薄封装，找-换逻辑（多级容错匹配、diff 输出、行尾/BOM 保留）
// 全部复用上游实现。
type EditChapterTool struct {
	store *store.Store
	edit  *agentcoretools.EditTool
}

func NewEditChapterTool(s *store.Store) *EditChapterTool {
	return &EditChapterTool{
		store: s,
		edit:  agentcoretools.NewEdit(s.Dir(), nil),
	}
}

func (t *EditChapterTool) Name() string  { return "edit_chapter" }
func (t *EditChapterTool) Label() string { return "编辑章节" }

// ReadOnly 明确声明写工具（配合 ConcurrencySafeTool 防止被并发调度）。
func (t *EditChapterTool) ReadOnly(_ json.RawMessage) bool { return false }

// ConcurrencySafe 显式禁止并发：同章节多次 edit_chapter 并行会读-改-写竞态，
// 即使不同章节并行也会穿插 checkpoint 顺序。统一串行最稳。
func (t *EditChapterTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

// ActivityDescription 供 UI/日志展示当前工具的活动描述。
func (t *EditChapterTool) ActivityDescription(_ json.RawMessage) string { return "编辑章节草稿" }

func (t *EditChapterTool) Description() string {
	return "对章节草稿做定点字符串替换（打磨场景首选，比 draft_chapter 整章重写省 token）。" +
		"找到 old_string 并替换为 new_string，要求精确匹配且唯一（多处匹配需 replace_all=true）。" +
		"写入 drafts/{ch}.draft.md；drafts 不存在时自动从 chapters 播种。" +
		"章节已完成且不在 PendingRewrites 队列中时拒绝执行。每次调用只改一处，多处修改请多次调用。"
}

func (t *EditChapterTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("chapter", schema.Int("章节号")).Required(),
		schema.Property("old_string", schema.String("要替换的原文精确片段，多行需包含换行；不加 replace_all 时必须在草稿中唯一出现")).Required(),
		schema.Property("new_string", schema.String("替换后的新文本")).Required(),
		schema.Property("replace_all", schema.Bool("替换所有匹配（默认 false）")),
	)
}

func (t *EditChapterTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapter    int    `json:"chapter"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if a.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}
	if a.OldString == "" {
		return nil, fmt.Errorf("old_string 不能为空: %w", errs.ErrToolArgs)
	}
	if a.OldString == a.NewString {
		return nil, fmt.Errorf("old_string 与 new_string 相同，无需修改: %w", errs.ErrToolArgs)
	}
	if err := t.store.Progress.ValidateChapterWork(a.Chapter); err != nil {
		return nil, err
	}
	if err := EnsureChapterExpanded(t.store, a.Chapter); err != nil {
		return nil, err
	}

	// 归属检查：已完成章节必须在重写队列中，避免污染终稿
	completed, err := t.store.Progress.IsChapterCompleted(a.Chapter)
	if err != nil {
		return nil, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
	}
	if completed {
		progress, err := t.store.Progress.Load()
		if err != nil {
			return nil, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
		}
		if progress == nil || !slices.Contains(progress.PendingRewrites, a.Chapter) {
			return nil, fmt.Errorf("第 %d 章已完成且不在 PendingRewrites 队列中，不能编辑；需修改请先由 editor 评审触发重写/打磨: %w", a.Chapter, errs.ErrToolPrecondition)
		}
	}

	// Seed：drafts 不存在时从 chapters 复制一份作为起点
	if err := t.ensureDraft(a.Chapter); err != nil {
		return nil, err
	}

	// 委托 agentcore.EditTool 完成找-换
	subArgs, _ := json.Marshal(map[string]any{
		"path":        fmt.Sprintf("drafts/%02d.draft.md", a.Chapter),
		"file_path":   fmt.Sprintf("drafts/%02d.draft.md", a.Chapter),
		"old_text":    a.OldString,
		"old_string":  a.OldString,
		"new_text":    a.NewString,
		"new_string":  a.NewString,
		"replace_all": a.ReplaceAll,
	})
	result, err := t.edit.Execute(ctx, subArgs)
	if err != nil {
		return nil, fmt.Errorf("apply edit: %w: %w", errs.ErrToolPrecondition, err)
	}

	if _, err := t.store.Checkpoints.AppendArtifact(
		domain.ChapterScope(a.Chapter), "edit",
		fmt.Sprintf("drafts/%02d.draft.md", a.Chapter),
	); err != nil {
		return nil, fmt.Errorf("checkpoint edit: %w: %w", errs.ErrStoreWrite, err)
	}

	// 附加指引：让 writer 知道后续步骤，避免遗漏 check_consistency / commit_chapter
	var passthrough map[string]any
	if err := json.Unmarshal(result, &passthrough); err != nil {
		return result, nil
	}
	passthrough["chapter"] = a.Chapter
	passthrough["next_step"] = "edit 已落盘。仍有硬伤可再次 edit_chapter；否则 check_consistency 后 commit_chapter"
	return json.Marshal(passthrough)
}

// ensureDraft 保证 drafts/{ch}.draft.md 存在：
//   - 已有草稿 → 直接返回
//   - 无草稿但有终稿 → 把终稿复制到 drafts 作为修改起点（常见于打磨场景）
//   - 都没有 → 报错，提示先用 draft_chapter 创建初稿
func (t *EditChapterTool) ensureDraft(chapter int) error {
	draft, err := t.store.Drafts.LoadDraft(chapter)
	if err != nil {
		return fmt.Errorf("load draft: %w: %w", errs.ErrStoreRead, err)
	}
	if draft != "" {
		return nil
	}
	text, err := t.store.Drafts.LoadChapterText(chapter)
	if err != nil {
		return fmt.Errorf("load chapter: %w: %w", errs.ErrStoreRead, err)
	}
	if text == "" {
		return fmt.Errorf("第 %d 章无草稿也无终稿，请先调 draft_chapter(mode=write, chapter=%d) 创建初稿: %w", chapter, chapter, errs.ErrToolPrecondition)
	}
	if err := t.store.Drafts.SaveDraft(chapter, text); err != nil {
		return fmt.Errorf("seed draft from chapter: %w: %w", errs.ErrStoreWrite, err)
	}
	return nil
}
