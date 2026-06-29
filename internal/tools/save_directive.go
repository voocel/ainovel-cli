package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// SaveDirectiveTool 持久化用户的长效创作要求（仅 Coordinator 持有）。
// 落盘到 meta/user_directives.json，novel_context 注入 working_memory.user_directives，
// 所有子Proxy每章自动看到——不依赖 Coordinator 派单时人肉转达，跨压缩、跨重启生效。
type SaveDirectiveTool struct {
	store *store.Store
}

func NewSaveDirectiveTool(s *store.Store) *SaveDirectiveTool {
	return &SaveDirectiveTool{store: s}
}

func (t *SaveDirectiveTool) Name() string  { return "save_directive" }
func (t *SaveDirectiveTool) Label() string { return "Lưu长效指令" }

func (t *SaveDirectiveTool) Description() string {
	return "持久化用户的长效创作要求（如\"以后对话占比提高\"\"ChươngTiêu đề只用中文\"）。" +
		"Lưu后所有子Proxy每章都会在 working_memory.user_directives 看到，Không có需再转达。" +
		"action=add 追加一条（text 必填，原样保留用户意图，可适当凝练）；" +
		"action=remove 按序号删除（index 必填，序号见上次Quay lại的列表）。" +
		"Quay lại更Mới后的全量列表。只LưuTrạng thái式要求（任何时候重读都成立的描述）；" +
		"相对式/动作式指令（如\"增加10章\"）禁止Lưu——本工具不派发子Proxy，存了等于没人执行，Vui lòng走子Proxy路由立即处理。"
}

// 写工具，禁止并发。
func (t *SaveDirectiveTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *SaveDirectiveTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *SaveDirectiveTool) ActivityDescription(_ json.RawMessage) string { return "Lưu长效指令" }

func (t *SaveDirectiveTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("action", schema.Enum("操作类型", "add", "remove")).Required(),
		schema.Property("text", schema.String("要求内容（add 时必填）：一句话说清要求，保留用户原意")),
		schema.Property("index", schema.Int("要删除的条目序号（remove 时必填，1-based，见列表Quay lại的 index）")),
	)
}

func (t *SaveDirectiveTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Action string `json:"action"`
		Text   string `json:"text"`
		Index  int    `json:"index"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}

	var (
		list []domain.UserDirective
		err  error
	)
	switch a.Action {
	case "add":
		text := strings.TrimSpace(a.Text)
		if text == "" {
			return nil, fmt.Errorf("add Cần非Rỗng text: %w", errs.ErrToolArgs)
		}
		chapter, total := 0, 0
		if progress, perr := t.store.Progress.Load(); perr == nil && progress != nil {
			chapter = progress.NextChapter()
			total = progress.TotalChapters
		}
		list, err = t.store.Directives.Add(domain.UserDirective{
			Text:          text,
			Chapter:       chapter,
			TotalChapters: total,
			CreatedAt:     time.Now().Format(time.RFC3339),
		})
	case "remove":
		if a.Index < 1 {
			return nil, fmt.Errorf("remove Cần index >= 1: %w", errs.ErrToolArgs)
		}
		list, err = t.store.Directives.Remove(a.Index)
	default:
		return nil, fmt.Errorf("unknown action %q: %w", a.Action, errs.ErrToolArgs)
	}
	if err != nil {
		return nil, err
	}

	items := directiveFacts(list)
	return json.Marshal(map[string]any{
		"saved":      true,
		"directives": items,
		"count":      len(items),
	})
}

// directiveFacts 把长效指令转为给 LLM 的事实视图（工具Kết quả与信封注入同形）：
// at_* 是下达时的Tiến độChụp——指令自 at_chapter 起向后生效，相对式表述可据
// at_total_chapters 判定Có czy không已满足。created_at 是审计信息，不进 LLM。
func directiveFacts(list []domain.UserDirective) []map[string]any {
	items := make([]map[string]any, len(list))
	for i, d := range list {
		items[i] = map[string]any{
			"index":             i + 1,
			"text":              d.Text,
			"at_chapter":        d.Chapter,
			"at_total_chapters": d.TotalChapters,
		}
	}
	return items
}
