package diag

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestPlanActionsOnlyHighConfSafe(t *testing.T) {
	findings := []Finding{
		{Rule: "PhaseFlowMismatch", Severity: SevCritical, Confidence: ConfHigh, AutoLevel: AutoSafe},
		{Rule: "ChronicLowDimension", Severity: SevWarning, Confidence: ConfMedium, AutoLevel: AutoNone},
		{Rule: "WordCountAnomaly", Severity: SevInfo, Confidence: ConfLow, AutoLevel: AutoNone},
	}
	actions := PlanActions(findings)
	for _, a := range actions {
		if a.SourceRule != "PhaseFlowMismatch" {
			t.Fatalf("unexpected action from rule %q, only PhaseFlowMismatch should produce actions", a.SourceRule)
		}
	}
	if len(actions) == 0 {
		t.Fatal("expected at least one action from PhaseFlowMismatch")
	}
}

func TestPlanActionsDedup(t *testing.T) {
	findings := []Finding{
		{Rule: "OrphanedSteer", Severity: SevWarning, Confidence: ConfHigh, AutoLevel: AutoSafe},
		{Rule: "OrphanedSteer", Severity: SevWarning, Confidence: ConfHigh, AutoLevel: AutoSafe},
	}
	actions := PlanActions(findings)
	count := 0
	for _, a := range actions {
		if a.SourceRule == "OrphanedSteer" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected 1 action from OrphanedSteer (dedup), got %d", count)
	}
}

func TestPhaseFlowMismatchMeta(t *testing.T) {
	snap := &Snapshot{
		Progress: &domain.Progress{
			Phase: domain.PhaseOutline,
			Flow:  domain.FlowRewriting,
		},
	}
	findings := PhaseFlowMismatch(snap)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	f := findings[0]
	if f.Confidence != ConfHigh || f.AutoLevel != AutoSafe {
		t.Fatalf("expected high/safe, got %s/%s", f.Confidence, f.AutoLevel)
	}
	actions := PlanActions(findings)
	if len(actions) == 0 {
		t.Fatal("expected actions from PhaseFlowMismatch")
	}
	hasFollowUp := false
	for _, a := range actions {
		if a.Kind == ActionEnqueueFollowUp {
			hasFollowUp = true
		}
	}
	if !hasFollowUp {
		t.Fatal("expected enqueue_follow_up action")
	}
}

func TestOutlineExhaustedMeta(t *testing.T) {
	snap := &Snapshot{
		Progress: &domain.Progress{
			Phase:             domain.PhaseWriting,
			TotalChapters:     5,
			CompletedChapters: []int{1, 2, 3, 4, 5},
		},
	}
	findings := OutlineExhausted(snap)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	f := findings[0]
	if f.Confidence != ConfHigh || f.AutoLevel != AutoSafe {
		t.Fatalf("expected high/safe, got %s/%s", f.Confidence, f.AutoLevel)
	}
	actions := PlanActions(findings)
	if len(actions) != 1 || actions[0].Kind != ActionEnqueueFollowUp {
		t.Fatalf("expected 1 enqueue_follow_up action, got %+v", actions)
	}
}

func TestOrphanedSteerMeta(t *testing.T) {
	snap := &Snapshot{
		RunMeta: &domain.RunMeta{
			PendingSteer: "把主角的性格改一下",
		},
		Progress: &domain.Progress{
			Flow: domain.FlowWriting,
		},
	}
	findings := OrphanedSteer(snap)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	f := findings[0]
	if f.Confidence != ConfHigh || f.AutoLevel != AutoSafe {
		t.Fatalf("expected high/safe, got %s/%s", f.Confidence, f.AutoLevel)
	}
	actions := PlanActions(findings)
	if len(actions) != 1 || actions[0].Kind != ActionEnqueueFollowUp {
		t.Fatalf("expected 1 enqueue_follow_up action, got %+v", actions)
	}
}
