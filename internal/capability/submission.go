package capability

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/workspace"
)

func (r *Runtime) validateSubmissionArtifact(
	ctx context.Context,
	operation domain.Operation,
	workspaceKeys []string, reviewKey string,
	patches []domain.Patch,
) error {
	switch operation.Kind {
	case domain.OperationWriteChapter, domain.OperationRewriteChapter:
		if len(workspaceKeys) != 1 {
			return fmt.Errorf("writer submission requires workspace_key: %w", domain.ErrInvalid)
		}
		return r.validateWorkspaceChapters(ctx, operation, workspaceKeys, patches)
	case domain.OperationRewriteAffected:
		if len(workspaceKeys) == 0 {
			return fmt.Errorf("affected rewrite submission requires workspace_keys: %w", domain.ErrInvalid)
		}
		return r.validateWorkspaceChapters(ctx, operation, workspaceKeys, patches)
	case domain.OperationReviewRange:
		if reviewKey == "" {
			return fmt.Errorf("review submission requires review_key: %w", domain.ErrInvalid)
		}
		if _, err := r.store.GetWorkspaceArtifact(ctx, operation.ID, reviewKey); err != nil {
			return fmt.Errorf("read submitted workspace review: %w", err)
		}
	}
	return nil
}

// validatePlanTarget 在工具边界提前执行滚动规划的数量不变量（§6.3）：偏差
// 当场反馈给模型在同一会话内纠正，而不是等 Operation 收尾时才失败。
func (r *Runtime) validatePlanTarget(
	ctx context.Context,
	operation domain.Operation,
	patches []domain.Patch,
) error {
	expected, ok, err := domain.PlanChapterTargetForOperation(operation)
	if err != nil || !ok {
		return err
	}
	base, err := r.store.ListPlanNodes(ctx, operation.Target, operation.Snapshot.BaseRevision)
	if err != nil {
		return err
	}
	return domain.ValidatePlanChapterTarget(base, patches, expected)
}

