package policy_test

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/orchestrator/action"
	"github.com/voocel/ainovel-cli/internal/orchestrator/policy"
)

func TestEvaluateCommitPolicy_RewriteFlowUnexpectedChapter(t *testing.T) {
	progress := &domain.Progress{
		Flow:            domain.FlowRewriting,
		PendingRewrites: []int{2, 3},
	}
	result := &domain.CommitResult{
		Chapter: 4,
	}

	actions := policy.EvaluateCommit(progress, nil, result)
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(actions))
	}
	if actions[0].Kind != action.KindFollowUp {
		t.Fatalf("expected follow_up action, got %s", actions[0].Kind)
	}
	if actions[0].Message == "" {
		t.Fatalf("expected follow_up message")
	}
}

func TestEvaluateCommitPolicy_LayeredArcEnd(t *testing.T) {
	progress := &domain.Progress{
		Layered:       true,
		TotalChapters: 12,
	}
	result := &domain.CommitResult{
		Chapter:    6,
		ArcEnd:     true,
		Volume:     1,
		Arc:        2,
		VolumeEnd:  false,
		NextVolume: 1,
		NextArc:    3,
	}

	actions := policy.EvaluateCommit(progress, nil, result)
	if len(actions) == 0 {
		t.Fatalf("expected actions, got none")
	}
	if actions[0].Kind != action.KindSetFlow || actions[0].Flow != domain.FlowReviewing {
		t.Fatalf("expected first action set_flow(reviewing), got %+v", actions[0])
	}

	var hasFollowUp, hasCheckpoint bool
	for _, act := range actions {
		switch act.Kind {
		case action.KindFollowUp:
			hasFollowUp = true
		case action.KindSaveCheckpoint:
			hasCheckpoint = true
		}
	}
	if !hasFollowUp || !hasCheckpoint {
		t.Fatalf("expected follow_up/checkpoint actions, got %+v", actions)
	}
}

func TestEvaluateReviewPolicy_Rewrite(t *testing.T) {
	review := &domain.ReviewEntry{
		Chapter:          8,
		Verdict:          "rewrite",
		Summary:          "主角动机不连贯",
		AffectedChapters: []int{7, 8},
	}

	actions := policy.EvaluateReview(nil, review)
	if len(actions) == 0 {
		t.Fatalf("expected actions, got none")
	}

	var hasPending, hasFlow, hasFollowUp bool
	for _, act := range actions {
		switch act.Kind {
		case action.KindSetPendingRewrites:
			hasPending = true
		case action.KindSetFlow:
			hasFlow = act.Flow == domain.FlowRewriting
		case action.KindFollowUp:
			hasFollowUp = true
		}
	}
	if !hasPending || !hasFlow || !hasFollowUp {
		t.Fatalf("expected pending/flow/follow_up actions, got %+v", actions)
	}
}

func TestEvaluateReviewPolicy_AcceptWithPartialContractEscalatesToPolish(t *testing.T) {
	review := &domain.ReviewEntry{
		Chapter:          6,
		Verdict:          "accept",
		ContractStatus:   "partial",
		ContractMisses:   []string{"本章未明确埋下第二条支线伏笔"},
		ContractNotes:    "主线推进达成，但 contract 仍有漏项。",
		Summary:          "整体可读，但 contract 没有完全达成。",
		AffectedChapters: []int{6},
	}

	actions := policy.EvaluateReview(nil, review)
	if len(actions) == 0 {
		t.Fatal("expected actions, got none")
	}

	var hasWarnNotice, hasFlow, hasPending, hasFollowUp bool
	for _, act := range actions {
		switch act.Kind {
		case action.KindEmitNotice:
			if act.Summary == "contract_status=partial，accept 被提升为 polish" {
				hasWarnNotice = true
			}
		case action.KindSetFlow:
			hasFlow = act.Flow == domain.FlowPolishing
		case action.KindSetPendingRewrites:
			hasPending = true
		case action.KindFollowUp:
			if act.Message != "" && act.Message != "[系统] " {
				hasFollowUp = true
			}
		}
	}
	if !hasWarnNotice || !hasFlow || !hasPending || !hasFollowUp {
		t.Fatalf("expected escalation to polish, got %+v", actions)
	}
}

