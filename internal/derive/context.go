package derive

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// StoryContextKind 是上下文的显式 schema 版本：结构演进时必须升版，
// 旧 Execution Profile 仍按其记录的版本解释，不做静默兼容。
const StoryContextKind = "story_context.v1"

func ContextKey(kind domain.OperationKind, task json.RawMessage) (string, error) {
	if kind == "" || len(task) == 0 || !json.Valid(task) {
		return "", fmt.Errorf("context operation kind and task are required: %w", domain.ErrInvalid)
	}
	decoder := json.NewDecoder(bytes.NewReader(task))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return "", fmt.Errorf("context task must contain exactly one JSON value: %w", domain.ErrInvalid)
	}
	return domain.DigestJSON(struct {
		Version string               `json:"version"`
		Kind    domain.OperationKind `json:"kind"`
		Task    any                  `json:"task"`
	}{Version: StoryContextKind, Kind: kind, Task: value})
}

type ProjectContent struct {
	ID         string
	Revision   domain.Revision
	Intent     domain.Intent
	Plan       []domain.PlanNode
	Canon      []domain.CanonFact
	Manuscript []domain.ManuscriptChapter
	Ownership  []domain.OwnershipRule
}

type ContextDocument struct {
	Ref     domain.DocumentRef `json:"ref"`
	Content json.RawMessage    `json:"content"`
}

