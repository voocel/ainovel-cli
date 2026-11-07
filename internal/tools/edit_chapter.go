package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/voocel/agentcore"
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
// 归属检查：仅允许编辑已完成且位于 PendingRewrites 队列中的章节。
//
// 找-换逻辑（多级容错匹配、diff 输出、行尾/BOM 保留）全部复用
// agentcore/tools 的 Workspace.Edit()（v1.8.8 已移除根包 EditTool）。
type EditChapterTool struct {
	store *store.Store
	edit  agentcore.Tool
}

func NewEditChapterTool(s *store.Store) *EditChapterTool {
	return &EditChapterTool{
		store: s,
		// Files 留空：读前校验由调用方流程保证（本工具先自行 ensureDraft）。
		edit: agentcoretools.Workspace{Dir: s.Dir()}.Edit(),
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
	return "仅对已完成且进入 PendingRewrites 队列的章节草稿做定点字符串替换（打磨场景首选，比 draft_chapter 整章重写省 token）。" +
		"新章初稿禁止使用本工具；初稿有硬伤请调用 draft_chapter(mode=\"write\") 整章覆盖。" +
		"找到 old_string 并替换为 new_string，要求精确匹配且唯一（多处匹配需 replace_all=true）。" +
		"old_string 必须从最近一次 read_chapter(source=\"draft\") 的返回中逐字复制，禁止凭记忆重构原文；" +
		"注意返回值是 JSON 字符串，\\n 须还原为真实换行。draft_chapter 改写过草稿后必须先重新 read_chapter 再编辑。" +
		"匹配失败的报错会附上草稿中最接近的候选片段，请从候选逐字复制后重试。" +
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

	// 归属检查：机械落实 writer 协议。新章初稿只能整章覆盖，不能依赖
	// 模型自行遵守提示词后仍把脆弱的精确编辑暴露为可执行路径。
	completed, err := t.store.Progress.IsChapterCompleted(a.Chapter)
	if err != nil {
		return nil, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
	}
	if !completed {
		return nil, fmt.Errorf("第 %d 章尚未完成，初稿禁止使用 edit_chapter；有硬伤请调用 draft_chapter(mode=\"write\", chapter=%d) 整章覆盖: %w", a.Chapter, a.Chapter, errs.ErrToolPrecondition)
	}
	progress, err := t.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
	}
	if progress == nil || !slices.Contains(progress.PendingRewrites, a.Chapter) {
		return nil, fmt.Errorf("第 %d 章已完成且不在 PendingRewrites 队列中，不能编辑；需修改请先由 editor 评审触发重写/打磨: %w", a.Chapter, errs.ErrToolPrecondition)
	}
	if err := EnsureChapterExpanded(t.store, a.Chapter); err != nil {
		return nil, err
	}

	// Seed：drafts 不存在时从 chapters 复制一份作为起点
	if err := t.ensureDraft(a.Chapter); err != nil {
		return nil, err
	}

	// agentcore v1.8.3 的 edit 在精确与宽松匹配都失败后，会对三行及以上的 old_string
	// 按首尾行锚定整块替换，中间对不上的正文被静默删掉（上游 v1.8.5 已删除这一级）。
	// 这类多行编辑必须逐字命中草稿，不交给上游去猜。
	if old := strings.ReplaceAll(a.OldString, "\r\n", "\n"); strings.Count(strings.TrimSuffix(old, "\n"), "\n") >= 2 {
		draft, err := t.store.Drafts.LoadDraft(a.Chapter)
		if err != nil {
			return nil, fmt.Errorf("load draft: %w: %w", errs.ErrStoreRead, err)
		}
		if !strings.Contains(strings.ReplaceAll(draft, "\r\n", "\n"), old) {
			return nil, fmt.Errorf("多行 old_string 未在第 %d 章草稿中逐字出现：请重新 read_chapter(source=\"draft\") 后逐字复制，或只改一两行: %w", a.Chapter, errs.ErrToolPrecondition)
		}
	}

	// 委托 agentcore/tools 的 edit 完成找-换。
	// v1.8.8 的 Tool 拆成 Check（预览/校验，不写盘）与 Run（写盘），
	// 用 Check 拿到与旧版一致的 diff，再交给 Run 落盘。
	relPath := fmt.Sprintf("drafts/%02d.draft.md", a.Chapter)
	// 用绝对路径：Workspace 会优先按 ctx 里的 cwd 解析相对路径，
	// 绝对路径可确保只改本次 store 目录下的草稿。
	absPath, err := filepath.Abs(filepath.Join(t.store.Dir(), filepath.FromSlash(relPath)))
	if err != nil {
		return nil, fmt.Errorf("resolve draft path: %w: %w", errs.ErrStoreRead, err)
	}
	subArgs, _ := json.Marshal(map[string]any{
		"file_path":   absPath,
		"old_string":  a.OldString,
		"new_string":  a.NewString,
		"replace_all": a.ReplaceAll,
	})
	diff, err := t.edit.Check(ctx, subArgs)
	if err != nil {
		return nil, fmt.Errorf("apply edit: %w: %w", errs.ErrToolPrecondition, err)
	}
	if _, err := t.edit.Run(ctx, subArgs); err != nil {
		return nil, fmt.Errorf("apply edit: %w: %w", errs.ErrToolPrecondition, err)
	}

	if _, err := t.store.Checkpoints.AppendArtifact(
		domain.ChapterScope(a.Chapter), "edit", relPath,
	); err != nil {
		return nil, fmt.Errorf("checkpoint edit: %w: %w", errs.ErrStoreWrite, err)
	}

	// 附加指引：让 writer 知道后续步骤，避免遗漏 check_consistency / commit_chapter
	return json.Marshal(map[string]any{
		"message":   fmt.Sprintf("Successfully replaced text in %s.", relPath),
		"diff":      diff,
		"chapter":   a.Chapter,
		"next_step": "edit 已落盘。仍有硬伤可再次 edit_chapter；否则 check_consistency 后 commit_chapter",
	})
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
