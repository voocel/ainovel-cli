package capability

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
)

func TestSubmissionGuardStopsRepeatingFailuresAcrossReads(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	guard := submissionGuard(cancel)
	cause := errors.New("canon old_value mismatch")
	for i := 0; i < 3; i++ {
		_, err := guard(ctx, agentcore.ToolCall{Name: prompt.ToolProposalSubmit}, func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, cause })
		if !errors.Is(err, cause) {
			t.Fatal("original error lost")
		}
		_, _ = guard(ctx, agentcore.ToolCall{Name: prompt.ToolAuthorityRead}, func(context.Context, json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
		if i < 2 && ctx.Err() != nil {
			t.Fatal("did not allow correction")
		}
	}
	if !errors.Is(context.Cause(ctx), cause) || !strings.Contains(context.Cause(ctx).Error(), "保留工作区草稿") {
		t.Fatalf("missing actionable failure: %v", context.Cause(ctx))
	}
}

func TestSubmissionGuardResetsOnProgress(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	guard := submissionGuard(cancel)
	for _, message := range []string{"A", "A", "B", "B", "", "B", "B"} {
		_, _ = guard(ctx, agentcore.ToolCall{Name: prompt.ToolProposalSubmit}, func(context.Context, json.RawMessage) (json.RawMessage, error) {
			if message == "" {
				return json.RawMessage(`{}`), nil
			}
			return nil, errors.New(message)
		})
		if ctx.Err() != nil {
			t.Fatal("different failure or success must reset the counter")
		}
	}
}
