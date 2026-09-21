package prompt

import (
	"encoding/json"
	"testing"
)

func TestSubmissionSchemaSeparatesSingleAndMultipleDrafts(t *testing.T) {
	for _, tc := range []struct{ field, version, forbidden string }{
		{"workspace_key", "workspace_version", "workspace_keys"},
		{"workspace_keys", "workspace_versions", "workspace_key"},
	} {
		tool := proposalTool([]string{tc.field})
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatal(err)
		}
		if schema.Properties[tc.field] == nil || schema.Properties[tc.version] == nil || schema.Properties[tc.forbidden] != nil {
			t.Fatalf("ambiguous schema: %s", tool.InputSchema)
		}
		if schema.Properties["confirm_canon"] == nil {
			t.Fatal("missing explicit canon confirmation")
		}
		var patches struct {
			MinItems int `json:"minItems"`
		}
		if err := json.Unmarshal(schema.Properties["patches"], &patches); err != nil || patches.MinItems != 0 {
			t.Fatal("confirmation-only submission must allow an empty patches list")
		}
	}
}
