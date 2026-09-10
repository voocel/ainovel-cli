package service

import (
	"context"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func (s *Service) Artifacts(ctx context.Context, projectID string) ([]domain.Artifact, error) {
	return s.store.ListArtifacts(ctx, projectID)
}

// CollectArtifactGarbage 显式回收无引用且超过宽限期的工件对象与暂存文件，返回删除数。
func (s *Service) CollectArtifactGarbage(ctx context.Context, grace time.Duration) (int, error) {
	return s.store.CollectArtifactGarbage(ctx, s.now(), grace)
}