func TestEvaluateReviewPolicy_AcceptWithMissedContractEscalatesToRewrite(t *testing.T) {
	review := &domain.ReviewEntry{
		Chapter:          6,
		Verdict:          "accept",
		ContractStatus:   "missed",
		ContractMisses:   []string{"核心 required_beats 未完成"},
		ContractNotes:    "本章直接跳过了 contract 要求的核心推进。",
		Summary:          "文字尚可，但 contract 核心目标没有完成。",
		AffectedChapters: []int{6},
	}

	actions := policy.EvaluateReview(nil, review)
	if len(actions) == 0 {
		t.Fatal("expected actions, got none")
	}

	var hasWarnNotice, hasFlow bool
	for _, act := range actions {
		switch act.Kind {
		case action.KindEmitNotice:
			if act.Summary == "contract_status=missed，accept 被提升为 rewrite" {
				hasWarnNotice = true
			}
		case action.KindSetFlow:
			hasFlow = act.Flow == domain.FlowRewriting
		}
	}
	if !hasWarnNotice || !hasFlow {
		t.Fatalf("expected escalation to rewrite, got %+v", actions)
	}
}

func TestEvaluateReviewPolicy_ContractMissedOverridesScorecardPolish(t *testing.T) {
	review := &domain.ReviewEntry{
		Chapter:        6,
		Verdict:        "accept",
		ContractStatus: "missed",
		Summary:        "contract 核心目标未完成。",
		Dimensions: []domain.DimensionScore{
			{Dimension: "consistency", Score: 88, Verdict: "pass", Comment: "一致"},
			{Dimension: "character", Score: 86, Verdict: "pass", Comment: "稳定"},
			{Dimension: "pacing", Score: 82, Verdict: "pass", Comment: "正常"},
			{Dimension: "continuity", Score: 84, Verdict: "pass", Comment: "连贯"},
			{Dimension: "foreshadow", Score: 80, Verdict: "pass", Comment: "正常"},
			{Dimension: "hook", Score: 78, Verdict: "warning", Comment: "偏弱"},
			{Dimension: "aesthetic", Score: 58, Verdict: "fail", Comment: "文风重复"},
		},
		AffectedChapters: []int{6},
	}

	actions := policy.EvaluateReview(nil, review)
	if len(actions) == 0 {
		t.Fatal("expected actions, got none")
	}

	var hasContractRewrite, hasPolishNotice bool
	for _, act := range actions {
		if act.Kind != action.KindEmitNotice {
			continue
		}
		if act.Summary == "contract_status=missed，accept 被提升为 rewrite" {
			hasContractRewrite = true
		}
		if act.Summary == "scorecard gate 触发，accept 被提升为 polish" {
			hasPolishNotice = true
		}
	}
	if !hasContractRewrite || hasPolishNotice {
		t.Fatalf("expected contract gate to override scorecard, got %+v", actions)
	}
}

func TestEvaluateReviewPolicy_AcceptWithCriticalScorecardFailureEscalatesToRewrite(t *testing.T) {
	review := &domain.ReviewEntry{
		Chapter: 5,
		Verdict: "accept",
		Summary: "总体可读。",
		Dimensions: []domain.DimensionScore{
			{Dimension: "consistency", Score: 55, Verdict: "fail", Comment: "设定冲突"},
			{Dimension: "character", Score: 86, Verdict: "pass", Comment: "稳定"},
			{Dimension: "pacing", Score: 82, Verdict: "pass", Comment: "正常"},
			{Dimension: "continuity", Score: 84, Verdict: "pass", Comment: "连贯"},
			{Dimension: "foreshadow", Score: 80, Verdict: "pass", Comment: "正常"},
			{Dimension: "hook", Score: 78, Verdict: "warning", Comment: "偏弱"},
			{Dimension: "aesthetic", Score: 76, Verdict: "warning", Comment: "可打磨"},
		},
		AffectedChapters: []int{5},
	}

	actions := policy.EvaluateReview(nil, review)
	if len(actions) == 0 {
		t.Fatal("expected actions, got none")
	}

	var hasWarnNotice, hasFlow bool
	for _, act := range actions {
		switch act.Kind {
		case action.KindEmitNotice:
			if act.Summary == "scorecard gate 触发，accept 被提升为 rewrite" {
				hasWarnNotice = true
			}
		case action.KindSetFlow:
			hasFlow = act.Flow == domain.FlowRewriting
		}
	}
	if !hasWarnNotice || !hasFlow {
		t.Fatalf("expected scorecard escalation to rewrite, got %+v", actions)
	}
}

