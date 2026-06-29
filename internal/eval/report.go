package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Suite 是一次评测运行的聚合结果。
type Suite struct {
	RunID   string   `json:"run_id"`
	Mode    string   `json:"mode"` // 本期固定 "single"：单路运行 + 绝对门禁，非 baseline/variant A/B
	Variant string   `json:"variant,omitempty"`
	Gate    Outcome  `json:"gate"`
	Results []Result `json:"cases"`
}

// Aggregate 把单 case 结果汇总成 suite，并计算整体门禁：任一 FAIL→FAIL，否则任一 WARN→WARN。
func Aggregate(runID, mode, variant string, results []Result) Suite {
	gate := Pass
	for _, r := range results {
		switch r.Outcome {
		case Fail:
			gate = Fail
		case Warn:
			if gate != Fail {
				gate = Warn
			}
		}
	}
	return Suite{RunID: runID, Mode: mode, Variant: variant, Gate: gate, Results: results}
}

// WriteReport 在 outDir 下写 report.json（机读）与 report.md（人读）。
func WriteReport(s Suite, outDir string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "report.json"), data, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "report.md"), []byte(renderMarkdown(s)), 0o644)
}

// Summary 是给 stdout 的精简结论。
func Summary(s Suite) string {
	var hardFails, warnings int
	for _, r := range s.Results {
		hardFails += len(r.HardFails)
		warnings += len(r.Warnings)
	}
	return fmt.Sprintf("Gate: %s  (%d cases, %d hard fails, %d warnings)",
		s.Gate, len(s.Results), hardFails, warnings)
}

func renderMarkdown(s Suite) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Eval Report\n\n")
	fmt.Fprintf(&b, "Gate: **%s**\n\n", s.Gate)

	fmt.Fprintf(&b, "## Summary\n\n")
	fmt.Fprintf(&b, "- run_id: %s\n", s.RunID)
	fmt.Fprintf(&b, "- mode: %s\n", s.Mode)
	if s.Variant != "" {
		fmt.Fprintf(&b, "- variant: %s（单路运行，baseline 未跑；A/B delta 为后续阶段）\n", s.Variant)
	}
	fmt.Fprintf(&b, "- cases: %d\n\n", len(s.Results))

	fmt.Fprintf(&b, "## Cases\n\n")
	for _, r := range s.Results {
		fmt.Fprintf(&b, "### %s  [%s]\n\n", r.CaseID, r.Outcome)
		if r.Role != "" {
			fmt.Fprintf(&b, "- category=%s role=%s\n", r.Category, r.Role)
		} else {
			fmt.Fprintf(&b, "- category=%s\n", r.Category)
		}
		m := r.Metrics
		fmt.Fprintf(&b, "- metrics: phase=%s flow=%s completed=%d/%d words=%d findings(crit=%d warn=%d)\n",
			m.Phase, m.Flow, m.CompletedChapters, m.TotalChapters, m.TotalWords, m.CriticalFindings, m.WarningFindings)
		writeIssues(&b, "Hard Fail", r.HardFails)
		writeIssues(&b, "Warnings", r.Warnings)
		writeIssues(&b, "Notes", r.Notes)
		fmt.Fprintf(&b, "- artifacts: %s\n\n", r.Dir)
	}
	return b.String()
}

func writeIssues(b *strings.Builder, title string, issues []Issue) {
	if len(issues) == 0 {
		return
	}
	fmt.Fprintf(b, "- %s:\n", title)
	for _, it := range issues {
		if it.Severity != "" {
			fmt.Fprintf(b, "  - [%s] %s — %s\n", it.Severity, it.Source, it.Detail)
		} else {
			fmt.Fprintf(b, "  - %s — %s\n", it.Source, it.Detail)
		}
	}
}
