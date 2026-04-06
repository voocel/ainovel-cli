package diag

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestChapterPlanInjectionGap(t *testing.T) {
	snap := &Snapshot{
		ChapterTraces: map[int]*chapterTrace{
			1: {Chapter: 1, Context: &domain.ContextBuildEvidence{Chapter: 1, HasChapterPlan: false}, ReviewOutcome: &domain.ReviewOutcomeEvidence{Chapter: 1, Verdict: "rewrite"}},
			2: {Chapter: 2, Context: &domain.ContextBuildEvidence{Chapter: 2, HasChapterPlan: false}, ReviewOutcome: &domain.ReviewOutcomeEvidence{Chapter: 2, Verdict: "polish"}},
			3: {Chapter: 3, Context: &domain.ContextBuildEvidence{Chapter: 3, HasChapterPlan: true}, ReviewOutcome: &domain.ReviewOutcomeEvidence{Chapter: 3, Verdict: "rewrite"}},
		},
	}

	findings := ChapterPlanInjectionGap(snap)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].Rule != "ChapterPlanInjectionGap" {
		t.Fatalf("unexpected finding: %+v", findings[0])
	}
	if !strings.Contains(findings[0].Evidence, "缺少 chapter_plan") && !strings.Contains(findings[0].Evidence, "缺少 chapter_plan:") {
		t.Fatalf("unexpected evidence: %s", findings[0].Evidence)
	}
}

func TestContinuitySupportWeak(t *testing.T) {
	snap := &Snapshot{
		ChapterTraces: map[int]*chapterTrace{
			4: {Chapter: 4, Context: &domain.ContextBuildEvidence{Chapter: 4}, ReviewOutcome: &domain.ReviewOutcomeEvidence{Chapter: 4, Verdict: "rewrite", LowDimensions: []string{"continuity"}}},
			5: {Chapter: 5, Context: &domain.ContextBuildEvidence{Chapter: 5, TimelineCount: 1}, ReviewOutcome: &domain.ReviewOutcomeEvidence{Chapter: 5, Verdict: "rewrite", LowDimensions: []string{"continuity"}}},
			6: {Chapter: 6, Context: &domain.ContextBuildEvidence{Chapter: 6}, ReviewOutcome: &domain.ReviewOutcomeEvidence{Chapter: 6, Verdict: "polish", LowDimensions: []string{"continuity"}}},
		},
	}

	findings := ContinuitySupportWeak(snap)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].Rule != "ContinuitySupportWeak" {
		t.Fatalf("unexpected finding: %+v", findings[0])
	}
}

func TestRewriteAfterCompaction(t *testing.T) {
	snap := &Snapshot{
		ChapterTraces: map[int]*chapterTrace{
			7: {Chapter: 7, ContextCompacted: true, ContextCompactionKind: "compacted", ReviewOutcome: &domain.ReviewOutcomeEvidence{Chapter: 7, Verdict: "rewrite"}},
			8: {Chapter: 8, ContextCompacted: true, ContextCompactionKind: "recovered", ReviewOutcome: &domain.ReviewOutcomeEvidence{Chapter: 8, Verdict: "rewrite"}},
			9: {Chapter: 9, ContextCompacted: true, ContextCompactionKind: "compacted", ReviewOutcome: &domain.ReviewOutcomeEvidence{Chapter: 9, Verdict: "accept"}},
		},
	}

	findings := RewriteAfterCompaction(snap)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].Rule != "RewriteAfterCompaction" {
		t.Fatalf("unexpected finding: %+v", findings[0])
	}
}

func TestContractExecutionWeakPrefersPromptDiagnosisWhenContractPresent(t *testing.T) {
	snap := &Snapshot{
		ChapterTraces: map[int]*chapterTrace{
			10: {Chapter: 10, Context: &domain.ContextBuildEvidence{Chapter: 10, HasChapterContract: true}, ReviewOutcome: &domain.ReviewOutcomeEvidence{Chapter: 10, Verdict: "rewrite", ContractStatus: "missed"}},
			11: {Chapter: 11, Context: &domain.ContextBuildEvidence{Chapter: 11, HasChapterContract: true}, ReviewOutcome: &domain.ReviewOutcomeEvidence{Chapter: 11, Verdict: "polish", ContractStatus: "partial"}},
			12: {Chapter: 12, Context: &domain.ContextBuildEvidence{Chapter: 12, HasChapterContract: true}, ReviewOutcome: &domain.ReviewOutcomeEvidence{Chapter: 12, Verdict: "rewrite", ContractStatus: "missed"}},
		},
	}

	findings := ContractExecutionWeak(snap)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].Target != "prompt.writer" {
		t.Fatalf("expected prompt.writer target, got %+v", findings[0])
	}
}
