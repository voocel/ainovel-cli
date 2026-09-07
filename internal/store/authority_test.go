package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestCommitChangeSetAndReadHistory(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}
	intent := domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"}

	first := testChange("change-1", target, 0, domain.Patch{
		Document: intent, Operation: domain.PatchPut, Content: json.RawMessage(`{"premise":"凡人修仙"}`),
	})
	committed, err := commitTestProposal(ctx, s, first)
	if err != nil {
		t.Fatalf("commit first: %v", err)
	}
	if committed.NewRevision != 1 {
		t.Fatalf("revision = %d, want 1", committed.NewRevision)
	}

	second := testChange("change-2", target, 1, domain.Patch{Document: intent, Operation: domain.PatchDelete})
	committed, err = commitTestProposal(ctx, s, second)
	if err != nil {
		t.Fatalf("commit second: %v", err)
	}
	if committed.NewRevision != 2 {
		t.Fatalf("revision = %d, want 2", committed.NewRevision)
	}

	old, err := s.GetDocument(ctx, target, intent, 1)
	if err != nil {
		t.Fatalf("get revision 1: %v", err)
	}
	if string(old.Content) != `{"premise":"凡人修仙"}` {
		t.Fatalf("content = %s", old.Content)
	}
	if _, err := s.GetDocument(ctx, target, intent, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted document error = %v, want ErrNotFound", err)
	}
	if revision, err := s.CurrentRevision(ctx, target); err != nil || revision != 2 {
		t.Fatalf("current revision = %d, %v; want 2", revision, err)
	}
}

func TestCommitChangeSetIsIdempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}
	change := testChange("same-id", target, 0, domain.Patch{
		Document:  domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"},
		Operation: domain.PatchPut,
		Content:   json.RawMessage(`{"premise":"凡人修仙"}`),
	})

	first, err := commitTestProposal(ctx, s, change)
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}
	second, err := commitTestProposal(ctx, s, change)
	if err != nil {
		t.Fatalf("idempotent commit: %v", err)
	}
	if first.NewRevision != second.NewRevision {
		t.Fatalf("revisions = %d and %d", first.NewRevision, second.NewRevision)
	}

	change.Reason = "different input"
	if _, err := commitTestProposal(ctx, s, change); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed input error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestRevisionConflictDoesNotWriteDocuments(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}
	intent := testChange("intent", target, 0, domain.Patch{
		Document:  domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"},
		Operation: domain.PatchPut,
		Content:   json.RawMessage(`{"premise":"凡人修仙"}`),
	})
	if _, err := commitTestProposal(ctx, s, intent); err != nil {
		t.Fatalf("commit intent: %v", err)
	}

	planRef := domain.DocumentRef{Kind: domain.DocumentPlan, ID: "ending"}
	stale := testChange("stale", target, 0, domain.Patch{
		Document: planRef, Operation: domain.PatchPut, Content: json.RawMessage(`{"ending":"飞升"}`),
	})
	if _, err := commitTestProposal(ctx, s, stale); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale commit error = %v, want ErrRevisionConflict", err)
	}
	if _, err := s.GetDocument(ctx, target, planRef, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale document error = %v, want ErrNotFound", err)
	}
}

func TestCommitRollsBackEveryWriteOnPatchFailure(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `
		CREATE TRIGGER fail_second_patch
		BEFORE INSERT ON document_versions
		WHEN NEW.document_id = 'fail'
		BEGIN
			SELECT RAISE(ABORT, 'injected patch failure');
		END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}
	change := testChange("atomic", target, 0,
		domain.Patch{
			Document:  domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"},
			Operation: domain.PatchPut,
			Content:   json.RawMessage(`{"premise":"凡人修仙"}`),
		},
		domain.Patch{
			Document:  domain.DocumentRef{Kind: domain.DocumentPlan, ID: "fail"},
			Operation: domain.PatchPut,
			Content:   json.RawMessage(`{"ending":"飞升"}`),
		},
	)
	if _, err := commitTestProposal(ctx, s, change); err == nil {
		t.Fatal("commit succeeded, want injected failure")
	}
	if _, err := s.CurrentRevision(ctx, target); !errors.Is(err, ErrNotFound) {
		t.Fatalf("current revision error = %v, want ErrNotFound", err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM document_versions`).Scan(&count); err != nil {
		t.Fatalf("count document versions: %v", err)
	}
	if count != 0 {
		t.Fatalf("document version count = %d, want 0", count)
	}
	proposal, err := s.GetProposal(ctx, change.ID)
	if err != nil {
		t.Fatalf("get rolled back proposal: %v", err)
	}
	if proposal.ApprovalState != domain.ApprovalPending {
		t.Fatalf("proposal state = %s, want pending", proposal.ApprovalState)
	}
}

