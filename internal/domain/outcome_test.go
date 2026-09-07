package domain

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestReviewVerdictDirectiveCoverageAndPassGate(t *testing.T) {
	// §4.9：任务输入携带的每条要求都必须被恰好声明一次；pass 要求全部满足，
	// 未满足只能以阻塞发现表达。
	operation := Operation{
		Kind: OperationReviewRange, Snapshot: ExecutionSnapshot{BaseRevision: 3},
		Input: json.RawMessage(`{"range":{"chapter_ids":["chapter-1"]},"directives":[{"id":"hook"},{"id":"rain"}]}`),
	}
	base := func(status string, directives []DirectiveVerification, findings []ReviewFinding) ReviewVerdict {
		if findings == nil {
			findings = []ReviewFinding{}
		}
		return ReviewVerdict{
			Status: status, Revision: 3, ChapterIDs: []string{"chapter-1"}, ReviewKey: "review",
			Directives: directives, Findings: findings,
		}
	}
	blocking := []ReviewFinding{{ChapterID: "chapter-1", Severity: FindingBlocking, Note: "结尾没有钩子"}}
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
		{"blocked with unmet", base(ReviewBlocked, []DirectiveVerification{{DirectiveID: "hook", Satisfied: false}, {DirectiveID: "rain", Satisfied: true}}, blocking), false},
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
		Input: json.RawMessage(`{"range":{"chapter_ids":["chapter-1"]}}`),
	}
	if err := ValidateReviewVerdictForOperation(plain, base(ReviewPass, []DirectiveVerification{{DirectiveID: "hook", Satisfied: true}}, nil)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unrequested directive verification err = %v", err)
	}
	if err := ValidateReviewVerdictForOperation(plain, base(ReviewPass, nil, nil)); err != nil {
		t.Fatalf("plain pass err = %v", err)
	}
}
