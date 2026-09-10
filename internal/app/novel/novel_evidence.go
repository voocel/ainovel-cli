package novel

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// StoredVerdict 是一条已校验的原始裁定及其派生记录坐标（选"最新"用）；Accepted 是
// 仍然有效的用户裁决所接受的发现（D43），生效裁定由 Effective 套用得到。
type StoredVerdict struct {
	Verdict   model.ReviewVerdict
	Key       string
	CreatedAt time.Time
	Accepted  map[string]struct{}
}

func (v StoredVerdict) Effective() model.ReviewVerdict {
	return v.Verdict.Adjudicated(v.Key, v.Accepted)
}

// ListVerdicts 取当前快照下仍然有效的全部裁定并附上有效裁决（D43）：协调器与工作台
// 共用，两处"最新裁定"与"生效裁定"语义一致。
func (s *Reviews) ListVerdicts(ctx context.Context, project projectdoc.Snapshot) ([]StoredVerdict, error) {
	verdicts, err := s.rawVerdicts(ctx, project)
	if err != nil {
		return nil, err
	}
	adjudications, err := s.ValidAdjudications(ctx, project)
	if err != nil {
		return nil, err
	}
	accepted := model.AcceptedFindings(adjudications)
	for index := range verdicts {
		verdicts[index].Accepted = accepted
	}
	return verdicts, nil
}

// rawVerdicts 取当前快照下基线仍成立的全部原始裁定（D48）：操作必须已成功、内容
// 可解码且结构合法——缺失或损坏是数据错误，一律上抛，不得静默当作"没有裁定"。
func (s *Reviews) rawVerdicts(ctx context.Context, project projectdoc.Snapshot) ([]StoredVerdict, error) {
	derived, err := s.store.ListDerivedDocumentsByKind(ctx, project.ID, model.DerivedVerdictKind)
	if err != nil {
		return nil, err
	}
	var verdicts []StoredVerdict
	for _, document := range derived {
		operation, err := s.store.GetOperation(ctx, document.Key)
		if err != nil {
			return nil, fmt.Errorf("review verdict %q has no operation: %w", document.Key, err)
		}
		if operation.State != model.OperationSucceeded || operation.Kind != model.OperationReviewRange {
			continue
		}
		var verdict model.ReviewVerdict
		if err := json.Unmarshal(document.Content, &verdict); err != nil {
			return nil, fmt.Errorf("decode review verdict %q: %w", document.Key, err)
		}
		if err := verdict.Validate(); err != nil {
			return nil, fmt.Errorf("review verdict %q: %w", document.Key, err)
		}
		basisValid, err := s.basisValid(ctx, project, verdict.Basis)
		if err != nil {
			return nil, err
		}
		if !basisValid {
			continue
		}
		verdicts = append(verdicts, StoredVerdict{Verdict: verdict, Key: document.Key, CreatedAt: document.CreatedAt})
	}
	return verdicts, nil
}

// latestVerdict 在满足条件的裁定里选最新并返回其生效形态；条件按原始裁定匹配。
func latestVerdict(verdicts []StoredVerdict, matches func(model.ReviewVerdict) bool) *model.ReviewVerdict {
	best := LatestStoredVerdict(verdicts, matches)
	if best == nil {
		return nil
	}
	verdict := best.Effective()
	return &verdict
}

// LatestStoredVerdict 选最新：CreatedAt 优先，相同则 Key 决胜。
func LatestStoredVerdict(verdicts []StoredVerdict, matches func(model.ReviewVerdict) bool) *StoredVerdict {
	var best *StoredVerdict
	for index := range verdicts {
		candidate := &verdicts[index]
		if !matches(candidate.Verdict) {
			continue
		}
		if best == nil || candidate.CreatedAt.After(best.CreatedAt) ||
			(candidate.CreatedAt.Equal(best.CreatedAt) && candidate.Key > best.Key) {
			best = candidate
		}
	}
	return best
}
