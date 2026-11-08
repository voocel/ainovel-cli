package diag

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestUnverifiedReviewRevisions(t *testing.T) {
	version := domain.ChapterVersion{Chapter: 3, Revision: 2, ContentSHA256: "revised"}
	review := &domain.ReviewEntry{Chapter: 4, Scope: "arc", RevisedChapters: []domain.ChapterVersion{version}}
	snap := &Snapshot{Reviews: map[int]*domain.ReviewEntry{4: review}}
	if findings := UnverifiedReviewRevisions(snap); len(findings) != 1 || findings[0].AutoLevel != AutoNone {
		t.Fatalf("改后正文未复核必须可见，但不得自动派单: %+v", findings)
	}
	review.Recheck = &domain.ReviewRecheck{Chapters: []int{3}, Result: &domain.ReviewRecheckResult{Basis: []domain.ChapterVersion{version}}}
	if findings := UnverifiedReviewRevisions(snap); len(findings) != 0 {
		t.Fatalf("已经复核的同一版本不应重复告警: %+v", findings)
	}
	review.RevisedChapters[0].Revision++
	if findings := UnverifiedReviewRevisions(snap); len(findings) != 1 {
		t.Fatalf("再次返工不能复用旧版复核: %+v", findings)
	}
}

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
