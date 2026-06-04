package sim

import (
	"encoding/json"
	"fmt"
	"strings"
)

func parseJSONPayload(text string, out any) error {
	body := strings.TrimSpace(text)
	if strings.HasPrefix(body, "```") {
		lines := strings.Split(body, "\n")
		if len(lines) >= 2 {
			lines = lines[1:]
			if n := len(lines); n > 0 && strings.HasPrefix(strings.TrimSpace(lines[n-1]), "```") {
				lines = lines[:n-1]
			}
			body = strings.TrimSpace(strings.Join(lines, "\n"))
		}
	}
	start := strings.Index(body, "{")
	end := strings.LastIndex(body, "}")
	if start < 0 || end < start {
		return fmt.Errorf("no JSON object in response")
	}
	if err := json.Unmarshal([]byte(body[start:end+1]), out); err != nil {
		return fmt.Errorf("parse JSON response: %w", err)
	}
	return nil
}
