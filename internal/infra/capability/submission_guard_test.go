package capability

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
)

// returning 是总返回 result 的工具调用。
func returning(result agentcore.Result) agentcore.ToolFunc {
	return func(context.Context, agentcore.ToolCall) (agentcore.Result, error) { return result, nil }
}

func TestSubmissionGuardStopsRepeatingFailuresAcrossReads(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	guard := submissionGuard(cancel)
	for i := 0; i < 3; i++ {
		result, err := guard(ctx, agentcore.ToolCall{Name: prompt.ToolProposalSubmit}, returning(agentcore.ErrorResult("canon old_value mismatch")))
		if err != nil || !result.IsError || result.Text() != "canon old_value mismatch" {
			t.Fatalf("failure must reach the model as it is: %+v, %v", result, err)
		}
		_, _ = guard(ctx, agentcore.ToolCall{Name: prompt.ToolAuthorityRead}, returning(agentcore.TextResult("{}")))
		if i < 2 && ctx.Err() != nil {
			t.Fatal("did not allow correction")
		}
	}
	cause := context.Cause(ctx)
	if !errors.Is(cause, model.ErrSubmissionBlocked) || !strings.Contains(cause.Error(), "canon old_value mismatch") || !strings.Contains(cause.Error(), "保留工作区草稿") {
		t.Fatalf("missing actionable failure: %v", cause)
	}
}

func TestSubmissionGuardResetsOnProgress(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	guard := submissionGuard(cancel)
	for _, message := range []string{"A", "A", "B", "B", "", "B", "B"} {
		result := agentcore.TextResult("{}")
		if message != "" {
			result = agentcore.ErrorResult(message)
		}
		_, _ = guard(ctx, agentcore.ToolCall{Name: prompt.ToolProposalSubmit}, returning(result))
		if ctx.Err() != nil {
			t.Fatal("different failure or success must reset the counter")
		}
	}
}
