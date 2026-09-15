package capability

import (
	"testing"

	"github.com/voocel/agentcore"
)

// Each Execute creates a fresh agent. Restoring previous messages must not
// turn its end summary into a cumulative bill for previous attempts.
func TestRestoredMessagesDoNotAccumulateAttemptUsage(t *testing.T) {
	agent := agentcore.NewAgent()
	if err := agent.ImportMessages([]agentcore.Message{{Role: agentcore.RoleAssistant, Usage: &agentcore.Usage{Input: 100, Output: 20, TotalTokens: 120}}}); err != nil {
		t.Fatal(err)
	}
	usage := agent.State().TotalUsage
	if usage.Input != 0 || usage.Output != 0 || usage.TotalTokens != 0 {
		t.Fatalf("restored usage counted again: %+v", usage)
	}
}
