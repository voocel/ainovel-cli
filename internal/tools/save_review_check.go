package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/flow"
	"github.com/voocel/ainovel-cli/internal/store"
)

// SaveReviewCheckTool 只保存原问题的一次复核结果，不再驱动返工队列。
type SaveReviewCheckTool struct{ store *store.Store }

func NewSaveReviewCheckTool(st *store.Store) *SaveReviewCheckTool {
	return &SaveReviewCheckTool{store: st}
}

func (t *SaveReviewCheckTool) Name() string                         { return "save_review_check" }
func (t *SaveReviewCheckTool) Label() string                        { return "保存返工复核" }
func (t *SaveReviewCheckTool) ReadOnly(json.RawMessage) bool        { return false }
func (t *SaveReviewCheckTool) ConcurrencySafe(json.RawMessage) bool { return false }
func (t *SaveReviewCheckTool) StrictSchema() bool                   { return true }
func (t *SaveReviewCheckTool) Description() string {
	return "整批正文返工结束后保存一次定向复核。仅核对 working_memory.review_recheck 中的原问题；未解决项引用原 issue 序号并附当前正文证据，不会再自动入队。"
}

func (t *SaveReviewCheckTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("chapter", schema.Int("原评审的章节号")).Required(),
		schema.Property("scope", schema.Enum("原评审范围", "chapter", "arc", "global")).Required(),
		schema.Property("summary", schema.String("针对原问题的复核结论，不重做全量评分")).Required(),
		schema.Property("unresolved_issues", schema.Array("仍未解决的原问题；全部解决时传空数组", schema.Object(
			schema.Property("issue", schema.Int("原评审 issues 中的序号，从 1 开始；以 review_recheck.issues[].number 为准")).Required(),
			schema.Property("evidence", schema.String("当前正文中证明问题仍存在的具体证据")).Required(),
		))).Required(),
	)
}

func (t *SaveReviewCheckTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapter int    `json:"chapter"`
		Scope   string `json:"scope"`
		domain.ReviewRecheckResult
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if a.Chapter <= 0 || strings.TrimSpace(a.Summary) == "" || a.UnresolvedIssues == nil {
		return nil, fmt.Errorf("chapter, summary and unresolved_issues are required: %w", errs.ErrToolArgs)
	}
	if a.Basis != nil {
		return nil, fmt.Errorf("basis is captured by the system; do not submit it: %w", errs.ErrToolArgs)
	}
	var review *domain.ReviewEntry
	var err error
	switch a.Scope {
	case "chapter", "arc":
		review, err = t.store.World.LoadReview(a.Chapter)
	case "global":
		review, err = t.store.World.LoadGlobalReview(a.Chapter)
	default:
		return nil, fmt.Errorf("invalid scope: %q: %w", a.Scope, errs.ErrToolArgs)
	}
	if err != nil {
		return nil, fmt.Errorf("load original review: %w: %w", errs.ErrStoreRead, err)
	}
	if review == nil || review.Scope != a.Scope || review.Recheck == nil {
		return nil, fmt.Errorf("原评审没有待复核正文返工: %w", errs.ErrToolPrecondition)
	}
	seen := make(map[int]bool)
	for _, issue := range a.UnresolvedIssues {
		if !review.RecheckIssue(issue.Issue) || seen[issue.Issue] || strings.TrimSpace(issue.Evidence) == "" {
			return nil, fmt.Errorf("unresolved issue %d 必须属于本次复核范围、不得重复且须附正文证据: %w", issue.Issue, errs.ErrToolArgs)
		}
		seen[issue.Issue] = true
	}
	if review.Recheck.Result != nil {
		original := *review.Recheck.Result
		original.Basis = nil
		if !reflect.DeepEqual(original, a.ReviewRecheckResult) {
			return nil, fmt.Errorf("复核已完成，不可覆盖或启动第二轮: %w", errs.ErrToolConflict)
		}
	} else {
		state, err := flow.LoadState(t.store)
		if err != nil {
			return nil, fmt.Errorf("load recheck state: %w: %w", errs.ErrStoreRead, err)
		}
		due := state.ReviewRecheck
		if state.Progress == nil || state.Progress.Phase != domain.PhaseWriting || len(state.Progress.PendingRewrites) > 0 ||
			due == nil || due.Chapter != a.Chapter || due.Scope != a.Scope {
			return nil, fmt.Errorf("须先排空全部返工，再按路由指定目标复核: %w", errs.ErrToolPrecondition)
		}
		a.Basis, err = t.store.ChapterRecords.LoadVersions(review.Recheck.Chapters)
		if err != nil {
			return nil, fmt.Errorf("capture review check basis: %w: %w", errs.ErrStoreRead, err)
		}
		review.Recheck.Result = &a.ReviewRecheckResult
		if err := t.store.World.SaveReview(*review); err != nil {
			return nil, fmt.Errorf("save review check: %w: %w", errs.ErrStoreWrite, err)
		}
	}
	artifact := fmt.Sprintf("reviews/%02d.json", a.Chapter)
	if a.Scope == "global" {
		artifact = fmt.Sprintf("reviews/%02d-global.json", a.Chapter)
	}
	if _, err := t.store.Checkpoints.AppendArtifact(domain.ChapterScope(a.Chapter), "review_check", artifact); err != nil {
		return nil, fmt.Errorf("checkpoint review check: %w: %w", errs.ErrStoreWrite, err)
	}
	progress, err := t.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress after recheck: %w: %w", errs.ErrStoreRead, err)
	}
	complete, err := ReconcileRewrittenBook(t.store, progress)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"saved": true, "chapter": a.Chapter, "scope": a.Scope,
		"summary":  a.Summary,
		"resolved": len(a.UnresolvedIssues) == 0, "unresolved_issues": a.UnresolvedIssues,
		"recheck_complete": true, "book_complete": complete,
	})
}