type ChapterIndexEntry struct {
	ID         string `json:"id"`
	PlanNodeID string `json:"plan_node_id"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	Blocks     int    `json:"blocks"`
}

type StoryContext struct {
	SchemaVersion   string                 `json:"schema_version"`
	ProjectID       string                 `json:"project_id"`
	Revision        domain.Revision        `json:"revision"`
	Intent          domain.Intent          `json:"intent"`
	Documents       []ContextDocument      `json:"relevant_documents"`
	ManuscriptIndex []ChapterIndexEntry    `json:"manuscript_index"`
	Ownership       []domain.OwnershipRule `json:"ownership"`
}

func BuildStoryContext(content ProjectContent, kind domain.OperationKind, task json.RawMessage) (StoryContext, error) {
	if strings.TrimSpace(content.ID) == "" || content.Revision <= domain.InitialRevision || len(task) == 0 || !json.Valid(task) {
		return StoryContext{}, fmt.Errorf("context project, positive revision and task are required: %w", domain.ErrInvalid)
	}
	if err := content.Intent.Validate(); err != nil {
		return StoryContext{}, err
	}
	documents := make(map[string]ContextDocument, len(content.Plan)+len(content.Canon)+len(content.Manuscript))
	addValue := func(ref domain.DocumentRef, value any) error {
		payload, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("encode context document %q: %w", ref.Key(), err)
		}
		if err := domain.ValidateDocumentContent(ref, payload); err != nil {
			return err
		}
		documents[ref.Key()] = ContextDocument{Ref: ref, Content: payload}
		return nil
	}
	for _, node := range content.Plan {
		if err := addValue(domain.DocumentRef{Kind: domain.DocumentPlan, ID: node.ID}, node); err != nil {
			return StoryContext{}, err
		}
	}
	for _, fact := range content.Canon {
		if err := addValue(domain.DocumentRef{Kind: domain.DocumentCanon, ID: fact.ID}, fact); err != nil {
			return StoryContext{}, err
		}
	}
	for _, chapter := range content.Manuscript {
		if err := addValue(domain.DocumentRef{Kind: domain.DocumentManuscript, ID: chapter.ID}, chapter); err != nil {
			return StoryContext{}, err
		}
	}

	selected := make(map[string]ContextDocument)
	var include func(domain.DocumentRef) error
	include = func(ref domain.DocumentRef) error {
		if ref.Kind == domain.DocumentIntent && ref.ID == "root" {
			return nil
		}
		if _, exists := selected[ref.Key()]; exists {
			return nil
		}
		document, exists := documents[ref.Key()]
		if !exists {
			return fmt.Errorf("context dependency %q does not exist: %w", ref.Key(), domain.ErrInvalid)
		}
		selected[ref.Key()] = document
		dependencies, err := domain.DocumentDependencies(ref, document.Content)
		if err != nil {
			return err
		}
		for _, dependency := range dependencies {
			if dependency.Kind == domain.DocumentOwnership {
				continue
			}
			if err := include(dependency); err != nil {
				return err
			}
		}
		return nil
	}

	// writingBaseline 落实 §6.5 写作最低契约：全部计划节点（当前章及其 arc/volume
	// 祖先、各章摘要）、全部 Canon 事实（最新事实状态与未解决伏笔、相关角色地点
	// 物品），以及紧邻的上一章正文（结尾衔接）。
	writingBaseline := func(currentPlanID string) error {
		for _, document := range documents {
			if document.Ref.Kind != domain.DocumentManuscript {
				selected[document.Ref.Key()] = document
			}
		}
		if previous, ok := previousChapterManuscript(content, currentPlanID); ok {
			return include(domain.DocumentRef{Kind: domain.DocumentManuscript, ID: previous})
		}
		return nil
	}

	switch kind {
	case domain.OperationInitializeProject, domain.OperationDevelopPlan,
		domain.OperationRevisePlan, domain.OperationReviseCanon:
		for _, document := range documents {
			if document.Ref.Kind != domain.DocumentManuscript {
				selected[document.Ref.Key()] = document
			}
		}
	case domain.OperationWriteChapter:
		var input struct {
			ChapterPlanID string `json:"chapter_plan_id"`
		}
		if err := json.Unmarshal(task, &input); err != nil {
			return StoryContext{}, fmt.Errorf("decode write context task: %w", err)
		}
		if strings.TrimSpace(input.ChapterPlanID) == "" {
			return StoryContext{}, fmt.Errorf("write context requires chapter_plan_id: %w", domain.ErrInvalid)
		}
		if err := include(domain.DocumentRef{Kind: domain.DocumentPlan, ID: input.ChapterPlanID}); err != nil {
			return StoryContext{}, err
		}
		if err := writingBaseline(input.ChapterPlanID); err != nil {
			return StoryContext{}, err
		}
	case domain.OperationRewriteChapter:
		var input struct {
			ChapterID string `json:"chapter_id"`
		}
		if err := json.Unmarshal(task, &input); err != nil {
			return StoryContext{}, fmt.Errorf("decode rewrite context task: %w", err)
		}
		if strings.TrimSpace(input.ChapterID) == "" {
			return StoryContext{}, fmt.Errorf("rewrite context requires chapter_id: %w", domain.ErrInvalid)
		}
		if err := include(domain.DocumentRef{Kind: domain.DocumentManuscript, ID: input.ChapterID}); err != nil {
			return StoryContext{}, err
		}
		planID := ""
		for _, chapter := range content.Manuscript {
			if chapter.ID == input.ChapterID {
				planID = chapter.PlanNodeID
			}
		}
		if err := writingBaseline(planID); err != nil {
			return StoryContext{}, err
		}
	case domain.OperationRewriteAffected:
		var input struct {
			ChapterIDs []string `json:"chapter_ids"`
		}
		if err := json.Unmarshal(task, &input); err != nil {
			return StoryContext{}, fmt.Errorf("decode affected rewrite context task: %w", err)
		}
		if len(input.ChapterIDs) == 0 {
			return StoryContext{}, fmt.Errorf("affected rewrite context requires chapter_ids: %w", domain.ErrInvalid)
		}
		seen := make(map[string]struct{}, len(input.ChapterIDs))
		for _, id := range input.ChapterIDs {
			if strings.TrimSpace(id) == "" {
				return StoryContext{}, fmt.Errorf("affected rewrite chapter id is required: %w", domain.ErrInvalid)
			}
			if _, exists := seen[id]; exists {
				return StoryContext{}, fmt.Errorf("duplicate affected rewrite chapter %q: %w", id, domain.ErrInvalid)
			}
			seen[id] = struct{}{}
			if err := include(domain.DocumentRef{Kind: domain.DocumentManuscript, ID: id}); err != nil {
				return StoryContext{}, err
			}
		}
	case domain.OperationReviewRange:
		var input struct {
			Range struct {
				ChapterIDs []string `json:"chapter_ids"`
			} `json:"range"`
		}
		if err := json.Unmarshal(task, &input); err != nil {
			return StoryContext{}, fmt.Errorf("decode review context task: %w", err)
		}
		if len(input.Range.ChapterIDs) == 0 {
			return StoryContext{}, fmt.Errorf("review context requires range.chapter_ids: %w", domain.ErrInvalid)
		}
		for _, id := range input.Range.ChapterIDs {
			if err := include(domain.DocumentRef{Kind: domain.DocumentManuscript, ID: id}); err != nil {
				return StoryContext{}, err
			}
		}
	default:
		return StoryContext{}, fmt.Errorf("operation %s has no story context contract: %w", kind, domain.ErrInvalid)
	}

	ownership := append([]domain.OwnershipRule(nil), content.Ownership...)
	slices.SortFunc(ownership, func(left, right domain.OwnershipRule) int {
		return strings.Compare(left.Target.Key(), right.Target.Key())
	})
	for _, rule := range ownership {
		if err := rule.Validate(); err != nil {
			return StoryContext{}, err
		}
		if rule.Control == domain.ControlLocked || rule.Control == domain.ControlGuided {
			if err := include(rule.Target); err != nil {
				return StoryContext{}, err
			}
		}
	}

	result := StoryContext{
		SchemaVersion: StoryContextKind,
		ProjectID:     content.ID, Revision: content.Revision, Intent: content.Intent,
		Ownership: ownership,
	}
	for _, document := range selected {
		result.Documents = append(result.Documents, document)
	}
	slices.SortFunc(result.Documents, func(left, right ContextDocument) int {
		return strings.Compare(left.Ref.Key(), right.Ref.Key())
	})
	for _, chapter := range content.Manuscript {
		result.ManuscriptIndex = append(result.ManuscriptIndex, ChapterIndexEntry{
			ID: chapter.ID, PlanNodeID: chapter.PlanNodeID, Number: chapter.Number,
			Title: chapter.Title, Blocks: len(chapter.Blocks),
		})
	}
	slices.SortFunc(result.ManuscriptIndex, func(left, right ChapterIndexEntry) int {
		if left.Number != right.Number {
			return left.Number - right.Number
		}
		return strings.Compare(left.ID, right.ID)
	})
	return result, nil
}

// previousChapterManuscript 按章节计划顺序找到当前章之前最近一篇已有正文的
// 章节：写下一章时模型必须看到上一章结尾（§6.5）。
func previousChapterManuscript(content ProjectContent, currentPlanID string) (string, bool) {
	chapters := make([]domain.PlanNode, 0, len(content.Plan))
	for _, node := range content.Plan {
		if node.Kind == domain.PlanChapter {
			chapters = append(chapters, node)
		}
	}
	slices.SortFunc(chapters, func(left, right domain.PlanNode) int {
		if left.Order != right.Order {
			return left.Order - right.Order
		}
		return strings.Compare(left.ID, right.ID)
	})
	written := make(map[string]string, len(content.Manuscript))
	for _, chapter := range content.Manuscript {
		written[chapter.PlanNodeID] = chapter.ID
	}
	current := -1
	for index, node := range chapters {
		if node.ID == currentPlanID {
			current = index
			break
		}
	}
	if current < 0 {
		return "", false
	}
	for index := current - 1; index >= 0; index-- {
		if id, ok := written[chapters[index].ID]; ok {
			return id, true
		}
	}
	return "", false
}
