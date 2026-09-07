package domain

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestAuthorityTargetValidation(t *testing.T) {
	tests := []struct {
		name    string
		target  AuthorityTarget
		wantKey string
		wantErr bool
	}{
		{name: "project", target: AuthorityTarget{Kind: AuthorityProject, ID: "book-1"}, wantKey: "project:book-1"},
		{name: "profile", target: AuthorityTarget{Kind: AuthorityProfile, ID: "me", Scope: "global"}, wantKey: "creator_profile:me/global"},
		{name: "profile needs scope", target: AuthorityTarget{Kind: AuthorityProfile, ID: "me"}, wantErr: true},
		{name: "project rejects scope", target: AuthorityTarget{Kind: AuthorityProject, ID: "book-1", Scope: "global"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.target.Validate()
			if tt.wantErr {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("error = %v, want ErrInvalid", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validate: %v", err)
			}
			if got := tt.target.Key(); got != tt.wantKey {
				t.Fatalf("key = %q, want %q", got, tt.wantKey)
			}
		})
	}
}

func TestChangeSetRejectsDuplicateDocument(t *testing.T) {
	change := validChangeSet()
	change.Patches = append(change.Patches, change.Patches[0])
	if err := change.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestDeletePatchRejectsContent(t *testing.T) {
	patch := Patch{
		Document:  DocumentRef{Kind: DocumentCanon, ID: "fact-1"},
		Operation: PatchDelete,
		Content:   json.RawMessage(`{"unexpected":true}`),
	}
	if err := patch.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func validChangeSet() ChangeSet {
	decidedAt := time.Date(2026, 8, 18, 0, 0, 1, 0, time.UTC)
	return ChangeSet{
		Proposal: Proposal{
			ID:           "change-1",
			Target:       AuthorityTarget{Kind: AuthorityProject, ID: "book-1"},
			BaseRevision: InitialRevision,
			Author:       Author{Kind: AuthorUser, ID: "user-1"},
			Reason:       "创建故事意图",
			Patches: []Patch{{
				Document:  DocumentRef{Kind: DocumentIntent, ID: "root"},
				Operation: PatchPut,
				Content:   json.RawMessage(`{"premise":"凡人修仙"}`),
			}},
			ApprovalState: ApprovalApproved,
			DecidedBy:     &Author{Kind: AuthorUser, ID: "user-1"},
			DecidedAt:     &decidedAt,
			CreatedAt:     time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC),
		},
		NewRevision: 1,
	}
}
