package capability

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/workspace"
)

func (r *Runtime) validateSubmissionArtifact(
	ctx context.Context,
	operation model.Operation,
	workspaceKeys []string, reviewKey string,
	patches []model.Patch,
) error {
	switch operation.Kind {
	case model.OperationWriteChapter, model.OperationRewriteChapter:
		if len(workspaceKeys) != 1 {
			return fmt.Errorf("writer submission requires workspace_key: %w", model.ErrInvalid)
		}
		return r.validateWorkspaceChapters(ctx, operation, workspaceKeys, patches)
	case model.OperationRewriteAffected:
		if len(workspaceKeys) == 0 {
			return fmt.Errorf("affected rewrite submission requires workspace_keys: %w", model.ErrInvalid)
		}
		return r.validateWorkspaceChapters(ctx, operation, workspaceKeys, patches)
	case model.OperationReviewRange:
		if reviewKey == "" {
			return fmt.Errorf("review submission requires review_key: %w", model.ErrInvalid)
		}
		if _, err := r.store.GetWorkspaceArtifact(ctx, operation.ID, reviewKey); err != nil {
			return fmt.Errorf("read submitted workspace review: %w", err)
		}
	case model.OperationReviseCanon:
		return validateCanonRevision(operation, patches)
	}
	return nil
}

// validateCanonRevision 是事实核验任务的提交门（D41）：只允许 canon 补丁，每条 put 的
// 来源章是任务章节，任务列出的待核验事实全部被确认（put）或删除，且该章至少留一条事实。
func validateCanonRevision(operation model.Operation, patches []model.Patch) error {
	input, err := model.TaskInputAs[model.ReviseCanonInput](operation)
	if err != nil {
		return err
	}
	touched := make(map[string]struct{}, len(patches))
	declared := 0
	for _, patch := range patches {
		if patch.Document.Kind != model.DocumentCanon {
			return fmt.Errorf("canon revision may only change canon facts, got %s: %w", patch.Document.Key(), model.ErrInvalid)
		}
		touched[patch.Document.ID] = struct{}{}
		if patch.Operation != model.PatchPut {
			continue
		}
		var fact model.CanonFact
		if err := decodeToolArgs(patch.Content, &fact); err != nil {
			return fmt.Errorf("decode canon %q: %w", patch.Document.ID, err)
		}
		if fact.SourceChapterID != input.ChapterID {
			return fmt.Errorf("canon %q must be sourced from chapter %q: %w", fact.ID, input.ChapterID, model.ErrInvalid)
		}
		declared++
	}
	for _, id := range input.FactIDs {
		if _, ok := touched[id]; !ok {
			return fmt.Errorf("canon %q is pending verification and must be confirmed, updated or deleted: %w", id, model.ErrInvalid)
		}
	}
	if declared == 0 {
		return fmt.Errorf("chapter %q needs at least one canon fact: %w", input.ChapterID, model.ErrInvalid)
	}
	return nil
}

// validateChapterCanon 在工具边界执行 D41 的来源归属与重申报：每条新事实的来源章必须是
// 本次提交的正文，提交的每章必须重申报 base 上来源于它的全部事实（put 或 delete）。
func validateChapterCanon(base []model.DocumentVersion, patches []model.Patch, chapters map[string]struct{}) error {
	touched := make(map[string]struct{}, len(patches))
	for _, patch := range patches {
		if patch.Document.Kind != model.DocumentCanon {
			continue
		}
		touched[patch.Document.ID] = struct{}{}
		if patch.Operation != model.PatchPut {
			continue
		}
		var fact model.CanonFact
		if err := decodeToolArgs(patch.Content, &fact); err != nil {
			return fmt.Errorf("decode proposed Canon Delta: %w", err)
		}
		if _, ok := chapters[fact.SourceChapterID]; !ok {
			return fmt.Errorf("canon %q must be sourced from a submitted chapter: %w", fact.ID, model.ErrInvalid)
		}
	}
	for _, document := range base {
		var fact model.CanonFact
		if err := json.Unmarshal(document.Content, &fact); err != nil {
			return fmt.Errorf("decode canon %q: %w", document.Document.ID, err)
		}
		if _, rewritten := chapters[fact.SourceChapterID]; !rewritten {
			continue
		}
		if _, ok := touched[fact.ID]; !ok {
			return fmt.Errorf("chapter %q must redeclare canon %q (confirm, update or delete): %w", fact.SourceChapterID, fact.ID, model.ErrInvalid)
		}
	}
	return nil
}

// validatePlanTarget 在工具边界提前执行滚动规划的数量不变量（§6.3）：偏差
// 当场反馈给模型在同一会话内纠正，而不是等 Operation 收尾时才失败。
func (r *Runtime) validatePlanTarget(
	ctx context.Context,
	operation model.Operation,
	patches []model.Patch,
) error {
	expected, ok, err := model.PlanChapterTargetForOperation(operation)
	if err != nil || !ok {
		return err
	}
	base, err := r.store.ListPlanNodes(ctx, operation.Target, operation.Snapshot.BaseRevision)
	if err != nil {
		return err
	}
	return model.ValidatePlanChapterTarget(base, patches, expected)
}

