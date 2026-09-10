package model

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestReviewVerdictDirectiveCoverageAndPassGate(t *testing.T) {
	// §4.9：任务输入携带的每条要求都必须被恰好声明一次；pass 要求全部满足，
	// 未满足只能以阻塞发现表达。
	operation := Operation{
		Kind: OperationReviewRange, Snapshot: ExecutionSnapshot{BaseRevision: 3},
		Input: json.RawMessage(`{"chapter_ids":["chapter-1"],"directives":[{"id":"hook","scope":"project","text":"结尾留钩子","status":"active"},{"id":"rain","scope":"project","text":"要下雨","status":"active"}],"basis":{"documents":[{"ref":{"kind":"manuscript","id":"chapter-1"},"revision":2}]}}`),
	}
	basis := EvidenceBasis{Documents: []DocumentBasis{{Ref: DocumentRef{Kind: DocumentManuscript, ID: "chapter-1"}, Revision: 2}}}
	base := func(status string, directives []DirectiveVerification, findings []ReviewFinding) ReviewVerdict {
		if findings == nil {
			findings = []ReviewFinding{}
		}
		return ReviewVerdict{
			Status: status, Revision: 3, ChapterIDs: []string{"chapter-1"}, ReviewKey: "review", Basis: basis,
			Directives: directives, Findings: findings,
		}
	}
	blocking := []ReviewFinding{{ChapterID: "chapter-1", Severity: FindingBlocking, Note: "结尾没有钩子"}}
	linked := []ReviewFinding{{ChapterID: "chapter-1", Severity: FindingBlocking, Note: "结尾没有钩子", DirectiveID: "hook"}}
	cases := []struct {
		name    string
		verdict ReviewVerdict
		wantErr bool
	}{
		{"missing all", base(ReviewPass, nil, nil), true},
		{"missing one", base(ReviewPass, []DirectiveVerification{{DirectiveID: "hook", Satisfied: true}}, nil), true},
		{"outside requested", base(ReviewPass, []DirectiveVerification{{DirectiveID: "hook", Satisfied: true}, {DirectiveID: "other", Satisfied: true}}, nil), true},
		{"duplicated", base(ReviewPass, []DirectiveVerification{{DirectiveID: "hook", Satisfied: true}, {DirectiveID: "hook", Satisfied: true}}, nil), true},
		{"pass with unmet", base(ReviewPass, []DirectiveVerification{{DirectiveID: "hook", Satisfied: false}, {DirectiveID: "rain", Satisfied: true}}, nil), true},
		{"blocked with unmet unlinked", base(ReviewBlocked, []DirectiveVerification{{DirectiveID: "hook", Satisfied: false}, {DirectiveID: "rain", Satisfied: true}}, blocking), true},
		{"blocked with unmet linked", base(ReviewBlocked, []DirectiveVerification{{DirectiveID: "hook", Satisfied: false}, {DirectiveID: "rain", Satisfied: true}}, linked), false},
		{"link to satisfied directive", base(ReviewBlocked, []DirectiveVerification{{DirectiveID: "hook", Satisfied: true}, {DirectiveID: "rain", Satisfied: true}}, linked), true},
		{"pass all satisfied", base(ReviewPass, []DirectiveVerification{{DirectiveID: "rain", Satisfied: true}, {DirectiveID: "hook", Satisfied: true}}, nil), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateReviewVerdictForOperation(operation, tc.verdict)
			if tc.wantErr && !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected err = %v", err)
			}
		})
	}

	// 任务未携带要求时，裁定不得凭空声明。
	plain := Operation{
		Kind: OperationReviewRange, Snapshot: ExecutionSnapshot{BaseRevision: 3},
		Input: json.RawMessage(`{"chapter_ids":["chapter-1"],"basis":{"documents":[{"ref":{"kind":"manuscript","id":"chapter-1"},"revision":2}]}}`),
	}
	if err := ValidateReviewVerdictForOperation(plain, base(ReviewPass, []DirectiveVerification{{DirectiveID: "hook", Satisfied: true}}, nil)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unrequested directive verification err = %v", err)
	}
	if err := ValidateReviewVerdictForOperation(plain, base(ReviewPass, nil, nil)); err != nil {
		t.Fatalf("plain pass err = %v", err)
	}
}

func TestOperationOutcomeCombinations(t *testing.T) {
	proposal := &Proposal{ID: "p"}
	verdict := json.RawMessage(`{"status":"pass"}`)
	artifacts := []Artifact{{
		ID: "op/cover", ProjectID: "book-1", Digest: Digest([]byte("d")), MediaType: "image/png",
		OperationID: "op", Attempt: 1, CreatedAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC),
	}}
	cases := []struct {
		name    string
		outcome OperationOutcome
		valid   bool
	}{
		{"empty", OperationOutcome{}, false},
		{"proposal", OperationOutcome{Proposal: proposal}, true},
		{"verdict", OperationOutcome{Verdict: verdict}, true},
		{"artifacts", OperationOutcome{Artifacts: artifacts}, true},
		{"proposal+verdict", OperationOutcome{Proposal: proposal, Verdict: verdict}, false},
		{"proposal+artifacts", OperationOutcome{Proposal: proposal, Artifacts: artifacts}, true},
		{"verdict+artifacts", OperationOutcome{Verdict: verdict, Artifacts: artifacts}, true},
		{"all", OperationOutcome{Proposal: proposal, Verdict: verdict, Artifacts: artifacts}, false},
	}
	for _, testCase := range cases {
		if err := testCase.outcome.Validate(); (err == nil) != testCase.valid {
			t.Fatalf("%s: err = %v, want valid=%v", testCase.name, err, testCase.valid)
		}
	}
	duplicate := OperationOutcome{Artifacts: append(append([]Artifact(nil), artifacts...), artifacts...)}
	if err := duplicate.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate artifacts err = %v", err)
	}
}
