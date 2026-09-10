package change

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// VerifyBasis 核对证据基线在 at 上成立（D48）：文档最后变化 revision 相等、要求作用域
// 成员摘要相等、工件已发布且摘要一致。不成立返回 ErrBasisMismatch 并说明条目。
func (e *Engine) VerifyBasis(ctx context.Context, target model.AuthorityTarget, basis model.EvidenceBasis, at model.Revision) error {
	for _, entry := range basis.Documents {
		version, err := e.store.GetDocument(ctx, target, entry.Ref, at)
		if errors.Is(err, model.ErrNotFound) {
			return fmt.Errorf("basis document %s is absent at revision %d: %w", entry.Ref.Key(), at, ErrBasisMismatch)
		}
		if err != nil {
			return err
		}
		if version.Revision != entry.Revision {
			return fmt.Errorf("basis document %s claims revision %d, stored %d at revision %d: %w",
				entry.Ref.Key(), entry.Revision, version.Revision, at, ErrBasisMismatch)
		}
	}
	if len(basis.Scopes) > 0 {
		directives, revisionOf, err := e.directivesAt(ctx, target, at)
		if err != nil {
			return err
		}
		for _, scope := range basis.Scopes {
			var members []model.DocumentBasis
			switch scope.Kind {
			case model.ScopeDirective:
				members = model.ScopeMembers(directives, scope.Target, revisionOf)
			case model.ScopeCanon:
				members, err = e.canonScopeAt(ctx, target, at, *scope.Canon)
				if err != nil {
					return err
				}
			}
			if model.ScopeDigest(members) != scope.Digest {
				if scope.Kind == model.ScopeCanon {
					return fmt.Errorf("Canon facts in the reviewed range changed by revision %d: %w", at, ErrBasisMismatch)
				}
				return fmt.Errorf("directives covering chapter %d changed by revision %d: %w",
					scope.Target.ChapterNumber, at, ErrBasisMismatch)
			}
		}
	}
	for _, ref := range basis.Artifacts {
		artifact, err := e.store.GetArtifact(ctx, ref.ID)
		if errors.Is(err, model.ErrNotFound) {
			return fmt.Errorf("basis artifact %s is not published: %w", ref.ID, ErrBasisMismatch)
		}
		if err != nil {
			return err
		}
		if artifact.Digest != ref.Digest {
			return fmt.Errorf("basis artifact %s digest mismatch: %w", ref.ID, ErrBasisMismatch)
		}
	}
	return nil
}

func (e *Engine) canonScopeAt(ctx context.Context, target model.AuthorityTarget, at model.Revision, scope model.CanonScope) ([]model.DocumentBasis, error) {
	documents, err := e.store.ListDocuments(ctx, target, model.DocumentCanon, at)
	if err != nil {
		return nil, err
	}
	facts := make([]model.CanonFact, 0, len(documents))
	revisions := make(map[string]model.Revision, len(documents))
	for _, document := range documents {
		var fact model.CanonFact
		if err := json.Unmarshal(document.Content, &fact); err != nil {
			return nil, fmt.Errorf("decode Canon scope member: %w", err)
		}
		facts = append(facts, fact)
		revisions[document.Document.Key()] = document.Revision
	}
	documents, err = e.store.ListDocuments(ctx, target, model.DocumentManuscript, at)
	if err != nil {
		return nil, err
	}
	chapters := make([]model.ManuscriptChapter, 0, len(documents))
	for _, document := range documents {
		var chapter model.ManuscriptChapter
		if err := json.Unmarshal(document.Content, &chapter); err != nil {
			return nil, fmt.Errorf("decode Canon scope chapter: %w", err)
		}
		chapters = append(chapters, chapter)
	}
	var members []model.DocumentBasis
	for _, ref := range model.CanonScopeRefs(facts, chapters, scope) {
		members = append(members, model.DocumentBasis{Ref: ref, Revision: revisions[ref.Key()]})
	}
	return members, nil
}

func (e *Engine) directivesAt(
	ctx context.Context,
	target model.AuthorityTarget,
	at model.Revision,
) ([]model.Directive, func(model.DocumentRef) model.Revision, error) {
	revisions := make(map[string]model.Revision)
	var directives []model.Directive
	if at > model.InitialRevision {
		documents, err := e.store.ListDocuments(ctx, target, model.DocumentDirective, at)
		if err != nil {
			return nil, nil, err
		}
		for _, document := range documents {
			var directive model.Directive
			if err := json.Unmarshal(document.Content, &directive); err != nil {
				return nil, nil, fmt.Errorf("decode directive %q: %w", document.Document.ID, err)
			}
			directives = append(directives, directive)
			revisions[document.Document.Key()] = document.Revision
		}
	}
	return directives, func(ref model.DocumentRef) model.Revision { return revisions[ref.Key()] }, nil
}

// Relocate 落实提案重定位规则（D51 / §5.5 第 4 条）：提案基线落后于当前 Revision 时，
// 仅当中间各 Revision 只改过用户专属文档、没碰本提案的文档，且任务基线在当前仍成立，
// 才把基线搬到当前并重算结构影响；否则返回 model.ErrRevisionConflict 并说明原因。
// 不落库、不经 Prepare；基线已是当前时原样返回。
func (e *Engine) Relocate(
	ctx context.Context,
	proposal model.Proposal,
	basis model.EvidenceBasis,
) (model.Proposal, bool, error) {
	current, err := e.currentRevision(ctx, proposal.Target)
	if err != nil {
		return model.Proposal{}, false, err
	}
	if current == proposal.BaseRevision {
		return proposal, false, nil
	}
	if current < proposal.BaseRevision {
		return model.Proposal{}, false, fmt.Errorf("base revision %d is ahead of current revision %d: %w",
			proposal.BaseRevision, current, model.ErrRevisionConflict)
	}
	own := make(map[string]struct{}, len(proposal.Patches))
	for _, patch := range proposal.Patches {
		own[patch.Document.Key()] = struct{}{}
	}
	for revision := proposal.BaseRevision + 1; revision <= current; revision++ {
		changeSet, err := e.store.GetChangeSetByRevision(ctx, proposal.Target, revision)
		if err != nil {
			return model.Proposal{}, false, err
		}
		for _, patch := range changeSet.Patches {
			spec, err := model.DocumentType(patch.Document.Kind)
			if err != nil {
				return model.Proposal{}, false, err
			}
			if _, touched := own[patch.Document.Key()]; !spec.UserOnly || touched {
				return model.Proposal{}, false, fmt.Errorf("%s changed at revision %d: %w",
					patch.Document.Key(), revision, model.ErrRevisionConflict)
			}
		}
	}
	if err := e.VerifyBasis(ctx, proposal.Target, basis, current); errors.Is(err, ErrBasisMismatch) {
		return model.Proposal{}, false, fmt.Errorf("%w: %w", model.ErrRevisionConflict, err)
	} else if err != nil {
		return model.Proposal{}, false, err
	}
	relocated := proposal
	relocated.BaseRevision = current
	impact, _, err := e.validateAndAnalyze(ctx, relocated)
	if err != nil {
		return model.Proposal{}, false, err
	}
	payload, err := json.Marshal(impact)
	if err != nil {
		return model.Proposal{}, false, fmt.Errorf("marshal structural impact: %w", err)
	}
	relocated.Impact.Structural = payload
	return relocated, true, nil
}