func (r *Runtime) validateWorkspaceChapters(
	ctx context.Context,
	operation model.Operation,
	workspaceKeys []string,
	patches []model.Patch,
) error {
	seenKeys := make(map[string]struct{}, len(workspaceKeys))
	seenChapters := make(map[string]struct{}, len(workspaceKeys))
	var expectedPlanID string
	expectedChapters := make(map[string]struct{})
	task, err := model.DecodeTaskInput(operation.Kind, operation.Input)
	if err != nil {
		return err
	}
	directives := model.TaskDirectives(task)
	switch input := task.(type) {
	case *model.WriteChapterInput:
		expectedPlanID = input.ChapterPlanID
	case *model.RewriteChapterInput:
		expectedChapters[input.ChapterID] = struct{}{}
	case *model.RewriteAffectedInput:
		if input.BaseRevision != operation.Snapshot.BaseRevision {
			return fmt.Errorf("affected rewrite operation input does not match its execution snapshot: %w", model.ErrInvalid)
		}
		for _, chapterID := range input.ChapterIDs {
			expectedChapters[chapterID] = struct{}{}
		}
	}
	for _, workspaceKey := range workspaceKeys {
		if workspaceKey == "" {
			return fmt.Errorf("workspace chapter key is required: %w", model.ErrInvalid)
		}
		if _, exists := seenKeys[workspaceKey]; exists {
			return fmt.Errorf("duplicate workspace chapter key %q: %w", workspaceKey, model.ErrInvalid)
		}
		seenKeys[workspaceKey] = struct{}{}
		artifact, err := r.store.GetWorkspaceArtifact(ctx, operation.ID, workspaceKey)
		if err != nil {
			return fmt.Errorf("read submitted workspace chapter: %w", err)
		}
		if artifact.MediaType != workspace.ChapterMediaType {
			return fmt.Errorf("workspace artifact %q is not a chapter: %w", workspaceKey, model.ErrInvalid)
		}
		var workspaceChapter model.ManuscriptChapter
		if err := decodeToolArgs(artifact.Content, &workspaceChapter); err != nil {
			return fmt.Errorf("decode submitted workspace chapter: %w", err)
		}
		if _, exists := seenChapters[workspaceChapter.ID]; exists {
			return fmt.Errorf("duplicate submitted workspace chapter %q: %w", workspaceChapter.ID, model.ErrInvalid)
		}
		seenChapters[workspaceChapter.ID] = struct{}{}
		if err := checkDirectiveWordCounts(directives, workspaceChapter); err != nil {
			return err
		}
		if expectedPlanID != "" && workspaceChapter.PlanNodeID != expectedPlanID {
			return fmt.Errorf("workspace chapter %q does not implement requested plan %q: %w", workspaceChapter.ID, expectedPlanID, model.ErrInvalid)
		}
		if len(expectedChapters) != 0 {
			if _, expected := expectedChapters[workspaceChapter.ID]; !expected {
				return fmt.Errorf("workspace chapter %q is outside the rewrite task: %w", workspaceChapter.ID, model.ErrInvalid)
			}
		}
		workspaceContent, err := json.Marshal(workspaceChapter)
		if err != nil {
			return fmt.Errorf("encode submitted workspace chapter: %w", err)
		}
		manuscriptMatches, canonDeltaDeclared := false, false
		for _, patch := range patches {
			if patch.Document.Kind == model.DocumentCanon && patch.Operation == model.PatchPut {
				var fact model.CanonFact
				if err := decodeToolArgs(patch.Content, &fact); err != nil {
					return fmt.Errorf("decode proposed Canon Delta: %w", err)
				}
				canonDeltaDeclared = canonDeltaDeclared || fact.SourceChapterID == workspaceChapter.ID
			}
			if patch.Document.Kind != model.DocumentManuscript || patch.Document.ID != workspaceChapter.ID || patch.Operation != model.PatchPut {
				continue
			}
			var proposedChapter model.ManuscriptChapter
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
			return fmt.Errorf("proposal manuscript must match workspace artifact %q: %w", workspaceKey, model.ErrInvalid)
		}
		if !canonDeltaDeclared {
			return fmt.Errorf("writer submission requires a Canon Delta for chapter %q: %w", workspaceChapter.ID, model.ErrInvalid)
		}
	}
	if len(expectedChapters) != 0 && len(seenChapters) != len(expectedChapters) {
		return fmt.Errorf("writer submission does not cover every requested chapter: %w", model.ErrInvalid)
	}
	patchedChapters := make(map[string]struct{})
	for _, patch := range patches {
		if patch.Document.Kind == model.DocumentManuscript && patch.Operation == model.PatchPut {
			patchedChapters[patch.Document.ID] = struct{}{}
		}
	}
	if len(patchedChapters) != len(seenChapters) {
		return fmt.Errorf("writer proposal manuscript patches must exactly match submitted workspace chapters: %w", model.ErrInvalid)
	}
	for chapterID := range patchedChapters {
		if _, exists := seenChapters[chapterID]; !exists {
			return fmt.Errorf("writer proposal includes unverified manuscript %q: %w", chapterID, model.ErrInvalid)
		}
	}
	var base []model.DocumentVersion
	if operation.Snapshot.BaseRevision > model.InitialRevision {
		if base, err = r.store.ListDocuments(ctx, operation.Target, model.DocumentCanon, operation.Snapshot.BaseRevision); err != nil {
			return err
		}
	}
	return validateChapterCanon(base, patches, seenChapters)
}

// checkDirectiveWordCounts 是量化要求的确定性校验（§4.9 / S13）：字数按各 block
// 正文字符数累加、不含标题；越界连同实际值与区间原样回给模型自纠。
func checkDirectiveWordCounts(directives []model.Directive, chapter model.ManuscriptChapter) error {
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
				chapter.ID, words, directive.ID, low, high, model.ErrInvalid)
		}
	}
	return nil
}
