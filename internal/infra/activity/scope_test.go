package activity

import "testing"

func TestOutputScopeSurvivesTaskAndSnapshotMutation(t *testing.T) {
	h := NewHub()
	e := Event{ProjectID: "book", RunID: "run", OperationID: "review", Kind: TaskStart, Scope: Scope{ChapterIDs: []string{"one", "two"}}}
	h.Publish(e)
	e.Scope.ChapterIDs[0] = "mutated"
	e.Kind, e.Text = Text, "审阅结果"
	h.Publish(e)
	s, _ := h.Snapshot("book")
	if s.Output[0].Scope.ChapterIDs[0] != "one" {
		t.Fatal("publisher mutated stored task scope")
	}
	s.Output[0].Scope.ChapterIDs[0] = "changed-output"
	s.Tasks[0].Scope.ChapterIDs[0] = "changed-task"
	fresh, _ := h.Snapshot("book")
	if fresh.Tasks[0].Scope.ChapterIDs[0] != "one" || fresh.Output[0].Scope.ChapterIDs[0] != "one" {
		t.Fatal("reader mutated shared scope")
	}
}
