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
	}
}
