package novel

import (
	"context"
	"errors"
	"fmt"

	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/change"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// basisFor 构造证据基线（D48）：目标文档及其结构依赖闭包加 Intent，各取最后变化
// revision；作用域由调用方给出。目标不在快照里是调用方的错误。
func basisFor(project projectdoc.Snapshot, targets []model.DocumentRef, scopes []model.ScopeBasis) (model.EvidenceBasis, error) {
	basis := model.EvidenceBasis{Scopes: scopes}
	visited := make(map[string]struct{})
	queue := append([]model.DocumentRef{{Kind: model.DocumentIntent, ID: "root"}}, targets...)
	for len(queue) > 0 {
		ref := queue[0]
		queue = queue[1:]
		if _, seen := visited[ref.Key()]; seen {
			continue
		}
		visited[ref.Key()] = struct{}{}
		entry, ok := project.Index[ref.Key()]
		if !ok {
			return model.EvidenceBasis{}, fmt.Errorf("basis document %s is absent at revision %d: %w", ref.Key(), project.Revision, model.ErrInvalid)
		}
		basis.Documents = append(basis.Documents, model.DocumentBasis{Ref: ref, Revision: entry.Revision})
		queue = append(queue, entry.Dependencies...)
	}
	return basis.Normalize(), nil
}

// directiveTarget 是章节的要求作用域匹配对象：章号加该章 Plan 节点及其祖先。
func directiveTarget(project projectdoc.Snapshot, number int, planID string) model.DirectiveTarget {
	return model.DirectiveTarget{ChapterNumber: number, PlanNodeIDs: model.PlanAncestry(project.Plan, planID)}
}

// directiveScope 把命中 target 的 active 要求集合（含各自版本）钉成作用域基线：
// 相干要求的增删改改变摘要，不相干要求的变化不影响。
func directiveScope(project projectdoc.Snapshot, target model.DirectiveTarget) model.ScopeBasis {
	members := model.ScopeMembers(project.Directives, target, func(ref model.DocumentRef) model.Revision {
		return project.Index[ref.Key()].Revision
	})
	return model.ScopeBasis{Kind: model.ScopeDirective, Target: target, Digest: model.ScopeDigest(members)}
}

func canonScope(project projectdoc.Snapshot, target model.CanonScope) model.ScopeBasis {
	refs := model.CanonScopeRefs(project.Canon, project.Manuscript, target)
	members := make([]model.DocumentBasis, 0, len(refs))
	for _, ref := range refs {
		members = append(members, model.DocumentBasis{Ref: ref, Revision: project.Index[ref.Key()].Revision})
	}
	return model.ScopeBasis{Kind: model.ScopeCanon, Canon: &target, Digest: model.ScopeDigest(members)}
}

// basisValid delegates validity to the same contract used by change submission.
func (s *Reviews) basisValid(ctx context.Context, project projectdoc.Snapshot, basis model.EvidenceBasis) (bool, error) {
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: project.ID}
	err := s.changes.VerifyBasis(ctx, target, basis, project.Revision)
	if errors.Is(err, change.ErrBasisMismatch) {
		return false, nil
	}
	return err == nil, err
}
