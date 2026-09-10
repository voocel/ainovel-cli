package service

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/voocel/ainovel-cli/internal/domain"
	"time"
)

// storedVerdict 是一条已校验的原始裁定及其派生记录坐标（选"最新"用）；accepted 是
// 仍然有效的用户裁决所接受的发现（D43），生效裁定由 effective 套用得到。
type storedVerdict struct {
	verdict   domain.ReviewVerdict
	key       string
	createdAt time.Time
	accepted  map[string]struct{}
}

func (v storedVerdict) effective() domain.ReviewVerdict {
	return v.verdict.Adjudicated(v.key, v.accepted)
}

// listVerdicts 取当前快照下仍然有效的全部裁定并附上有效裁决（D43）：协调器与工作台
// 共用，两处"最新裁定"与"生效裁定"语义一致。
func (s *Service) listVerdicts(ctx context.Context, project ProjectSnapshot) ([]storedVerdict, error) {
	verdicts, err := s.rawVerdicts(ctx, project)
	if err != nil {
		return nil, err
	}
	adjudications, err := s.validAdjudications(ctx, project)
	if err != nil {
		return nil, err
	}
	accepted := domain.AcceptedFindings(adjudications)
	for index := range verdicts {
		verdicts[index].accepted = accepted
	}
	return verdicts, nil
}

// rawVerdicts 取当前快照下基线仍成立的全部原始裁定（D48）：操作必须已成功、内容
// 可解码且结构合法——缺失或损坏是数据错误，一律上抛，不得静默当作"没有裁定"。
func (s *Service) rawVerdicts(ctx context.Context, project ProjectSnapshot) ([]storedVerdict, error) {
	derived, err := s.store.ListDerivedDocumentsByKind(ctx, project.ID, domain.DerivedVerdictKind)
	if err != nil {
		return nil, err
	}
	var verdicts []storedVerdict
	for _, document := range derived {
		operation, err := s.store.GetOperation(ctx, document.Key)
		if err != nil {
			return nil, fmt.Errorf("review verdict %q has no operation: %w", document.Key, err)
		}
		if operation.State != domain.OperationSucceeded || operation.Kind != domain.OperationReviewRange {
			continue
		}
		var verdict domain.ReviewVerdict
		if err := json.Unmarshal(document.Content, &verdict); err != nil {
			return nil, fmt.Errorf("decode review verdict %q: %w", document.Key, err)
		}
		if err := verdict.Validate(); err != nil {
			return nil, fmt.Errorf("review verdict %q: %w", document.Key, err)
		}
		stale, err := s.staleBasis(ctx, project, verdict.Basis)
		if err != nil {
			return nil, err
		}
		if len(stale) > 0 {
			continue
		}
		verdicts = append(verdicts, storedVerdict{verdict: verdict, key: document.Key, createdAt: document.CreatedAt})
	}
	return verdicts, nil
}

// latestVerdict 在满足条件的裁定里选最新并返回其生效形态；条件按原始裁定匹配。
func latestVerdict(verdicts []storedVerdict, matches func(domain.ReviewVerdict) bool) *domain.ReviewVerdict {
	best := latestStoredVerdict(verdicts, matches)
	if best == nil {
		return nil
	}
	verdict := best.effective()
	return &verdict
}

// latestStoredVerdict 选最新：CreatedAt 优先，相同则 Key 决胜。
func latestStoredVerdict(verdicts []storedVerdict, matches func(domain.ReviewVerdict) bool) *storedVerdict {
	var best *storedVerdict
	for index := range verdicts {
		candidate := &verdicts[index]
		if !matches(candidate.verdict) {
			continue
		}
		if best == nil || candidate.createdAt.After(best.createdAt) ||
			(candidate.createdAt.Equal(best.createdAt) && candidate.key > best.key) {
			best = candidate
		}
	}
	return best
}
