package pack

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/tailscale/hujson"
	"github.com/voocel/ainovel-cli/internal/domain"
)

type EvalCheckType string

const (
	EvalContains    EvalCheckType = "contains"
	EvalNotContains EvalCheckType = "not_contains"
	EvalMinRunes    EvalCheckType = "min_runes"
)

type EvalCheck struct {
	ID   string        `json:"id"`
	Type EvalCheckType `json:"type"`
	Text string        `json:"text,omitempty"`
	Min  int           `json:"min,omitempty"`
}

type EvalSuite struct {
	ID     string      `json:"id"`
	Checks []EvalCheck `json:"checks"`
}

type EvalCheckResult struct {
	ID     string        `json:"id"`
	Type   EvalCheckType `json:"type"`
	Passed bool          `json:"passed"`
}

type EvalSuiteResult struct {
	ID     string            `json:"id"`
	Source string            `json:"source"`
	Passed bool              `json:"passed"`
	Checks []EvalCheckResult `json:"checks"`
}

type EvalResult struct {
	Passed bool              `json:"passed"`
	Suites []EvalSuiteResult `json:"suites"`
}

func ParseEvals(manifest domain.PackManifest) (map[string]EvalSuite, error) {
	paths := make([]string, 0, len(manifest.EvalData))
	for path := range manifest.EvalData {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	suites := make(map[string]EvalSuite, len(paths))
	seenIDs := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		standard, err := hujson.Standardize([]byte(manifest.EvalData[path]))
		if err != nil {
			return nil, fmt.Errorf("parse pack eval %q: %w", path, err)
		}
		var suite EvalSuite
		if err := domain.DecodeStrict(standard, &suite); err != nil {
			return nil, fmt.Errorf("decode pack eval %q: %w", path, err)
		}
		if err := suite.validate(); err != nil {
			return nil, fmt.Errorf("pack eval %q: %w", path, err)
		}
		if _, exists := seenIDs[suite.ID]; exists {
			return nil, fmt.Errorf("duplicate pack eval id %q: %w", suite.ID, domain.ErrInvalid)
		}
		seenIDs[suite.ID] = struct{}{}
		suites[path] = suite
	}
	return suites, nil
}

func RunEvals(manifest domain.PackManifest, output string) (EvalResult, error) {
	suites, err := ParseEvals(manifest)
	if err != nil {
		return EvalResult{}, err
	}
	paths := make([]string, 0, len(suites))
	for path := range suites {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	result := EvalResult{Passed: true, Suites: make([]EvalSuiteResult, 0, len(paths))}
	for _, path := range paths {
		suite := suites[path]
		suiteResult := EvalSuiteResult{ID: suite.ID, Source: path, Passed: true, Checks: make([]EvalCheckResult, 0, len(suite.Checks))}
		for _, check := range suite.Checks {
			passed := false
			switch check.Type {
			case EvalContains:
				passed = strings.Contains(output, check.Text)
			case EvalNotContains:
				passed = !strings.Contains(output, check.Text)
			case EvalMinRunes:
				passed = utf8.RuneCountInString(output) >= check.Min
			}
			suiteResult.Checks = append(suiteResult.Checks, EvalCheckResult{ID: check.ID, Type: check.Type, Passed: passed})
			suiteResult.Passed = suiteResult.Passed && passed
		}
		result.Suites = append(result.Suites, suiteResult)
		result.Passed = result.Passed && suiteResult.Passed
	}
	return result, nil
}

func (suite EvalSuite) validate() error {
	if strings.TrimSpace(suite.ID) == "" || len(suite.Checks) == 0 {
		return fmt.Errorf("eval id and checks are required: %w", domain.ErrInvalid)
	}
	seen := make(map[string]struct{}, len(suite.Checks))
	for index, check := range suite.Checks {
		if strings.TrimSpace(check.ID) == "" {
			return fmt.Errorf("eval check %d requires an id: %w", index, domain.ErrInvalid)
		}
		if _, exists := seen[check.ID]; exists {
			return fmt.Errorf("duplicate eval check %q: %w", check.ID, domain.ErrInvalid)
		}
		seen[check.ID] = struct{}{}
		switch check.Type {
		case EvalContains, EvalNotContains:
			if check.Text == "" || check.Min != 0 {
				return fmt.Errorf("eval check %q requires text and forbids min: %w", check.ID, domain.ErrInvalid)
			}
		case EvalMinRunes:
			if check.Min <= 0 || check.Text != "" {
				return fmt.Errorf("eval check %q requires a positive min and forbids text: %w", check.ID, domain.ErrInvalid)
			}
		default:
			return fmt.Errorf("eval check %q has unknown type %q: %w", check.ID, check.Type, domain.ErrInvalid)
		}
	}
	return nil
}
