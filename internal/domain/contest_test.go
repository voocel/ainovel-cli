package domain

import "testing"

func TestVerdict_WinnerScore(t *testing.T) {
	v := Verdict{
		Chapter: 12,
		Winner:  "wuzei",
		Scores: []PersonaScore{
			{Persona: "wuzei", Score: 8.5, Comment: "节奏好"},
			{Persona: "tudou", Score: 7.0, Comment: "略平"},
		},
		RevisionNotes: "强化钩子",
		Promoted:      false,
	}
	if got := v.WinnerScore(); got != 8.5 {
		t.Fatalf("WinnerScore = %v, want 8.5", got)
	}
}

func TestVerdict_WinnerScore_Missing(t *testing.T) {
	v := Verdict{Winner: "ghost", Scores: []PersonaScore{{Persona: "wuzei", Score: 9}}}
	if got := v.WinnerScore(); got != 0 {
		t.Fatalf("WinnerScore for missing winner = %v, want 0", got)
	}
}
