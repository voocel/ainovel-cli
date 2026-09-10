package change

import (
	"context"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// Store 定义提交机制需要的持久化契约；实现必须保证执行归属检查与写入原子化。
type Store interface {
	CommitExecutionProposal(ctx context.Context, proposal model.Proposal, attempt int) (model.ChangeSet, error)
	CommitProposal(ctx context.Context, proposal model.Proposal) (model.ChangeSet, error)
	CurrentRevision(ctx context.Context, target model.AuthorityTarget) (model.Revision, error)
	GetArtifact(ctx context.Context, id string) (model.Artifact, error)
	GetChangeSet(ctx context.Context, id string) (model.ChangeSet, error)
	GetChangeSetByRevision(
		ctx context.Context,
		target model.AuthorityTarget,
		revision model.Revision,
	) (model.ChangeSet, error)
	GetDocument(ctx context.Context, target model.AuthorityTarget, ref model.DocumentRef, at model.Revision) (model.DocumentVersion, error)
	GetProposal(ctx context.Context, id string) (model.Proposal, error)
	ListDocuments(ctx context.Context, target model.AuthorityTarget, kind model.DocumentKind, at model.Revision) ([]model.DocumentVersion, error)
	RejectProposal(ctx context.Context, proposal model.Proposal) (model.Proposal, error)
	SaveExecutionProposal(ctx context.Context, proposal model.Proposal, attempt int) (model.Proposal, error)
	SaveProposal(ctx context.Context, proposal model.Proposal) (model.Proposal, error)
}
