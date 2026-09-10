package domain

import "testing"

func TestExternalRequestIdentityIncludesExecutionAndSource(t *testing.T) {
	operation := Operation{Target: AuthorityTarget{Kind: AuthorityProject, ID: "book"}, Snapshot: ExecutionSnapshot{Executor: "external@1", ConfigDigest: "config", InputDigest: "input-with-basis", BaseRevision: 1}}
	identity := ExternalIdentityFor(operation)
	for _, change := range []func(*Operation){
		func(o *Operation) { o.Snapshot.Executor = "external@2" },
		func(o *Operation) { o.Snapshot.ConfigDigest = "config2" },
		func(o *Operation) { o.Snapshot.InputDigest = "input-with-new-basis" },
		func(o *Operation) { o.Snapshot.BaseRevision++ },
		func(o *Operation) { o.Target.ID = "another-book" },
	} {
		altered := operation
		change(&altered)
		if identity == ExternalIdentityFor(altered) {
			t.Fatal("changed generation identity still matches")
		}
	}
	operation.ID = "successor"
	operation.Attempt++
	if identity != ExternalIdentityFor(operation) {
		t.Fatal("restart identity alone must not invalidate identical generation inputs")
	}
}
