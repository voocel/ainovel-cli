package workbench

import (
	"context"
	"sync"

	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// Reader belongs to one open workbench. Immutable authority data is reused only
// at the same revision; task state and evidence are queried on every refresh.
// Closing the workbench releases its cache, rather than retaining every book.
type Reader struct {
	query     *Query
	projectID string
	mu        sync.Mutex
	project   projectdoc.Snapshot
	loaded    bool
}

func (s *Query) OpenReader(projectID string) *Reader {
	return &Reader{query: s, projectID: projectID}
}

func (r *Reader) Snapshot(ctx context.Context) (WorkbenchSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	revision, err := r.query.store.CurrentRevision(ctx, model.AuthorityTarget{Kind: model.AuthorityProject, ID: r.projectID})
	if err != nil {
		return WorkbenchSnapshot{}, err
	}
	if !r.loaded || revision != r.project.Revision {
		project, err := r.query.projects.Project(ctx, r.projectID, revision)
		if err != nil {
			return WorkbenchSnapshot{}, err
		}
		r.project, r.loaded = project, true
	}
	return r.query.snapshotFromProject(ctx, r.project)
}
