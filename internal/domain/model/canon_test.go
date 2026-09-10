package model

import (
	"encoding/json"
	"testing"
)

func TestCanonFactEffectiveChapterDefaultsToSource(t *testing.T) {
	fact := CanonFact{ID: "mood", Kind: CanonState, SubjectID: "hero", Predicate: "state.mood", Value: json.RawMessage(`"忐忑"`), SourceChapterID: "chapter-3"}
	if fact.EffectiveChapter() != "chapter-3" || fact.IsEvent() {
		t.Fatalf("effective = %q, event = %v", fact.EffectiveChapter(), fact.IsEvent())
	}
	fact.EffectiveChapterID = "chapter-1"
	if fact.EffectiveChapter() != "chapter-1" || fact.Validate() != nil {
		t.Fatalf("flashback effective = %q, err = %v", fact.EffectiveChapter(), fact.Validate())
	}
	fact.EffectiveChapterID = " "
	if err := fact.Validate(); err == nil {
		t.Fatal("blank effective chapter must be rejected")
	}
	if (CanonFact{Kind: CanonEvent}).IsEvent() != true || (CanonFact{}).EffectiveChapter() != "" {
		t.Fatal("event kind and planning-time fact classification")
	}
}