func TestConcurrentCommitsAllowOneRevisionWinner(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}

	changes := []domain.Proposal{
		testChange("left", target, 0, domain.Patch{
			Document:  domain.DocumentRef{Kind: domain.DocumentPlan, ID: "left"},
			Operation: domain.PatchPut,
			Content:   json.RawMessage(`{"path":"left"}`),
		}),
		testChange("right", target, 0, domain.Patch{
			Document:  domain.DocumentRef{Kind: domain.DocumentPlan, ID: "right"},
			Operation: domain.PatchPut,
			Content:   json.RawMessage(`{"path":"right"}`),
		}),
	}

	start := make(chan struct{})
	errs := make(chan error, len(changes))
	var wg sync.WaitGroup
	for _, change := range changes {
		change := change
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := commitTestProposal(ctx, s, change)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	var succeeded, conflicted int
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrRevisionConflict):
			conflicted++
		default:
			t.Fatalf("unexpected commit error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded=%d conflicted=%d, want 1/1", succeeded, conflicted)
	}
}

func TestCommittedRevisionSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ainovel.db")
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}
	chapter := domain.DocumentRef{Kind: domain.DocumentManuscript, ID: "chapter-1"}

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	change := testChange("chapter", target, 0, domain.Patch{
		Document: chapter, Operation: domain.PatchPut, Content: json.RawMessage(`{"title":"第一章"}`),
	})
	if _, err := commitTestProposal(ctx, s, change); err != nil {
		t.Fatalf("commit chapter: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer s.Close()
	document, err := s.GetDocument(ctx, target, chapter, 1)
	if err != nil {
		t.Fatalf("read reopened chapter: %v", err)
	}
	if document.Revision != 1 || string(document.Content) != `{"title":"第一章"}` {
		t.Fatalf("reopened document = revision %d, content %s", document.Revision, document.Content)
	}
}

func TestListDocumentsReturnsLatestLiveVersions(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: "book-1"}

	first := testChange("first", target, 0,
		domain.Patch{Document: domain.DocumentRef{Kind: domain.DocumentPlan, ID: "arc-1"}, Operation: domain.PatchPut, Content: json.RawMessage(`{"title":"旧标题"}`)},
		domain.Patch{Document: domain.DocumentRef{Kind: domain.DocumentPlan, ID: "arc-2"}, Operation: domain.PatchPut, Content: json.RawMessage(`{"title":"第二弧"}`)},
	)
	if _, err := commitTestProposal(ctx, s, first); err != nil {
		t.Fatalf("commit initial plan: %v", err)
	}
	second := testChange("second", target, 1,
		domain.Patch{Document: domain.DocumentRef{Kind: domain.DocumentPlan, ID: "arc-1"}, Operation: domain.PatchPut, Content: json.RawMessage(`{"title":"新标题"}`)},
		domain.Patch{Document: domain.DocumentRef{Kind: domain.DocumentPlan, ID: "arc-2"}, Operation: domain.PatchDelete},
	)
	if _, err := commitTestProposal(ctx, s, second); err != nil {
		t.Fatalf("commit revised plan: %v", err)
	}

	documents, err := s.ListDocuments(ctx, target, domain.DocumentPlan, 2)
	if err != nil {
		t.Fatalf("list plans: %v", err)
	}
	if len(documents) != 1 || documents[0].Document.ID != "arc-1" || string(documents[0].Content) != `{"title":"新标题"}` {
		t.Fatalf("documents = %#v", documents)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return s
}

func testChange(id string, target domain.AuthorityTarget, base domain.Revision, patches ...domain.Patch) domain.Proposal {
	decidedAt := time.Date(2026, 8, 18, 0, 0, 1, 0, time.UTC)
	return domain.Proposal{
		ID:            id,
		Target:        target,
		BaseRevision:  base,
		Author:        domain.Author{Kind: domain.AuthorUser, ID: "user-1"},
		Reason:        "test change",
		Patches:       patches,
		ApprovalState: domain.ApprovalApproved,
		DecidedBy:     &domain.Author{Kind: domain.AuthorUser, ID: "user-1"},
		DecidedAt:     &decidedAt,
		CreatedAt:     time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC),
	}
}

func commitTestProposal(ctx context.Context, s *Store, proposal domain.Proposal) (domain.ChangeSet, error) {
	if _, err := s.GetProposal(ctx, proposal.ID); errors.Is(err, ErrNotFound) {
		pending := proposal
		pending.ApprovalState = domain.ApprovalPending
		pending.DecidedBy = nil
		pending.DecidedAt = nil
		if _, err := s.SaveProposal(ctx, pending); err != nil {
			return domain.ChangeSet{}, err
		}
	} else if err != nil {
		return domain.ChangeSet{}, err
	}
	return s.CommitProposal(ctx, proposal)
}
