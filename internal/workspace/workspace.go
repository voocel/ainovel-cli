// Package workspace 提供 Operation 工作区的章节级读写。
//
// 工作区是持久化但非权威的草稿区：Worker 在其中反复编辑，只有最终候选才经
// Proposal 进入 Change Engine。它的消费者是 capability（Worker 工具层），
// 因此独立成包——放在 operation 下会让 capability 反向依赖 operation。
package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

const ChapterMediaType = "application/vnd.ainovel.manuscript+json"

var ErrBlockNotFound = errors.New("workspace block not found")

type Service struct {
	store *store.Store
}

func New(authorityStore *store.Store) *Service {
	return &Service{store: authorityStore}
}

func (s *Service) PutChapter(
	ctx context.Context,
	operationID, key string,
	chapter domain.ManuscriptChapter,
	expectedVersion int64,
	attempt int,
	updatedAt time.Time,
) (domain.WorkspaceArtifact, error) {
	payload, err := json.Marshal(chapter)
	if err != nil {
		return domain.WorkspaceArtifact{}, fmt.Errorf("encode workspace chapter: %w", err)
	}
	ref := domain.DocumentRef{Kind: domain.DocumentManuscript, ID: chapter.ID}
	if err := domain.ValidateDocumentContent(ref, payload); err != nil {
		return domain.WorkspaceArtifact{}, err
	}
	return s.store.PutWorkspaceArtifact(ctx, domain.WorkspaceArtifact{
		OperationID: operationID,
		Key:         key,
		MediaType:   ChapterMediaType,
		Content:     payload,
		UpdatedAt:   updatedAt,
	}, expectedVersion, attempt)
}

func (s *Service) ReplaceChapterBlock(
	ctx context.Context,
	operationID, key, blockID, text string,
	expectedVersion int64,
	attempt int,
	updatedAt time.Time,
) (domain.WorkspaceArtifact, error) {
	if strings.TrimSpace(blockID) == "" || strings.TrimSpace(text) == "" {
		return domain.WorkspaceArtifact{}, fmt.Errorf("block id and text are required: %w", domain.ErrInvalid)
	}
	artifact, err := s.store.GetWorkspaceArtifact(ctx, operationID, key)
	if err != nil {
		return domain.WorkspaceArtifact{}, err
	}
	if artifact.Version != expectedVersion {
		return domain.WorkspaceArtifact{}, fmt.Errorf(
			"expected workspace version %d, current %d: %w",
			expectedVersion, artifact.Version, store.ErrWorkspaceConflict,
		)
	}
	if artifact.MediaType != ChapterMediaType {
		return domain.WorkspaceArtifact{}, fmt.Errorf("artifact %q is %q, not a chapter: %w", key, artifact.MediaType, domain.ErrInvalid)
	}
	var chapter domain.ManuscriptChapter
	if err := json.Unmarshal(artifact.Content, &chapter); err != nil {
		return domain.WorkspaceArtifact{}, fmt.Errorf("decode workspace chapter: %w", err)
	}
	ref := domain.DocumentRef{Kind: domain.DocumentManuscript, ID: chapter.ID}
	if err := domain.ValidateDocumentContent(ref, artifact.Content); err != nil {
		return domain.WorkspaceArtifact{}, fmt.Errorf("validate workspace chapter: %w", err)
	}
	found := false
	for i := range chapter.Blocks {
		if chapter.Blocks[i].ID == blockID {
			chapter.Blocks[i].Text = text
			found = true
			break
		}
	}
	if !found {
		return domain.WorkspaceArtifact{}, fmt.Errorf("chapter block %q: %w", blockID, ErrBlockNotFound)
	}
	return s.PutChapter(ctx, operationID, key, chapter, expectedVersion, attempt, updatedAt)
}
