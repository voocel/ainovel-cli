package service

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// basisFor 构造证据基线（D48）：目标文档及其结构依赖闭包加 Intent，各取最后变化
// revision；作用域由调用方给出。目标不在快照里是调用方的错误。
func basisFor(project ProjectSnapshot, targets []domain.DocumentRef, scopes []domain.ScopeBasis) (domain.EvidenceBasis, error) {
	basis := domain.EvidenceBasis{Scopes: scopes}
	visited := make(map[string]struct{})
	queue := append([]domain.DocumentRef{{Kind: domain.DocumentIntent, ID: "root"}}, targets...)
	for len(queue) > 0 {
		ref := queue[0]
		queue = queue[1:]
		if _, seen := visited[ref.Key()]; seen {
			continue
		}
		visited[ref.Key()] = struct{}{}
		entry, ok := project.Index[ref.Key()]
		if !ok {
			return domain.EvidenceBasis{}, fmt.Errorf("basis document %s is absent at revision %d: %w", ref.Key(), project.Revision, domain.ErrInvalid)
		}
		basis.Documents = append(basis.Documents, domain.DocumentBasis{Ref: ref, Revision: entry.Revision})
		queue = append(queue, entry.Dependencies...)
	}
	return basis.Normalize(), nil
}

// directiveTarget 是章节的要求作用域匹配对象：章号加该章 Plan 节点及其祖先。
func directiveTarget(project ProjectSnapshot, number int, planID string) domain.DirectiveTarget {
	return domain.DirectiveTarget{ChapterNumber: number, PlanNodeIDs: domain.PlanAncestry(project.Plan, planID)}
}

// directiveScope 把命中 target 的 active 要求集合（含各自版本）钉成作用域基线：
// 相干要求的增删改改变摘要，不相干要求的变化不影响。
func directiveScope(project ProjectSnapshot, target domain.DirectiveTarget) domain.ScopeBasis {
	members := domain.ScopeMembers(project.Directives, target, func(ref domain.DocumentRef) domain.Revision {
		return project.Index[ref.Key()].Revision
	})
	return domain.ScopeBasis{Kind: domain.ScopeDirective, Target: target, Digest: domain.ScopeDigest(members)}
}

func canonScope(project ProjectSnapshot, target domain.CanonScope) domain.ScopeBasis {
	refs := domain.CanonScopeRefs(project.Canon, project.Manuscript, target)
	members := make([]domain.DocumentBasis, 0, len(refs))
	for _, ref := range refs {
		members = append(members, domain.DocumentBasis{Ref: ref, Revision: project.Index[ref.Key()].Revision})
	}
	return domain.ScopeBasis{Kind: domain.ScopeCanon, Canon: &target, Digest: domain.ScopeDigest(members)}
}

// staleBasis 报告基线里已不成立的条目：文档最后变化 revision 不同（含删除）、作用域
// 成员摘要不同、工件缺失或摘要不同。空切片表示证据仍然有效。
func (s *Service) staleBasis(ctx context.Context, project ProjectSnapshot, basis domain.EvidenceBasis) ([]string, error) {
	var stale []string
	for _, document := range basis.Documents {
		entry, ok := project.Index[document.Ref.Key()]
		switch {
		case !ok:
			stale = append(stale, fmt.Sprintf("%s deleted", document.Ref.Key()))
		case entry.Revision != document.Revision:
			stale = append(stale, fmt.Sprintf("%s changed at revision %d", document.Ref.Key(), entry.Revision))
		}
	}
	for _, scope := range basis.Scopes {
		switch scope.Kind {
		case domain.ScopeDirective:
			if directiveScope(project, scope.Target).Digest != scope.Digest {
				stale = append(stale, fmt.Sprintf("directives covering chapter %d changed", scope.Target.ChapterNumber))
			}
		case domain.ScopeCanon:
			if canonScope(project, *scope.Canon).Digest != scope.Digest {
				stale = append(stale, "Canon facts in the reviewed range changed")
			}
		}
	}
	for _, ref := range basis.Artifacts {
		artifact, err := s.store.GetArtifact(ctx, ref.ID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			stale = append(stale, fmt.Sprintf("artifact %s deleted", ref.ID))
		case err != nil:
			return nil, err
		case artifact.Digest != ref.Digest:
			stale = append(stale, fmt.Sprintf("artifact %s replaced", ref.ID))
		}
	}
	slices.Sort(stale)
	return stale, nil
}

// validArtifacts 取基线仍成立的全部工件：推导器据此判断哪些衍生产出需要重生。
func (s *Service) validArtifacts(ctx context.Context, project ProjectSnapshot) ([]domain.Artifact, error) {
	artifacts, err := s.store.ListArtifacts(ctx, project.ID)
	if err != nil {
		return nil, err
	}
	valid := artifacts[:0]
	for _, artifact := range artifacts {
		stale, err := s.staleBasis(ctx, project, artifact.Basis)
		if err != nil {
			return nil, err
		}
		if len(stale) == 0 {
			valid = append(valid, artifact)
		}
	}
	return valid, nil
}