func TestEvaluateReviewPolicy_AcceptWithAestheticFailEscalatesToPolish(t *testing.T) {
	review := &domain.ReviewEntry{
		Chapter: 5,
		Verdict: "accept",
		Summary: "总体可读。",
		Dimensions: []domain.DimensionScore{
			{Dimension: "consistency", Score: 88, Verdict: "pass", Comment: "一致"},
			{Dimension: "character", Score: 85, Verdict: "pass", Comment: "稳定"},
			{Dimension: "pacing", Score: 82, Verdict: "pass", Comment: "正常"},
			{Dimension: "continuity", Score: 84, Verdict: "pass", Comment: "连贯"},
			{Dimension: "foreshadow", Score: 80, Verdict: "pass", Comment: "正常"},
			{Dimension: "hook", Score: 78, Verdict: "warning", Comment: "偏弱"},
			{Dimension: "aesthetic", Score: 58, Verdict: "fail", Comment: "文风重复"},
		},
		AffectedChapters: []int{5},
	}

	actions := policy.EvaluateReview(nil, review)
	if len(actions) == 0 {
		t.Fatal("expected actions, got none")
	}

	var hasWarnNotice, hasFlow bool
	for _, act := range actions {
		switch act.Kind {
		case action.KindEmitNotice:
			if act.Summary == "scorecard gate 触发，accept 被提升为 polish" {
				hasWarnNotice = true
			}
		case action.KindSetFlow:
			hasFlow = act.Flow == domain.FlowPolishing
		}
	}
	if !hasWarnNotice || !hasFlow {
		t.Fatalf("expected scorecard escalation to polish, got %+v", actions)
	}
}

func TestEvaluateReviewPolicy_UsesEscalatedVerdictInCheckpointLabels(t *testing.T) {
	review := &domain.ReviewEntry{
		Chapter:          5,
		Verdict:          "accept",
		ContractStatus:   "partial",
		Summary:          "需要打磨。",
		AffectedChapters: []int{5},
	}

	actions := policy.EvaluateReview(nil, review)
	if len(actions) == 0 {
		t.Fatal("expected actions, got none")
	}

	var hasPolishCheckpoint, hasAcceptCheckpoint bool
	for _, act := range actions {
		if act.Kind != action.KindSaveCheckpoint {
			continue
		}
		if act.Label == "review-ch05-polish" {
			hasPolishCheckpoint = true
		}
		if act.Label == "review-ch05-accept" {
			hasAcceptCheckpoint = true
		}
	}
	if !hasPolishCheckpoint || hasAcceptCheckpoint {
		t.Fatalf("expected escalated checkpoint label, got %+v", actions)
	}
}

func TestEvaluateCommitPolicy_AppendsPendingSteerReminder(t *testing.T) {
	result := &domain.CommitResult{
		Chapter: 2,
	}
	runMeta := &domain.RunMeta{
		PendingSteer: "把主角改成女性",
	}

	actions := policy.EvaluateCommit(nil, runMeta, result)
	var hasReminder, hasSteerFollowUp, hasClear bool
	for _, act := range actions {
		switch act.Kind {
		case action.KindEmitNotice:
			if act.Summary == "提醒 Coordinator 处理用户干预" {
				hasReminder = true
			}
		case action.KindFollowUp:
			if act.Message != "" {
				hasSteerFollowUp = true
			}
		case action.KindClearHandledSteer:
			hasClear = true
		}
	}
	if !hasReminder || !hasSteerFollowUp || !hasClear {
		t.Fatalf("expected reminder/follow_up/clear actions, got %+v", actions)
	}
}
