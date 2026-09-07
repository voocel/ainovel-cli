package domain

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// Directive 是用户对 AI 的正式创作要求（§4.9）：回答"用户要什么"，与 Ownership
// （AI 不得动什么）、Intent（这本书是什么）正交。作者只能是用户，原话保留不翻译；
// 按作用域进入任务输入，由审阅逐项核验，可机械校验的字段走确定性校验器。
type Directive struct {
	ID          string                `json:"id"`
	Scope       string                `json:"scope"`
	Text        string                `json:"text"`
	Constraints *DirectiveConstraints `json:"constraints,omitempty"`
	Status      DirectiveStatus       `json:"status"`
}

type DirectiveStatus string

const (
	DirectiveActive  DirectiveStatus = "active"
	DirectiveRetired DirectiveStatus = "retired"
)

// 作用域字面量（§4.9）：
//
//	project                    全书
//	plan_node:<id>             某个 Plan 节点及其后代章节
//	chapter_range:<from>-<to>  章号闭区间
//	from_chapter:<n>           自第 n 章起
const (
	DirectiveScopeProject = "project"
	scopePlanNode         = "plan_node:"
	scopeChapterRange     = "chapter_range:"
	scopeFromChapter      = "from_chapter:"
)

// DirectiveScopePlanNode 构造命中某个 Plan 节点及其子树的作用域。
func DirectiveScopePlanNode(id string) string { return scopePlanNode + id }

// DirectiveConstraints 是可机械校验的量化约束（S13）。字数口径：各 block 正文
// 的 rune 数累加，不含标题。
type DirectiveConstraints struct {
	TargetWords int `json:"target_words,omitempty"`
	MinWords    int `json:"min_words,omitempty"`
	MaxWords    int `json:"max_words,omitempty"`
}

// targetWordsTolerance 是只给 target_words 时推导硬区间的容差：量化约束不得
// 降级为软偏好，所以"3000 字左右"必须落成可判定的区间。
const targetWordsTolerance = 0.1

func (c *DirectiveConstraints) Validate() error {
	if c == nil {
		return nil
	}
	if c.TargetWords < 0 || c.MinWords < 0 || c.MaxWords < 0 {
		return fmt.Errorf("directive word constraints cannot be negative: %w", ErrInvalid)
	}
	if c.MinWords > 0 && c.MaxWords > 0 && c.MinWords > c.MaxWords {
		return fmt.Errorf("directive min_words exceeds max_words: %w", ErrInvalid)
	}
	if c.TargetWords > 0 && ((c.MinWords > 0 && c.TargetWords < c.MinWords) ||
		(c.MaxWords > 0 && c.TargetWords > c.MaxWords)) {
		return fmt.Errorf("directive target_words falls outside min/max: %w", ErrInvalid)
	}
	return nil
}

// Bounds 返回字数硬区间；0 表示该侧无界，两侧皆无界时 ok=false。
func (c *DirectiveConstraints) Bounds() (min, max int, ok bool) {
	if c == nil {
		return 0, 0, false
	}
	min, max = c.MinWords, c.MaxWords
	if c.TargetWords > 0 {
		if min == 0 {
			min = int(math.Round(float64(c.TargetWords) * (1 - targetWordsTolerance)))
		}
		if max == 0 {
			max = int(math.Round(float64(c.TargetWords) * (1 + targetWordsTolerance)))
		}
	}
	return min, max, min > 0 || max > 0
}

func (v Directive) Validate() error {
	if strings.TrimSpace(v.ID) == "" || strings.TrimSpace(v.Text) == "" {
		return fmt.Errorf("directive id and text are required: %w", ErrInvalid)
	}
	if _, err := parseDirectiveScope(v.Scope); err != nil {
		return err
	}
	switch v.Status {
	case DirectiveActive, DirectiveRetired:
	default:
		return fmt.Errorf("unknown directive status %q: %w", v.Status, ErrInvalid)
	}
	return v.Constraints.Validate()
}

// DirectiveTarget 是作用域匹配的对象：章号与该章 Plan 节点及其祖先。
type DirectiveTarget struct {
	ChapterNumber int
	PlanNodeIDs   []string
}

// Covers 报告作用域是否命中目标；不看 status，调用方自行过滤。
func (v Directive) Covers(target DirectiveTarget) bool {
	scope, err := parseDirectiveScope(v.Scope)
	if err != nil {
		return false
	}
	if scope.planNodeID != "" {
		return slices.Contains(target.PlanNodeIDs, scope.planNodeID)
	}
	if scope.from > 0 && target.ChapterNumber < scope.from {
		return false
	}
	return scope.to == 0 || target.ChapterNumber <= scope.to
}

// ActiveDirectives 返回全部 active Directive，按 ID 排序保证确定性。
func ActiveDirectives(directives []Directive) []Directive {
	var active []Directive
	for _, directive := range directives {
		if directive.Status == DirectiveActive {
			active = append(active, directive)
		}
	}
	SortDirectives(active)
	return active
}

// ActiveDirectivesFor 返回命中目标的 active Directive，按 ID 排序保证确定性。
func ActiveDirectivesFor(directives []Directive, target DirectiveTarget) []Directive {
	var matched []Directive
	for _, directive := range ActiveDirectives(directives) {
		if directive.Covers(target) {
			matched = append(matched, directive)
		}
	}
	return matched
}

func SortDirectives(directives []Directive) {
	slices.SortFunc(directives, func(left, right Directive) int {
		return strings.Compare(left.ID, right.ID)
	})
}

type directiveScope struct {
	planNodeID string
	from, to   int // 章号区间，0 表示无界
}

func parseDirectiveScope(scope string) (directiveScope, error) {
	invalid := func() (directiveScope, error) {
		return directiveScope{}, fmt.Errorf("invalid directive scope %q: %w", scope, ErrInvalid)
	}
	switch {
	case scope == DirectiveScopeProject:
		return directiveScope{}, nil
	case strings.HasPrefix(scope, scopePlanNode):
		id := strings.TrimPrefix(scope, scopePlanNode)
		if strings.TrimSpace(id) == "" {
			return invalid()
		}
		return directiveScope{planNodeID: id}, nil
	case strings.HasPrefix(scope, scopeChapterRange):
		from, to, ok := strings.Cut(strings.TrimPrefix(scope, scopeChapterRange), "-")
		start, err1 := strconv.Atoi(from)
		end, err2 := strconv.Atoi(to)
		if !ok || err1 != nil || err2 != nil || start < 1 || end < start {
			return invalid()
		}
		return directiveScope{from: start, to: end}, nil
	case strings.HasPrefix(scope, scopeFromChapter):
		start, err := strconv.Atoi(strings.TrimPrefix(scope, scopeFromChapter))
		if err != nil || start < 1 {
			return invalid()
		}
		return directiveScope{from: start}, nil
	}
	return invalid()
}
