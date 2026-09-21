package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
)

// Three identical submission failures allow two corrections before escalating.
// Successful reads/writes do not reset this counter; successful submission does.
func submissionGuard(cancel context.CancelCauseFunc) agentcore.ToolMiddleware {
	type failure struct {
		message string
		count   int
	}
	failures := make(map[string]failure)
	var mu sync.Mutex
	return func(ctx context.Context, call agentcore.ToolCall, next agentcore.ToolExecuteFunc) (json.RawMessage, error) {
		result, err := next(ctx, call.Args)
		if call.Name != prompt.ToolProposalSubmit && call.Name != prompt.ToolVerdictSubmit {
			return result, err
		}
		mu.Lock()
		defer mu.Unlock()
		if err == nil {
			delete(failures, call.Name)
			return result, nil
		}
		f := failures[call.Name]
		if f.message != err.Error() {
			f = failure{message: err.Error()}
		}
		f.count++
		failures[call.Name] = f
		if f.count >= 3 {
			cancel(fmt.Errorf("%w：%s 连续 %d 次返回相同错误，已停止本次执行并保留工作区草稿：%w", model.ErrSubmissionBlocked, call.Name, f.count, err))
		}
		return result, err
	}
}