func (r *Runtime) validateWorkspaceChapters(
	ctx context.Context,
	operation domain.Operation,
	workspaceKeys []string,
	patches []domain.Patch,
) error {
	seenKeys := make(map[string]struct{}, len(workspaceKeys))
	seenChapters := make(map[string]struct{}, len(workspaceKeys))
	var expectedPlanID string
	expectedChapters := make(map[string]struct{})
	var task struct {
		Directives []domain.Directive `json:"directives"`
	}
	if err := json.Unmarshal(operation.Input, &task); err != nil {
		return fmt.Errorf("decode operation directives: %w", err)
	}
	switch operation.Kind {
	case domain.OperationWriteChapter:
		var input struct {
			ChapterPlanID string `json:"chapter_plan_id"`
		}
		if err := json.Unmarshal(operation.Input, &input); err != nil {
			return fmt.Errorf("decode write operation input: %w", err)
		}
		expectedPlanID = input.ChapterPlanID
	case domain.OperationRewriteChapter:
		var input struct {
			ChapterID    string          `json:"chapter_id"`
			BaseRevision domain.Revision `json:"base_revision"`
		}
		if err := json.Unmarshal(operation.Input, &input); err != nil {
			return fmt.Errorf("decode rewrite operation input: %w", err)
		}
		if input.ChapterID == "" || input.BaseRevision != operation.Snapshot.BaseRevision {
			return fmt.Errorf("rewrite operation input does not match its execution snapshot: %w", domain.ErrInvalid)
		}
		expectedChapters[input.ChapterID] = struct{}{}
	case domain.OperationRewriteAffected:
		var input struct {
			ChapterIDs           []string        `json:"chapter_ids"`
			BaseRevision         domain.Revision `json:"base_revision"`
			ResolutionProposalID string          `json:"resolution_proposal_id"`
			Reason               string          `json:"reason"`
		}
		if err := json.Unmarshal(operation.Input, &input); err != nil {
			return fmt.Errorf("decode affected rewrite operation input: %w", err)
		}
		if len(input.ChapterIDs) == 0 || input.BaseRevision != operation.Snapshot.BaseRevision ||
			input.ResolutionProposalID == "" || input.Reason == "" {
			return fmt.Errorf("affected rewrite operation input does not match its execution snapshot: %w", domain.ErrInvalid)
		}
		for _, chapterID := range input.ChapterIDs {
			expectedChapters[chapterID] = struct{}{}
		}
	}
	for _, workspaceKey := range workspaceKeys {
		if workspaceKey == "" {
			return fmt.Errorf("workspace chapter key is required: %w", domain.ErrInvalid)
		}
		if _, exists := seenKeys[workspaceKey]; exists {
			return fmt.Errorf("duplicate workspace chapter key %q: %w", workspaceKey, domain.ErrInvalid)
		}
		seenKeys[workspaceKey] = struct{}{}
		artifact, err := r.store.GetWorkspaceArtifact(ctx, operation.ID, workspaceKey)
		if err != nil {
			return fmt.Errorf("read submitted workspace chapter: %w", err)
		}
		if artifact.MediaType != workspace.ChapterMediaType {
			return fmt.Errorf("workspace artifact %q is not a chapter: %w", workspaceKey, domain.ErrInvalid)
		}
		var workspaceChapter domain.ManuscriptChapter
		if err := decodeToolArgs(artifact.Content, &workspaceChapter); err != nil {
			return fmt.Errorf("decode submitted workspace chapter: %w", err)
		}
		if _, exists := seenChapters[workspaceChapter.ID]; exists {
			return fmt.Errorf("duplicate submitted workspace chapter %q: %w", workspaceChapter.ID, domain.ErrInvalid)
		}
		seenChapters[workspaceChapter.ID] = struct{}{}
		if err := checkDirectiveWordCounts(task.Directives, workspaceChapter); err != nil {
			return err
		}
		if expectedPlanID != "" && workspaceChapter.PlanNodeID != expectedPlanID {
			return fmt.Errorf("workspace chapter %q does not implement requested plan %q: %w", workspaceChapter.ID, expectedPlanID, domain.ErrInvalid)
		}
		if len(expectedChapters) != 0 {
			if _, expected := expectedChapters[workspaceChapter.ID]; !expected {
				return fmt.Errorf("workspace chapter %q is outside the rewrite task: %w", workspaceChapter.ID, domain.ErrInvalid)
			}
		}
		workspaceContent, err := json.Marshal(workspaceChapter)
		if err != nil {
			return fmt.Errorf("encode submitted workspace chapter: %w", err)
		}
		manuscriptMatches, canonDeltaDeclared := false, false
		for _, patch := range patches {
			if patch.Document.Kind == domain.DocumentCanon && patch.Operation == domain.PatchPut {
				var fact domain.CanonFact
				if err := decodeToolArgs(patch.Content, &fact); err != nil {
					return fmt.Errorf("decode proposed Canon Delta: %w", err)
				}
				canonDeltaDeclared = canonDeltaDeclared || fact.SourceChapterID == workspaceChapter.ID
			}
			if patch.Document.Kind != domain.DocumentManuscript || patch.Document.ID != workspaceChapter.ID || patch.Operation != domain.PatchPut {
				continue
			}
			var proposedChapter domain.ManuscriptChapter
			if err := decodeToolArgs(patch.Content, &proposedChapter); err != nil {
				return fmt.Errorf("decode proposed manuscript: %w", err)
			}
			proposedContent, err := json.Marshal(proposedChapter)
			if err != nil {
				return fmt.Errorf("encode proposed manuscript: %w", err)
			}
			if bytes.Equal(workspaceContent, proposedContent) {
				manuscriptMatches = true
			}
		}
		if !manuscriptMatches {
			return fmt.Errorf("proposal manuscript must match workspace artifact %q: %w", workspaceKey, domain.ErrInvalid)
		}
		if !canonDeltaDeclared {
			return fmt.Errorf("writer submission requires a Canon Delta for chapter %q: %w", workspaceChapter.ID, domain.ErrInvalid)
		}
	}
	if len(expectedChapters) != 0 && len(seenChapters) != len(expectedChapters) {
		return fmt.Errorf("writer submission does not cover every requested chapter: %w", domain.ErrInvalid)
	}
	patchedChapters := make(map[string]struct{})
	for _, patch := range patches {
		if patch.Document.Kind == domain.DocumentManuscript && patch.Operation == domain.PatchPut {
			patchedChapters[patch.Document.ID] = struct{}{}
		}
	}
	if len(patchedChapters) != len(seenChapters) {
		return fmt.Errorf("writer proposal manuscript patches must exactly match submitted workspace chapters: %w", domain.ErrInvalid)
	}
	for chapterID := range patchedChapters {
		if _, exists := seenChapters[chapterID]; !exists {
			return fmt.Errorf("writer proposal includes unverified manuscript %q: %w", chapterID, domain.ErrInvalid)
		}
	}
	return nil
}

// checkDirectiveWordCounts 是量化要求的确定性校验（§4.9 / S13）：字数按各 block
// 正文字符数累加、不含标题；越界连同实际值与区间原样回给模型自纠。
func checkDirectiveWordCounts(directives []domain.Directive, chapter domain.ManuscriptChapter) error {
	words := 0
	for _, block := range chapter.Blocks {
		words += utf8.RuneCountInString(block.Text)
	}
	for _, directive := range directives {
		low, high, ok := directive.Constraints.Bounds()
		if !ok {
			continue
		}
		if words < low || (high > 0 && words > high) {
			return fmt.Errorf("chapter %q has %d characters, directive %q requires %d-%d: %w",
				chapter.ID, words, directive.ID, low, high, domain.ErrInvalid)
		}
	}
	return nil
}
