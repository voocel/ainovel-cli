package diag

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestHookWeakChain(t *testing.T) {
	snap := &Snapshot{
		Reviews: map[int]*domain.ReviewEntry{
			1: {Chapter: 1, Scope: "chapter", Dimensions: []domain.DimensionScore{{Dimension: "hook", Score: 72, Verdict: "warning"}}},
			2: {Chapter: 2, Scope: "chapter", Dimensions: []domain.DimensionScore{{Dimension: "hook", Score: 68, Verdict: "warning"}}},
			3: {Chapter: 3, Scope: "chapter", Dimensions: []domain.DimensionScore{{Dimension: "hook", Score: 74, Verdict: "warning"}}},
			4: {Chapter: 4, Scope: "chapter", Dimensions: []domain.DimensionScore{{Dimension: "hook", Score: 88, Verdict: "pass"}}},
		},
	}

	findings := HookWeakChain(snap)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].Rule != "HookWeakChain" {
		t.Fatalf("unexpected rule: %+v", findings[0])
	}
	if !strings.Contains(findings[0].Evidence, "ch1(72)") || !strings.Contains(findings[0].Evidence, "ch3(74)") {
		t.Fatalf("unexpected evidence: %s", findings[0].Evidence)
	}
}

func TestPayoffMissPattern(t *testing.T) {
	snap := &Snapshot{
		Plans: map[int]*domain.ChapterPlan{
			1: {Chapter: 1, Contract: domain.ChapterContract{PayoffPoints: []string{"首战取胜"}}},
			2: {Chapter: 2, Contract: domain.ChapterContract{PayoffPoints: []string{"确认搭档关系"}}},
			3: {Chapter: 3, Contract: domain.ChapterContract{PayoffPoints: []string{"揭开真相一角"}}},
		},
		Reviews: map[int]*domain.ReviewEntry{
			1: {Chapter: 1, Scope: "chapter", ContractStatus: "partial"},
			2: {Chapter: 2, Scope: "chapter", ContractStatus: "missed"},
			3: {Chapter: 3, Scope: "chapter", ContractStatus: "met"},
		},
	}

	findings := PayoffMissPattern(snap)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].Rule != "PayoffMissPattern" {
		t.Fatalf("unexpected rule: %+v", findings[0])
	}
	if !strings.Contains(findings[0].Evidence, "ch1(1项 payoff)") || !strings.Contains(findings[0].Evidence, "2/3") {
		t.Fatalf("unexpected evidence: %s", findings[0].Evidence)
	}
}
